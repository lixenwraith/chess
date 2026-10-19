// Runs the WASM client in #chess-terminal: its data-wasm-url names the binary,
// #chess-terminal-screen holds xterm, #chess-terminal-status (-state and
// -progress spans) reports loading. xterm with its fit and WebGL addons and
// wasm_exec.js load first. lixen.com wraps this same file in its own page.
(() => {
    'use strict';

    const host = document.getElementById('chess-terminal');
    if (!host) return;

    const screenEl = document.getElementById('chess-terminal-screen');
    const status   = document.getElementById('chess-terminal-status');
    const stateEl  = document.getElementById('chess-terminal-state');
    const progress = document.getElementById('chess-terminal-progress');
    const wasmURL  = host.dataset.wasmUrl;

    const setState = (text, isError) => {
        stateEl.textContent = text;
        progress.textContent = '';
        status.hidden = !text && !isError;
        status.classList.toggle('is-error', !!isError);
    };

    // A page script that should have removed .no-js may have been blocked; its
    // no-JS message would then cover a working terminal.
    document.documentElement.classList.remove('no-js');

    if (typeof Terminal === 'undefined' || typeof FitAddon === 'undefined') {
        setState('Terminal library failed to load. Reload the page to retry.', true);
        return;
    }
    if (typeof Go === 'undefined' || !globalThis.fs) {
        setState('WebAssembly support files failed to load. Reload the page to retry.', true);
        return;
    }

    // The theme's colours are the host's CSS custom properties.
    const css = getComputedStyle(host);
    const token = (name, fallback) => css.getPropertyValue(name).trim() || fallback;

    const term = new Terminal({
        cursorBlink: true,
        convertEol: true,
        scrollback: 1000,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
        fontSize: 14,
        theme: {
            background: token('--term-bg', '#1a1b26'),
            foreground: token('--term-fg', '#a9b1d6'),
            cursor: token('--term-cursor', '#a9b1d6'),
            // xterm 5 silently ignores the old `selection` key.
            selectionBackground: token('--term-selection', 'rgba(169,177,214,0.35)')
        }
    });

    const fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);

    // The client pages long output by the terminal's size, read from here
    // (display/termsize_wasm.go).
    globalThis.chessTerminal = {
        get rows() { return term.rows; },
        get cols() { return term.cols; }
    };

    term.open(screenEl);

    // WebGL must load after open(); the DOM renderer is the fallback.
    try {
        const webgl = new WebglAddon.WebglAddon();
        webgl.onContextLoss(() => webgl.dispose());
        term.loadAddon(webgl);
    } catch (e) {
        console.warn('xterm: WebGL renderer unavailable, using the DOM renderer', e);
    }

    // xterm captures Tab, so Escape, which the line editor ignores, is the
    // keyboard way out of the terminal (WCAG 2.1.2).
    term.attachCustomKeyEventHandler((e) => {
        if (e.type === 'keydown' && e.key === 'Escape') {
            term.blur();
            return false;
        }
        return true;
    });

    // fit() floors rows against the cell height it knew at call time, which a
    // zoom step can leave stale: drop a row while .xterm-screen (rows x cell
    // height) paints taller than the box.
    const availHeight = () => {
        const cs = getComputedStyle(screenEl);
        return screenEl.clientHeight
             - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
    };

    const snapRows = () => {
        const scr = term.element && term.element.querySelector('.xterm-screen');
        if (!scr || !term.rows) return;
        const cell = scr.getBoundingClientRect().height / term.rows;
        if (!cell) return;
        const max = Math.max(1, Math.floor(availHeight() / cell));
        if (term.rows > max) term.resize(term.cols, max);
    };

    const fitNow = (passes) => {
        fitAddon.fit();
        snapRows();
        if (passes > 1) requestAnimationFrame(() => fitNow(passes - 1));
    };

    let fitTimer = 0;
    const scheduleFit = () => {
        clearTimeout(fitTimer);
        fitTimer = setTimeout(() => fitNow(2), 50);
    };

    // A resolution query matches one ratio only, so re-arm on every change.
    let dprQuery = null;
    const onDprChange = () => { watchDpr(); scheduleFit(); };
    const watchDpr = () => {
        if (dprQuery) dprQuery.removeEventListener('change', onDprChange);
        dprQuery = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
        dprQuery.addEventListener('change', onDprChange);
    };

    fitNow(2);
    new ResizeObserver(scheduleFit).observe(screenEl);
    window.addEventListener('resize', scheduleFit);
    watchDpr();
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(scheduleFit);

    screenEl.addEventListener('click', () => term.focus());

    // Go's js/wasm routes os.Stdout and os.Stderr through fs.write, runtime
    // prints (panics) through fs.writeSync and os.Stdin through fs.read: fds
    // 0-2 go to xterm, the rest to wasm_exec.js's own implementations.
    const fs = globalThis.fs;
    const nativeWrite     = fs.write;
    const nativeWriteSync = fs.writeSync;
    const nativeRead      = fs.read;

    const encoder = new TextEncoder();
    // One streaming decoder per fd: a UTF-8 sequence split across two writes
    // must not lend its tail to the other stream.
    const decoders = { 1: new TextDecoder('utf-8'), 2: new TextDecoder('utf-8') };
    const toTerminal = (fd, bytes) => term.write(decoders[fd].decode(bytes, { stream: true }));

    fs.writeSync = function (fd, buf) {
        if (fd === 1 || fd === 2) {
            toTerminal(fd, buf);
            return buf.length;
        }
        return nativeWriteSync.call(this, fd, buf);
    };

    fs.write = function (fd, buf, offset, length, position, callback) {
        if (fd === 1 || fd === 2) {
            toTerminal(fd, buf.subarray(offset, offset + length));
            callback(null, length);   // synchronous, exactly as wasm_exec.js does
            return;
        }
        nativeWrite.call(this, fd, buf, offset, length, position, callback);
    };

    // Line editor. Input is taken only while Go blocks in read: keys buffered
    // across a read would echo twice. Per character, so a pasted "e2e4\r"
    // submits; a multi-line paste submits its first line and drops the rest,
    // which Go's next read would echo over a screen the client has repainted.
    let line = '';
    let pendingRead = null;
    let esc = 0;                    // 0 none, 1 saw ESC, 2 inside CSI/SS3

    term.onData((data) => {
        if (!pendingRead) return;
        for (const ch of data) {
            // Escape sequences (an arrow key) type nothing; CSI/SS3 end on 0x40-0x7E.
            if (esc === 1) { esc = (ch === '[' || ch === 'O') ? 2 : 0; continue; }
            if (esc === 2) { if (ch >= '@' && ch <= '~') esc = 0; continue; }
            if (ch === '\x1b') { esc = 1; continue; }

            if (ch === '\r') {
                term.write('\r\n');
                const resolve = pendingRead, value = line;
                pendingRead = null; line = ''; esc = 0;
                resolve(value);
                return;
            }
            if (ch === '\x03') {                 // Ctrl-C
                term.write('^C\r\n');
                const resolve = pendingRead;
                pendingRead = null; line = ''; esc = 0;
                resolve('');
                return;
            }
            if (ch === '\x7f' || ch === '\b') {
                if (line.length > 0) { line = line.slice(0, -1); term.write('\b \b'); }
                continue;
            }
            if (ch >= ' ' && ch <= '~') { line += ch; term.write(ch); }
        }
    });

    // The bytes of a submitted line that did not fit the caller's buffer.
    let residual = null;

    fs.read = function (fd, buf, offset, length, position, callback) {
        if (fd !== 0) {
            nativeRead.call(this, fd, buf, offset, length, position, callback);
            return;
        }
        const deliver = (bytes) => {
            const n = Math.min(length, bytes.length);
            buf.set(bytes.subarray(0, n), offset);
            residual = n < bytes.length ? bytes.subarray(n) : null;
            callback(null, n);
        };
        if (residual) {
            // Resolving inside the fs.read call would re-enter the Go runtime
            // through _resume() while it is still on the stack.
            const pending = residual;
            residual = null;
            queueMicrotask(() => deliver(pending));
            return;
        }
        new Promise((resolve) => { pendingRead = resolve; })
            .then((value) => deliver(encoder.encode(value + '\n')));
    };

    const MB = 1048576;
    // One unit on the pair: "2.1 / 10.3 MB".
    const fmtBytes = (received, total) => total
        ? (received / MB).toFixed(1) + ' / ' + (total / MB).toFixed(1) + ' MB'
        : (received / MB).toFixed(1) + ' MB';

    (async () => {
        const go = new Go();
        let instance;

        try {
            const response = await fetch(wasmURL);
            if (!response.ok) throw new Error('HTTP ' + response.status);

            let bytes;
            if (response.body) {
                const reader = response.body.getReader();
                const chunks = [];
                let received = 0;
                // The transferred length: compressed in transit, it is smaller
                // than the decoded chunks, so it is dropped once they pass it.
                let total = Number(response.headers.get('content-length')) || 0;
                for (;;) {
                    const { done, value } = await reader.read();
                    if (done) break;
                    chunks.push(value);
                    received += value.length;
                    if (total && received > total) total = 0;
                    progress.textContent = fmtBytes(received, total);
                }
                bytes = new Uint8Array(received);
                let offset = 0;
                for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
            } else {
                bytes = new Uint8Array(await response.arrayBuffer());
            }

            setState('Compiling the client…');
            const result = await WebAssembly.instantiate(bytes, go.importObject);
            instance = result.instance;
        } catch (err) {
            console.error('chess terminal: WASM load failed', err);
            setState('Could not load the client (' + err.message + '). Reload to retry.', true);
            return;
        }

        setState('');
        // The page is the terminal, opened on purpose; Escape is the way out.
        term.focus();

        go.run(instance).then(
            // Unreached while the client restarts on exit (exit_wasm.go).
            () => setState('Session ended. Reload the page to start a new one.'),
            (err) => {
                console.error('chess terminal: client crashed', err);
                setState('Client crashed: ' + err.message, true);
            }
        );
    })();
})();

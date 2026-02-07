const term = new Terminal({
    cursorBlink: true,
    convertEol: true,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    fontSize: 14,
    allowProposedApi: true,
    theme: {
        background: '#1a1b26',
        foreground: '#a9b1d6',
        cursor: '#a9b1d6',
        selection: 'rgba(169, 177, 214, 0.3)'
    }
});

// Load addons
const fitAddon = new FitAddon.FitAddon();
const webglAddon = new WebglAddon.WebglAddon();
const webLinksAddon = new WebLinksAddon.WebLinksAddon();
const unicode11Addon = new Unicode11Addon.Unicode11Addon();

term.loadAddon(fitAddon);
term.loadAddon(webLinksAddon);
term.loadAddon(unicode11Addon);
term.unicode.activeVersion = '11';

term.open(document.getElementById('terminal'));

// WebGL addon must load after open()
try {
    term.loadAddon(webglAddon);
    webglAddon.onContextLoss(() => {
        webglAddon.dispose();
    });
} catch (e) {
    console.warn('WebGL addon failed, using canvas renderer:', e);
}

fitAddon.fit();
term.focus();

let inputBuffer = '';
let inputResolver = null;

term.onData(data => {
    if (inputResolver) {
        if (data === '\r') {
            term.write('\r\n');
            const result = inputBuffer;
            inputBuffer = '';
            const resolver = inputResolver;
            inputResolver = null;
            resolver(result);
        } else if (data === '\x7f' || data === '\x08') {
            if (inputBuffer.length > 0) {
                inputBuffer = inputBuffer.slice(0, -1);
                term.write('\b \b');
            }
        } else if (data === '\x03') {
            term.write('^C\r\n');
            inputBuffer = '';
            if (inputResolver) {
                inputResolver('');
                inputResolver = null;
            }
        } else if (data >= ' ' && data <= '~') {
            inputBuffer += data;
            term.write(data);
        }
    }
});

const encoder = new TextEncoder();
const decoder = new TextDecoder();

if (!globalThis.fs) {
    globalThis.fs = {};
}

const originalWrite = globalThis.fs.write;
const originalRead = globalThis.fs.read;

globalThis.fs.write = function(fd, buf, offset, length, position, callback) {
    if (fd === 1 || fd === 2) {
        const text = decoder.decode(buf.slice(offset, offset + length));
        term.write(text);
        callback(null, length);
    } else if (originalWrite) {
        originalWrite.call(this, fd, buf, offset, length, position, callback);
    } else {
        callback(new Error('Invalid fd'));
    }
};

globalThis.fs.read = function(fd, buf, offset, length, position, callback) {
    if (fd === 0) {
        const promise = new Promise(resolve => {
            inputResolver = resolve;
        });

        promise.then(line => {
            const input = encoder.encode(line + '\n');
            const n = Math.min(length, input.length);
            buf.set(input.slice(0, n), offset);
            callback(null, n);
        });
    } else if (originalRead) {
        originalRead.call(this, fd, buf, offset, length, position, callback);
    } else {
        callback(new Error('Invalid fd'));
    }
};

const go = new Go();

WebAssembly.instantiateStreaming(fetch('chess-client.wasm'), go.importObject)
    .then(result => {
        go.run(result.instance);
    })
    .catch(err => {
        term.writeln('\r\n\x1b[31mError loading WASM: ' + err + '\x1b[0m');
        console.error('WASM load error:', err);
    });

// Resize handling with debounce for performance
let resizeTimeout;
const resizeObserver = new ResizeObserver(() => {
    clearTimeout(resizeTimeout);
    resizeTimeout = setTimeout(() => fitAddon.fit(), 16);
});
resizeObserver.observe(document.getElementById('terminal'));
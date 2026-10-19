// End-to-end: play a short game on the board, open My games, replay it with
// the controls and the keyboard, autoplay to the end, and compare the
// exported FEN and PGN with the API. Also the deep link, a checkmate's
// replay, and the device list for an anonymous visitor.
'use strict';
const { test, expect } = require('@playwright/test');

const API = process.env.CHESS_E2E_API || 'http://localhost:8080';
// Where the server writes password-reset email (-mail-dir); the reset test
// is skipped without it.
const MAIL_DIR = process.env.CHESS_E2E_MAIL_DIR || '';
const fs = require('fs');
const path = require('path');
const START_FEN = 'rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1';

// The engine level and search time are set through the sliders' inputs.
async function startGame(page, { color = 'white' } = {}) {
    await page.click('#new-game-btn');
    await expect(page.locator('#modal-overlay')).toHaveClass(/show/);
    await page.locator('#computer-level').fill('0');
    await page.locator('#search-time').fill('100');
    await page.check(`input[name="player-color"][value="${color}"]`);
    await page.click('#start-game-btn');
    await expect(page.locator('#modal-overlay')).not.toHaveClass(/show/);
}

async function move(page, from, to) {
    await page.click(`[data-square="${from}"]`);
    await page.click(`[data-square="${to}"]`);
}

// Waits for the computer's reply: n plies in the list and the board free.
async function expectPlies(page, n) {
    await expect(page.locator('#live-moves .move')).toHaveCount(n, { timeout: 60_000 });
    await expect(page.locator('#turn-text')).not.toContainText('thinking', { timeout: 60_000 });
}

async function register(page, request) {
    const username = `e2e${Date.now().toString(36)}${Math.floor(Math.random() * 1000)}`;
    const response = await request.post(`${API}/api/auth/register`, {
        data: { username, password: 'Passw0rd123' },
    });
    expect(response.status(), await response.text()).toBe(201);
    const auth = await response.json();
    // The client restores a session from localStorage on load.
    await page.addInitScript(token => localStorage.setItem('authToken', token), auth.token);
    return { ...auth, password: 'Passw0rd123' };
}

test.describe('web replay', () => {
    test('play, list, replay, export', async ({ page, request }) => {
        const user = await register(page, request);
        await page.goto('/');
        await expect(page.locator('#auth-label')).toHaveText(user.username);

        await startGame(page);
        await move(page, 'e2', 'e4');
        await expectPlies(page, 2);
        await move(page, 'g1', 'f3');
        await expectPlies(page, 4);
        // The live list shows SAN from the server.
        await expect(page.locator('#live-moves .move').first()).toHaveText('e4');
        await expect(page.locator('#live-moves .move').nth(2)).toHaveText('Nf3');
        const gameId = await page.evaluate(() => live.gameId);
        const liveFen = await page.evaluate(() => live.fen);
        const history = await (await request.get(`${API}/api/games/${gameId}/history`)).json();
        expect(history.moves.map(m => m.san)).toEqual(await page.$$eval('#live-moves .move', els => els.map(e => e.textContent)));

        // Copy PGN from the live view: the stored game's export.
        await page.click('#copy-pgn-btn');
        await expect(page.locator('#copy-pgn-btn')).toHaveClass(/done/);
        const copied = await page.evaluate(() => navigator.clipboard.readText());
        const apiPgn = await (await request.get(`${API}/api/games/${gameId}/pgn`)).text();
        expect(copied).toBe(apiPgn);

        // My games lists the game with the user's side named.
        await page.click('#games-btn');
        await expect(page.locator('#games-overlay')).toHaveClass(/show/);
        const row = page.locator('#games-list .game-row').first();
        await expect(row).toContainText('You vs Stockfish L0');
        await expect(row).toContainText('Ongoing');
        await expect(row).toHaveClass(/current/);
        await expect(page).toHaveURL(/\?games$/);
        await page.click('.seg[data-filter="status"][data-value="finished"]');
        await expect(page.locator('#games-status')).toContainText('No games match');
        await page.click('.seg[data-filter="status"][data-value=""]');
        await expect(page.locator('#games-list .game-row')).toHaveCount(1);

        // Replay from the list opens at the last ply; the live controls hide.
        await row.click();
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'replay');
        await expect(page.locator('#replay-ply')).toHaveText('4 / 4');
        await expect(page).toHaveURL(new RegExp(`\\?replay=${gameId}&ply=4$`));
        await expect(page.locator('#pane-live')).toBeHidden();
        await expect(page.locator('#replay-moves .move[aria-current="true"]')).toHaveText(history.moves[3].san);

        // Backward and forward: buttons, then the keyboard.
        await page.click('#replay-first');
        await expect(page.locator('#replay-ply')).toHaveText('0 / 4');
        await expect(page.locator('#replay-first')).toBeDisabled();
        await page.keyboard.press('ArrowRight');
        await page.keyboard.press('ArrowRight');
        await expect(page.locator('#replay-ply')).toHaveText('2 / 4');
        await expect(page.locator('#ply-announcer')).toContainText('Ply 2 of 4');
        await expect(page).toHaveURL(new RegExp(`ply=2$`));
        await page.keyboard.press('ArrowLeft');
        await expect(page.locator('#replay-ply')).toHaveText('1 / 4');
        await page.keyboard.press('End');
        await expect(page.locator('#replay-ply')).toHaveText('4 / 4');
        await page.keyboard.press('Home');
        await expect(page.locator('#replay-ply')).toHaveText('0 / 4');
        await page.click('#replay-moves .move[data-ply="3"]');
        await expect(page.locator('#replay-ply')).toHaveText('3 / 4');

        // The exported FEN and PGN at ply 3 match the API.
        await page.click('#replay-copy-fen');
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.moves[2].fenAfterMove);
        await page.click('#replay-copy-pgn');
        await expect(page.locator('#replay-copy-pgn')).toHaveClass(/done/);
        const pgn3 = await (await request.get(`${API}/api/games/${gameId}/pgn?ply=3`)).text();
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(pgn3);
        const downloadPromise = page.waitForEvent('download');
        await page.click('#replay-download');
        const download = await downloadPromise;
        expect(download.suggestedFilename()).toMatch(/^chess-\d{8}-[0-9a-f]{8}-ply3\.pgn$/);
        await page.click('#replay-link');
        expect(await page.evaluate(() => navigator.clipboard.readText()))
            .toBe(`${new URL(page.url()).origin}/?replay=${gameId}&ply=3`);

        // Autoplay to the end at 4x, then pause state clears.
        await page.selectOption('#replay-speed', '250');
        await page.click('#replay-play');
        await expect(page.locator('#replay-play')).toHaveAttribute('aria-pressed', 'true');
        await expect(page.locator('#replay-ply')).toHaveText('4 / 4', { timeout: 10_000 });
        await expect(page.locator('#replay-play')).toHaveAttribute('aria-pressed', 'false');

        // Flip, then back to the live game, which continues where it was.
        await page.click('#replay-flip');
        await expect(page.locator('.coordinates.ranks span').first()).toHaveText('1');
        await page.click('#replay-exit');
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'live');
        await expect(page.locator('.coordinates.ranks span').first()).toHaveText('8');
        expect(await page.evaluate(() => live.fen)).toBe(liveFen);
        await expect(page.locator('#live-moves .move')).toHaveCount(4);
        await move(page, 'b1', 'c3');
        await expectPlies(page, 6);

        // Browser history: back to the replay, back again to the list.
        await page.goBack();
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'replay');
        await page.goBack();
        await expect(page.locator('#games-overlay')).toHaveClass(/show/);
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'live');
    });

    test('deep link to a checkmate replays with the mated king marked', async ({ page, request }) => {
        // A human-vs-human game over the API: fool's mate.
        const created = await request.post(`${API}/api/games`, { data: { white: { type: 1 }, black: { type: 1 } } });
        expect(created.status()).toBe(201);
        const { gameId } = await created.json();
        for (const m of ['f2f3', 'e7e5', 'g2g4', 'd8h4']) {
            const r = await request.post(`${API}/api/games/${gameId}/moves`, { data: { move: m } });
            expect(r.status(), await r.text()).toBe(200);
        }
        await page.goto(`/?replay=${gameId}&ply=2`);
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'replay');
        await expect(page.locator('#replay-ply')).toHaveText('2 / 4');
        await expect(page.locator('#replay-result')).toContainText('0-1');
        await expect(page.locator('#replay-result')).toContainText('Checkmate');
        await expect(page.locator('#replay-exit')).toHaveText('Close replay');
        await expect(page.locator('#replay-moves .move').nth(3)).toHaveText('Qh4#');
        await page.keyboard.press('End');
        await expect(page.locator('[data-square="e1"]')).toHaveClass(/mated-king/);
        await expect(page.locator('[data-square="h4"]')).toHaveClass(/last-move-to/);
        // Escape leaves the replay; with no live game the start position shows.
        await page.keyboard.press('Escape');
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'live');
        await expect(page.locator('#turn-text')).toHaveText('No game');
        await expect(page).toHaveURL(/\/$/);
    });

    test('import a PGN, replay it, delete it', async ({ page, request }) => {
        const user = await register(page, request);
        await page.goto('/');
        await expect(page.locator('#auth-label')).toHaveText(user.username);
        await page.click('#games-btn');
        await expect(page.locator('#games-import-btn')).toBeVisible();
        await page.click('#games-import-btn');
        await expect(page.locator('#import-overlay')).toHaveClass(/show/);
        await page.fill('#import-pgn', 'not a game');
        await page.click('#import-submit-btn');
        await expect(page.locator('#import-modal-message')).toContainText('Not a valid game');
        await page.fill('#import-pgn', `[Event "Test event"]
[White "Spassky, Boris"]
[Black "Fischer, Robert"]
[Date "1972.07.11"]
[Result "1-0"]

1. d4 Nf6 2. c4 e6 3. Nf3 d5 {comment} (3... c5) 1-0
`);
        await page.click('#import-submit-btn');
        await expect(page.locator('#import-overlay')).not.toHaveClass(/show/);
        // The import opens as a replay with the tags shown.
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'replay');
        await expect(page.locator('#replay-players')).toHaveText('Spassky, Boris vs Fischer, Robert');
        await expect(page.locator('#replay-meta')).toContainText('Test event');
        await expect(page.locator('#replay-result')).toContainText('1-0');
        await expect(page.locator('#replay-moves .move')).toHaveCount(6);
        const gameId = await page.evaluate(() => replay.gameId);
        const pgn = await (await request.get(`${API}/api/games/${gameId}/pgn`)).text();
        expect(pgn).toContain('[Event "Test event"]');
        expect(pgn).toContain('{ White wins by resignation. } 1-0');

        // Listed as imported; delete it from the row.
        await page.click('#games-btn');
        const item = page.locator('#games-list .game-item').first();
        await expect(item.locator('.game-row')).toContainText('Spassky, Boris vs Fischer, Robert');
        await expect(item.locator('.game-origin')).toContainText('imported · Test event');
        await item.locator('.game-delete').click();
        await item.locator('.game-confirm .btn-secondary').click(); // Keep
        await expect(item.locator('.game-delete')).toBeVisible();
        await item.locator('.game-delete').click();
        await item.locator('.game-confirm .btn-danger').click();
        await expect(page.locator('#games-list .game-item')).toHaveCount(0);
        await expect(page.locator('#games-status')).toContainText('No stored games');
        // The replay of the deleted game closed and the game is gone.
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'live');
        expect((await request.get(`${API}/api/games/${gameId}/history`)).status()).toBe(404);
    });

    test('a phone fits the board and the controls without scrolling', async ({ browser }) => {
        const context = await browser.newContext({ viewport: { width: 412, height: 780 }, isMobile: true, hasTouch: true });
        const page = await context.newPage();
        await page.goto('/');
        const fits = () => page.evaluate(() => ({
            w: document.documentElement.scrollWidth <= innerWidth,
            h: document.documentElement.scrollHeight <= innerHeight,
        }));
        expect(await fits()).toEqual({ w: true, h: true });
        // The controls sit in one row of short labels.
        await expect(page.locator('#new-game-btn')).toHaveText('New');
        const [newBox, resignBox] = await Promise.all([
            page.locator('#new-game-btn').boundingBox(), page.locator('#resign-btn').boundingBox()]);
        expect(Math.abs(newBox.y - resignBox.y)).toBeLessThan(2);
        expect(newBox.height).toBeLessThan(50);
        await expect(page.locator('.pane-live .toolbar')).toBeInViewport();
        // A replay keeps its controls on screen too.
        const created = await (await context.request.post(`${API}/api/games`, { data: { white: { type: 1 }, black: { type: 1 } } })).json();
        await context.request.post(`${API}/api/games/${created.gameId}/moves`, { data: { move: 'e2e4' } });
        await page.goto(`/?replay=${created.gameId}&ply=1`);
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'replay');
        expect(await fits()).toEqual({ w: true, h: true });
        await expect(page.locator('#replay-exit')).toBeInViewport();
        await expect(page.locator('#replay-link')).toBeInViewport();
        // A long game scrolls inside its move list, not the page.
        const username = `phone${Date.now().toString(36)}${Math.floor(Math.random() * 1000)}`;
        const auth = await (await context.request.post(`${API}/api/auth/register`, { data: { username, password: 'Passw0rd123' } })).json();
        const moves = ['e4 e5', 'Nf3 Nc6', 'Bb5 a6', 'Ba4 Nf6', 'O-O Be7', 'Re1 b5', 'Bb3 d6', 'c3 O-O', 'h3 Nb8', 'd4 Nbd7',
            'c4 c6', 'cxb5 axb5', 'Nc3 Bb7', 'Bg5 b4', 'Nb1 h6', 'Bh4 c5', 'dxe5 Nxe4', 'Bxe7 Qxe7', 'exd6 Qf6', 'Nbd2 Nxd6'];
        const pgn = moves.map((m, i) => `${i + 1}. ${m}`).join(' ') + ' *\n';
        const imported = await (await context.request.post(`${API}/api/games/import`, {
            headers: { Authorization: `Bearer ${auth.token}` }, data: { pgn } })).json();
        expect(imported.moveCount).toBe(40);
        await page.goto(`/?replay=${imported.gameId}&ply=40`);
        await expect(page.locator('#replay-moves .move')).toHaveCount(40);
        expect(await fits()).toEqual({ w: true, h: true });
        await expect(page.locator('#replay-exit')).toBeInViewport();
        const box = await page.locator('#replay-moves-box').evaluate(el => ({ scrolls: el.scrollHeight > el.clientHeight, atEnd: el.scrollTop > 0 }));
        expect(box).toEqual({ scrolls: true, atEnd: true });
        await context.close();
    });

    test('a forgotten password is reset through the emailed link', async ({ page, request }) => {
        test.skip(!MAIL_DIR, 'CHESS_E2E_MAIL_DIR is not set (start the server with MAIL_DIR)');
        const username = `reset${Date.now().toString(36)}${Math.floor(Math.random() * 1000)}`;
        const email = `${username}@example.test`;
        const created = await request.post(`${API}/api/auth/register`, {
            data: { username, email, password: 'OldPass123' },
        });
        expect(created.status(), await created.text()).toBe(201);

        await page.goto('/');
        await expect(page.locator('#storage-dot')).toHaveAttribute('data-status', 'ok');
        await page.click('#auth-btn');
        // The message slot carries a hint until a result replaces it; a click
        // on the backdrop closes the dialog.
        await expect(page.locator('#auth-modal-message')).toHaveClass(/hint/);
        await page.mouse.click(5, 5);
        await expect(page.locator('#auth-modal-overlay')).not.toHaveClass(/show/);
        await page.click('#auth-btn');
        // A wrong password keeps the focus in the password field.
        await page.fill('#login-identifier', username);
        await page.fill('#login-password', 'wrong');
        await page.click('#login-submit-btn');
        await expect(page.locator('#auth-modal-message')).toContainText('Wrong username or password');
        await expect(page.locator('#login-password')).toBeFocused();

        await expect(page.locator('#forgot-btn')).toBeVisible();
        await page.click('#forgot-btn');
        await page.fill('#recover-identifier', email);
        const mails = () => (fs.existsSync(MAIL_DIR) ? fs.readdirSync(MAIL_DIR) : []);
        const before = mails().length;
        await page.click('#recover-submit-btn');
        await expect(page.locator('#auth-modal-message')).toContainText('on its way');
        await expect.poll(() => mails().length, { timeout: 10_000 }).toBeGreaterThan(before);
        const files = fs.readdirSync(MAIL_DIR).map(f => path.join(MAIL_DIR, f))
            .sort((a, b) => fs.statSync(b).mtimeMs - fs.statSync(a).mtimeMs);
        const mail = fs.readFileSync(files[0], 'utf8');
        expect(mail).toContain(`To: ${email}`);
        const link = /(https?:\/\/\S+\?reset=[A-Za-z0-9_-]+)/.exec(mail)[1];
        const origin = new URL(page.url()).origin;
        await page.goto(link.replace(/^https?:\/\/[^/]+/, origin));
        await expect(page.locator('#reset-overlay')).toHaveClass(/show/);
        // The token is removed from the address at once.
        expect(page.url()).not.toContain('reset=');
        await page.fill('#reset-password', 'NewPass123');
        await page.fill('#reset-password-2', 'NewPass124');
        await page.click('#reset-submit-btn');
        await expect(page.locator('#reset-modal-message')).toContainText('differ');
        await page.fill('#reset-password-2', 'NewPass123');
        await page.keyboard.press('Enter');
        await expect(page.locator('#auth-label')).toHaveText(username);
        await expect(page.locator('#reset-overlay')).not.toHaveClass(/show/, { timeout: 5_000 });
        // The old password is gone, the new one and the link are not reusable.
        const old = await request.post(`${API}/api/auth/login`, { data: { identifier: username, password: 'OldPass123' } });
        expect(old.status()).toBe(401);
        const fresh = await request.post(`${API}/api/auth/login`, { data: { identifier: username, password: 'NewPass123' } });
        expect(fresh.status()).toBe(200);
        await page.goto(link.replace(/^https?:\/\/[^/]+/, origin));
        await page.fill('#reset-password', 'NewPass125');
        await page.fill('#reset-password-2', 'NewPass125');
        await page.click('#reset-submit-btn');
        await expect(page.locator('#reset-modal-message')).toContainText('invalid or has expired');
    });

    test('an anonymous visitor replays from the device list', async ({ page }) => {
        await page.goto('/');
        await expect(page.locator('#auth-label')).toHaveText('Sign in');
        await startGame(page, { color: 'black' });
        await expectPlies(page, 1);
        // Black at the bottom while playing Black.
        await expect(page.locator('.coordinates.ranks span').first()).toHaveText('1');
        await page.click('#games-btn');
        await expect(page.locator('#games-signin-note')).toBeVisible();
        const row = page.locator('#local-games-list .game-row');
        await expect(row).toHaveCount(1);
        await expect(row).toContainText('Stockfish L0 vs You');
        await row.click();
        await expect(page.locator('body')).toHaveAttribute('data-mode', 'replay');
        await expect(page.locator('#replay-ply')).toHaveText('1 / 1');
        // The viewer's side stays at the bottom in the replay.
        await expect(page.locator('.coordinates.ranks span').first()).toHaveText('1');
        expect(await page.evaluate(() => replay.history.initialFen)).toBe(START_FEN);
        await page.click('#replay-exit');
        await expect(page.locator('#turn-text')).toHaveText('Your move');
        await expect(page.locator('#turn-dot')).toHaveAttribute('data-status', 'black');
    });
});

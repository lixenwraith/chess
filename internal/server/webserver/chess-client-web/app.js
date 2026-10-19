/* Chess web client: a board for live play against Stockfish, a list of the
   signed-in user's stored games, and a replay viewer for any stored game.
   Plain JavaScript, one file, no framework: the host's CSP allows only
   same-origin scripts, no inline code and no eval.

   Sections: state · utilities · API · auth · board · live game · recent games
   on this device · My games drawer · replay · routing · keyboard · init. */
'use strict';

// ---------------------------------------------------------------- state

// The API base, from the page that loads this script: its element's data-api,
// set by the embedded server from -web-api-url and by a host that publishes
// the files itself. Without it, /chess, where a static host proxies the API.
// Read while the script runs: document.currentScript is null in any callback.
function apiBase(script) {
    const api = script?.dataset.api?.trim();
    return api ? api.replace(/\/+$/, '') : '/chess';
}

const API = apiBase(document.currentScript);
const START_FEN = 'rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1';

// Chess piece Unicode: all black pieces for better fill, white pawn due to
// inability to override emoji variant display.
const pieceMap = {
    'p': '♙', 'r': '♜', 'n': '♞', 'b': '♝', 'q': '♛', 'k': '♚',
    'P': '♙', 'R': '♜', 'N': '♞', 'B': '♝', 'Q': '♛', 'K': '♚'
};

// How long a success message stays visible in a modal before it auto-closes
const MODAL_SUCCESS_DISPLAY_MS = 700;
const HEALTH_INTERVAL_MS = 10000;
const GAMES_PAGE_SIZE = 20;
const RECENT_GAMES_KEY = 'chess.recentGames';
const RECENT_GAMES_MAX = 20;
const RECENT_GAMES_TTL_MS = 24 * 60 * 60 * 1000; // the server's anonymous-game retention

// The live game played on this board.
const live = {
    gameId: null,
    initialFen: null,
    fen: null,
    turn: 'w',
    state: null,
    termination: '',
    moves: [],
    san: [],
    lastMove: null,
    isPlayerWhite: true,
    level: 10,
    isLocked: false,     // the computer is thinking
    polling: false,
    pollController: null,
    selectedSquare: null,
    actionBusy: false,   // a draw or resign request is in flight
    newGameBusy: false,
    anonymous: true,     // created without an account: listed under "On this device"
};

const auth = {
    token: null,
    userId: null,
    username: null,
    busy: false,         // login or registration in flight
    accountBusy: false,  // logout or account deletion in flight
};

// The stored game being replayed, if any. Separate from the live game: the
// live game stays in memory and is shown again by exitReplay.
const replay = {
    active: false,
    gameId: null,
    history: null,
    ply: 0,
    flipped: false,
    timer: null,         // autoplay timeout
    speedMs: 1000,
    loading: null,       // the game ID whose history is being fetched
};

// The My games drawer.
const games = {
    open: false,
    status: '',
    color: '',
    items: [],
    cursor: '',
    loading: false,
    loadedFor: null,     // user ID the list was loaded for
};

const ui = {
    healthTimer: null,
    storage: 'unknown',
    mail: 'unknown',    // "ok" when the server emails password-reset links
    toastTimer: null,
    historyCache: new Map(), // gameId -> {etag, history}
};

const $ = id => document.getElementById(id);

// ------------------------------------------------------------ utilities

// A short message under the board; 'error' for failures, 'info' for results
// and answers, 'success' for copies and downloads.
function toast(message, type = 'info', duration = 1800) {
    const el = $('toast');
    el.textContent = message;
    el.classList.remove('error', 'info', 'success');
    el.classList.add('show', type);
    if (ui.toastTimer) clearTimeout(ui.toastTimer);
    ui.toastTimer = setTimeout(() => {
        el.classList.remove('show');
        ui.toastTimer = null;
    }, duration);
}

function flashErrorMessage(message, duration = 1800) { toast(message, 'error', duration); }

// Show/clear a status line inside a modal. Distinct from toast, which is not
// visible while a modal's backdrop is up.
function setModalMessage(elementId, message, type = 'error') {
    const el = $(elementId);
    if (!el) return;
    el.textContent = message;
    el.classList.remove('error', 'success');
    el.classList.add('show', type);
}

function clearModalMessage(elementId) {
    const el = $(elementId);
    if (!el) return;
    el.textContent = '';
    el.classList.remove('show', 'error', 'success');
}

function anyModalOpen() {
    return !!document.querySelector('.modal-overlay.show');
}

// Copies text; the async clipboard API needs a secure context and, in a
// frame, clipboard-write permission, so a hidden textarea is the fallback.
async function copyText(text) {
    try {
        if (navigator.clipboard?.writeText) {
            await navigator.clipboard.writeText(text);
            return true;
        }
    } catch {
        // fall through
    }
    try {
        const area = document.createElement('textarea');
        area.value = text;
        area.setAttribute('readonly', '');
        area.className = 'visually-hidden';
        document.body.appendChild(area);
        area.select();
        const ok = document.execCommand('copy');
        area.remove();
        return ok;
    } catch {
        return false;
    }
}

// Marks a tool button done for a moment after a copy or download.
function markDone(button) {
    button.classList.add('done');
    setTimeout(() => button.classList.remove('done'), 1500);
}

function formatDate(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    return d.toLocaleString(undefined, {
        year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    });
}

// A player's display name: the claimant's name snapshot, the engine and its
// level, or "Anonymous".
function playerLabel(player) {
    if (!player) return '?';
    if (player.type === 2) return `Stockfish L${player.level ?? 0}`;
    return player.name || 'Anonymous';
}

// describeResult names a finished game's outcome and how it was reached, from
// a live state ("white wins") or a stored result ("white_wins").
function describeResult(state, termination) {
    const s = (state || '').replace('_', ' ');
    const winner = s === 'white wins' ? 'White' : 'Black';
    const loser = s === 'white wins' ? 'Black' : 'White';
    switch (termination) {
        case 'resignation': return `${loser} resigned — ${winner} wins`;
        case 'checkmate': return `Checkmate — ${winner} wins`;
        case 'stalemate': return 'Stalemate — draw';
        case 'insufficient_material': return 'Draw — insufficient material';
        case 'threefold_repetition': return 'Draw — threefold repetition';
        case 'fifty_move_rule': return 'Draw — fifty-move rule';
        case 'agreement': return 'Draw agreed';
    }
    switch (s) {
        case 'white wins':
        case 'black wins': return `${winner} wins`;
        case 'stalemate': return 'Stalemate — draw';
        case 'draw': return 'Draw';
    }
    return 'Game over';
}

// shortResult is the outcome in a few words, for the narrow turn strip.
function shortResult(state, termination) {
    const winner = state === 'white wins' ? 'White' : 'Black';
    switch (termination) {
        case 'resignation': return `${winner} wins · resigned`;
        case 'checkmate': return `${winner} wins · mate`;
        case 'stalemate': return 'Draw · stalemate';
        case 'insufficient_material': return 'Draw · material';
        case 'threefold_repetition': return 'Draw · repetition';
        case 'fifty_move_rule': return 'Draw · 50 moves';
        case 'agreement': return 'Draw · agreed';
    }
    if (state === 'white wins' || state === 'black wins') return `${winner} wins`;
    return 'Draw';
}

function terminationLabel(termination) {
    switch (termination) {
        case 'checkmate': return 'Checkmate';
        case 'resignation': return 'Resignation';
        case 'stalemate': return 'Stalemate';
        case 'insufficient_material': return 'Insufficient material';
        case 'threefold_repetition': return 'Threefold repetition';
        case 'fifty_move_rule': return 'Fifty-move rule';
        case 'agreement': return 'Agreed';
    }
    return '';
}

function isGameOver(state) {
    return ['white wins', 'black wins', 'stalemate', 'draw'].includes(state);
}

function isPlayable(state) {
    return !isGameOver(state) && state !== 'stuck';
}

// ------------------------------------------------------------------ API

// Wrapper for authenticated requests
function authFetch(url, options = {}) {
    if (auth.token) {
        options.headers = { ...options.headers, 'Authorization': `Bearer ${auth.token}` };
    }
    return fetch(url, options);
}

function apiJSON(method, path, body) {
    const options = { method, headers: {} };
    if (body !== undefined) {
        options.headers['Content-Type'] = 'application/json';
        options.body = JSON.stringify(body);
    }
    return authFetch(`${API}${path}`, options);
}

// Shared helper: safely parse error response regardless of Content-Type
async function parseErrorResponse(response) {
    try {
        return await response.json();
    } catch {
        return { error: `Server error (${response.status})`, details: null };
    }
}

// Maps a failed request to a short message and the server indicator state.
function handleApiError(action, error, response = null) {
    let serverStatus = 'degraded';
    let statusMessage = 'Server error';
    const isNetworkError = !response;

    if (isNetworkError) {
        statusMessage = 'Connection failed';
        console.error(`Network error during ${action}:`, error);
    } else {
        const status = response.status;
        switch (status) {
            case 400:
                serverStatus = 'healthy';
                if (action === 'undo') statusMessage = 'No moves to undo';
                else if (action === 'move') statusMessage = 'Invalid move';
                else statusMessage = 'Invalid request';
                break;
            case 401:
                serverStatus = 'healthy';
                statusMessage = 'Sign in again';
                break;
            case 403:
                serverStatus = 'healthy';
                if (action === 'move' || action === 'trigger computer move' || action === 'undo') {
                    statusMessage = 'Side claimed by another player';
                } else {
                    statusMessage = 'Not authorized';
                }
                break;
            case 404:
                serverStatus = 'healthy';
                statusMessage = action === 'history' ? 'Stored game not found' : 'Game not found';
                break;
            case 409:
                serverStatus = 'healthy';
                statusMessage = 'Game changed; try again';
                break;
            case 415:
                serverStatus = 'healthy';
                statusMessage = 'Invalid content type';
                break;
            case 429:
                serverStatus = 'degraded';
                statusMessage = 'Rate limited';
                break;
            case 503:
                serverStatus = 'degraded';
                statusMessage = action === 'history' || action === 'games' || action === 'pgn'
                    ? 'Stored games unavailable' : 'Service unavailable';
                break;
            default:
                if (status >= 500) {
                    serverStatus = 'degraded';
                    statusMessage = `Server error (${status})`;
                } else if (status >= 400) {
                    serverStatus = 'healthy';
                    statusMessage = `Request failed (${status})`;
                }
        }
        console.error(`API error during ${action}: ${status} - ${statusMessage}`);
    }

    flashErrorMessage(statusMessage);
    if (isNetworkError || response.status >= 500) {
        setServerStatus(serverStatus, statusMessage);
    } else {
        setServerStatus('healthy');
    }

    return {
        serverStatus,
        statusMessage,
        isNetworkError,
        isClientError: !!response && response.status >= 400 && response.status < 500,
        isServerError: !!response && response.status >= 500,
    };
}

function setDot(id, prefix, status, text) {
    const dot = $(id);
    dot.dataset.status = status;
    const label = `${prefix}: ${text || status}`;
    dot.title = label;
    dot.setAttribute('aria-label', label);
}

function setServerStatus(status, message) {
    setDot('server-dot', 'Server', status, message);
}

function setStorageStatus(status) {
    ui.storage = status;
    setDot('storage-dot', 'Storage', status, status === 'ok' ? 'ok (games are stored)' :
        status === 'disabled' ? 'disabled (no stored games)' : status);
    updateLiveTools();
}

function setMailStatus(status) {
    ui.mail = status;
    $('forgot-row').classList.toggle('hidden', status !== 'ok');
}

function startHealthCheck() {
    const checkHealth = async () => {
        try {
            const response = await fetch(`${API}/health`);
            if (response.ok) {
                const health = await response.json();
                setServerStatus(health.status === 'healthy' ? 'healthy' : 'degraded');
                setStorageStatus(health.storage || 'unknown');
                setMailStatus(health.mail || 'disabled');
            } else {
                handleApiError('health check', null, response);
                setStorageStatus('unknown');
            }
        } catch (error) {
            handleApiError('health check', error);
            setStorageStatus('unknown');
        }
    };
    checkHealth();
    ui.healthTimer = setInterval(checkHealth, HEALTH_INTERVAL_MS);
}

// ----------------------------------------------------------------- auth

function restoreAuthSession() {
    let token = null;
    try { token = localStorage.getItem('authToken'); } catch { /* storage blocked */ }
    if (token) {
        auth.token = token;
        return validateSession();
    }
    updateAuthButton();
    return Promise.resolve();
}

async function validateSession() {
    try {
        const response = await authFetch(`${API}/api/auth/me`);
        if (response.ok) {
            const user = await response.json();
            setSignedIn(user.userId, user.username, auth.token);
        } else {
            clearAuthState();
        }
    } catch {
        clearAuthState();
    }
}

function setSignedIn(userId, username, token) {
    auth.token = token;
    auth.userId = userId;
    auth.username = username;
    try { localStorage.setItem('authToken', token); } catch { /* storage blocked */ }
    updateAuthButton();
    if (games.open) refreshGamesForUser();
}

function clearAuthState() {
    auth.token = null;
    auth.userId = null;
    auth.username = null;
    try { localStorage.removeItem('authToken'); } catch { /* storage blocked */ }
    updateAuthButton();
    if (games.open) refreshGamesForUser();
}

function updateAuthButton() {
    const button = $('auth-btn');
    const signedIn = !!auth.token;
    button.dataset.status = signedIn ? 'authenticated' : 'anonymous';
    $('auth-dot').dataset.status = signedIn ? 'authenticated' : 'anonymous';
    $('auth-label').textContent = signedIn ? auth.username : 'Sign in';
    button.title = signedIn ? `Signed in as ${auth.username}` : 'Sign in or register';
    button.setAttribute('aria-label', button.title);
}

function handleAuthClick() {
    if (auth.token) showAccountModal(); else showAuthModal();
}

// Account dialog, for a signed-in user: sign out, or delete the account. It
// replaces a native confirm(), which a sandboxed embed may not be allowed.
function showAccountModal() {
    $('account-username').textContent = auth.username || '';
    $('delete-account').open = false;
    $('delete-password').value = '';
    clearModalMessage('account-modal-message');
    $('account-overlay').classList.add('show');
    document.removeEventListener('keydown', handleAccountKeydown);
    document.addEventListener('keydown', handleAccountKeydown);
    $('account-cancel-btn').focus();
}

function hideAccountModal() {
    if (auth.accountBusy) return;
    $('account-overlay').classList.remove('show');
    $('delete-password').value = '';
    document.removeEventListener('keydown', handleAccountKeydown);
}

function setAccountBusy(busy) {
    auth.accountBusy = busy;
    for (const id of ['logout-btn', 'account-cancel-btn', 'delete-account-btn', 'delete-password']) {
        $(id).disabled = busy;
    }
}

function handleAccountKeydown(e) {
    if (auth.accountBusy) {
        if (e.key === 'Enter' || e.key === 'Escape') e.preventDefault();
        return;
    }
    if (e.key === 'Escape') {
        e.preventDefault();
        hideAccountModal();
    } else if (e.key === 'Enter' && document.activeElement?.id === 'delete-password') {
        e.preventDefault();
        deleteAccount();
    }
}

async function handleLogoutClick() {
    setAccountBusy(true);
    await handleLogout();
    setAccountBusy(false);
    hideAccountModal();
}

async function handleLogout() {
    if (auth.token) {
        try {
            await apiJSON('POST', '/api/auth/logout');
        } catch {
            // Ignore - clear local state regardless
        }
    }
    clearAuthState();
}

// The server re-checks the password, ends the session, clears the account's
// claims and names from its games, then deletes it (DELETE /api/auth/me).
async function deleteAccount() {
    if (auth.accountBusy) return;
    const password = $('delete-password').value;
    if (!password) {
        setModalMessage('account-modal-message', 'Enter your password to delete the account', 'error');
        return;
    }

    setAccountBusy(true);
    clearModalMessage('account-modal-message');
    let response;
    try {
        response = await apiJSON('DELETE', '/api/auth/me', { password });
    } catch {
        setAccountBusy(false);
        setModalMessage('account-modal-message', 'Network error: the account was not deleted', 'error');
        return;
    }

    if (response.status === 204) {
        const name = auth.username;
        clearAuthState();
        setModalMessage('account-modal-message', `Account ${name} deleted`, 'success');
        setTimeout(() => {
            setAccountBusy(false);
            hideAccountModal();
        }, MODAL_SUCCESS_DISPLAY_MS * 2);
        return;
    }

    setAccountBusy(false);
    const data = await parseErrorResponse(response);
    let message;
    if (response.status === 401 && data.error === 'invalid credentials') {
        message = 'Wrong password';
    } else if (response.status === 401) {
        // The token itself was refused: the session is already gone.
        clearAuthState();
        message = 'Your session has ended. Sign in again to delete the account.';
    } else if (response.status === 429) {
        message = 'Too many attempts. Wait a minute and try again.';
    } else {
        message = data.error ? `Not deleted: ${data.error}` : `Server error (${response.status})`;
    }
    setModalMessage('account-modal-message', message, 'error');
}

// Disables every interactive control in the auth modal at once while a
// request is in flight (or its success message is showing): guards
// re-entrancy from the Enter-key handler and stops edits mid-request.
function setAuthModalBusy(busy) {
    auth.busy = busy;
    for (const id of ['login-submit-btn', 'register-submit-btn', 'auth-cancel-btn', 'auth-cancel-btn-2',
        'forgot-btn', 'recover-submit-btn', 'recover-back-btn']) {
        $(id).disabled = busy;
    }
    document.querySelectorAll('.auth-tab').forEach(t => t.disabled = busy);
    document.querySelectorAll('.auth-form input').forEach(i => i.disabled = busy);
}

function showAuthModal() {
    $('auth-modal-overlay').classList.add('show');
    switchAuthTab('login');
    document.removeEventListener('keydown', handleAuthModalKeydown);
    document.addEventListener('keydown', handleAuthModalKeydown);
}

function hideAuthModal() {
    $('auth-modal-overlay').classList.remove('show');
    document.querySelectorAll('.auth-form input').forEach(input => input.value = '');
    document.removeEventListener('keydown', handleAuthModalKeydown);
    clearModalMessage('auth-modal-message');
    setAuthModalBusy(false);
}

function handleAuthModalKeydown(e) {
    if (!$('auth-modal-overlay').classList.contains('show')) return;
    if (auth.busy) {
        if (e.key === 'Enter' || e.key === 'Escape') e.preventDefault();
        return;
    }
    if (e.key === 'Escape') {
        e.preventDefault();
        hideAuthModal();
    } else if (e.key === 'Enter') {
        e.preventDefault();
        ({ login: handleLogin, register: handleRegister, recover: handleRecover })[activeAuthForm()]();
    }
}

// The auth modal shows one of three forms: sign in, register, or password
// recovery (reached from the sign-in form, without a tab).
function switchAuthTab(tab) {
    document.querySelectorAll('.auth-tab').forEach(t => {
        const active = t.dataset.tab === tab;
        t.classList.toggle('active', active);
        t.setAttribute('aria-selected', active ? 'true' : 'false');
    });
    document.querySelector('.auth-tabs').classList.toggle('hidden', tab === 'recover');
    $('login-form').classList.toggle('hidden', tab !== 'login');
    $('register-form').classList.toggle('hidden', tab !== 'register');
    $('recover-form').classList.toggle('hidden', tab !== 'recover');
    clearModalMessage('auth-modal-message');
    $({ login: 'login-identifier', register: 'register-username', recover: 'recover-identifier' }[tab]).focus();
}

function activeAuthForm() {
    if (!$('login-form').classList.contains('hidden')) return 'login';
    if (!$('register-form').classList.contains('hidden')) return 'register';
    return 'recover';
}

// Returns the focus to a field after a failed request: the request disabled
// the fields, which drops the focus to the page, and a retyped password
// would otherwise go nowhere.
function refocus(id, select = true) {
    const el = $(id);
    if (!el) return;
    el.focus();
    if (select && typeof el.select === 'function') el.select();
}

async function submitAuth(path, body, failure, focusId) {
    setAuthModalBusy(true);
    clearModalMessage('auth-modal-message');

    const fail = message => {
        setModalMessage('auth-modal-message', message, 'error');
        setAuthModalBusy(false);
        refocus(focusId);
        return null;
    };

    let response;
    try {
        response = await apiJSON('POST', path, body);
    } catch (error) {
        return fail(handleApiError(path, error).statusMessage);
    }

    if (!response.ok) {
        const err = await parseErrorResponse(response);
        if (response.status === 401) return fail('Wrong username or password');
        if (response.status === 429) return fail('Too many attempts. Wait a minute and try again.');
        return fail(err.details || err.error || failure);
    }

    try {
        return await response.json();
    } catch (error) {
        console.error(`${path}: response OK but JSON parse failed:`, error);
        return fail('Unexpected response from server');
    }
}

async function handleLogin() {
    if (auth.busy) return;
    const identifier = $('login-identifier').value.trim();
    const password = $('login-password').value;
    if (!identifier || !password) {
        setModalMessage('auth-modal-message', 'Fill in both fields', 'error');
        refocus(identifier ? 'login-password' : 'login-identifier', false);
        return;
    }
    const result = await submitAuth('/api/auth/login', { identifier, password }, 'Sign-in failed', 'login-password');
    if (!result) return;
    setSignedIn(result.userId, result.username, result.token);
    setModalMessage('auth-modal-message', `Signed in as ${result.username}`, 'success');
    setTimeout(hideAuthModal, MODAL_SUCCESS_DISPLAY_MS);
}

// The server's password rule, checked first for a quicker answer.
function passwordProblem(password) {
    if (password.length < 8) return 'Password needs at least 8 characters';
    if (!/[a-zA-Z]/.test(password) || !/[0-9]/.test(password)) return 'Password needs a letter and a number';
    return '';
}

// Asks for a reset link. The server answers the same whether or not the
// account exists, so the message is the same too.
async function handleRecover() {
    if (auth.busy) return;
    const identifier = $('recover-identifier').value.trim();
    if (!identifier) {
        setModalMessage('auth-modal-message', 'Enter your username or email', 'error');
        refocus('recover-identifier', false);
        return;
    }
    setAuthModalBusy(true);
    clearModalMessage('auth-modal-message');
    let response;
    try {
        response = await apiJSON('POST', '/api/auth/recover', { identifier });
    } catch (error) {
        setModalMessage('auth-modal-message', handleApiError('recover', error).statusMessage, 'error');
        setAuthModalBusy(false);
        refocus('recover-identifier');
        return;
    }
    setAuthModalBusy(false);
    if (response.status === 202) {
        setModalMessage('auth-modal-message', 'If the account has an email address, a reset link is on its way. Check your inbox.', 'success');
        $('recover-identifier').value = '';
        $('recover-back-btn').focus();
        return;
    }
    const err = await parseErrorResponse(response);
    let message = err.error || 'Could not send the link';
    if (response.status === 429) message = 'Too many requests. Wait a minute and try again.';
    else if (response.status === 503 && err.code === 'NOT_CONFIGURED') message = 'This server cannot send email; ask its administrator for a reset link.';
    setModalMessage('auth-modal-message', message, 'error');
    refocus('recover-identifier');
}

// --- reset: the link in the recovery email opens the client with ?reset=<token>

const reset = { token: null, busy: false };

function openResetModal(token) {
    reset.token = token;
    reset.busy = false;
    $('reset-password').value = '';
    $('reset-password-2').value = '';
    clearModalMessage('reset-modal-message');
    $('reset-overlay').classList.add('show');
    document.removeEventListener('keydown', handleResetKeydown);
    document.addEventListener('keydown', handleResetKeydown);
    $('reset-password').focus();
}

function closeResetModal() {
    if (reset.busy) return;
    reset.token = null;
    $('reset-overlay').classList.remove('show');
    $('reset-password').value = '';
    $('reset-password-2').value = '';
    document.removeEventListener('keydown', handleResetKeydown);
}

function setResetBusy(busy) {
    reset.busy = busy;
    for (const id of ['reset-password', 'reset-password-2', 'reset-submit-btn', 'reset-cancel-btn']) {
        $(id).disabled = busy;
    }
}

function handleResetKeydown(e) {
    if (reset.busy) {
        if (e.key === 'Enter' || e.key === 'Escape') e.preventDefault();
        return;
    }
    if (e.key === 'Escape') {
        e.preventDefault();
        closeResetModal();
    } else if (e.key === 'Enter') {
        e.preventDefault();
        submitReset();
    }
}

async function submitReset() {
    if (reset.busy || !reset.token) return;
    const password = $('reset-password').value;
    const again = $('reset-password-2').value;
    const weak = passwordProblem(password);
    if (weak) {
        setModalMessage('reset-modal-message', weak, 'error');
        refocus('reset-password');
        return;
    }
    if (password !== again) {
        setModalMessage('reset-modal-message', 'The two passwords differ', 'error');
        refocus('reset-password-2');
        return;
    }
    setResetBusy(true);
    clearModalMessage('reset-modal-message');
    let response;
    try {
        response = await apiJSON('POST', '/api/auth/reset', { token: reset.token, password });
    } catch (error) {
        setResetBusy(false);
        setModalMessage('reset-modal-message', handleApiError('reset', error).statusMessage, 'error');
        refocus('reset-password');
        return;
    }
    if (!response.ok) {
        const err = await parseErrorResponse(response);
        setResetBusy(false);
        setModalMessage('reset-modal-message', response.status === 400
            ? (err.details && err.error === 'weak password' ? err.details : 'This link is invalid or has expired. Ask for a new one from Sign in.')
            : (err.error || `Server error (${response.status})`), 'error');
        refocus('reset-password');
        return;
    }
    let result;
    try {
        result = await response.json();
    } catch {
        setResetBusy(false);
        setModalMessage('reset-modal-message', 'Unexpected response from server', 'error');
        return;
    }
    setSignedIn(result.userId, result.username, result.token);
    setModalMessage('reset-modal-message', `Password changed — signed in as ${result.username}`, 'success');
    setTimeout(() => {
        setResetBusy(false);
        closeResetModal();
    }, MODAL_SUCCESS_DISPLAY_MS * 2);
}

async function handleRegister() {
    if (auth.busy) return;
    const username = $('register-username').value.trim();
    const email = $('register-email').value.trim();
    const password = $('register-password').value;
    if (!username || !password) {
        setModalMessage('auth-modal-message', 'Username and password required', 'error');
        return;
    }
    const weak = passwordProblem(password);
    if (weak) {
        setModalMessage('auth-modal-message', weak, 'error');
        refocus('register-password');
        return;
    }
    const body = { username, password };
    if (email) body.email = email;
    const result = await submitAuth('/api/auth/register', body, 'Registration failed', 'register-password');
    if (!result) return;
    setSignedIn(result.userId, result.username, result.token);
    setModalMessage('auth-modal-message', `Account created, welcome ${result.username}`, 'success');
    setTimeout(hideAuthModal, MODAL_SUCCESS_DISPLAY_MS);
}

// ---------------------------------------------------------------- board

const board = {
    flipped: false,
    squares: new Map(), // "e4" -> element
};

// Builds the 64 squares in the given orientation. The live game and the
// replay share one board; each sets its own orientation when it shows.
function buildBoard(flipped) {
    board.flipped = flipped;
    board.squares.clear();
    const boardEl = $('board');
    boardEl.innerHTML = '';
    const files = 'abcdefgh';
    const ranks = '87654321';
    const fileLabels = flipped ? [...files].reverse() : [...files];
    const rankLabels = flipped ? [...ranks].reverse() : [...ranks];
    $('board').parentElement.querySelector('.coordinates.files').replaceChildren(
        ...fileLabels.map(f => { const s = document.createElement('span'); s.textContent = f; return s; }));
    $('board').parentElement.querySelector('.coordinates.ranks').replaceChildren(
        ...rankLabels.map(r => { const s = document.createElement('span'); s.textContent = r; return s; }));

    for (let i = 0; i < 64; i++) {
        const row = Math.floor(i / 8); // 0 = top of the display
        const col = i % 8;
        const rank = flipped ? row : 7 - row;      // 0 = rank 1
        const file = flipped ? 7 - col : col;      // 0 = file a
        const name = `${files[file]}${rank + 1}`;
        const square = document.createElement('div');
        square.className = `square ${(rank + file) % 2 === 0 ? 'dark' : 'light'}`;
        square.dataset.square = name;
        square.setAttribute('role', 'gridcell');
        square.setAttribute('aria-label', name);
        square.addEventListener('click', handleSquareClick);
        boardEl.appendChild(square);
        board.squares.set(name, square);
    }
}

// Shows a position; the browser never computes moves. opts.lastMove (UCI)
// highlights a move's squares and opts.matedColor marks that king.
function renderPosition(fen, opts = {}) {
    const fenBoard = (fen || START_FEN).split(' ')[0];
    for (const s of board.squares.values()) {
        s.textContent = '';
        s.classList.remove('white-piece', 'black-piece', 'mated-king', 'last-move-from', 'last-move-to', 'selected');
        delete s.dataset.pieceColor;
        delete s.dataset.pieceType;
        s.setAttribute('aria-label', s.dataset.square);
    }

    let rank = 7, file = 0;
    for (const char of fenBoard) {
        if (char === '/') {
            rank--; file = 0;
        } else if (/\d/.test(char)) {
            file += parseInt(char, 10);
        } else {
            const name = `${String.fromCharCode(97 + file)}${rank + 1}`;
            const squareEl = board.squares.get(name);
            if (squareEl) {
                const pieceColor = (char === char.toUpperCase()) ? 'w' : 'b';
                squareEl.textContent = pieceMap[char === 'P' ? 'P' : char.toLowerCase()] || '';
                squareEl.classList.add(pieceColor === 'w' ? 'white-piece' : 'black-piece');
                squareEl.dataset.pieceColor = pieceColor;
                squareEl.dataset.pieceType = char.toLowerCase();
                squareEl.setAttribute('aria-label', `${name}: ${pieceColor === 'w' ? 'white' : 'black'} ${pieceName(char)}`);
            }
            file++;
        }
    }

    if (opts.lastMove && opts.lastMove.length >= 4) {
        board.squares.get(opts.lastMove.substring(0, 2))?.classList.add('last-move-from');
        board.squares.get(opts.lastMove.substring(2, 4))?.classList.add('last-move-to');
    }
    if (opts.matedColor) {
        for (const s of board.squares.values()) {
            if (s.dataset.pieceType === 'k' && s.dataset.pieceColor === opts.matedColor) {
                s.classList.add('mated-king');
            }
        }
    }
}

function pieceName(char) {
    switch (char.toLowerCase()) {
        case 'p': return 'pawn';
        case 'r': return 'rook';
        case 'n': return 'knight';
        case 'b': return 'bishop';
        case 'q': return 'queen';
        case 'k': return 'king';
    }
    return '';
}

// A thumbnail of a position for the games list: the same glyphs, 8x8 spans.
function renderMiniBoard(container, fen) {
    container.innerHTML = '';
    const rows = (fen || START_FEN).split(' ')[0].split('/');
    for (let r = 0; r < 8; r++) {
        let file = 0;
        for (const char of rows[r] || '8') {
            if (/\d/.test(char)) {
                const n = parseInt(char, 10);
                for (let k = 0; k < n; k++) container.appendChild(miniSquare(r, file++, ''));
            } else {
                container.appendChild(miniSquare(r, file++, char));
            }
        }
        while (file < 8) container.appendChild(miniSquare(r, file++, ''));
    }
}

function miniSquare(row, file, char) {
    const s = document.createElement('span');
    const rank = 7 - row;
    s.className = (rank + file) % 2 === 0 ? 'dark' : 'light';
    if (char) {
        s.textContent = pieceMap[char === 'P' ? 'P' : char.toLowerCase()] || '';
        s.classList.add(char === char.toUpperCase() ? 'white-piece' : 'black-piece');
    }
    return s;
}

function flashSquare(element, success = true) {
    if (!element) return;
    const className = success ? 'flash-green' : 'flash-red';
    element.classList.add(className);
    setTimeout(() => element.classList.remove(className), 400);
}

// Fills a move grid: number, white move, black move per row. moves[i] is the
// text of ply i+1; startsWithBlack when the first stored ply is Black's.
// onClick, when given, makes each move a button that selects its ply.
function renderMoveGrid(grid, moves, startsWithBlack, currentPly, onClick) {
    grid.innerHTML = '';
    let moveNumber = startingMoveNumber(grid);
    let i = 0;
    const cell = (text, cls, ply) => {
        let el;
        if (onClick && ply !== undefined) {
            el = document.createElement('button');
            el.type = 'button';
            el.addEventListener('click', () => onClick(ply));
            el.setAttribute('aria-label', `Move ${ply}: ${text}`);
        } else {
            el = document.createElement('div');
        }
        el.className = cls;
        el.textContent = text;
        if (ply !== undefined) {
            el.dataset.ply = String(ply);
            if (ply === currentPly) el.setAttribute('aria-current', 'true');
        }
        return el;
    };
    if (startsWithBlack && moves.length > 0) {
        grid.appendChild(cell(`${moveNumber}.`, 'move-number'));
        grid.appendChild(cell('…', 'move-empty'));
        grid.appendChild(cell(moves[0], 'move move-black', 1));
        i = 1;
        moveNumber++;
    }
    for (; i < moves.length; i += 2) {
        grid.appendChild(cell(`${moveNumber}.`, 'move-number'));
        grid.appendChild(cell(moves[i], 'move move-white', i + 1));
        if (i + 1 < moves.length) {
            grid.appendChild(cell(moves[i + 1], 'move move-black', i + 2));
        } else {
            grid.appendChild(cell('', 'move-empty'));
        }
        moveNumber++;
    }
}

// The first move number of a line comes from its initial FEN; a grid carries
// it in data-start so a custom position numbers its moves as PGN does.
function startingMoveNumber(grid) {
    const n = parseInt(grid.dataset.start || '1', 10);
    return Number.isFinite(n) && n > 0 ? n : 1;
}

function setGridStart(grid, initialFen) {
    const parts = (initialFen || START_FEN).split(' ');
    grid.dataset.start = parts[5] || '1';
    return parts[1] === 'b';
}

function scrollMoveIntoView(grid, ply) {
    const el = grid.querySelector(`[data-ply="${ply}"]`);
    if (el) el.scrollIntoView({ block: 'nearest' });
    else grid.parentElement.scrollTop = 0;
}

// ------------------------------------------------------------ live game

function playerColor() {
    return live.isPlayerWhite ? 'w' : 'b';
}

function setNewGameModalBusy(busy) {
    live.newGameBusy = busy;
    for (const id of ['start-game-btn', 'cancel-btn', 'computer-level', 'search-time', 'starting-fen']) {
        $(id).disabled = busy;
    }
    document.querySelectorAll('input[name="player-color"]').forEach(r => r.disabled = busy);
}

function showNewGameModal() {
    if (replay.active) return;
    $('modal-overlay').classList.add('show');
    document.addEventListener('keydown', handleModalKeydown);
    $('start-game-btn').focus();
}

function hideNewGameModal() {
    $('modal-overlay').classList.remove('show');
    document.removeEventListener('keydown', handleModalKeydown);
    clearModalMessage('new-game-modal-message');
    setNewGameModalBusy(false);
}

function handleModalKeydown(e) {
    if (!$('modal-overlay').classList.contains('show')) return;
    if (live.newGameBusy) {
        if (e.key === 'Enter' || e.key === 'Escape') e.preventDefault();
        return;
    }
    const typing = document.activeElement?.id === 'starting-fen';
    switch (e.key) {
        case 'Enter':
            if (typing && e.shiftKey) return;
            e.preventDefault();
            startNewGame();
            break;
        case 'Escape':
            e.preventDefault();
            hideNewGameModal();
            break;
        case 'w': case 'W':
            if (typing) return;
            e.preventDefault();
            document.querySelector('input[name="player-color"][value="white"]').checked = true;
            break;
        case 'b': case 'B':
            if (typing) return;
            e.preventDefault();
            document.querySelector('input[name="player-color"][value="black"]').checked = true;
            break;
        case 'l': case 'L':
            if (typing) return;
            e.preventDefault();
            $('computer-level').focus();
            break;
        case 's': case 'S':
            if (typing) return;
            e.preventDefault();
            $('search-time').focus();
            break;
    }
}

async function startNewGame() {
    if (live.newGameBusy) return;

    const chosenColor = document.querySelector('input[name="player-color"]:checked').value;
    const computerLevel = parseInt($('computer-level').value, 10);
    const searchTime = parseInt($('search-time').value, 10);
    const startingFEN = $('starting-fen').value.trim();
    const willBePlayerWhite = chosenColor === 'white';

    const computer = { type: 2, level: computerLevel, searchTime };
    const requestBody = {
        white: willBePlayerWhite ? { type: 1 } : computer,
        black: willBePlayerWhite ? computer : { type: 1 },
    };
    if (startingFEN && startingFEN !== START_FEN) requestBody.fen = startingFEN;

    setNewGameModalBusy(true);
    clearModalMessage('new-game-modal-message');

    let response;
    try {
        response = await apiJSON('POST', '/api/games', requestBody);
    } catch (error) {
        const errorInfo = handleApiError('create game', error);
        setModalMessage('new-game-modal-message', errorInfo.statusMessage, 'error');
        setNewGameModalBusy(false);
        return;
    }

    if (!response.ok) {
        const err = await parseErrorResponse(response);
        const info = handleApiError('create game', null, response);
        setModalMessage('new-game-modal-message', err.details || err.error || info.statusMessage, 'error');
        setNewGameModalBusy(false);
        return;
    }

    let game;
    try {
        game = await response.json();
    } catch (error) {
        console.error('Create game: response OK but JSON parse failed:', error);
        setModalMessage('new-game-modal-message', 'Unexpected response from server', 'error');
        setNewGameModalBusy(false);
        return;
    }

    stopPolling();
    live.isPlayerWhite = willBePlayerWhite;
    live.gameId = game.gameId;
    live.initialFen = game.fen;
    live.level = computerLevel;
    live.moves = [];
    live.san = [];
    live.isLocked = false;
    live.selectedSquare = null;
    live.anonymous = !auth.token;
    buildBoard(!live.isPlayerWhite);
    updateGameDisplay(game);
    const computerTurn = live.isPlayerWhite ? 'b' : 'w';
    if (isPlayable(game.state) && game.turn === computerTurn) {
        triggerComputerMove();
    }

    setModalMessage('new-game-modal-message', `Game started — you play ${willBePlayerWhite ? 'White' : 'Black'}`, 'success');
    setTimeout(hideNewGameModal, MODAL_SUCCESS_DISPLAY_MS);
}

function handleSquareClick(e) {
    if (replay.active) return;
    if (!live.gameId) {
        showNewGameModal();
        return;
    }
    if (live.isLocked || !isPlayable(live.state)) return;

    const squareEl = e.currentTarget;
    const { square, pieceColor } = squareEl.dataset;
    if (live.turn !== playerColor()) return;

    if (live.selectedSquare) {
        const from = live.selectedSquare;
        board.squares.get(from)?.classList.remove('selected');
        live.selectedSquare = null;
        if (from !== square) handleHumanMove(from, square);
    } else if (pieceColor === playerColor()) {
        live.selectedSquare = square;
        squareEl.classList.add('selected');
    } else if (pieceColor) {
        flashErrorMessage('Not your piece');
        flashSquare(squareEl, false);
    }
}

// A pawn stepping or capturing onto its last rank needs a promotion piece; UCI
// requires the suffix (e7e8q), and the server rejects the bare move. Other
// moves go to the server as-is, which remains the only legality check.
function isPromotionMove(from, to) {
    const fromEl = board.squares.get(from);
    if (!fromEl || fromEl.dataset.pieceType !== 'p') return false;
    const fileDelta = Math.abs(from.charCodeAt(0) - to.charCodeAt(0));
    if (fileDelta > 1) return false;
    const color = fromEl.dataset.pieceColor;
    return (color === 'w' && from[1] === '7' && to[1] === '8') ||
        (color === 'b' && from[1] === '2' && to[1] === '1');
}

// Resolves with 'q', 'r', 'b' or 'n', or null when cancelled. Keyboard: Q/R/B/N
// pick, Escape cancels, Tab/Enter work on the focused button.
let promotionResolve = null;

function choosePromotion(color) {
    const overlay = $('promotion-overlay');
    const choices = overlay.querySelector('.promotion-choices');
    choices.classList.toggle('white', color === 'w');
    choices.classList.toggle('black', color !== 'w');
    overlay.classList.add('show');
    document.addEventListener('keydown', handlePromotionKeydown);
    overlay.querySelector('.promotion-choice[data-piece="q"]').focus();
    return new Promise(resolve => { promotionResolve = resolve; });
}

function closePromotion(piece) {
    $('promotion-overlay').classList.remove('show');
    document.removeEventListener('keydown', handlePromotionKeydown);
    const resolve = promotionResolve;
    promotionResolve = null;
    if (resolve) resolve(piece);
}

function handlePromotionKeydown(e) {
    const key = e.key.toLowerCase();
    if (key === 'q' || key === 'r' || key === 'b' || key === 'n') {
        e.preventDefault();
        closePromotion(key);
    } else if (e.key === 'Escape') {
        e.preventDefault();
        closePromotion(null);
    }
}

async function handleHumanMove(from, to) {
    let move = from + to;
    const fromEl = board.squares.get(from);
    const toEl = board.squares.get(to);

    if (isPromotionMove(from, to)) {
        const piece = await choosePromotion(fromEl.dataset.pieceColor);
        if (!piece) return;
        move += piece;
    }

    try {
        const response = await apiJSON('POST', `/api/games/${live.gameId}/moves`, { move });
        if (!response.ok) {
            if (response.status === 400) {
                const err = await parseErrorResponse(response);
                flashErrorMessage(err.error?.startsWith('promotion') ? 'Choose a promotion piece' : 'Invalid move');
                flashSquare(fromEl, false);
                flashSquare(toEl, false);
                renderLive();
                return;
            }
            handleApiError('move', null, response);
            return;
        }
        const game = await response.json();
        flashSquare(fromEl, true);
        flashSquare(toEl, true);
        updateGameDisplay(game);
        if (isPlayable(game.state)) triggerComputerMove();
    } catch (error) {
        handleApiError('move', error);
    }
}

async function triggerComputerMove() {
    lockBoard();
    try {
        const response = await apiJSON('POST', `/api/games/${live.gameId}/moves`, { move: 'cccc' });
        if (!response.ok) {
            handleApiError('trigger computer move', null, response);
            unlockBoard();
            return;
        }
        startPolling();
    } catch (error) {
        handleApiError('trigger computer move', error);
        unlockBoard();
    }
}

function startPolling() {
    live.polling = true;
    pollOnce();
}

function stopPolling() {
    live.polling = false;
    if (live.pollController) {
        live.pollController.abort();
        live.pollController = null;
    }
}

async function pollOnce() {
    if (!live.polling || !live.gameId || replay.active) return;

    const moveCount = live.moves.length;
    live.pollController = new AbortController();

    try {
        const response = await authFetch(
            `${API}/api/games/${live.gameId}?wait=true&moveCount=${moveCount}`,
            { signal: live.pollController.signal }
        );
        if (!live.polling) return;

        if (!response.ok) {
            handleApiError('poll game state', null, response);
            if (response.status === 404) {
                stopPolling();
                unlockBoard();
                flashErrorMessage('Game no longer exists');
                live.gameId = null;
                updateGameControls();
                return;
            }
            setTimeout(pollOnce, 2000);
            return;
        }

        const game = await response.json();
        setServerStatus('healthy');
        if (game.state !== 'pending') {
            stopPolling();
            updateGameDisplay(game);
            unlockBoard();
        } else {
            pollOnce();
        }
    } catch (error) {
        if (error.name === 'AbortError') return;
        handleApiError('poll game state', error);
        stopPolling();
        unlockBoard();
    }
}

function lockBoard() {
    live.isLocked = true;
    updateTurnStrip();
    updateGameControls();
}

function unlockBoard() {
    live.isLocked = false;
    updateTurnStrip();
    updateGameControls();
}

async function undoMoves() {
    if (live.isLocked || replay.active || !live.gameId) return;
    if (live.moves.length < 2) return;
    // A resignation or agreed draw stays on record when play continues.
    const conceded = isGameOver(live.state) &&
        (live.termination === 'resignation' || live.termination === 'agreement');

    try {
        const response = await apiJSON('POST', `/api/games/${live.gameId}/undo`, { count: 2 });
        if (!response.ok) {
            handleApiError('undo', null, response);
            return;
        }
        const game = await response.json();
        updateGameDisplay(game);
        if (game.state === 'stuck') {
            flashErrorMessage('Engine error — undo again or start a new game', 3000);
        } else if (conceded && game.concession) {
            const what = game.concession.termination === 'agreement' ? 'agreed draw' : 'resignation';
            toast(`Play continues; the ${what} stays on record`, 'info', 3000);
        }
    } catch (error) {
        handleApiError('undo', error);
    }
}

async function offerDraw() {
    if (live.actionBusy || !live.gameId || replay.active) return;
    live.actionBusy = true;
    updateGameControls();
    try {
        const response = await apiJSON('POST', `/api/games/${live.gameId}/draw`, { action: 'offer', color: playerColor() });
        if (response.status === 409) {
            flashErrorMessage('Make a move before offering again', 2500);
            return;
        }
        if (!response.ok) {
            handleApiError('offer draw', null, response);
            return;
        }
        const game = await response.json();
        if (game.drawOutcome === 'declined') toast('Draw declined', 'info', 2500);
        live.actionBusy = false;
        updateGameDisplay(game);
    } catch (error) {
        handleApiError('offer draw', error);
    } finally {
        live.actionBusy = false;
        updateGameControls();
    }
}

function showResignModal() {
    if (!live.gameId || isGameOver(live.state) || replay.active) return;
    $('resign-overlay').classList.add('show');
    document.addEventListener('keydown', handleResignKeydown);
    $('resign-cancel-btn').focus();
}

function hideResignModal() {
    $('resign-overlay').classList.remove('show');
    document.removeEventListener('keydown', handleResignKeydown);
}

function handleResignKeydown(e) {
    if (e.key === 'Escape') {
        e.preventDefault();
        hideResignModal();
    }
}

async function resignGame() {
    hideResignModal();
    if (live.actionBusy || !live.gameId) return;
    live.actionBusy = true;
    updateGameControls();
    try {
        const response = await apiJSON('POST', `/api/games/${live.gameId}/resign`, { color: playerColor() });
        if (!response.ok) {
            handleApiError('resign', null, response);
            return;
        }
        const game = await response.json();
        stopPolling(); // a computer move in flight is discarded by the server
        live.isLocked = false;
        live.actionBusy = false;
        updateGameDisplay(game);
    } catch (error) {
        handleApiError('resign', error);
    } finally {
        live.actionBusy = false;
        updateGameControls();
    }
}

// Applies a game response: state, board (unless a replay is showing), move
// list, controls, and a result message when the game just ended.
function updateGameDisplay(game) {
    const wasOver = isGameOver(live.state);
    live.fen = game.fen;
    live.turn = game.turn;
    live.state = game.state;
    live.termination = game.termination || '';
    live.moves = game.moves || [];
    live.san = Array.isArray(game.san) && game.san.length === live.moves.length ? game.san : [];
    live.lastMove = game.lastMove?.move || null;

    if (!replay.active) renderLive();
    renderLiveMoves();
    updateTurnStrip();
    updateGameControls();
    if (live.anonymous && live.gameId) recordRecentGame(game);
    if (!wasOver && isGameOver(game.state)) {
        toast(describeResult(game.state, game.termination), 'info', 4000);
    }
}

// Shows the live position on the shared board.
function renderLive() {
    if (board.flipped !== !live.isPlayerWhite) buildBoard(!live.isPlayerWhite);
    const mated = (live.state === 'white wins' || live.state === 'black wins') &&
        (live.termination || 'checkmate') === 'checkmate';
    renderPosition(live.fen, {
        lastMove: live.lastMove,
        matedColor: mated ? (live.state === 'white wins' ? 'b' : 'w') : null,
    });
}

function renderLiveMoves() {
    const grid = $('live-moves');
    const startsWithBlack = setGridStart(grid, startingFenOfLive());
    const texts = live.moves.map((uci, i) => live.san[i] || uci);
    renderMoveGrid(grid, texts, startsWithBlack, live.moves.length, null);
    const box = $('live-moves-box');
    box.scrollTop = box.scrollHeight;
}

// The position the live game started from; the create response's FEN.
function startingFenOfLive() {
    return live.initialFen || START_FEN;
}

function updateTurnStrip() {
    const dot = $('turn-dot');
    const text = $('turn-text');
    let status = 'idle', label = 'No game';
    let title = '';
    if (!live.gameId) {
        status = 'idle';
        label = 'No game';
    } else if (live.state === 'pending' || live.isLocked) {
        status = 'thinking';
        label = 'Thinking…';
        title = 'The computer is thinking';
    } else if (isGameOver(live.state)) {
        status = live.state === 'white wins' ? 'white-wins' : live.state === 'black wins' ? 'black-wins' : live.state;
        label = shortResult(live.state, live.termination);
        title = describeResult(live.state, live.termination);
    } else if (live.state === 'stuck') {
        status = 'stuck';
        label = 'Engine error';
        title = 'Engine error — undo or start a new game';
    } else {
        // The dot shows the side to move; the text says whose it is.
        status = live.turn === 'w' ? 'white' : 'black';
        const side = live.turn === 'w' ? 'White' : 'Black';
        label = live.turn === playerColor() ? 'Your move' : 'Computer';
        title = `${side} to move: ${live.turn === playerColor() ? 'you' : 'the computer'}`;
    }
    dot.dataset.status = status;
    text.textContent = label;
    $('turn-strip').title = title || label;
}

// Draw needs a game in progress with the board free; resigning only needs
// an unfinished game (also while the computer thinks or the engine is stuck).
function updateGameControls() {
    const hasGame = !!live.gameId;
    const idle = !live.isLocked && !live.actionBusy;
    $('undo-btn').disabled = !hasGame || live.isLocked || live.moves.length < 2;
    $('draw-btn').disabled = !hasGame || !idle || live.state !== 'ongoing';
    $('resign-btn').disabled = !hasGame || live.actionBusy || isGameOver(live.state);
    updateLiveTools();
}

function updateLiveTools() {
    const hasGame = !!live.gameId;
    $('copy-fen-btn').disabled = !hasGame;
    $('copy-pgn-btn').disabled = !hasGame;
    $('replay-live-btn').disabled = !hasGame || ui.storage !== 'ok';
    $('replay-live-btn').title = ui.storage === 'ok' ? 'Replay this game' : 'Replay needs stored games (storage is off)';
}

async function copyLiveFen() {
    if (!live.fen) return;
    if (await copyText(live.fen)) {
        markDone($('copy-fen-btn'));
        toast('FEN copied', 'success');
    } else {
        flashErrorMessage('Copy failed');
    }
}

// Copies the stored game's PGN; without storage, the SAN (or UCI) line and
// the current FEN as a plain-text fallback.
async function copyLivePgn() {
    if (!live.gameId) return;
    let text = null;
    try {
        const response = await authFetch(`${API}/api/games/${live.gameId}/pgn`);
        if (response.ok) text = await response.text();
    } catch {
        // fallback below
    }
    if (!text) text = localPgnText();
    if (await copyText(text)) {
        markDone($('copy-pgn-btn'));
        toast('PGN copied', 'success');
    } else {
        flashErrorMessage('Copy failed');
    }
}

function localPgnText() {
    const startsWithBlack = startingFenOfLive().split(' ')[1] === 'b';
    let number = parseInt(startingFenOfLive().split(' ')[5], 10) || 1;
    const parts = [];
    live.moves.forEach((uci, i) => {
        const text = live.san[i] || uci;
        const whiteMove = startsWithBlack ? i % 2 === 1 : i % 2 === 0;
        if (i === 0 && startsWithBlack) parts.push(`${number}...`);
        else if (whiteMove) parts.push(`${number}.`);
        parts.push(text);
        if (!whiteMove) number++;
    });
    let pgn = parts.join(' ');
    if (live.fen) pgn += `\n\n[FEN "${live.fen}"]`;
    return pgn.trim();
}

// --------------------------------------------- recent games on this device

// Games created without an account are listed from localStorage, since the
// server lists stored games per account only. The server deletes them 24
// hours after their last move, so entries expire on the same schedule.
function loadRecentGames() {
    let list = [];
    try {
        list = JSON.parse(localStorage.getItem(RECENT_GAMES_KEY) || '[]');
    } catch {
        list = [];
    }
    if (!Array.isArray(list)) list = [];
    const cutoff = Date.now() - RECENT_GAMES_TTL_MS;
    return list.filter(g => g && typeof g.gameId === 'string' && (g.lastActivity || 0) > cutoff);
}

function saveRecentGames(list) {
    try {
        localStorage.setItem(RECENT_GAMES_KEY, JSON.stringify(list.slice(0, RECENT_GAMES_MAX)));
    } catch {
        // storage blocked or full: the list is a convenience only
    }
}

function recordRecentGame(game) {
    const list = loadRecentGames();
    const existing = list.find(g => g.gameId === game.gameId);
    const now = Date.now();
    const entry = {
        gameId: game.gameId,
        startedAt: existing?.startedAt || now,
        lastActivity: now,
        initialFen: existing?.initialFen || startingFenOfLive(),
        playerColor: live.isPlayerWhite ? 'w' : 'b',
        level: live.level,
        moveCount: (game.moves || []).length,
        finalFen: game.fen,
        state: game.state,
        termination: game.termination || '',
    };
    saveRecentGames([entry, ...list.filter(g => g.gameId !== game.gameId)]);
}

// A local entry in the shape of a stored-game summary, for the shared row.
function recentToSummary(g) {
    const you = { type: 1, name: 'You' };
    const engine = { type: 2, level: g.level };
    const result = isGameOver(g.state) ? g.state.replace(' ', '_') : '';
    return {
        gameId: g.gameId,
        initialFen: g.initialFen,
        finalFen: g.finalFen,
        result,
        pgnResult: pgnResultOf(result),
        termination: g.termination,
        startTimeUtc: new Date(g.startedAt).toISOString(),
        moveCount: g.moveCount,
        players: {
            white: g.playerColor === 'w' ? you : engine,
            black: g.playerColor === 'b' ? you : engine,
        },
        local: true,
    };
}

function pgnResultOf(result) {
    switch (result) {
        case 'white_wins': return '1-0';
        case 'black_wins': return '0-1';
        case 'draw':
        case 'stalemate': return '1/2-1/2';
    }
    return '*';
}

// ------------------------------------------------------- My games drawer

function openGames({ push = true } = {}) {
    if (games.open) return;
    games.open = true;
    $('games-overlay').classList.add('show');
    document.addEventListener('keydown', handleGamesKeydown);
    renderGamesSections();
    if (auth.token) loadGames(true, games.loadedFor === auth.userId);
    if (push) pushURL();
    $('games-close').focus();
}

function closeGames({ push = true } = {}) {
    if (!games.open) return;
    games.open = false;
    $('games-overlay').classList.remove('show');
    document.removeEventListener('keydown', handleGamesKeydown);
    if (push) pushURL();
    $('games-btn').focus();
}

function handleGamesKeydown(e) {
    if (e.key === 'Escape') {
        e.preventDefault();
        closeGames();
    }
}

// The account changed while the drawer is open: reload for the new user, or
// fall back to the device list.
function refreshGamesForUser() {
    games.items = [];
    games.cursor = '';
    games.loadedFor = null;
    renderGamesSections();
    if (auth.token) loadGames(true);
}

function renderGamesSections() {
    const signedIn = !!auth.token;
    $('games-signin-note').classList.toggle('hidden', signedIn);
    $('games-filters').classList.toggle('hidden', !signedIn);
    $('account-games-heading').textContent = 'Your games';
    $('games-list').classList.toggle('hidden', !signedIn);
    $('games-more').classList.toggle('hidden', !signedIn || !games.cursor);
    if (!signedIn) $('games-status').textContent = '';
    renderLocalGames();
}

function setGamesFilter(kind, value) {
    document.querySelectorAll(`.seg[data-filter="${kind}"]`).forEach(b => {
        b.classList.toggle('active', b.dataset.value === value);
    });
    games[kind] = value;
    loadGames(true);
}

// Loads the first page (reset) or the next one. keep leaves the current
// rows in place until the fresh page arrives, for a reopened drawer.
async function loadGames(reset, keep = false) {
    if (!auth.token || games.loading) return;
    if (reset) {
        games.cursor = '';
        if (!keep) {
            games.items = [];
            renderGamesList();
        }
    }
    games.loading = true;
    games.loadedFor = auth.userId;
    const status = $('games-status');
    status.classList.remove('error');
    status.textContent = 'Loading…';
    $('games-more').disabled = true;

    const query = new URLSearchParams({ limit: String(GAMES_PAGE_SIZE) });
    if (games.status) query.set('status', games.status);
    if (games.color) query.set('color', games.color);
    if (!reset && games.cursor) query.set('cursor', games.cursor);

    let response;
    try {
        response = await authFetch(`${API}/api/users/me/games?${query}`);
    } catch {
        games.loading = false;
        status.classList.add('error');
        status.textContent = 'Connection failed. ';
        appendRetry(status, () => loadGames(reset));
        $('games-more').disabled = false;
        return;
    }
    games.loading = false;
    $('games-more').disabled = false;

    if (response.status === 401) {
        clearAuthState();
        return;
    }
    if (!response.ok) {
        const info = handleApiError('games', null, response);
        status.classList.add('error');
        status.textContent = `${info.statusMessage}. `;
        appendRetry(status, () => loadGames(reset));
        return;
    }

    let page;
    try {
        page = await response.json();
    } catch {
        status.classList.add('error');
        status.textContent = 'Unexpected response from server';
        return;
    }
    games.items = reset ? (page.games || []) : games.items.concat(page.games || []);
    games.cursor = page.nextCursor || '';
    status.textContent = '';
    renderGamesList();
}

function appendRetry(status, action) {
    const retry = document.createElement('button');
    retry.type = 'button';
    retry.className = 'btn btn-ghost btn-sm';
    retry.textContent = 'Retry';
    retry.addEventListener('click', action);
    status.appendChild(retry);
}

function renderGamesList() {
    const list = $('games-list');
    list.innerHTML = '';
    for (const item of games.items) list.appendChild(renderGameRow(item));
    const status = $('games-status');
    if (!games.loading && games.items.length === 0 && auth.token) {
        status.classList.remove('error');
        status.textContent = games.status || games.color ? 'No games match these filters.' :
            'No stored games yet. Finish a move in a new game and it will appear here.';
    }
    $('games-more').classList.toggle('hidden', !games.cursor);
}

function renderLocalGames() {
    const entries = loadRecentGames();
    const section = $('local-games-section');
    section.classList.toggle('hidden', entries.length === 0);
    const list = $('local-games-list');
    list.innerHTML = '';
    for (const entry of entries) list.appendChild(renderGameRow(recentToSummary(entry)));
}

// One game in the list: thumbnail, players, outcome, date, and a replay
// affordance. The whole row is a button.
function renderGameRow(summary) {
    const li = document.createElement('li');
    const row = document.createElement('button');
    row.type = 'button';
    row.className = 'game-row';
    if (summary.gameId === live.gameId) row.classList.add('current');
    row.dataset.gameId = summary.gameId;

    const mini = document.createElement('div');
    mini.className = 'mini-board';
    mini.setAttribute('aria-hidden', 'true');
    renderMiniBoard(mini, summary.finalFen);

    const info = document.createElement('div');
    info.className = 'game-info';
    const players = document.createElement('div');
    players.className = 'game-players';
    players.textContent = `${sideLabel(summary.players?.white)} vs ${sideLabel(summary.players?.black)}`;
    const outcome = document.createElement('div');
    outcome.className = 'game-outcome';
    const token = document.createElement('span');
    token.className = 'pgn-result';
    const ongoing = !summary.result;
    token.textContent = ongoing ? '*' : (summary.pgnResult || pgnResultOf(summary.result));
    outcome.appendChild(token);
    outcome.appendChild(document.createTextNode(' '));
    outcome.classList.toggle('ongoing', ongoing);
    const moves = Math.ceil((summary.moveCount || 0) / 2);
    outcome.appendChild(document.createTextNode(ongoing
        ? `Ongoing · ${moves} move${moves === 1 ? '' : 's'}`
        : `${terminationLabel(summary.termination) || describeResult(summary.result, summary.termination)} · ${moves} move${moves === 1 ? '' : 's'}`));
    const when = document.createElement('div');
    when.className = 'game-when';
    when.textContent = formatDate(summary.startTimeUtc) + (summary.gameId === live.gameId ? ' · this game' : '');
    info.append(players, outcome, when);

    const go = document.createElement('span');
    go.className = 'game-go';
    go.setAttribute('aria-hidden', 'true');
    go.innerHTML = '<svg class="icon" viewBox="0 0 24 24"><path d="M8 5v14l11-7z" fill="currentColor"/></svg>';

    row.append(mini, info, go);
    row.setAttribute('aria-label', `Replay ${players.textContent}, ${outcome.textContent}, ${when.textContent}`);
    row.addEventListener('click', () => enterReplay(summary.gameId, summary.moveCount || 0));
    li.appendChild(row);
    return li;
}

// A side's name in the list, "You" for the signed-in user's claim.
function sideLabel(player) {
    if (player && player.type !== 2 && auth.userId && player.claimedBy === auth.userId) return 'You';
    return playerLabel(player);
}

// --------------------------------------------------------------- replay

function isUUID(s) {
    return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s || '');
}

// Fetches a stored game's history, revalidating a cached copy with its ETag.
async function fetchHistory(gameId) {
    const cached = ui.historyCache.get(gameId);
    const headers = {};
    if (cached?.etag) headers['If-None-Match'] = cached.etag;
    const response = await authFetch(`${API}/api/games/${gameId}/history`, { headers });
    if (response.status === 304 && cached) return cached.history;
    if (!response.ok) {
        const info = handleApiError('history', null, response);
        throw new Error(info.statusMessage);
    }
    const history = await response.json();
    ui.historyCache.set(gameId, { etag: response.headers.get('ETag'), history });
    return history;
}

// Opens a stored game at a ply. The live game is suspended: its polling
// stops and its controls hide until exitReplay.
async function enterReplay(gameId, ply = 0, { push = true } = {}) {
    if (!isUUID(gameId)) {
        flashErrorMessage('Invalid game ID');
        return;
    }
    stopAutoplay();
    if (!replay.active) {
        stopPolling();
        replay.active = true;
        document.body.dataset.mode = 'replay';
        for (const s of board.squares.values()) s.classList.remove('selected');
        live.selectedSquare = null;
    }
    if (games.open) closeGames({ push: false });
    replay.loading = gameId;
    replay.gameId = gameId;
    replay.history = null;
    replay.ply = 0;
    renderReplayLoading();
    if (push) pushURL(); else replaceURL();

    let history;
    try {
        history = await fetchHistory(gameId);
    } catch (error) {
        if (replay.loading !== gameId) return; // superseded
        replay.loading = null;
        renderReplayError(error.message || 'Could not load the game');
        return;
    }
    if (replay.loading !== gameId || !replay.active) return;
    replay.loading = null;
    replay.history = history;
    replay.flipped = defaultFlip(history);
    buildBoard(replay.flipped);
    $('replay-flip').setAttribute('aria-pressed', replay.flipped ? 'true' : 'false');
    renderReplayHead();
    renderReplayMoves();
    gotoPly(ply, { announce: false });
}

// Black at the bottom when the viewer claimed Black, or when Black is the
// only human side (a computer took White).
function defaultFlip(history) {
    const white = history.players?.white, black = history.players?.black;
    if (auth.userId) {
        if (black?.claimedBy === auth.userId) return true;
        if (white?.claimedBy === auth.userId) return false;
    }
    if (history.gameId === live.gameId) return !live.isPlayerWhite;
    return white?.type === 2 && black?.type !== 2;
}

function exitReplay({ push = true } = {}) {
    if (!replay.active) return;
    stopAutoplay();
    replay.active = false;
    replay.loading = null;
    replay.gameId = null;
    replay.history = null;
    replay.ply = 0;
    document.body.dataset.mode = 'live';
    buildBoard(!live.isPlayerWhite);
    if (live.gameId) renderLive(); else renderPosition(START_FEN);
    $('ply-announcer').textContent = '';
    if (push) pushURL();
    // A computer move may have completed meanwhile.
    if (live.gameId && live.isLocked) startPolling();
    updateGameControls();
}

function renderReplayLoading() {
    $('replay-result').textContent = '';
    $('replay-ply').textContent = '';
    $('replay-players').textContent = 'Loading game…';
    $('replay-meta').textContent = replay.gameId || '';
    const grid = $('replay-moves');
    grid.innerHTML = '';
    $('replay-moves-empty').textContent = 'Loading…';
    setReplayNavEnabled(false);
    $('replay-exit').textContent = live.gameId ? 'Back to live game' : 'Close replay';
}

function renderReplayError(message) {
    $('replay-players').textContent = message;
    $('replay-meta').textContent = replay.gameId || '';
    $('replay-moves-empty').textContent = 'The stored game could not be loaded. It may have been deleted, or storage is off.';
    renderPosition(START_FEN);
    setReplayNavEnabled(false);
}

function setReplayNavEnabled(enabled) {
    for (const id of ['replay-first', 'replay-prev', 'replay-play', 'replay-next', 'replay-last',
        'replay-flip', 'replay-copy-fen', 'replay-copy-pgn', 'replay-download', 'replay-link', 'replay-speed']) {
        $(id).disabled = !enabled;
    }
}

function renderReplayHead() {
    const h = replay.history;
    const finished = !!h.result;
    $('replay-result').textContent = finished
        ? `${h.pgnResult} · ${terminationLabel(h.termination) || describeResult(h.result, h.termination)}`
        : 'Ongoing';
    $('replay-players').textContent = `${sideLabel(h.players?.white)} vs ${sideLabel(h.players?.black)}`;
    $('replay-players').title = `White: ${sideLabel(h.players?.white)} · Black: ${sideLabel(h.players?.black)}`;
    const plies = h.moves.length;
    const meta = [formatDate(h.startTimeUtc), `${plies} ${plies === 1 ? 'ply' : 'plies'}`];
    if (h.concession && h.concession.termination) {
        meta.push(h.concession.termination === 'agreement'
            ? `draw agreed at ply ${h.concession.ply}`
            : `resignation at ply ${h.concession.ply}`);
    }
    $('replay-meta').textContent = meta.join(' · ');
    $('replay-exit').textContent = live.gameId ? 'Back to live game' : 'Close replay';
    setReplayNavEnabled(true);
}

function renderReplayMoves() {
    const h = replay.history;
    const grid = $('replay-moves');
    const startsWithBlack = setGridStart(grid, h.initialFen);
    const texts = h.moves.map(m => m.san || m.moveUci);
    renderMoveGrid(grid, texts, startsWithBlack, replay.ply, ply => { stopAutoplay(); gotoPly(ply); });
    $('replay-moves-empty').textContent = 'No moves were played.';
}

// Shows the position after n plies; the stored FEN is assigned as is.
function gotoPly(n, { announce = true } = {}) {
    const h = replay.history;
    if (!h) return;
    const last = h.moves.length;
    n = Math.max(0, Math.min(last, Number.isFinite(n) ? n : 0));
    replay.ply = n;
    const fen = n === 0 ? h.initialFen : h.moves[n - 1].fenAfterMove;
    const lastMove = n > 0 ? h.moves[n - 1].moveUci : null;
    let matedColor = null;
    if (n === last && h.termination === 'checkmate') {
        matedColor = h.result === 'white_wins' ? 'b' : 'w';
    }
    renderPosition(fen, { lastMove, matedColor });

    const grid = $('replay-moves');
    grid.querySelectorAll('[aria-current]').forEach(el => el.removeAttribute('aria-current'));
    const current = grid.querySelector(`[data-ply="${n}"]`);
    if (current) current.setAttribute('aria-current', 'true');
    scrollMoveIntoView(grid, n);

    $('replay-ply').textContent = `${n} / ${last}`;
    $('replay-first').disabled = n === 0;
    $('replay-prev').disabled = n === 0;
    $('replay-next').disabled = n === last;
    $('replay-last').disabled = n === last;
    $('replay-play').disabled = last === 0;

    if (announce) $('ply-announcer').textContent = plyLabel(n);
    replaceURL();
}

function plyLabel(n) {
    const h = replay.history;
    if (!h || n === 0) return 'Start position';
    const move = h.moves[n - 1];
    const startsWithBlack = (h.initialFen || START_FEN).split(' ')[1] === 'b';
    const first = parseInt((h.initialFen || START_FEN).split(' ')[5], 10) || 1;
    const number = first + Math.floor((n - 1 + (startsWithBlack ? 1 : 0)) / 2);
    const dots = move.playerColor === 'b' ? '…' : '.';
    return `Ply ${n} of ${h.moves.length}: ${number}${dots} ${move.san || move.moveUci}`;
}

function stepReplay(delta) {
    if (!replay.history) return;
    gotoPly(replay.ply + delta);
}

function toggleAutoplay() {
    if (replay.timer) {
        stopAutoplay();
        return;
    }
    if (!replay.history || replay.history.moves.length === 0) return;
    if (replay.ply >= replay.history.moves.length) gotoPly(0);
    $('replay-play').setAttribute('aria-pressed', 'true');
    $('replay-play').setAttribute('aria-label', 'Pause');
    scheduleAutoplay();
}

function scheduleAutoplay() {
    replay.timer = setTimeout(() => {
        replay.timer = null;
        if (!replay.active || !replay.history) return;
        gotoPly(replay.ply + 1);
        if (replay.ply >= replay.history.moves.length) {
            stopAutoplay();
            return;
        }
        scheduleAutoplay();
    }, replay.speedMs);
}

function stopAutoplay() {
    if (replay.timer) {
        clearTimeout(replay.timer);
        replay.timer = null;
    }
    $('replay-play').setAttribute('aria-pressed', 'false');
    $('replay-play').setAttribute('aria-label', 'Autoplay');
}

function flipReplayBoard() {
    if (!replay.history) return;
    replay.flipped = !replay.flipped;
    $('replay-flip').setAttribute('aria-pressed', replay.flipped ? 'true' : 'false');
    buildBoard(replay.flipped);
    gotoPly(replay.ply, { announce: false });
}

function replayFen() {
    const h = replay.history;
    return replay.ply === 0 ? h.initialFen : h.moves[replay.ply - 1].fenAfterMove;
}

function replayPgnPath() {
    const h = replay.history;
    const full = replay.ply === h.moves.length;
    return `/api/games/${replay.gameId}/pgn${full ? '' : `?ply=${replay.ply}`}`;
}

async function copyReplayFen() {
    if (!replay.history) return;
    if (await copyText(replayFen())) {
        markDone($('replay-copy-fen'));
        toast('FEN copied', 'success');
    } else {
        flashErrorMessage('Copy failed');
    }
}

async function fetchReplayPgn() {
    const response = await authFetch(`${API}${replayPgnPath()}`);
    if (!response.ok) {
        handleApiError('pgn', null, response);
        return null;
    }
    const disposition = response.headers.get('Content-Disposition') || '';
    const match = /filename="([^"]+)"/.exec(disposition);
    return { text: await response.text(), filename: match ? match[1] : `chess-${replay.gameId.slice(0, 8)}.pgn` };
}

async function copyReplayPgn() {
    if (!replay.history) return;
    let pgn;
    try {
        pgn = await fetchReplayPgn();
    } catch (error) {
        handleApiError('pgn', error);
        return;
    }
    if (!pgn) return;
    if (await copyText(pgn.text)) {
        markDone($('replay-copy-pgn'));
        toast('PGN copied', 'success');
    } else {
        flashErrorMessage('Copy failed');
    }
}

// Saves the PGN through a temporary link to a blob: URL. In a sandboxed frame
// this needs the allow-downloads flag on the host's iframe.
async function downloadReplayPgn() {
    if (!replay.history) return;
    let pgn;
    try {
        pgn = await fetchReplayPgn();
    } catch (error) {
        handleApiError('pgn', error);
        return;
    }
    if (!pgn) return;
    const blob = new Blob([pgn.text], { type: 'application/x-chess-pgn' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = pgn.filename;
    a.className = 'visually-hidden';
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    markDone($('replay-download'));
    toast(`Saved ${pgn.filename}`, 'success');
}

async function copyReplayLink() {
    if (!replay.gameId) return;
    const link = `${location.origin}${location.pathname}?replay=${replay.gameId}&ply=${replay.ply}`;
    if (await copyText(link)) {
        markDone($('replay-link'));
        toast('Link copied', 'success');
    } else {
        flashErrorMessage('Copy failed');
    }
}

// -------------------------------------------------------------- routing

// The URL names the view: ?replay=<id>&ply=<n> for a replay, with &games
// while the drawer is open. Views push history entries; stepping through a
// replay replaces the entry, so back and forward move between views.
function urlForState() {
    const parts = [];
    if (replay.active && replay.gameId) {
        parts.push(`replay=${encodeURIComponent(replay.gameId)}`, `ply=${replay.ply}`);
    }
    if (games.open) parts.push('games');
    return location.pathname + (parts.length ? `?${parts.join('&')}` : '');
}

function pushURL() {
    try {
        if (urlForState() !== location.pathname + location.search) history.pushState({ chess: true }, '', urlForState());
    } catch { /* history blocked */ }
}

function replaceURL() {
    try {
        history.replaceState({ chess: true }, '', urlForState());
    } catch { /* history blocked */ }
}

// Brings the page to what the URL names, without touching history.
async function applyURL() {
    const params = new URLSearchParams(location.search);
    const id = params.get('replay');
    const ply = parseInt(params.get('ply') || '0', 10);
    const wantGames = params.has('games');

    if (id && isUUID(id)) {
        if (!replay.active || replay.gameId !== id) {
            await enterReplay(id, ply, { push: false });
        } else if (replay.history) {
            stopAutoplay();
            gotoPly(ply, { announce: false });
        }
    } else if (replay.active) {
        exitReplay({ push: false });
    }
    if (wantGames && !games.open) openGames({ push: false });
    else if (!wantGames && games.open) closeGames({ push: false });
}

// ------------------------------------------------------------- keyboard

function handleGlobalKeydown(e) {
    if (anyModalOpen()) return;
    const tag = document.activeElement?.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    if (!replay.active || !replay.history) return;
    switch (e.key) {
        case 'ArrowLeft':
            e.preventDefault(); stopAutoplay(); stepReplay(-1); break;
        case 'ArrowRight':
            e.preventDefault(); stopAutoplay(); stepReplay(1); break;
        case 'Home':
            e.preventDefault(); stopAutoplay(); gotoPly(0); break;
        case 'End':
            e.preventDefault(); stopAutoplay(); gotoPly(replay.history.moves.length); break;
        case ' ':
            e.preventDefault(); toggleAutoplay(); break;
        case 'f': case 'F':
            e.preventDefault(); flipReplayBoard(); break;
        case 'Escape':
            e.preventDefault(); exitReplay(); break;
    }
}

// ----------------------------------------------------------------- init

function bindEvents() {
    $('new-game-btn').addEventListener('click', showNewGameModal);
    $('undo-btn').addEventListener('click', undoMoves);
    $('draw-btn').addEventListener('click', offerDraw);
    $('resign-btn').addEventListener('click', showResignModal);
    $('resign-confirm-btn').addEventListener('click', resignGame);
    $('resign-cancel-btn').addEventListener('click', hideResignModal);
    $('start-game-btn').addEventListener('click', startNewGame);
    $('cancel-btn').addEventListener('click', hideNewGameModal);
    $('copy-fen-btn').addEventListener('click', copyLiveFen);
    $('copy-pgn-btn').addEventListener('click', copyLivePgn);
    $('replay-live-btn').addEventListener('click', () => {
        if (live.gameId) enterReplay(live.gameId, live.moves.length);
    });

    const levelSlider = $('computer-level');
    levelSlider.addEventListener('input', () => { $('level-value').textContent = levelSlider.value; });
    const timeSlider = $('search-time');
    timeSlider.addEventListener('input', () => { $('time-value').textContent = timeSlider.value; });

    $('auth-btn').addEventListener('click', handleAuthClick);
    document.querySelectorAll('.auth-tab').forEach(tab => {
        tab.addEventListener('click', () => switchAuthTab(tab.dataset.tab));
    });
    $('login-submit-btn').addEventListener('click', handleLogin);
    $('register-submit-btn').addEventListener('click', handleRegister);
    $('forgot-btn').addEventListener('click', () => switchAuthTab('recover'));
    $('recover-back-btn').addEventListener('click', () => switchAuthTab('login'));
    $('recover-submit-btn').addEventListener('click', handleRecover);
    $('reset-submit-btn').addEventListener('click', submitReset);
    $('reset-cancel-btn').addEventListener('click', closeResetModal);
    $('auth-cancel-btn').addEventListener('click', hideAuthModal);
    $('auth-cancel-btn-2').addEventListener('click', hideAuthModal);
    $('logout-btn').addEventListener('click', handleLogoutClick);
    $('account-cancel-btn').addEventListener('click', hideAccountModal);
    $('delete-account-btn').addEventListener('click', deleteAccount);
    document.querySelectorAll('.promotion-choice').forEach(btn => {
        btn.addEventListener('click', () => closePromotion(btn.dataset.piece));
    });
    $('promotion-cancel-btn').addEventListener('click', () => closePromotion(null));
    $('promotion-overlay').addEventListener('click', (e) => {
        if (e.target === e.currentTarget) closePromotion(null);
    });

    $('games-btn').addEventListener('click', () => { if (games.open) closeGames(); else openGames(); });
    $('games-close').addEventListener('click', () => closeGames());
    $('games-overlay').addEventListener('click', (e) => {
        if (e.target === e.currentTarget) closeGames();
    });
    $('games-more').addEventListener('click', () => loadGames(false));
    document.querySelectorAll('.seg').forEach(seg => {
        seg.addEventListener('click', () => setGamesFilter(seg.dataset.filter, seg.dataset.value));
    });

    $('replay-first').addEventListener('click', () => { stopAutoplay(); gotoPly(0); });
    $('replay-prev').addEventListener('click', () => { stopAutoplay(); stepReplay(-1); });
    $('replay-next').addEventListener('click', () => { stopAutoplay(); stepReplay(1); });
    $('replay-last').addEventListener('click', () => { stopAutoplay(); gotoPly(replay.history?.moves.length || 0); });
    $('replay-play').addEventListener('click', toggleAutoplay);
    $('replay-speed').addEventListener('change', () => {
        replay.speedMs = parseInt($('replay-speed').value, 10) || 1000;
        if (replay.timer) {
            clearTimeout(replay.timer);
            replay.timer = null;
            scheduleAutoplay();
        }
    });
    $('replay-flip').addEventListener('click', flipReplayBoard);
    $('replay-copy-fen').addEventListener('click', copyReplayFen);
    $('replay-copy-pgn').addEventListener('click', copyReplayPgn);
    $('replay-download').addEventListener('click', downloadReplayPgn);
    $('replay-link').addEventListener('click', copyReplayLink);
    $('replay-exit').addEventListener('click', () => exitReplay());

    document.addEventListener('keydown', handleGlobalKeydown);
    window.addEventListener('popstate', () => { applyURL(); });
}

document.addEventListener('DOMContentLoaded', async () => {
    bindEvents();
    buildBoard(false);
    renderPosition(START_FEN);
    updateTurnStrip();
    updateGameControls();
    startHealthCheck();
    // A reset link: take the token off the address before anything else, so
    // it is neither kept in history nor shared with a copied link.
    const params = new URLSearchParams(location.search);
    const resetToken = params.get('reset');
    if (resetToken !== null) {
        params.delete('reset');
        const query = params.toString();
        try {
            history.replaceState({ chess: true }, '', location.pathname + (query ? `?${query}` : ''));
        } catch { /* history blocked */ }
    }
    await restoreAuthSession();
    await applyURL();
    if (resetToken) openResetModal(resetToken);
});

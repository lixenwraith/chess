// Game state management
let gameState = {
    gameId: null,
    fen: null,
    turn: 'w',
    isPlayerWhite: true,
    isLocked: false,
    polling: false,
    pollController: null,
    apiUrl: '',
    selectedSquare: null,
    healthCheckInterval: null,
    networkError: false,
    moveList: [],
    authToken: null,
    userId: null,
    username: null,
};

// Chess piece Unicode: all black pieces for better fill, white pawn due to inability to override emoji variant display
const pieceMap = {
    'p': '♙', 'r': '♜', 'n': '♞', 'b': '♝', 'q': '♛', 'k': '♚',
    'P': '♙', 'R': '♜', 'N': '♞', 'B': '♝', 'Q': '♛', 'K': '♚'
};

// Initialize on page load
document.addEventListener('DOMContentLoaded', async () => {
    const config = await getConfig();
    gameState.apiUrl = config.apiUrl;

    // Check for existing session on load
    restoreAuthSession();

    document.getElementById('new-game-btn').addEventListener('click', showNewGameModal);
    document.getElementById('undo-btn').addEventListener('click', undoMoves);
    document.getElementById('start-game-btn').addEventListener('click', startNewGame);
    document.getElementById('cancel-btn').addEventListener('click', hideNewGameModal);
    document.getElementById('copy-history').addEventListener('click', copyHistory);

    const levelSlider = document.getElementById('computer-level');
    const levelValue = document.getElementById('level-value');
    levelSlider.addEventListener('input', () => { levelValue.textContent = levelSlider.value; });

    const timeSlider = document.getElementById('search-time');
    const timeValue = document.getElementById('time-value');
    timeSlider.addEventListener('input', () => { timeValue.textContent = timeSlider.value; });

    startHealthCheck();
    // Don't auto-show modal on load
});

document.getElementById('auth-indicator').addEventListener('click', handleAuthClick);
document.querySelectorAll('.auth-tab').forEach(tab => {
    tab.addEventListener('click', (e) => switchAuthTab(e.target.dataset.tab));
});
document.getElementById('login-submit-btn').addEventListener('click', handleLogin);
document.getElementById('register-submit-btn').addEventListener('click', handleRegister);
document.getElementById('auth-cancel-btn').addEventListener('click', hideAuthModal);
document.getElementById('auth-cancel-btn-2').addEventListener('click', hideAuthModal);


// Auth functions
function restoreAuthSession() {
    const token = localStorage.getItem('authToken');
    if (token) {
        gameState.authToken = token;
        validateSession();
    }
}

async function validateSession() {
    try {
        const response = await authFetch(`${gameState.apiUrl}/api/v1/auth/me`);
        if (response.ok) {
            const user = await response.json();
            gameState.userId = user.userId;
            gameState.username = user.username;
            updateAuthIndicator(true);
        } else {
            clearAuthState();
        }
    } catch {
        clearAuthState();
    }
}

function clearAuthState() {
    gameState.authToken = null;
    gameState.userId = null;
    gameState.username = null;
    localStorage.removeItem('authToken');
    updateAuthIndicator(false);
}

function updateAuthIndicator(authenticated) {
    const indicator = document.getElementById('auth-indicator');
    const light = indicator.querySelector('.light');
    if (authenticated) {
        light.setAttribute('data-status', 'authenticated');
        indicator.setAttribute('data-status', gameState.username);
    } else {
        light.setAttribute('data-status', 'anonymous');
        indicator.setAttribute('data-status', 'anonymous');
    }
}

function handleAuthClick() {
    if (gameState.authToken) {
        // Logged in - confirm logout
        if (confirm(`Logout ${gameState.username}?`)) {
            handleLogout();
        }
    } else {
        showAuthModal();
    }
}

function showAuthModal() {
    document.getElementById('auth-modal-overlay').classList.add('show');
    document.getElementById('login-identifier').focus();
    // Remove first to prevent duplicate registrations
    document.removeEventListener('keydown', handleAuthModalKeydown);
    document.addEventListener('keydown', handleAuthModalKeydown);
}

function hideAuthModal() {
    document.getElementById('auth-modal-overlay').classList.remove('show');
    document.querySelectorAll('.auth-form input').forEach(input => input.value = '');
    document.removeEventListener('keydown', handleAuthModalKeydown);
}

function handleAuthModalKeydown(e) {
    const modal = document.getElementById('auth-modal-overlay');
    if (!modal.classList.contains('show')) return;

    if (e.key === 'Escape') {
        e.preventDefault();
        hideAuthModal();
    } else if (e.key === 'Enter') {
        e.preventDefault();
        const loginForm = document.getElementById('login-form');
        if (loginForm.style.display !== 'none') {
            handleLogin();
        } else {
            handleRegister();
        }
    }
}

function switchAuthTab(tab) {
    document.querySelectorAll('.auth-tab').forEach(t => t.classList.remove('active'));
    document.querySelector(`[data-tab="${tab}"]`).classList.add('active');

    document.getElementById('login-form').style.display = tab === 'login' ? 'block' : 'none';
    document.getElementById('register-form').style.display = tab === 'register' ? 'block' : 'none';
}

// Shared helper: safely parse error response regardless of Content-Type
async function parseErrorResponse(response) {
    try {
        return await response.json();
    } catch {
        return { error: `Server error (${response.status})`, details: null };
    }
}

async function handleLogin() {
    const identifier = document.getElementById('login-identifier').value.trim();
    const password = document.getElementById('login-password').value;

    if (!identifier || !password) {
        flashErrorMessage('Fill all fields');
        return;
    }

    const submitBtn = document.getElementById('login-submit-btn');
    submitBtn.disabled = true;

    try {
        const response = await fetch(`${gameState.apiUrl}/api/v1/auth/login`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ identifier, password })
        });

        if (!response.ok) {
            const err = await parseErrorResponse(response);
            flashErrorMessage(err.details || err.error || 'Login failed', 3000);
            return;
        }

        const auth = await response.json();
        gameState.authToken = auth.token;
        gameState.userId = auth.userId;
        gameState.username = auth.username;
        localStorage.setItem('authToken', auth.token);
        updateAuthIndicator(true);
        hideAuthModal();
    } catch (error) {
        flashErrorMessage('Connection failed');
    } finally {
        submitBtn.disabled = false;
    }
}

async function handleRegister() {
    const username = document.getElementById('register-username').value.trim();
    const email = document.getElementById('register-email').value.trim();
    const password = document.getElementById('register-password').value;

    if (!username || !password) {
        flashErrorMessage('Username and password required');
        return;
    }
    if (password.length < 8) {
        flashErrorMessage('Password min 8 chars');
        return;
    }
    if (!/[a-zA-Z]/.test(password) || !/[0-9]/.test(password)) {
        flashErrorMessage('Password needs a letter and number');
        return;
    }

    const submitBtn = document.getElementById('register-submit-btn');
    submitBtn.disabled = true;

    try {
        const body = { username, password };
        if (email) body.email = email;

        const response = await fetch(`${gameState.apiUrl}/api/v1/auth/register`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });

        if (!response.ok) {
            const err = await parseErrorResponse(response);
            flashErrorMessage(err.details || err.error || 'Registration failed', 3000);
            return;
        }

        const auth = await response.json();
        gameState.authToken = auth.token;
        gameState.userId = auth.userId;
        gameState.username = auth.username;
        localStorage.setItem('authToken', auth.token);
        updateAuthIndicator(true);
        hideAuthModal();
    } catch (error) {
        flashErrorMessage('Connection failed');
    } finally {
        submitBtn.disabled = false;
    }
}

async function handleLogout() {
    if (gameState.authToken) {
        try {
            await authFetch(`${gameState.apiUrl}/api/v1/auth/logout`, { method: 'POST' });
        } catch {
            // Ignore - clear local state regardless
        }
    }
    clearAuthState();
}

// Wrapper for authenticated requests
function authFetch(url, options = {}) {
    if (gameState.authToken) {
        options.headers = {
            ...options.headers,
            'Authorization': `Bearer ${gameState.authToken}`
        };
    }
    return fetch(url, options);
}

async function getConfig() {
    return { apiUrl: '/chess' };
}

function startHealthCheck() {
    const checkHealth = async () => {
        try {
            const response = await fetch(`${gameState.apiUrl}/health`);
            if (response.ok) {
                const health = await response.json();
                updateServerIndicator(health.status === 'healthy' ? 'healthy' : 'degraded');
                updateStorageIndicator(health.storage || 'unknown');
                gameState.networkError = false;
            } else {
                handleApiError('health check', null, response);
                updateStorageIndicator('unknown');
            }
        } catch (error) {
            handleApiError('health check', error);
            updateStorageIndicator('unknown');
        }
    };

    checkHealth();
    gameState.healthCheckInterval = setInterval(checkHealth, 10000);
}

function updateServerIndicator(status, message = null) {
    const indicator = document.getElementById('server-indicator');
    const light = indicator.querySelector('.light');
    light.setAttribute('data-status', status);
    // Set custom tooltip if message provided
    if (message) {
        indicator.setAttribute('data-status', message);
    } else {
        // Default messages
        const defaultMessages = {
            'healthy': 'healthy',
            'degraded': 'degraded',
            'unknown': 'unknown'
        };
        indicator.setAttribute('data-status', defaultMessages[status] || status);
    }
}

function updateStorageIndicator(status) {
    const indicator = document.getElementById('storage-indicator');
    const light = indicator.querySelector('.light');
    light.setAttribute('data-status', status);
    indicator.setAttribute('data-status', status);
}

function updateTurnIndicator(state, turn) {
    const indicator = document.getElementById('turn-indicator');
    const light = indicator.querySelector('.light');

    let status = '';
    let tooltipText = '';

    if (state === 'pending' || gameState.isLocked) {
        status = 'thinking';
        tooltipText = 'Computer Thinking';
    } else if (state && isGameOver(state)) {
        switch(state) {
            case 'white wins':
                status = 'white-wins';
                tooltipText = 'White Wins';
                break;
            case 'black wins':
                status = 'black-wins';
                tooltipText = 'Black Wins';
                break;
            case 'stalemate':
                status = 'stalemate';
                tooltipText = 'Stalemate';
                break;
            case 'draw':
                status = 'draw';
                tooltipText = 'Draw';
                break;
            default:
                status = 'unknown';
                tooltipText = 'Game Over';
        }
    } else if (turn === 'w') {
        status = 'white';
        tooltipText = 'White';
    } else if (turn === 'b') {
        status = 'black';
        tooltipText = 'Black';
    } else {
        status = 'unknown';
        tooltipText = 'Unknown';
    }

    light.setAttribute('data-status', status);
    indicator.setAttribute('data-status', tooltipText);
}

function showNewGameModal() {
    const modal = document.getElementById('modal-overlay');
    modal.classList.add('show');
    setupModalKeyboardNav();
}

function hideNewGameModal() {
    const modal = document.getElementById('modal-overlay');
    modal.classList.remove('show');
    teardownModalKeyboardNav();
}

function setupModalKeyboardNav() {
    document.addEventListener('keydown', handleModalKeydown);
}

function teardownModalKeyboardNav() {
    document.removeEventListener('keydown', handleModalKeydown);
}

function handleModalKeydown(e) {
    const modal = document.getElementById('modal-overlay');
    if (!modal.classList.contains('show')) return;

    switch(e.key) {
        case 'Enter':
            e.preventDefault();
            startNewGame();
            break;
        case 'Escape':
            e.preventDefault();
            hideNewGameModal();
            break;
        case 'w':
        case 'W':
            e.preventDefault();
            document.querySelector('input[name="player-color"][value="white"]').checked = true;
            break;
        case 'b':
        case 'B':
            e.preventDefault();
            document.querySelector('input[name="player-color"][value="black"]').checked = true;
            break;
        case 'l':
        case 'L':
            e.preventDefault();
            document.getElementById('computer-level').focus();
            break;
        case 's':
        case 'S':
            e.preventDefault();
            document.getElementById('search-time').focus();
            break;
        case 'ArrowLeft':
            handleSliderNav(e, -1);
            break;
        case 'ArrowRight':
            handleSliderNav(e, 1);
            break;
    }
}

function handleSliderNav(e, direction) {
    const activeEl = document.activeElement;
    if (activeEl.id === 'computer-level') {
        e.preventDefault();
        activeEl.value = Math.max(0, Math.min(20, parseInt(activeEl.value) + direction));
        activeEl.dispatchEvent(new Event('input'));
    } else if (activeEl.id === 'search-time') {
        e.preventDefault();
        activeEl.value = Math.max(100, Math.min(10000, parseInt(activeEl.value) + direction * 100));
        activeEl.dispatchEvent(new Event('input'));
    }
}

function copyHistory() {
    const moves = gameState.moveList;
    let pgn = '';
    for (let i = 0; i < moves.length; i++) {
        if (i % 2 === 0) {
            pgn += `${Math.floor(i / 2) + 1}. `;
        }
        pgn += moves[i] + ' ';
    }

    if (gameState.fen) {
        pgn += `\n\n[FEN "${gameState.fen}"]`;
    }

    navigator.clipboard.writeText(pgn.trim()).then(() => {
        const btn = document.getElementById('copy-history');
        btn.classList.add('copied');
        setTimeout(() => {
            btn.classList.remove('copied');
        }, 2000);
    });
}

async function startNewGame() {
    const playerColor = document.querySelector('input[name="player-color"]:checked').value;
    const computerLevel = parseInt(document.getElementById('computer-level').value);
    const searchTime = parseInt(document.getElementById('search-time').value);
    const startingFEN = document.getElementById('starting-fen').value.trim();
    gameState.isPlayerWhite = (playerColor === 'white');

    const whiteConfig = gameState.isPlayerWhite ? { type: 1 } : { type: 2, level: computerLevel, searchTime: searchTime };
    const blackConfig = gameState.isPlayerWhite ? { type: 2, level: computerLevel, searchTime: searchTime } : { type: 1 };

    const requestBody = {
        white: whiteConfig,
        black: blackConfig
    };

    const defaultFEN = 'rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1';
    if (startingFEN && startingFEN !== defaultFEN) {
        requestBody.fen = startingFEN;
    }

    try {
        const response = await authFetch(`${gameState.apiUrl}/api/v1/games`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(requestBody)
        });
        if (!response.ok) {
            const errorInfo = handleApiError('create game', null, response);
            throw new Error(errorInfo.statusMessage);
        }

        const game = await response.json();
        gameState.gameId = game.gameId;
        gameState.moveList = [];
        hideNewGameModal();
        initializeBoard();
        updateGameDisplay(game);
        document.getElementById('undo-btn').disabled = true;
        if (!gameState.isPlayerWhite) triggerComputerMove();

    } catch (error) {
        if (error.message === 'Failed to fetch') {
            handleApiError('create game', error);
        } else {
            flashErrorMessage(error.message);
        }
        updateTurnIndicator('', '');
    }
}

function initializeBoard() {
    const boardEl = document.getElementById('board');
    boardEl.innerHTML = '';
    const isBlackPov = !gameState.isPlayerWhite;

    // Update coordinate labels based on perspective
    const topCoords = document.querySelector('.coordinates.top');
    const leftCoords = document.querySelector('.coordinates.left');

    if (isBlackPov) {
        topCoords.innerHTML = '<span>h</span><span>g</span><span>f</span><span>e</span><span>d</span><span>c</span><span>b</span><span>a</span>';
        leftCoords.innerHTML = '<span>1</span><span>2</span><span>3</span><span>4</span><span>5</span><span>6</span><span>7</span><span>8</span>';
    } else {
        topCoords.innerHTML = '<span>a</span><span>b</span><span>c</span><span>d</span><span>e</span><span>f</span><span>g</span><span>h</span>';
        leftCoords.innerHTML = '<span>8</span><span>7</span><span>6</span><span>5</span><span>4</span><span>3</span><span>2</span><span>1</span>';
    }

    for (let i = 0; i < 64; i++) {
        const square = document.createElement('div');
        const rank = 7 - Math.floor(i / 8);
        const file = i % 8;
        const squareName = `${String.fromCharCode(97 + file)}${rank + 1}`;

        const displayRank = isBlackPov ? 7 - rank : rank;
        const displayFile = isBlackPov ? 7 - file : file;

        square.className = `square ${(displayRank + displayFile) % 2 === 0 ? 'dark' : 'light'}`;
        square.dataset.square = squareName;
        square.addEventListener('click', handleSquareClick);
        boardEl.appendChild(square);
    }
}

function renderBoardFromFEN(fen) {
    const fenBoard = fen.split(' ')[0];

    // Clear board and remove checkmate indicators
    document.querySelectorAll('.square').forEach(s => {
        s.textContent = '';
        s.classList.remove('white-piece', 'black-piece', 'mated-king');
        delete s.dataset.pieceColor;
    });

    let rank = 7, file = 0;
    for (const char of fenBoard) {
        if (char === '/') {
            rank--; file = 0;
        } else if (/\d/.test(char)) {
            file += parseInt(char, 10);
        } else {
            const squareName = `${String.fromCharCode(97 + file)}${rank + 1}`;
            const squareEl = document.querySelector(`[data-square="${squareName}"]`);
            if (squareEl) {
                const pieceColor = (char === char.toUpperCase()) ? 'w' : 'b';
                squareEl.textContent = pieceMap[char === 'P' ? 'P' : char.toLowerCase()] || '';
                squareEl.classList.add(pieceColor === 'w' ? 'white-piece' : 'black-piece');
                squareEl.dataset.pieceColor = pieceColor;
                squareEl.dataset.pieceType = char.toLowerCase();
            }
            file++;
        }
    }
}

function handleSquareClick(e) {
    if (gameState.isLocked) return;

    // Block moves after game over
    if (isGameOver(gameState.state)) return;

    const squareEl = e.currentTarget;
    const { square, pieceColor } = squareEl.dataset;
    const playerTurnColor = gameState.isPlayerWhite ? 'w' : 'b';

    if (gameState.turn !== playerTurnColor) return;

    if (gameState.selectedSquare) {
        const from = gameState.selectedSquare;
        const fromEl = document.querySelector(`[data-square="${from}"]`);
        fromEl.classList.remove('selected');
        gameState.selectedSquare = null;

        if (from !== square) {
            handleHumanMove(from, square);
        }
    } else if (pieceColor === playerTurnColor) {
        gameState.selectedSquare = square;
        squareEl.classList.add('selected');
    } else {
        flashErrorMessage('Invalid Piece Selection');
        // Flash red for invalid piece selection
        flashSquare(squareEl, false);
    }
}

function flashSquare(element, success = true) {
    const className = success ? 'flash-green' : 'flash-red';
    element.classList.add(className);
    setTimeout(() => element.classList.remove(className), 400);
}

async function handleHumanMove(from, to) {
    const move = from + to;
    const fromEl = document.querySelector(`[data-square="${from}"]`);
    const toEl = document.querySelector(`[data-square="${to}"]`);

    try {
        const response = await authFetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}/moves`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ move })
        });

        const game = await response.json();
        if (!response.ok) {
            // Handle client errors differently - these aren't network issues
            if (response.status === 400) {
                // Invalid move - flash message and squares
                // Handled early, not shown as server error, and bypasses handleApiError
                flashErrorMessage('Invalid Move');
                flashSquare(fromEl, false);
                flashSquare(toEl, false);
                renderBoardFromFEN(gameState.fen);
                return;
            }
            // Other errors use error handler
            handleApiError('move', null, response);
            return;
        }

        flashSquare(fromEl, true);
        flashSquare(toEl, true);
        updateGameDisplay(game);
        if (!isGameOver(game.state)) {
            triggerComputerMove();
        }
    } catch (error) {
        handleApiError('move', error);
    }
}

async function triggerComputerMove() {
    lockBoard();
    try {
        const response = await authFetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}/moves`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ move: 'cccc' })
        });

        if (!response.ok) {
            handleApiError('trigger computer move', null, response);
            unlockBoard();
            return;
        }

        gameState.networkError = false;
        startPolling();
    } catch (error) {
        handleApiError('trigger computer move', error);
        unlockBoard();
    }
}

function startPolling() {
    gameState.polling = true;
    pollOnce();
}

function stopPolling() {
    gameState.polling = false;
    if (gameState.pollController) {
        gameState.pollController.abort();
        gameState.pollController = null;
    }
}

async function pollOnce() {
    if (!gameState.polling || !gameState.gameId) return;

    const moveCount = (gameState.moveList || []).length;
    gameState.pollController = new AbortController();

    try {
        const response = await fetch(
            `${gameState.apiUrl}/api/v1/games/${gameState.gameId}?wait=true&moveCount=${moveCount}`,
            { signal: gameState.pollController.signal }
        );

        if (!gameState.polling) return;

        if (!response.ok) {
            const errorInfo = handleApiError('poll game state', null, response);
            if (response.status === 404) {
                stopPolling();
                unlockBoard();
                flashErrorMessage('Game no longer exists');
                gameState.gameId = null;
                return;
            }
            // Retry after delay for transient errors
            setTimeout(pollOnce, 2000);
            return;
        }

        const game = await response.json();
        gameState.networkError = false;
        updateServerIndicator('healthy');

        if (game.state !== 'pending') {
            stopPolling();
            updateGameDisplay(game);
            unlockBoard();
        } else {
            // Still pending, long-poll again
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
    gameState.isLocked = true;
    updateTurnIndicator('pending', gameState.turn);
}

function unlockBoard() {
    gameState.isLocked = false;
    updateTurnIndicator('', gameState.turn);
}

async function undoMoves() {
    if (gameState.isLocked) return;

    if (!gameState.moveList || gameState.moveList.length < 2) {
        console.log('No moves to undo');
        return;
    }

    try {
        const response = await fetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}/undo`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ count: 2 })
        });

        if (!response.ok) {
            const errorInfo = handleApiError('undo', null, response);
            // For client errors like "no moves to undo", don't throw
            if (errorInfo.isClientError) {
                console.log('Undo failed:', errorInfo.statusMessage);
                return;
            }
            throw new Error(errorInfo.statusMessage);
        }

        const game = await response.json();
        gameState.state = game.state;
        updateGameDisplay(game);
    } catch (error) {
        if (error.message === 'Failed to fetch') {
            handleApiError('undo', error);
        }
    }
}

function renderMoveHistory(moves) {
    const grid = document.getElementById('move-grid');
    grid.innerHTML = '';

    let startsWithBlack = false;
    if (gameState.fen) {
        const fenParts = gameState.fen.split(' ');
        const moveNum = parseInt(fenParts[5]) || 1;
        const activeColor = fenParts[1];
        startsWithBlack = (moveNum === 1 && activeColor === 'b' && moves.length === 0);
    }

    for (let i = 0; i < moves.length; i++) {
        const isWhiteMove = (i % 2 === 0);
        const moveNumber = Math.floor(i / 2) + 1;

        if (i === 0 || i % 2 === 0) {
            const numEl = document.createElement('div');
            numEl.className = 'move-number';
            numEl.textContent = moveNumber + '.';
            grid.appendChild(numEl);

            const whiteEl = document.createElement('div');
            if (isWhiteMove && !startsWithBlack) {
                whiteEl.className = 'move-white';
                whiteEl.textContent = moves[i];
            } else if (!isWhiteMove && startsWithBlack) {
                whiteEl.className = 'move-empty';
                whiteEl.textContent = '...';
            } else {
                whiteEl.className = 'move-empty';
                whiteEl.textContent = '';
            }
            grid.appendChild(whiteEl);

            const blackEl = document.createElement('div');
            if (i + 1 < moves.length && !startsWithBlack) {
                blackEl.className = 'move-black';
                blackEl.textContent = moves[i + 1];
                i++;
            } else if (isWhiteMove && startsWithBlack) {
                blackEl.className = 'move-black';
                blackEl.textContent = moves[i];
            } else {
                blackEl.className = 'move-empty';
                blackEl.textContent = '';
            }
            grid.appendChild(blackEl);
        }
    }

    const historyContainer = document.getElementById('move-history');
    historyContainer.scrollTop = historyContainer.scrollHeight;
}

function updateGameDisplay(game) {
    gameState.fen = game.fen;
    gameState.turn = game.turn;
    gameState.state = game.state;
    gameState.moveList = game.moves || [];

    renderBoardFromFEN(game.fen);
    updateTurnIndicator(game.state, game.turn);

    // Clear previous checkmate indicators
    document.querySelectorAll('.mated-king').forEach(el => {
        el.classList.remove('mated-king');
    });

    // Highlight last move
    document.querySelectorAll('.last-move-from, .last-move-to').forEach(el => {
        el.classList.remove('last-move-from', 'last-move-to');
    });

    if (game.lastMove && game.lastMove.move) {
        const from = game.lastMove.move.substring(0, 2);
        const to = game.lastMove.move.substring(2, 4);
        const fromEl = document.querySelector(`[data-square="${from}"]`);
        const toEl = document.querySelector(`[data-square="${to}"]`);
        if (fromEl) fromEl.classList.add('last-move-from');
        if (toEl) toEl.classList.add('last-move-to');
    }

    // Update move history
    renderMoveHistory(game.moves || []);

    // Update undo button
    document.getElementById('undo-btn').disabled = !game.moves || game.moves.length < 2;

    // Handle checkmate visually
    if (game.state === 'white wins' || game.state === 'black wins') {
        markMatedKing(game);
    }
}

function markMatedKing(game) {
    // Find and mark the mated king
    const matedColor = game.state === 'white wins' ? 'b' : 'w';
    document.querySelectorAll('.square').forEach(square => {
        if (square.dataset.pieceType === 'k' && square.dataset.pieceColor === matedColor) {
            square.classList.add('mated-king');
        }
    });
}

function isGameOver(state) {
    return ['white wins', 'black wins', 'stalemate', 'draw'].includes(state);
}

function handleApiError(action, error, response = null) {
    let serverStatus = 'degraded';
    let statusMessage = 'Server Error';
    let isNetworkError = !response;

    if (isNetworkError) {
        // Network/connection error
        statusMessage = 'Connection Failed';
        console.error(`Network error during ${action}:`, error);
    } else if (response) {
        const status = response.status;

        // Map status codes to user-friendly messages
        switch (status) {
            case 400:
                // Bad request - not a server issue, game logic error
                serverStatus = 'healthy'; // Server is fine, request was invalid
                if (action === 'undo') {
                    statusMessage = 'No Moves to Undo';
                } else if (action === 'move') {
                    statusMessage = 'Invalid Move';
                } else {
                    statusMessage = 'Invalid Request';
                }
                break;
            case 403:
                serverStatus = 'healthy';
                if (action === 'move' || action === 'trigger computer move') {
                    statusMessage = 'Slot Claimed';
                } else {
                    statusMessage = 'Not Authorized';
                }
                break;
            case 404:
                serverStatus = 'healthy'; // Server is fine, game doesn't exist
                statusMessage = 'Game Not Found';
                break;
            case 429:
                serverStatus = 'degraded';
                statusMessage = 'Rate Limited';
                break;
            case 415:
                serverStatus = 'healthy';
                statusMessage = 'Invalid Content Type';
                break;
            case 500:
            case 502:
            case 503:
                serverStatus = 'degraded';
                statusMessage = status === 503 ? 'Service Unavailable' : 'Server Error';
                break;
            default:
                if (status >= 500) {
                    serverStatus = 'degraded';
                    statusMessage = `Server Error (${status})`;
                } else if (status >= 400) {
                    serverStatus = 'healthy';
                    statusMessage = `Request Failed (${status})`;
                }
        }

        console.error(`API error during ${action}: ${status} - ${statusMessage}`);
    }

    flashErrorMessage(statusMessage);

    // Update indicators based on error type
    if (isNetworkError || (response && response.status >= 500)) {
        updateServerIndicator(serverStatus, statusMessage);
        gameState.networkError = true;
    } else {
        // For client errors (4xx), server is healthy but request failed
        updateServerIndicator('healthy');
        gameState.networkError = false;
    }

    return {
        serverStatus,
        statusMessage,
        isNetworkError,
        isClientError: response && response.status >= 400 && response.status < 500,
        isServerError: response && response.status >= 500
    };
}

function flashErrorMessage(message, duration = 1500) {
    const overlay = document.getElementById('error-flash-overlay');
    const messageEl = document.getElementById('error-flash-message');

    messageEl.textContent = message;
    overlay.classList.add('show');

    // Clear any pending timeout to avoid premature hide on rapid calls
    if (overlay._flashTimeout) clearTimeout(overlay._flashTimeout);
    overlay._flashTimeout = setTimeout(() => {
        overlay.classList.remove('show');
        overlay._flashTimeout = null;
    }, duration);
}

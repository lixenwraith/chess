// FILE: internal/webserver/web/app.js
// Game state management
let gameState = {
    gameId: null,
    fen: null,
    turn: 'w',
    isPlayerWhite: true,
    isLocked: false,
    pollInterval: null,
    apiUrl: '',
    selectedSquare: null,
    healthCheckInterval: null,
    networkError: false,
    moveList: [],
};

// Chess piece Unicode
const pieceMap = {
    'p': '♙', 'r': '♜', 'n': '♞', 'b': '♝', 'q': '♛', 'k': '♚',
    'P': '♙', 'R': '♜', 'N': '♞', 'B': '♝', 'Q': '♛', 'K': '♚'
};

// Initialize on page load
document.addEventListener('DOMContentLoaded', async () => {
    const config = await getConfig();
    gameState.apiUrl = config.apiUrl;

    document.getElementById('new-game-btn').addEventListener('click', showNewGameModal);
    document.getElementById('undo-btn').addEventListener('click', undoMoves);
    document.getElementById('start-game-btn').addEventListener('click', startNewGame);
    document.getElementById('cancel-btn').addEventListener('click', hideNewGameModal);
    document.getElementById('copy-fen').addEventListener('click', copyFEN);
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

async function getConfig() {
    try {
        const response = await fetch('/config');
        return await response.json();
    } catch (error) {
        console.error('Failed to get config:', error);
        return { apiUrl: 'http://localhost:8080' };
    }
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
                updateServerIndicator('degraded');
                updateStorageIndicator('unknown');
            }
        } catch (error) {
            updateServerIndicator('degraded');
            updateStorageIndicator('unknown');
            gameState.networkError = true;
        }
    };

    checkHealth();
    gameState.healthCheckInterval = setInterval(checkHealth, 10000);
}

function updateServerIndicator(status) {
    const indicator = document.getElementById('server-indicator');
    const light = indicator.querySelector('.light');
    light.setAttribute('data-status', status);
    indicator.setAttribute('data-status', status);
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
    let tooltip = 'Turn: ';

    if (gameState.networkError) {
        status = 'network-error';
        tooltip += 'Network Error';
    } else if (state === 'pending' || gameState.isLocked) {
        status = 'thinking';
        tooltip += 'Computer Thinking';
    } else if (state && isGameOver(state)) {
        switch(state) {
            case 'white wins':
                status = 'white-wins';
                tooltip = 'White Wins';
                break;
            case 'black wins':
                status = 'black-wins';
                tooltip = 'Black Wins';
                break;
            case 'stalemate':
                status = 'stalemate';
                tooltip = 'Stalemate';
                break;
            case 'draw':
                status = 'draw';
                tooltip = 'Draw';
                break;
            default:
                status = 'unknown';
                tooltip = 'Game Over';
        }
    } else if (turn === 'w') {
        status = 'white';
        tooltip += 'White';
    } else {
        status = 'black';
        tooltip += 'Black';
    }

    light.setAttribute('data-status', status);
    indicator.setAttribute('data-status', tooltip.split(': ')[0]);
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

function copyFEN() {
    const fenText = document.getElementById('fen-display').textContent;
    navigator.clipboard.writeText(fenText).then(() => {
        const btn = document.getElementById('copy-fen');
        btn.classList.add('copied');
        setTimeout(() => {
            btn.classList.remove('copied');
        }, 2000);
    });
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
    gameState.isPlayerWhite = (playerColor === 'white');

    const whiteConfig = gameState.isPlayerWhite ? { type: 1 } : { type: 2, level: computerLevel, searchTime: searchTime };
    const blackConfig = gameState.isPlayerWhite ? { type: 2, level: computerLevel, searchTime: searchTime } : { type: 1 };

    try {
        const response = await fetch(`${gameState.apiUrl}/api/v1/games`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ white: whiteConfig, black: blackConfig })
        });
        if (!response.ok) throw new Error('Failed to create game');

        const game = await response.json();
        gameState.gameId = game.gameId;
        gameState.moveList = [];
        hideNewGameModal();
        initializeBoard();
        updateGameDisplay(game);
        document.getElementById('undo-btn').disabled = false;
        if (!gameState.isPlayerWhite) triggerComputerMove();

    } catch (error) {
        console.error('Error starting game:', error);
        alert('Failed to start new game');
        gameState.networkError = true;
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
        const response = await fetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}/moves`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ move })
        });

        const game = await response.json();
        if (!response.ok) {
            // Invalid move - flash both squares red
            flashSquare(fromEl, false);
            flashSquare(toEl, false);
            renderBoardFromFEN(gameState.fen);
            return;
        }

        // Valid move - flash both squares green
        flashSquare(fromEl, true);
        flashSquare(toEl, true);

        updateGameDisplay(game);
        if (!isGameOver(game.state)) {
            triggerComputerMove();
        }
    } catch (error) {
        console.error('Error making move:', error);
        gameState.networkError = true;
        updateTurnIndicator('', gameState.turn);
    }
}

async function triggerComputerMove() {
    lockBoard();
    try {
        await fetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}/moves`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ move: 'cccc' })
        });
        gameState.networkError = false;
        startPolling();
    } catch (error) {
        console.error('Error triggering computer move:', error);
        gameState.networkError = true;
        updateTurnIndicator('', gameState.turn);
        unlockBoard();
    }
}

function startPolling() {
    gameState.pollInterval = setInterval(async () => {
        try {
            const response = await fetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}`);
            if (!response.ok) throw new Error('Failed to get game state');

            const game = await response.json();
            if (game.state !== 'pending') {
                stopPolling();
                updateGameDisplay(game);
                unlockBoard();
            }
            gameState.networkError = false;
        } catch (error) {
            console.error('Error polling game state:', error);
            gameState.networkError = true;
            updateTurnIndicator('', gameState.turn);
            stopPolling();
            unlockBoard();
        }
    }, 1500);
}

function stopPolling() {
    clearInterval(gameState.pollInterval);
    gameState.pollInterval = null;
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
    try {
        const response = await fetch(`${gameState.apiUrl}/api/v1/games/${gameState.gameId}/undo`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ count: 2 })
        });
        if (!response.ok) throw new Error('Failed to undo moves');
        const game = await response.json();

        // Clear checkmate state
        gameState.state = game.state;

        updateGameDisplay(game);
    } catch (error) {
        console.error('Error undoing moves:', error);
        gameState.networkError = true;
        updateTurnIndicator('', gameState.turn);
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

    // Update FEN display
    document.getElementById('fen-display').textContent = game.fen || '-';

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

function getGameOverText(state) {
    switch(state) {
        case 'white wins': return 'White Wins';
        case 'black wins': return 'Black Wins';
        case 'stalemate': return 'Stalemate';
        case 'draw': return 'Draw';
        default: return state;
    }
}
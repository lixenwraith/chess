# Architecture

## Components

### Transport Layer (`internal/http`)
Fiber web server handling HTTP requests/responses. Implements routing, rate limiting, content-type validation, request parsing. Translates HTTP to internal Command objects.

### Processing Layer (`internal/processor`)
Central command handler containing business logic. Single `Execute(Command)` entry point decouples transport from logic. Uses synchronous UCI engine for validation, asynchronous EngineQueue for computer moves.

### Service Layer (`internal/service`)
In-memory state storage without chess logic. Thread-safe game map protected by RWMutex. Manages game lifecycle, snapshots, and player configuration. Coordinates with storage layer for optional persistence.

### Storage Layer (`internal/storage`)
Optional SQLite persistence with async write pattern. Buffered channel (1000 ops) processes writes sequentially in background. Graceful degradation on write failures. WAL mode for development environments.

### Supporting Modules
- **Engine** (`internal/engine`): UCI protocol wrapper for Stockfish process communication
- **Game** (`internal/game`): Game state with snapshot history
- **Board** (`internal/board`): FEN parsing and ASCII generation
- **Core** (`internal/core`): Shared types, API models, error constants
- **CLI** (`cmd/chessd/cli`): Database management commands

## Request Flow

### Human Move
1. HTTP handler receives `POST /games/{id}/moves` with UCI move
2. Creates MakeMoveCommand, calls `processor.Execute()`
3. Processor validates move via locked validation engine
4. If legal, gets new FEN from engine
5. Calls `service.ApplyMove()` to update state
6. Returns GameResponse

### Computer Move
1. HTTP handler receives `POST /games/{id}/moves` with `{"move": "cccc"}`
2. Processor sets game state to `pending`
3. Submits task to EngineQueue, returns immediately
4. Worker goroutine calculates move with dedicated Stockfish instance
5. Callback updates game state via service
6. Client polls for completion

## Persistence Flow

### Write Operations
1. Service layer calls storage method (RecordNewGame, RecordMove, DeleteUndoneMoves)
2. Operation queued to buffered channel (non-blocking)
3. Writer goroutine processes queue sequentially
4. Transactions ensure atomicity
5. Failures trigger degradation to memory-only mode

### Query Operations
1. CLI invokes Store.QueryGames with filters
2. Direct database read (no queue)
3. Results formatted as tabular output

## Concurrency

- **HTTP Server**: Fiber handles concurrent connections
- **Game State**: Single RWMutex protects game map (concurrent reads, serial writes)
- **Engine Workers**: Fixed pool (2 workers) with dedicated Stockfish processes
- **Validation Engine**: Single mutex-protected instance for synchronous validation
- **Storage Writer**: Single goroutine processes write queue sequentially
- **PID Lock**: File-based exclusive lock prevents multiple instances

## Data Structures

### Game Snapshot
```go
type Snapshot struct {
    FEN           string
    PreviousMove  string
    NextTurnColor Color
    PlayerID      string
}
```

### Command Pattern
Commands encapsulate operations with type and arguments, processed by single Execute method.

### Player Configuration
Players identified by UUID, configured with type (human/computer), skill level, and search time.

### Storage Schema
```sql
games (
    game_id TEXT PRIMARY KEY,
    initial_fen TEXT,
    white_player_id TEXT,
    white_type INTEGER,
    white_level INTEGER,
    white_search_time INTEGER,
    black_player_id TEXT,
    black_type INTEGER,
    black_level INTEGER,
    black_search_time INTEGER,
    start_time_utc DATETIME
)

moves (
    move_id INTEGER PRIMARY KEY,
    game_id TEXT,
    move_number INTEGER,
    move_uci TEXT,
    fen_after_move TEXT,
    player_color TEXT,
    move_time_utc DATETIME,
    FOREIGN KEY (game_id) REFERENCES games(game_id)
)
```
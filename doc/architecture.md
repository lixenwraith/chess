# Architecture

## Components

### Transport Layer (`internal/http`)
Fiber web server handling HTTP requests/responses. Implements routing, rate limiting, content-type validation, request parsing. Translates HTTP to internal Command objects.

### Processing Layer (`internal/processor`)
Central command handler containing business logic. Single `Execute(Command)` entry point decouples transport from logic. Uses synchronous UCI engine for validation, asynchronous EngineQueue for computer moves.

### Service Layer (`internal/service`)
In-memory state storage without chess logic. Thread-safe game map protected by RWMutex. Manages game lifecycle, snapshots, and player configuration.

### Supporting Modules
- **Engine** (`internal/engine`): UCI protocol wrapper for Stockfish process communication
- **Game** (`internal/game`): Game state with snapshot history
- **Board** (`internal/board`): FEN parsing and ASCII generation
- **Core** (`internal/core`): Shared types, API models, error constants

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

## Concurrency

- **HTTP Server**: Fiber handles concurrent connections
- **Game State**: Single RWMutex protects game map (concurrent reads, serial writes)
- **Engine Workers**: Fixed pool (2 workers) with dedicated Stockfish processes
- **Validation Engine**: Single mutex-protected instance for synchronous validation

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
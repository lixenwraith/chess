# Development Guide

## Prerequisites

- Go 1.24+
- Stockfish in PATH
- Git
- curl, jq (for testing)

## Building

```bash
git clone https://git.lixen.com/lixen/chess
cd chess
go build ./cmd/chessd
```

## Running

### Flags
- `-api-host`: API server host (default: localhost)
- `-api-port`: API server port (default: 8080)
- `-serve`: Enable embedded web UI server
- `-web-host`: Web UI server host (default: localhost)
- `-web-port`: Web UI server port (default: 9090)
- `-dev`: Development mode with relaxed rate limits
- `-storage-path`: SQLite database file path (enables persistence)
- `-pid`: PID file path for process tracking
- `-pid-lock`: Enable exclusive locking (requires -pid)

### Modes
```bash
# In-memory only
./chessd

# With persistence
./chessd -storage-path ./db/chess.db

# Singleton enforcement (requires same PID file path across instances)
./chessd -pid /var/run/chessd.pid -pid-lock

# Development with all features
./chessd -dev -storage-path chess.db -pid /tmp/chessd.pid
```

## Database Management

### CLI Commands
```bash
# Initialize database schema
./chessd db init -path chess.db

# Query games
./chessd db query -path chess.db [-gameId ID] [-playerId ID]

# Delete database
./chessd db delete -path chess.db
```

Query parameters accept `*` for all records (default if omitted) or specific IDs for filtering.

## Project Structure

```
chess/
├── cmd/chessd/
│   ├── main.go        # Entry point
│   ├── pid.go         # PID file management
│   └── cli/           # Database CLI
├── internal/
│   ├── board/         # FEN/ASCII operations
│   ├── core/          # Shared types
│   ├── engine/        # Stockfish UCI wrapper
│   ├── game/          # Game state
│   ├── http/          # Fiber handlers
│   ├── processor/     # Command processing
│   ├── service/       # State management
│   └── storage/       # SQLite persistence
└── test/              # Test scripts
```

## Testing

See [test documentation](../test/README.md) for details.

## Configuration

### Fixed Values
- Engine path: `"stockfish"` (internal/engine/engine.go)
- Worker count: 2 (internal/processor/processor.go)
- Queue capacity: 100 (internal/processor/queue.go)
- Min search time: 100ms (internal/processor/processor.go)
- Write queue: 1000 operations (internal/storage/storage.go)
- DB connections: 25 max, 5 idle (internal/storage/storage.go)

### Storage Configuration
- WAL mode enabled in development for concurrency
- Foreign key constraints enforced
- Async write pattern with 2-second drain on shutdown
- Degradation to memory-only on write failures

### PID Management
- Singleton enforcement requires same PID file path - all instances must use the same -pid value
- Stale PID detection via signal 0 checking
- Exclusive file locking with LOCK_EX|LOCK_NB
- Automatic cleanup on graceful shutdown

### Validation Rules
- Player type: 1 (human) or 2 (computer)
- Skill level: 0-20
- Search time: 100-10000ms
- UCI moves: 4-5 characters ([a-h][1-8][a-h][1-8][qrbn]?)
- Undo count: 1-300

## Limitations

- No persistence (memory only)
- Hardcoded Stockfish path
- Fixed worker pool size
- No game history beyond current session
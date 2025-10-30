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
- `-host`: Server host (default: localhost)
- `-port`: Server port (default: 8080)
- `-dev`: Development mode with relaxed rate limits

### Modes
```bash
# Production (1 req/s rate limit)
./chessd

# Development (10 req/s rate limit)
./chessd -dev
```

## Project Structure

```
chess/
├── cmd/chessd/        # Entry point
├── internal/
│   ├── board/         # FEN/ASCII operations
│   ├── core/          # Shared types
│   ├── engine/        # Stockfish UCI wrapper
│   ├── game/          # Game state
│   ├── http/          # Fiber handlers
│   ├── processor/     # Command processing
│   └── service/       # State management
└── test/              # Test scripts
```

## Testing

```bash
# Unit tests
go test ./...

# API tests (requires dev mode)
./chessd -dev &
./test/test-api.sh
```

Test script validates:
- Basic CRUD operations
- Computer move triggering ("cccc" mechanism)
- Pending state protection
- Rate limiting
- Input validation
- Error handling

## Configuration

### Fixed Values
- Engine path: `"stockfish"` (internal/engine/engine.go)
- Worker count: 2 (internal/processor/processor.go)
- Queue capacity: 100 (internal/processor/queue.go)
- Min search time: 100ms (internal/processor/processor.go)

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
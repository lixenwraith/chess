# Development Guide

## Prerequisites

- Go 1.26+
- Stockfish in PATH
- SQLite3
- Git
- curl, jq (for testing)

## Building
```bash
#git clone https://git.lixen.com/lixen/chess # Mirror
git clone https://github.com/lixenwraith/chess
cd chess
go build ./cmd/chess-server
go build ./cmd/chess-client-cli
```

## Running

### Flags
- `-api-host`: API server host (default: localhost)
- `-api-port`: API server port (default: 8080)
- `-serve`: Enable embedded web UI server
- `-web-host`: Web UI server host (default: localhost)
- `-web-port`: Web UI server port (default: 9090)
- `-dev`: Development mode with relaxed rate limits and fixed JWT secret
- `-storage-path`: SQLite database file path (enables persistence and authentication)
- `-pid`: PID file path for process tracking
- `-pid-lock`: Enable exclusive locking (requires -pid)
- `-log-level`: `debug`, `info`, `warn`, or `error` (default: `info`)
- `-log-http`: Enable API and web request logs (default: `true`)
- `-finished-game-ttl`: How long terminal games stay in memory (default: `1h`; `0` disables eviction)
- `-web-api-url`: Browser-visible API origin for the embedded web client; useful when its public origin differs from the listen address

### Modes
```bash
# In-memory only (no persistence or auth)
./chess-server

# With persistence and authentication
./chess-server -storage-path ./db/chess.db

# Development with all features
./chess-server -dev -storage-path chess.db -pid /tmp/chess-server.pid -serve

# Detailed persistence, engine-queue, cleanup, and request logs
./chess-server -dev -storage-path chess.db -serve -log-level debug -log-http=true

# Web UI is public at one origin while the API is exposed at another
./chess-server -serve -web-api-url https://api.example.test

# Initialize database with user tables
./chess-server db init -path chess.db
```

## Database Management

### Schema Initialization
```bash
# Create all tables (users, games, moves)
./chess-server db init -path chess.db
```

### User Management CLI
```bash
# Add user with password
./chess-server db user add -path chess.db -username alice -password SecurePass123

# Add user with email
./chess-server db user add -path chess.db -username bob -email bob@example.com -password BobPass456

# Interactive password input
./chess-server db user add -path chess.db -username charlie -interactive

# List all users
./chess-server db user list -path chess.db

# Update password
./chess-server db user set-password -path chess.db -username alice -password NewPass789

# Update email
./chess-server db user set-email -path chess.db -username alice -email newemail@example.com

# Update username
./chess-server db user set-username -path chess.db -current alice -new alice2

# Import with existing Argon2 hash
./chess-server db user set-hash -path chess.db -username alice -hash '$argon2id$v=19$m=65536,t=3,p=2$...'

# Delete user
./chess-server db user delete -path chess.db -username alice
```

### Game Query CLI
```bash
# Query all games
./chess-server db query -path chess.db -gameId "*"

# Query games for specific user
./chess-server db query -path chess.db -playerId "550e8400-e29b-41d4-a716-446655440000"

# Query specific game
./chess-server db query -path chess.db -gameId "a1b2c3d4-e5f6-7890-1234-567890abcdef"

# Delete database (destructive)
./chess-server db delete -path chess.db
```

## Authentication Configuration

### JWT Secret Management
- **Production**: A cryptographically secure 32-byte secret is generated on
  startup. This intentionally invalidates JWTs after a restart even though the
  SQLite session rows remain; configuring a stable deployment secret is tracked
  in `doc/todo.md`.
- **Development** (`-dev`): Fixed secret for testing consistency
- **Sessions**: Stored for 7 days and renewed on each login; effective token
  lifetime is also bounded by signing-key rotation

### Password Requirements
- Minimum 8 characters
- At least one letter and one number
- Argon2id hashing with secure defaults

### User Account Features
- Case-insensitive username and email matching
- Optional email addresses
- Last login tracking
- Unique constraint enforcement with transaction isolation

## Project Structure
```
chess/
├── cmd/
│   ├── chess-server/            # Server app
│   │   ├── main.go              # Server entry point
│   │   ├── pid.go               # PID file management
│   │   └── cli/                 # Database and user CLI
│   └── chess-client/            # Client app
│       └── main.go              # Interactive debugging client
├── internal/
│   ├── client/                  # Client components
│   │   ├── api/                 # HTTP client for server API
│   │   ├── commands/            # Command registry and handlers
│   │   ├── display/             # Terminal output formatting
│   │   └── session/             # Session state management
│   └── server/                  # Server components
│       ├── board/               # FEN/ASCII operations
│       ├── core/                # Shared types and API models
│       ├── engine/              # Stockfish UCI wrapper
│       ├── game/                # Game state with player associations
│       ├── http/                # Fiber handlers and auth endpoints
│       │   ├── handler.go       # Game endpoints
│       │   ├── auth.go          # Authentication endpoints
│       │   └── middleware.go    # JWT validation
│       ├── processor/           # Command processing with user context
│       ├── service/             # State and user management
│       │   ├── service.go       # Core service
│       │   ├── game.go          # Game operations
│       │   └── user.go          # User and auth operations
│       └── storage/             # SQLite persistence
│           ├── storage.go       # Async writer for games
│           ├── game.go          # Game persistence
│           ├── user.go          # User persistence (synchronous)
│           └── schema.go        # Database schema
└── test/                        # Test scripts
```

## Testing

See [test documentation](../test/README.md) for comprehensive test suites covering API, authentication, and database operations.

### Quick Test Commands
```bash
# API functionality tests
./test/test-api.sh

# User authentication and database tests
./test/test-db.sh

# Run test server with sample users
./test/test-db-server.sh

# Test real-time game updates via long-polling
./test/test-longpoll.sh

# Unit, migration, and persistence tests
go test ./...

# Concurrency checks for the state/persistence boundary
go test -race ./internal/server/storage ./internal/server/service
```

## Configuration

### Fixed Values
- Engine path: `"stockfish"` (internal/engine/engine.go)
- Worker count: 2 (internal/processor/processor.go)
- Queue capacity: 100 (internal/processor/queue.go)
- Min search time: 100ms (internal/processor/processor.go)
- Write queue: 1000 operations (internal/server/storage/storage.go)
- DB connections: 8 max, 4 idle (internal/server/storage/storage.go)
- JWT expiration: 7 days (internal/service/user.go)
- Long-poll timeout: 30 seconds (internal/server/service/waiter.go)
- Long-poll channel buffer: 1 (internal/service/waiter.go)

### Authentication Configuration
- Password minimum: 8 characters with letter and number
- Username format: 1-40 characters, alphanumeric and underscore
- Email validation: Standard RFC 5322 format
- Hash algorithm: Argon2id (memory-hard, side-channel resistant)

### Storage Configuration
- WAL mode and NORMAL synchronous mode enabled on every connection
- Foreign key constraints and a five-second busy timeout enabled on every connection
- Async write pattern for games; shutdown drains every accepted write
- Replay reads wait for prior queued writes and use one read transaction
- Synchronous writes for user operations (data consistency)
- Registration capacity/eviction, user creation, and initial session are atomic
- A full queue or write failure degrades to memory-only and is visible in logs and `/health`
- Case-insensitive collation for usernames and emails

### Rate Limiting Configuration
- General endpoints: 10 req/s (20 in dev mode)
- User registration: 5 req/min
- User login: 10 req/min
- Rate limit key: IP address from X-Forwarded-For or connection

### PID Management
- Singleton enforcement requires same PID file path
- Stale PID detection via signal 0 checking
- Exclusive file locking with LOCK_EX|LOCK_NB
- Automatic cleanup on graceful shutdown

### Validation Rules
- Player type: 1 (human) or 2 (computer)
- Skill level: 0-20
- Search time: 100-10000ms
- UCI moves: 4-5 characters ([a-h][1-8][a-h][1-8][qrbn]?)
- Undo count: 1-300
- Username: 1-40 characters, [a-zA-Z0-9_]
- Password: 8-128 characters, requires letter and number

## Security Considerations

### Authentication Security
- Passwords hashed with Argon2id before storage
- JWT tokens signed with HS256
- Constant-time password comparison
- Case-insensitive matching prevents user enumeration
- Rate limiting on auth endpoints prevents brute force

### Input Validation
- All user inputs validated and sanitized
- SQL injection prevented via parameterized queries
- UCI command injection blocked via character validation
- FEN strings validated against strict regex pattern

### Session Management
- JWT tokens expire after 7 days
- No token refresh mechanism (re-login required)
- Tokens include minimal claims (user ID, username, email)
- Secret rotates on server restart (except dev mode)

## Limitations

- JWT tokens don't support refresh (must re-login after expiry)
- User deletion doesn't cascade to games (games remain with player IDs)
- No password recovery mechanism
- No email verification for registration
- Fixed worker pool size for engine calculations
- No push-based game updates (30-second long-polling is used)
- Live games are not rehydrated after restart; persisted games are currently replay-only
- Database history has no automatic retention policy
- Curated-game metadata and replay controls are deferred to [Replay Implementation Tasks](./todo.md)
- REST API only

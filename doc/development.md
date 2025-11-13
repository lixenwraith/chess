# Development Guide

## Prerequisites

- Go 1.24+
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
go build ./cmd/chess-client
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

### Modes
```bash
# In-memory only (no persistence or auth)
./chessd

# With persistence and authentication
./chessd -storage-path ./db/chess.db

# Development with all features
./chessd -dev -storage-path chess.db -pid /tmp/chessd.pid -serve

# Initialize database with user tables
./chessd db init -path chess.db
```

## Database Management

### Schema Initialization
```bash
# Create all tables (users, games, moves)
./chessd db init -path chess.db
```

### User Management CLI
```bash
# Add user with password
./chessd db user add -path chess.db -username alice -password SecurePass123

# Add user with email
./chessd db user add -path chess.db -username bob -email bob@example.com -password BobPass456

# Interactive password input
./chessd db user add -path chess.db -username charlie -interactive

# List all users
./chessd db user list -path chess.db

# Update password
./chessd db user set-password -path chess.db -username alice -password NewPass789

# Update email
./chessd db user set-email -path chess.db -username alice -email newemail@example.com

# Update username
./chessd db user set-username -path chess.db -current alice -new alice2

# Import with existing Argon2 hash
./chessd db user set-hash -path chess.db -username alice -hash '$argon2id$v=19$m=65536,t=3,p=2$...'

# Delete user
./chessd db user delete -path chess.db -username alice
```

### Game Query CLI
```bash
# Query all games
./chessd db query -path chess.db -gameId "*"

# Query games for specific user
./chessd db query -path chess.db -playerId "550e8400-e29b-41d4-a716-446655440000"

# Query specific game
./chessd db query -path chess.db -gameId "a1b2c3d4-e5f6-7890-1234-567890abcdef"

# Delete database (destructive)
./chessd db delete -path chess.db
```

## Authentication Configuration

### JWT Secret Management
- **Production**: Cryptographically secure 32-byte secret generated on startup
- **Development** (`-dev`): Fixed secret for testing consistency
- **Sessions**: Valid for 7 days, renewed on each login

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
```

## Configuration

### Fixed Values
- Engine path: `"stockfish"` (internal/engine/engine.go)
- Worker count: 2 (internal/processor/processor.go)
- Queue capacity: 100 (internal/processor/queue.go)
- Min search time: 100ms (internal/processor/processor.go)
- Write queue: 1000 operations (internal/storage/storage.go)
- DB connections: 25 max, 5 idle (internal/storage/storage.go)
- JWT expiration: 7 days (internal/service/user.go)
- Long-poll timeout: 25 seconds (internal/service/waiter.go)
- Long-poll channel buffer: 1 (internal/service/waiter.go)

### Authentication Configuration
- Password minimum: 8 characters with letter and number
- Username format: 1-40 characters, alphanumeric and underscore
- Email validation: Standard RFC 5322 format
- Hash algorithm: Argon2id (memory-hard, side-channel resistant)

### Storage Configuration
- WAL mode enabled in development for concurrency
- Foreign key constraints enforced
- Async write pattern for games with 2-second drain on shutdown
- Synchronous writes for user operations (data consistency)
- Degradation to memory-only on write failures
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
- No real-time game updates (polling required)
- Long-polling limited to 25 seconds per request
- REST API only
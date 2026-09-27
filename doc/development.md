# Development Guide

## Prerequisites

- Go 1.26+
- Stockfish in PATH
- PostgreSQL 18 (server for persistence; `psql` for the shell test suites)
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
- `-dsn`: PostgreSQL connection string (default `$CHESS_DSN`); enables persistence and authentication
- `-jwt-secret-file`: Stable JWT signing key file, mode 0600, at least 32 bytes (default `$CHESS_JWT_SECRET_FILE`)
- `-trusted-proxies`: Comma-separated reverse-proxy IPs/CIDRs whose `-proxy-header` is trusted
- `-proxy-header`: Client-IP header from a trusted proxy (default `X-Real-IP`)
- `-pid`: PID file path for process tracking
- `-pid-lock`: Enable exclusive locking (requires -pid)
- `-log-level`: `debug`, `info`, `warn`, or `error` (default: `info`)
- `-log-http`: Enable API and web request logs (default: `true`)
- `-finished-game-ttl`: How long terminal games stay in memory (default: `1h`; `0` disables eviction)
- `-anonymous-game-ttl`: How long games without a registered player survive after their last activity, in memory and in the database (default: `24h`; `0` keeps them)
- `-max-users`: Accounts at which public registration closes (default: `100`; `0` = no limit; the CLI is not limited)
- `-web-api-url`: Browser-visible API origin for the embedded web client; useful when its public origin differs from the listen address

### Modes
```bash
# In-memory only (no persistence or auth)
./chess-server

# With persistence and authentication
./chess-server -dsn 'dbname=chess'

# Development with all features
./chess-server -dev -dsn 'dbname=chess' -pid /tmp/chess-server.pid -serve

# Detailed persistence, engine-queue, cleanup, and request logs
./chess-server -dev -dsn 'dbname=chess' -serve -log-level debug -log-http=true

# Behind a reverse proxy that sets X-Real-IP
./chess-server -dsn 'dbname=chess' -trusted-proxies 10.0.0.1

# Web UI is public at one origin while the API is exposed at another
./chess-server -serve -web-api-url https://api.example.test

# Create or migrate the schema
./chess-server db init -dsn 'dbname=chess'
```

### Local PostgreSQL

Any PostgreSQL 18 works for development. With a local server and peer
authentication for your OS user:

```bash
sudo -u postgres createuser "$USER"
sudo -u postgres createdb -O "$USER" chess
export CHESS_DSN='dbname=chess'
```

Production provisioning (roles, schema, `pg_hba.conf`) is in
[deployment.md](./deployment.md).

## Database Management

All `db` subcommands take `-dsn`, defaulting to `$CHESS_DSN`; the examples
below assume it is exported. Commands other than `init` and `delete` refuse to
run against a missing or outdated schema.

### Schema Initialization
```bash
# Create or migrate all tables; safe to repeat
./chess-server db init
```

### User Management CLI
```bash
# Add user with password
./chess-server db user add -username alice -password SecurePass123

# Add user with email
./chess-server db user add -username bob -email bob@example.com -password BobPass456

# Interactive password input
./chess-server db user add -username charlie -interactive

# List all users
./chess-server db user list

# Update password
./chess-server db user set-password -username alice -password NewPass789

# Update email
./chess-server db user set-email -username alice -email newemail@example.com

# Update username
./chess-server db user set-username -current alice -new alice2

# Import with existing Argon2 hash
./chess-server db user set-hash -username alice -hash '$argon2id$v=19$m=65536,t=3,p=2$...'

# Delete user
./chess-server db user delete -username alice
```

### Game Query CLI
```bash
# Query all games
./chess-server db query -gameId "*"

# Query games for specific user
./chess-server db query -playerId "550e8400-e29b-41d4-a716-446655440000"

# Query specific game
./chess-server db query -gameId "a1b2c3d4-e5f6-7890-1234-567890abcdef"

# Drop every chess table in the DSN's search_path (destructive)
./chess-server db delete -confirm
```

## Authentication Configuration

### JWT Secret Management
- **Production**: Provide `-jwt-secret-file` so tokens survive restarts. The
  file must be a regular file with no group/other permissions and hold at
  least 32 bytes (`openssl rand -base64 48`). Without it, a random key is
  generated per process and every token is invalidated on restart, although
  the session rows remain.
- **Development** (`-dev`): Fixed secret for testing consistency when no key file is given
- **Scope**: Tokens are issued for `chess-server` / `chess-api`, carry only the
  subject and session ID, and are accepted only while their session row exists
- **Sessions**: Stored for 7 days and renewed on each login

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
│       └── storage/             # PostgreSQL persistence (pgtest: per-test schemas)
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

# Run test server with sample users (needs a disposable CHESS_TEST_DSN)
./test/run-test-server.sh

# Test real-time game updates via long-polling
./test/test-longpoll.sh

# Unit, migration, and persistence tests; database tests are skipped unless
# CHESS_TEST_DSN names a database the test role may create schemas in
export CHESS_TEST_DSN='postgres://chess_test:chess_test@localhost:5432/chess_test?sslmode=disable'
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
- DB connections: 10 max, 5 idle (internal/server/storage/storage.go)
- DB operation deadline: 5 seconds; write transactions 10 seconds
- Transient write retries: 6 attempts, 100 ms doubling backoff
- JWT expiration: 7 days (internal/service/user.go)
- Concurrent Argon2id derivations: 4, five-second wait (internal/service/user.go)
- Long-poll timeout: 30 seconds (internal/server/service/waiter.go)
- Long-poll channel buffer: 1 (internal/service/waiter.go)

### Authentication Configuration
- Password minimum: 8 characters with letter and number
- Username format: 1-40 characters, alphanumeric and underscore
- Email validation: Standard RFC 5322 format
- Hash algorithm: Argon2id (memory-hard, side-channel resistant)

### Storage Configuration
- PostgreSQL via pgx; schema chosen by `search_path`; migrations run at startup
- Async write pattern for games; shutdown drains every accepted write
- Transient failures before COMMIT are retried; others degrade storage
- Replay reads wait for prior queued writes and use one REPEATABLE READ transaction
- Synchronous writes for user operations (data consistency)
- Registration limit check, user creation, and initial session are atomic
- Games without a registered player are deleted 24 hours after their last activity
- A full queue or unrecovered write failure degrades to memory-only and is visible in logs and `/health`
- Usernames and emails stored lowercase with unique constraints

### Rate Limiting Configuration
- General endpoints: 10 req/s (20 in dev mode)
- User registration: 5 req/min
- User login: 10 req/min
- Rate limit key: `-proxy-header` (default `X-Real-IP`) from `-trusted-proxies`, otherwise the TCP peer

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
- JWT tokens signed with HS256 and scoped by issuer/audience
- Constant-time password comparison; unknown accounts verify a dummy hash
- Case-insensitive matching prevents duplicate accounts
- Rate limiting on auth endpoints prevents brute force
- Argon2id concurrency is bounded to cap memory under load

### Input Validation
- All user inputs validated and sanitized
- SQL injection prevented via parameterized queries
- UCI command injection blocked via character validation
- FEN strings validated against strict regex pattern

### Session Management
- JWT tokens expire after 7 days
- No token refresh mechanism (re-login required)
- Tokens carry only the user ID and session ID
- Key is stable with `-jwt-secret-file`; otherwise it rotates on restart (fixed in dev mode)

## Limitations

- JWT tokens don't support refresh (must re-login after expiry)
- User deletion doesn't cascade to games (games keep player and claim IDs)
- Games played without a registered player are deleted 24 hours after their last activity
- No password recovery mechanism
- No email verification for registration
- Fixed worker pool size for engine calculations
- No push-based game updates (30-second long-polling is used)
- Live games are not rehydrated after restart; persisted games are currently replay-only
- Database history has no automatic retention policy
- Replay UI, PGN export, and curated games are planned in [Replay Plan](./todo.md)
- REST API only

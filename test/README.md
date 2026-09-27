# Chess API Test Suite

This directory contains comprehensive test suites for the Chess API server, covering API functionality, database operations, authentication, and real-time updates.

## Prerequisites

- `jq` - JSON processor
- `curl` - HTTP client
- `psql` - PostgreSQL client (for database tests)
- `base64` - Base64 encoder (for JWT tests)
- Stockfish in `PATH`
- Compiled `bin/chess-server` binary (or pass another path to the scripts)
- A **disposable** PostgreSQL database named by `CHESS_TEST_DSN`

`CHESS_TEST_DSN` must be a libpq-compatible connection string, because the
scripts pass it to both `chess-server` and `psql`. The test server drops every
chess table in that database on start and exit, so never point it at real data.

```bash
# One-time setup as a PostgreSQL superuser
psql -U postgres -c "CREATE ROLE chess_test LOGIN PASSWORD 'chess_test'"
psql -U postgres -c "CREATE DATABASE chess_test OWNER chess_test"

export CHESS_TEST_DSN='postgres://chess_test:chess_test@localhost:5432/chess_test?sslmode=disable'
```

The same variable enables the Go database tests (`go test ./...`), which create
and drop an isolated schema per test and are skipped when it is unset.

## Running the test server
From repo root
```bash
test/run-test-server.sh
```

Pass the binary path as the first argument when it is not at `bin/chess-server`.
The server runs with `-dev`, debug logging, HTTP request logging, and relaxed rate limiting.
It drops the test tables on exit, so it's preferred for clean testing.
Can be used for all the tests.

Logging can be adjusted without editing the script:
```bash
LOG_LEVEL=info LOG_HTTP=false test/run-test-server.sh bin/chess-server
```

### Pre-configured Users
| Username | Password | Email |
|----------|----------|-------|
| alice | AlicePass123 | alice@example.com |
| bob | BobPass456 | bob@example.com |

### Features
- Automatically initializes database schema
- Creates three test users
- Runs on port 8080 (API) and 9090 (Web UI)
- Development mode with relaxed rate limits
- Fixed JWT secret for consistent tokens
- Debug persistence and engine-queue logs by default
- Graceful shutdown on Ctrl+C

### Manual Testing Examples
```bash
# Login as alice
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"identifier":"alice","password":"AlicePass123"}'

# Create authenticated game
TOKEN="<jwt-from-login>"
curl -X POST http://localhost:8080/api/games \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"white":{"type":1},"black":{"type":2,"level":10}}'
```

## Test Suite Overview

| Test Suite | File | Coverage |
|------------|------|----------|
| API Functionality | `test-api.sh` | Game operations, moves, undo, rate limiting |
| Database & Auth | `test-db.sh` | User registration, login, JWT tokens, persistence |
| Long-Polling | `test-longpoll.sh` | Real-time updates, wait behavior, timeouts |
| Test Server | `run-test-server.sh` | Pre-populated test environment |

## 1. API Functionality Tests (`test-api.sh`)

Tests core game mechanics and API endpoints.

### Running the test
```bash
# Terminal 1: Start server in development mode
test/run-test-server.sh bin/chess-server
# Direct (no cleanup required): bin/chess-server -dev

# Terminal 2: Run API tests
test/test-api.sh
```

### Coverage
- **Game Creation**: Human vs Human, Human vs Computer, Computer vs Computer
- **Move Validation**: Legal/illegal moves, UCI notation
- **Computer Play**: Async engine moves with "cccc" trigger
- **Undo System**: Single and multiple move reversal
- **Player Configuration**: Dynamic player type changes
- **Rate Limiting**: 20 req/s in dev mode
- **Error Handling**: Invalid inputs, missing games, wrong content-types
- **Security**: Input validation, FEN injection prevention

## 2. Database & Authentication Tests (`test-db.sh`)

Tests user management, authentication, and persistence via API integration.
**Requires Running Server Script**

### Running the test
```bash
# Terminal 1: Start test server with database
# Server is running with -dev and PostgreSQL storage
test/run-test-server.sh bin/chess-server

# Terminal 2: Run API integration tests
test/test-db.sh bin/chess-server
```

### Coverage
- **User Registration**: Account creation, password hashing
- **Duplicate Prevention**: Username/email uniqueness
- **Authentication**: Login with JWT generation
- **Token Validation**: JWT parsing and claims verification
- **Password Security**: Argon2id hashing, complexity requirements
- **Case Sensitivity**: Case-insensitive username/email matching
- **Database Schema**: Table creation, constraints, indexes

### Test Flow
1. Re-runs the idempotent schema initialization against `CHESS_TEST_DSN`
2. Registers test users (alice, bob, charlie)
3. Tests authentication endpoints
4. Validates JWT tokens and claims
5. Tests duplicate user prevention
6. Leaves cleanup (dropping tables) to `run-test-server.sh`

## 3. Long-Polling Tests (`test-longpoll.sh`)

Tests real-time game updates via HTTP long-polling.

### Running the test
```bash
# Terminal 1: Start server with storage
test/run-test-server.sh bin/chess-server
# Direct (table cleanup required): CHESS_DSN="$CHESS_TEST_DSN" bin/chess-server -dev

# Terminal 2: Run long-polling tests
test/test-longpoll.sh
```

### Coverage
- **Basic Long-Polling**: Wait for game state changes
- **Multi-Client**: Multiple simultaneous waiters
- **Timeout Behavior**: 25-second timeout verification
- **Immediate Response**: No wait when state already changed
- **Connection Handling**: Client disconnect cleanup
- **Game Deletion**: Notification on game removal
- **Move Detection**: Accurate move count tracking

### Test Scenarios
1. **Single Waiter**: Client waits, receives update after move
2. **Multiple Waiters**: 3 clients wait, all receive notification
3. **Timeout**: Verify 25-second timeout with valid response
4. **Skip Wait**: Immediate return when moveCount outdated
5. **Disconnection**: Proper cleanup on client disconnect

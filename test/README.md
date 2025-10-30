# Chess API Test Suite

This directory contains comprehensive test suites for the Chess API server, covering both API functionality and database persistence.

## Prerequisites

- `jq` - JSON processor
- `curl` - HTTP client
- `sqlite3` - SQLite CLI (for database tests only)
- Compiled `chessd` binary in parent directory

## Test Suites

### 1. API Functionality Tests (`test-api.sh`)

Tests all API endpoints, error handling, rate limiting, and game logic.

**Running the test:**
```bash
# Start server in development mode (required for tests to pass)
../chessd -dev

# In another terminal, run tests
./test-api.sh
```

**Coverage:**
- Game creation and management (HvH, HvC, CvC)
- Move validation and execution
- Computer move triggering with "cccc"
- Undo functionality
- Player configuration changes
- Rate limiting (dev mode: 20 req/s)
- Security hardening and input validation

### 2. Database Persistence Tests (`test-db.sh`)

Tests database storage, async writes, and data integrity.

**Running the test:**
```bash
# Terminal 1: Start server with database
./run-server-with-db.sh ../chessd

# Terminal 2: Run database tests
./test-db.sh

# When done, press Ctrl+C in Terminal 1
```

**Coverage:**
- Game and move persistence
- Async write buffer behavior
- Multi-game isolation
- Undo effects on database
- WAL mode verification
- Foreign key constraints

## Important Notes

1. **Development Mode Required**: The server MUST be started with `-dev` flag for tests to pass. This enables:
    - Relaxed rate limiting (20 req/s instead of 10)
    - WAL mode for SQLite (better concurrency)

2. **Database Tests**: The `run-server-with-db.sh` script automatically:
    - Creates a temporary test database
    - Initializes the schema
    - Cleans up on exit (Ctrl+C)

3. **Test Isolation**: Each test suite can be run independently. The database tests use a separate `test.db` file that doesn't affect production data.

## Troubleshooting

**Rate limiting failures:** Ensure server is running with `-dev` flag  
**Database test failures:** Check that no other instance is using `test.db`  
**Port conflicts:** Default port is 8080, ensure it's available

## Exit Codes

- `0` - All tests passed
- `1` - One or more tests failed

Check colored output for detailed pass/fail information for each test case.
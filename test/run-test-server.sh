#!/usr/bin/env bash

set -e

# Configuration
CHESS_SERVER_EXEC=${1:-"bin/chess-server"}
# A DISPOSABLE database: every chess table in it is dropped on start and exit.
# Use a libpq-compatible DSN; test-db.sh also passes it to psql.
: "${CHESS_TEST_DSN:?set CHESS_TEST_DSN to a disposable PostgreSQL database}"
export CHESS_DSN="$CHESS_TEST_DSN"
PID_FILE="/tmp/chess-server_test.pid"
API_PORT=${API_PORT:-8080}
LOG_LEVEL=${LOG_LEVEL:-debug}
LOG_HTTP=${LOG_HTTP:-true}

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# Check executable
if [ ! -x "$CHESS_SERVER_EXEC" ]; then
    echo -e "${RED}Error: chess-server executable not found or not executable: $CHESS_SERVER_EXEC${NC}"
    echo "Provide the path to chess-server as the first argument or build bin/chess-server."
    echo "Build the binary if not available: go build ./cmd/chess-server"
    exit 1
fi

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    # Kill server if PID file exists
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            echo "Stopping chess-server server (PID: $PID)"
            kill "$PID" 2>/dev/null || true
            sleep 0.5
            kill -9 "$PID" 2>/dev/null || true
        fi
        rm -f "$PID_FILE"
    fi

    # Drop test tables
    echo "Dropping test database tables..."
    "$CHESS_SERVER_EXEC" db delete -confirm >/dev/null || true

    echo -e "${GREEN}Cleanup complete${NC}"
}

# Set up trap for cleanup on exit
trap cleanup EXIT SIGINT SIGTERM

# Clean slate - drop tables left by an earlier run
echo -e "${CYAN}Preparing test environment...${NC}"
rm -f "$PID_FILE"
"$CHESS_SERVER_EXEC" db delete -confirm

# Initialize database
echo -e "${CYAN}Initializing test database...${NC}"
"$CHESS_SERVER_EXEC" db init
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to initialize database${NC}"
    exit 1
fi

# Add test users
echo -e "${CYAN}Adding test users...${NC}"
"$CHESS_SERVER_EXEC" db user add \
    -username alice -email alice@test.com -password AlicePass123
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to create user alice${NC}"
    exit 1
fi

"$CHESS_SERVER_EXEC" db user add \
    -username bob -email bob@test.com -password BobSecure456
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to create user bob${NC}"
    exit 1
fi

echo -e "${CYAN}Test users created:${NC}"
echo "  • alice / AlicePass123"
echo "  • bob / BobSecure456"

# Start server
echo -e "${CYAN}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Chess API Test Server with User Management           ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""
echo "Configuration:"
echo "  Executable: $CHESS_SERVER_EXEC"
echo "  Database:   \$CHESS_TEST_DSN (tables dropped on exit)"
echo "  Port:       $API_PORT"
echo "  Mode:       Development (relaxed rate limits; PostgreSQL storage)"
echo "  Log level:  $LOG_LEVEL (HTTP requests: $LOG_HTTP)"
echo "  Purpose:    Backend for chess-server tests"
echo "  PID File:   $PID_FILE"
echo ""
echo -e "${YELLOW}Instructions:${NC}"
echo "  1. Server will run in foreground with test database"
echo "  2. Open another terminal and run the test script or manual tests"
echo "  3. Press Ctrl+C here when testing is complete"
echo ""
echo -e "${CYAN}──────────────────────────────────────────────────────────${NC}"
echo "Starting server..."
echo ""

# Start chess-server in foreground with dev mode and storage
"$CHESS_SERVER_EXEC" \
	-dev \
	-log-level "$LOG_LEVEL" \
	-log-http="$LOG_HTTP" \
    -api-port "$API_PORT" \
    -pid "$PID_FILE" \
    -pid-lock

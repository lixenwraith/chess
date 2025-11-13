#!/usr/bin/env bash
# FILE: lixenwraith/chess/test/run-test-server.sh

set -e

# Configuration
CHESS_SERVER_EXEC=${1:-"bin/chess-server"}
TEST_DB="test.db"
PID_FILE="/tmp/chess-server_test.pid"
API_PORT=${API_PORT:-8080}

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# Check executable
if [ ! -x "$CHESS_SERVER_EXEC" ]; then
    echo -e "${RED}Error: chess-server executable not found or not executable: $CHESS_SERVER_EXEC${NC}"
    echo "Provide the path to chess-server binary as first argument or place it in the current directory."
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

    # Clean up database files
    echo "Removing test database files..."
    rm -f "$TEST_DB" "${TEST_DB}-wal" "${TEST_DB}-shm"

    echo -e "${GREEN}Cleanup complete${NC}"
}

# Set up trap for cleanup on exit
trap cleanup EXIT SIGINT SIGTERM

# Clean slate - remove any existing test DB files
echo -e "${CYAN}Preparing test environment...${NC}"
rm -f "$TEST_DB" "${TEST_DB}-wal" "${TEST_DB}-shm" "$PID_FILE"

# Initialize database
echo -e "${CYAN}Initializing test database...${NC}"
"$CHESS_SERVER_EXEC" db init -path "$TEST_DB"
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to initialize database${NC}"
    exit 1
fi

# Add test users
echo -e "${CYAN}Adding test users...${NC}"
"$CHESS_SERVER_EXEC" db user add -path "$TEST_DB" \
    -username alice -email alice@test.com -password AlicePass123
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to create user alice${NC}"
    exit 1
fi

"$CHESS_SERVER_EXEC" db user add -path "$TEST_DB" \
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
echo "  Database:   $TEST_DB"
echo "  Port:       $API_PORT"
echo "  Mode:       Development (WAL enabled, relaxed rate limits)"
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
    -storage-path "$TEST_DB" \
    -api-port "$API_PORT" \
    -pid "$PID_FILE" \
    -pid-lock
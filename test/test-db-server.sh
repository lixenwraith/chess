#!/usr/bin/env bash
# FILE: run-test-db-server.sh

set -e

# Configuration
CHESSD_EXEC=${1:-"./chessd"}
TEST_DB="test.db"
PID_FILE="/tmp/chessd_test.pid"
API_PORT=${API_PORT:-8080}

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# Check executable
if [ ! -x "$CHESSD_EXEC" ]; then
    echo -e "${RED}Error: chessd executable not found or not executable: $CHESSD_EXEC${NC}"
    echo "Please build the application first: go build ./cmd/chessd"
    exit 1
fi

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    # Kill server if PID file exists
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            echo "Stopping chessd server (PID: $PID)"
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
"$CHESSD_EXEC" db init -path "$TEST_DB"
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to initialize database${NC}"
    exit 1
fi

# Add test users
echo -e "${CYAN}Adding test users...${NC}"
"$CHESSD_EXEC" db user add -path "$TEST_DB" \
    -username alice -email alice@test.com -password AlicePass123
if [ $? -ne 0 ]; then
    echo -e "${RED}Failed to create user alice${NC}"
    exit 1
fi

"$CHESSD_EXEC" db user add -path "$TEST_DB" \
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
echo -e "${GREEN}║     Chess API Test Server with User Management          ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""
echo "Configuration:"
echo "  Executable: $CHESSD_EXEC"
echo "  Database:   $TEST_DB"
echo "  Port:       $API_PORT"
echo "  Mode:       Development (WAL enabled, relaxed rate limits)"
echo "  PID File:   $PID_FILE"
echo ""
echo -e "${YELLOW}Instructions:${NC}"
echo "  1. Server will start in foreground"
echo "  2. Open another terminal and run: ./test-db.sh"
echo "  3. Press Ctrl+C here when testing is complete"
echo ""
echo -e "${CYAN}──────────────────────────────────────────────────────────${NC}"
echo "Starting server..."
echo ""

# Start chessd in foreground with dev mode and storage
"$CHESSD_EXEC" \
    -dev \
    -storage-path "$TEST_DB" \
    -api-port "$API_PORT" \
    -pid "$PID_FILE" \
    -pid-lock
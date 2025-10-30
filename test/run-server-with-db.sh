#!/usr/bin/env bash
# FILE: run-server-with-db.sh

set -e

# Check for argument
if [ $# -ne 1 ]; then
    echo "Usage: $0 <path_to_chessd_executable>"
    exit 1
fi

CHESSD_EXEC="$1"
TEST_DB="test.db"
PID_FILE="/tmp/chessd_test.pid"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

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
rm -f "$TEST_DB" "${TEST_DB}-wal" "${TEST_DB}-shm"

# Initialize database
echo -e "${CYAN}Initializing test database...${NC}"
"$CHESSD_EXEC" db init -path "$TEST_DB"

# Start server
echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Starting chessd server with database persistence${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"
echo "Executable: $CHESSD_EXEC"
echo "Database: $TEST_DB"
echo "Mode: Development (WAL enabled)"
echo ""
echo -e "${YELLOW}Instructions:${NC}"
echo "1. Open another terminal and run: ./test-db.sh"
echo "2. Press Ctrl+C here when testing is complete"
echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"
echo ""

# Start chessd in foreground with dev mode and storage
"$CHESSD_EXEC" -dev -storage-path "$TEST_DB" -port 8080
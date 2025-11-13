#!/bin/bash
# FILE: lixenwraith/chess/test/test-longpoll.sh

set -e

# Configuration
API_URL="${API_URL:-http://localhost:8080/api/v1}"  # Updated to include /api/v1
VERBOSE="${VERBOSE:-false}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Helper functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_test() {
    echo -e "${YELLOW}[TEST]${NC} $1"
}

# Function to create a test game
create_game() {
    local white_type=$1
    local black_type=$2

    response=$(curl -s -X POST "$API_URL/games" \
        -H "Content-Type: application/json" \
        -d "{\"white\": {\"type\": $white_type}, \"black\": {\"type\": $black_type}}")

    echo "$response" | jq -r '.gameId'
}

# Test 1: Basic long-polling
test_basic_longpoll() {
    log_test "Basic long-polling functionality"

    # Create a human vs human game
    GAME_ID=$(create_game 1 1)
    log_info "Created game: $GAME_ID"

    # Start background poller
    log_info "Starting long-poll request (25s timeout)..."
    curl -s -X GET "$API_URL/games/$GAME_ID?wait=true&moveCount=0" > /tmp/poll_result.json &
    POLL_PID=$!

    # Wait a moment then make a move
    sleep 2
    log_info "Making move e2e4..."
    curl -s -X POST "$API_URL/games/$GAME_ID/moves" \
        -H "Content-Type: application/json" \
        -d '{"move":"e2e4"}' > /dev/null

    # Wait for poller to complete
    if wait $POLL_PID; then
        # Check if we got the updated game state
        moves=$(cat /tmp/poll_result.json | jq -r '.moves | length')
        if [ "$moves" = "1" ]; then
            log_info "✓ Long-poll received notification successfully"
        else
            log_error "✗ Long-poll did not receive correct state"
            exit 1
        fi
    else
        log_error "✗ Long-poll request failed"
        exit 1
    fi

    # Cleanup
    curl -s -X DELETE "$API_URL/games/$GAME_ID" > /dev/null
}

# Test 2: Multiple concurrent waiters
test_multiple_waiters() {
    log_test "Multiple concurrent waiters"

    # Create a game
    GAME_ID=$(create_game 1 1)
    log_info "Created game: $GAME_ID"

    # Start multiple background pollers
    log_info "Starting 3 concurrent long-poll requests..."
    curl -s -X GET "$API_URL/games/$GAME_ID?wait=true&moveCount=0" > /tmp/poll1.json &
    PID1=$!
    curl -s -X GET "$API_URL/games/$GAME_ID?wait=true&moveCount=0" > /tmp/poll2.json &
    PID2=$!
    curl -s -X GET "$API_URL/games/$GAME_ID?wait=true&moveCount=0" > /tmp/poll3.json &
    PID3=$!

    # Make a move
    sleep 1
    log_info "Making move e2e4..."
    curl -s -X POST "$API_URL/games/$GAME_ID/moves" \
        -H "Content-Type: application/json" \
        -d '{"move":"e2e4"}' > /dev/null

    # Wait for all pollers
    wait $PID1 $PID2 $PID3

    # Check all received the update
    success=true
    for i in 1 2 3; do
        moves=$(cat /tmp/poll$i.json | jq -r '.moves | length')
        if [ "$moves" != "1" ]; then
            log_error "✗ Poller $i did not receive update"
            success=false
        fi
    done

    if $success; then
        log_info "✓ All waiters received notifications"
    else
        exit 1
    fi

    # Cleanup
    curl -s -X DELETE "$API_URL/games/$GAME_ID" > /dev/null
}

# Test 3: Timeout behavior
test_timeout() {
    log_test "Timeout behavior (this takes 25 seconds)"

    # Create a game
    GAME_ID=$(create_game 1 1)
    log_info "Created game: $GAME_ID"

    # Start poller without making any moves
    log_info "Starting long-poll that will timeout..."
    start_time=$(date +%s)
    curl -s -X GET "$API_URL/games/$GAME_ID?wait=true&moveCount=0" > /tmp/timeout.json
    end_time=$(date +%s)
    elapsed=$((end_time - start_time))

    # Check timeout was ~25 seconds
    if [ "$elapsed" -ge 24 ] && [ "$elapsed" -le 26 ]; then
        log_info "✓ Request timed out after ~25 seconds"
    else
        log_error "✗ Timeout was $elapsed seconds (expected ~25)"
        exit 1
    fi

    # Should still get valid game state
    game_id=$(cat /tmp/timeout.json | jq -r '.gameId')
    if [ "$game_id" = "$GAME_ID" ]; then
        log_info "✓ Timeout response contains valid game state"
    else
        log_error "✗ Timeout response invalid"
        exit 1
    fi

    # Cleanup
    curl -s -X DELETE "$API_URL/games/$GAME_ID" > /dev/null
}

# Test 4: Immediate response when state already changed
test_immediate_response() {
    log_test "Immediate response when state already changed"

    # Create game and make a move
    GAME_ID=$(create_game 1 1)
    log_info "Created game: $GAME_ID"

    curl -s -X POST "$API_URL/games/$GAME_ID/moves" \
        -H "Content-Type: application/json" \
        -d '{"move":"e2e4"}' > /dev/null

    # Poll with moveCount=0 (should return immediately)
    log_info "Polling with outdated move count..."
    start_time=$(date +%s)
    response=$(curl -s -X GET "$API_URL/games/$GAME_ID?wait=true&moveCount=0")
    end_time=$(date +%s)
    elapsed=$((end_time - start_time))

    if [ "$elapsed" -le 1 ]; then
        log_info "✓ Immediate response when move count differs"
    else
        log_error "✗ Response took $elapsed seconds (should be immediate)"
        exit 1
    fi

    # Cleanup
    curl -s -X DELETE "$API_URL/games/$GAME_ID" > /dev/null
}

# Run tests
log_info "Starting long-poll tests against $API_URL"
echo ""

test_basic_longpoll
echo ""

test_multiple_waiters
echo ""

test_immediate_response
echo ""

if [ "${SKIP_TIMEOUT_TEST:-false}" = "false" ]; then
    test_timeout
else
    log_info "Skipping timeout test (set SKIP_TIMEOUT_TEST=false to run)"
fi

echo ""
log_info "All tests passed! ✓"
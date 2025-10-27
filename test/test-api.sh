#!/usr/bin/env bash
# FILE: test/test-api.sh

# Chess API Test Suite
# Requires: curl, jq (optional for pretty output)

BASE_URL="http://localhost:8080"
API_URL="${BASE_URL}/api/v1"

# Configurable delay between API calls (in seconds)
API_DELAY=${API_DELAY:-0.2}  # Default 200ms between calls

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Test counters
PASS=0
FAIL=0

# Helper functions
test_case() {
    echo -e "\n${YELLOW}TEST: $1${NC}"
    sleep $API_DELAY  # Add delay before each test
}

assert_status() {
    local expected=$1
    local actual=$2
    local test_name=$3

    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}✓ $test_name: HTTP $actual${NC}"
        ((PASS++))
    else
        echo -e "${RED}✗ $test_name: Expected HTTP $expected, got $actual${NC}"
        ((FAIL++))
    fi
}

api_request() {
    local method=$1
    local url=$2
    shift 2
    curl -s "$@" -X "$method" "$url"
    local status=$?
    sleep $API_DELAY
    return $status
}

# Start tests
echo "=== Chess API Test Suite ==="
echo "Server: $BASE_URL"
echo "Starting tests..."

# Test 1: Health check
test_case "Health Check"
STATUS=$(api_request GET "$BASE_URL/health" -o /dev/null -w "%{http_code}")
assert_status 200 "$STATUS" "Health endpoint"

# Test 2: Create human vs computer game
test_case "Create Human vs Computer Game"
GAME_RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": 0, "black": 1}')
GAME_ID=$(echo "$GAME_RESPONSE" | grep -o '"gameId":"[^"]*' | cut -d'"' -f4)
STATUS=$(api_request POST "$API_URL/games" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d '{"white": 0, "black": 1}')
assert_status 201 "$STATUS" "Create game"
echo "Game ID: $GAME_ID"

# Test 3: Make valid human move
test_case "Make Valid Human Move"
STATUS=$(api_request POST "$API_URL/games/$GAME_ID/moves" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d '{"move": "e2e4"}')
assert_status 200 "$STATUS" "Valid move e2e4"

# Test 4: Get game state (should auto-execute computer move)
test_case "Get Game State (Auto-Execute Computer)"
RESPONSE=$(api_request GET "$API_URL/games/$GAME_ID")
STATUS=$(api_request GET "$API_URL/games/$GAME_ID" -o /dev/null -w "%{http_code}")
assert_status 200 "$STATUS" "Get game state"
echo "Current turn: $(echo "$RESPONSE" | grep -o '"turn":"[^"]*' | cut -d'"' -f4)"

# Test 5: Make invalid human move
test_case "Make Invalid Human Move"
STATUS=$(api_request POST "$API_URL/games/$GAME_ID/moves" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d '{"move": "e2e5"}')
assert_status 400 "$STATUS" "Invalid move e2e5"

# Test 6: Undo last move
test_case "Undo Last Move"
STATUS=$(api_request POST "$API_URL/games/$GAME_ID/undo" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d '{"count": 2}')
assert_status 200 "$STATUS" "Undo 2 moves"

# Test 7: Get ASCII board
test_case "Get ASCII Board"
BOARD_RESPONSE=$(api_request GET "$API_URL/games/$GAME_ID/board")
STATUS=$(api_request GET "$API_URL/games/$GAME_ID/board" -o /dev/null -w "%{http_code}")
assert_status 200 "$STATUS" "Get board"
echo "$BOARD_RESPONSE" | grep -o '"board":"[^"]*' | cut -d'"' -f4 | sed 's/\\n/\n/g'

# Test 8: Delete game
test_case "Delete Game"
STATUS=$(api_request DELETE "$API_URL/games/$GAME_ID" -o /dev/null -w "%{http_code}")
assert_status 204 "$STATUS" "Delete game"

# Test 9: Create computer vs computer game
test_case "Create Computer vs Computer Game"
COMP_RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": 1, "black": 1}')
COMP_ID=$(echo "$COMP_RESPONSE" | grep -o '"gameId":"[^"]*' | cut -d'"' -f4)
echo "Computer game ID: $COMP_ID"

# Test 10: Multiple GET requests to observe progress
test_case "Computer vs Computer Progress"
for i in {1..3}; do
    RESPONSE=$(api_request GET "$API_URL/games/$COMP_ID")
    MOVES=$(echo "$RESPONSE" | grep -o '"moves":\[[^]]*' | cut -d'[' -f2 | cut -d']' -f1)
    STATE=$(echo "$RESPONSE" | grep -o '"state":"[^"]*' | cut -d'"' -f4)
    echo "Move $i - State: $STATE, Moves made: $(echo "$MOVES" | grep -o ',' | wc -l)"
    if [ "$STATE" != "ongoing" ]; then
        echo "Game ended: $STATE"
        break
    fi
done

# Test 11: Clean up computer game
api_request DELETE "$API_URL/games/$COMP_ID" > /dev/null

# Test 12: Rate limiting - 2 requests within 1 second
test_case "Rate Limiting - Rapid Requests"
STATUS1=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/games/test")
STATUS2=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/games/test")
assert_status 404 "$STATUS1" "First request"
assert_status 429 "$STATUS2" "Second request (rate limited)"
echo "Test will fail in dev mode as rate limiter is permissive."

# Test 13: Wait and retry after rate limit
test_case "Rate Limit Recovery"
sleep 1.5
STATUS=$(api_request GET "$API_URL/games/test" -o /dev/null -w "%{http_code}")
assert_status 404 "$STATUS" "Request after waiting"

# Test 14: Test with X-Forwarded-For (different IPs)
test_case "Rate Limiting with Different IPs"
STATUS1=$(api_request GET "$API_URL/games/test" -o /dev/null -w "%{http_code}" -H "X-Forwarded-For: 192.168.1.1")
STATUS2=$(api_request GET "$API_URL/games/test" -o /dev/null -w "%{http_code}" -H "X-Forwarded-For: 192.168.1.2")
assert_status 404 "$STATUS1" "IP 192.168.1.1"
assert_status 404 "$STATUS2" "IP 192.168.1.2 (different IP)"

# Test 15: Invalid JSON body
test_case "Invalid JSON Body"
STATUS=$(api_request POST "$API_URL/games" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d 'invalid json')
assert_status 400 "$STATUS" "Invalid JSON"

# Test 16: Wrong Content-Type header
test_case "Wrong Content-Type Header"
STATUS=$(api_request POST "$API_URL/games" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: text/plain" \
    -d '{"white": 0, "black": 1}')
assert_status 415 "$STATUS" "Wrong Content-Type"

# Test 17: Non-existent gameId
test_case "Non-existent Game ID"
STATUS=$(api_request GET "$API_URL/games/non-existent-id" -o /dev/null -w "%{http_code}")
assert_status 404 "$STATUS" "Game not found"

# Test 18: Create game to test end conditions
test_case "Move When Game Over"
ENDGAME_RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": 0, "black": 0, "fen": "7k/5Q2/5K2/8/8/8/8/8 w - - 0 1"}')
ENDGAME_ID=$(echo "$ENDGAME_RESPONSE" | grep -o '"gameId":"[^"]*' | cut -d'"' -f4)

# Make checkmate move
api_request POST "$API_URL/games/$ENDGAME_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "f7g7"}' > /dev/null

# Try to make another move after checkmate
STATUS=$(api_request POST "$API_URL/games/$ENDGAME_ID/moves" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d '{"move": "h8h7"}')
assert_status 400 "$STATUS" "Move after game over"

# Clean up
api_request DELETE "$API_URL/games/$ENDGAME_ID" > /dev/null

# Test 19: Move when not player's turn - Use human vs human game
test_case "Move When Not Player's Turn"
# Create human vs human game so no automatic moves happen
TURN_RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": 0, "black": 0}')  # BOTH are human players
TURN_ID=$(echo "$TURN_RESPONSE" | grep -o '"gameId":"[^"]*' | cut -d'"' -f4)

# White moves
api_request POST "$API_URL/games/$TURN_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "e2e4"}' > /dev/null

# Now it's black's turn, try to move as white again (should fail)
STATUS=$(api_request POST "$API_URL/games/$TURN_ID/moves" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d '{"move": "d2d4"}')
assert_status 400 "$STATUS" "Move when not turn"

# Clean up
api_request DELETE "$API_URL/games/$TURN_ID" > /dev/null

# Summary
echo -e "\n=== Test Summary ==="
echo -e "${GREEN}Passed: $PASS${NC}"
echo -e "${RED}Failed: $FAIL${NC}"

if [ $FAIL -eq 0 ]; then
    echo -e "\n${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "\n${RED}Some tests failed${NC}"
    exit 1
fi
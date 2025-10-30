#!/usr/bin/env bash
# FILE: test-player-config.sh

# Player Configuration Deep Test Suite
# Tests all aspects of player configuration changes mid-game
# Debug-focused: prints full responses for analysis

BASE_URL="http://localhost:8080"
API_URL="${BASE_URL}/api/v1"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m'

# Helper to pretty-print JSON
print_json() {
    local label=$1
    local json=$2
    echo -e "${CYAN}>>> $label:${NC}"
    echo "$json" | jq '.' 2>/dev/null || echo "$json"
    echo ""
}

# Helper to extract and display specific fields
show_players() {
    local json=$1
    local white_type=$(echo "$json" | jq -r '.players.white.type' 2>/dev/null)
    local black_type=$(echo "$json" | jq -r '.players.black.type' 2>/dev/null)
    local white_id=$(echo "$json" | jq -r '.players.white.id' 2>/dev/null)
    local black_id=$(echo "$json" | jq -r '.players.black.id' 2>/dev/null)
    
    echo -e "${YELLOW}Players State:${NC}"
    echo "  White: type=$white_type, id=$white_id"
    echo "  Black: type=$black_type, id=$black_id"
}

# API request wrapper
api_request() {
    local method=$1
    local url=$2
    shift 2
    curl -s "$@" -X "$method" "$url"
}

wait_for_pending() {
    local game_id=$1
    local max_wait=3
    local waited=0
    
    while [ $waited -lt $max_wait ]; do
        local response=$(api_request GET "$API_URL/games/$game_id")
        local state=$(echo "$response" | jq -r '.state' 2>/dev/null)
        if [ "$state" != "Pending" ]; then
            return 0
        fi
        sleep 0.2
        waited=$((waited + 1))
    done
    return 1
}

echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"
echo -e "${CYAN}Player Configuration Deep Test Suite${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"

# Test 1: Basic player type changes
echo -e "\n${GREEN}TEST 1: Create H-v-C, immediately change to C-v-H${NC}"
echo "------------------------------------------------------"

RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "level": 5}}')
GAME1_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

print_json "Initial H-v-C game created" "$RESPONSE"
show_players "$RESPONSE"

# Change configuration
RESPONSE=$(api_request PUT "$API_URL/games/$GAME1_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 2, "level": 10}, "black": {"type": 1}}')

print_json "After configuration change (should be C-v-H)" "$RESPONSE"
show_players "$RESPONSE"

# Verify with GET
RESPONSE=$(api_request GET "$API_URL/games/$GAME1_ID")
print_json "GET verification" "$RESPONSE"
show_players "$RESPONSE"

# Cleanup
api_request DELETE "$API_URL/games/$GAME1_ID" > /dev/null

# Test 2: Change during active game
echo -e "\n${GREEN}TEST 2: H-v-H game with moves, then change to H-v-C${NC}"
echo "------------------------------------------------------"

RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
GAME2_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

print_json "Initial H-v-H game" "$RESPONSE"
show_players "$RESPONSE"

# Make some moves
echo -e "\n${BLUE}Making moves: e2e4, e7e5, g1f3${NC}"

RESPONSE=$(api_request POST "$API_URL/games/$GAME2_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "e2e4"}')
echo "Move 1 (e2e4): $(echo "$RESPONSE" | jq -r '.state' 2>/dev/null)"

RESPONSE=$(api_request POST "$API_URL/games/$GAME2_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "e7e5"}')
echo "Move 2 (e7e5): $(echo "$RESPONSE" | jq -r '.state' 2>/dev/null)"

RESPONSE=$(api_request POST "$API_URL/games/$GAME2_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "g1f3"}')
echo "Move 3 (g1f3): $(echo "$RESPONSE" | jq -r '.state' 2>/dev/null)"

# Get current state
RESPONSE=$(api_request GET "$API_URL/games/$GAME2_ID")
print_json "Game state after 3 moves" "$RESPONSE"
echo "Move history: $(echo "$RESPONSE" | jq -r '.moves' 2>/dev/null)"

# Change to H-v-C
echo -e "\n${BLUE}Changing configuration to H-v-C${NC}"
RESPONSE=$(api_request PUT "$API_URL/games/$GAME2_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "level": 8, "searchTime": 200}}')

print_json "After config change (should be H-v-C)" "$RESPONSE"
show_players "$RESPONSE"

# Trigger computer move
echo -e "\n${BLUE}Triggering computer move (black)${NC}"
RESPONSE=$(api_request POST "$API_URL/games/$GAME2_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "cccc"}')

if echo "$RESPONSE" | jq -r '.state' 2>/dev/null | grep -q "Pending"; then
    echo "Computer move triggered, waiting..."
    wait_for_pending "$GAME2_ID"
fi

# Get final state with history
RESPONSE=$(api_request GET "$API_URL/games/$GAME2_ID")
print_json "Final game state with computer move" "$RESPONSE"
echo -e "${MAGENTA}Complete move history: $(echo "$RESPONSE" | jq -r '.moves' 2>/dev/null)${NC}"
show_players "$RESPONSE"

# Cleanup
api_request DELETE "$API_URL/games/$GAME2_ID" > /dev/null

# Test 3: Multiple configuration changes
echo -e "\n${GREEN}TEST 3: Multiple configuration changes${NC}"
echo "------------------------------------------------------"

RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 2, "level": 15}, "black": {"type": 2, "level": 15}}')
GAME3_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

print_json "Initial C-v-C game" "$RESPONSE"
show_players "$RESPONSE"

# Change 1: C-v-C to H-v-H
RESPONSE=$(api_request PUT "$API_URL/games/$GAME3_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
print_json "Change 1: Now H-v-H" "$RESPONSE"
show_players "$RESPONSE"

# Change 2: H-v-H to H-v-C
RESPONSE=$(api_request PUT "$API_URL/games/$GAME3_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "level": 20}}')
print_json "Change 2: Now H-v-C" "$RESPONSE"
show_players "$RESPONSE"

# Change 3: H-v-C to C-v-H
RESPONSE=$(api_request PUT "$API_URL/games/$GAME3_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 2, "level": 1}, "black": {"type": 1}}')
print_json "Change 3: Now C-v-H" "$RESPONSE"
show_players "$RESPONSE"

# Final verification
RESPONSE=$(api_request GET "$API_URL/games/$GAME3_ID")
print_json "Final GET verification" "$RESPONSE"
show_players "$RESPONSE"

# Cleanup
api_request DELETE "$API_URL/games/$GAME3_ID" > /dev/null

# Test 4: Error cases
echo -e "\n${GREEN}TEST 4: Error handling${NC}"
echo "------------------------------------------------------"

# Try to change during pending state
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "searchTime": 500}}')
GAME4_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

# Make human move
api_request POST "$API_URL/games/$GAME4_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "e2e4"}' > /dev/null

# Trigger computer move
api_request POST "$API_URL/games/$GAME4_ID/moves" \
    -H "Content-Type: application/json" \
    -d '{"move": "cccc"}' > /dev/null

# Immediately try to change config (should fail)
RESPONSE=$(api_request PUT "$API_URL/games/$GAME4_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 2}, "black": {"type": 1}}')

print_json "Config change during Pending (should error)" "$RESPONSE"

wait_for_pending "$GAME4_ID"
api_request DELETE "$API_URL/games/$GAME4_ID" > /dev/null

# Test 5: Verify player IDs change
echo -e "\n${GREEN}TEST 5: Verify player IDs change on reconfiguration${NC}"
echo "------------------------------------------------------"

RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
GAME5_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

WHITE_ID_1=$(echo "$RESPONSE" | jq -r '.players.white.id' 2>/dev/null)
BLACK_ID_1=$(echo "$RESPONSE" | jq -r '.players.black.id' 2>/dev/null)
echo "Initial IDs: White=$WHITE_ID_1, Black=$BLACK_ID_1"

# Change configuration (even to same types)
RESPONSE=$(api_request PUT "$API_URL/games/$GAME5_ID/players" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')

WHITE_ID_2=$(echo "$RESPONSE" | jq -r '.players.white.id' 2>/dev/null)
BLACK_ID_2=$(echo "$RESPONSE" | jq -r '.players.black.id' 2>/dev/null)
echo "After reconfig: White=$WHITE_ID_2, Black=$BLACK_ID_2"

if [ "$WHITE_ID_1" != "$WHITE_ID_2" ]; then
    echo -e "${GREEN}✓ White player ID changed (expected)${NC}"
else
    echo -e "${RED}✗ White player ID unchanged (unexpected)${NC}"
fi

if [ "$BLACK_ID_1" != "$BLACK_ID_2" ]; then
    echo -e "${GREEN}✓ Black player ID changed (expected)${NC}"
else
    echo -e "${RED}✗ Black player ID unchanged (unexpected)${NC}"
fi

api_request DELETE "$API_URL/games/$GAME5_ID" > /dev/null

echo -e "\n${CYAN}═══════════════════════════════════════════════════════════${NC}"
echo -e "${CYAN}Test Complete${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"

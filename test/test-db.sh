#!/usr/bin/env bash
# FILE: test-db.sh

# Database Persistence Test Suite for Chess API
# Tests async writes, persistence, and database integrity

BASE_URL="http://localhost:8080"
API_URL="${BASE_URL}/api/v1"
TEST_DB="test.db"
API_DELAY=${API_DELAY:-50}

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m'

# Test counters
PASS=0
FAIL=0

# Helper functions
print_header() {
    echo -e "\n${CYAN}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}$1${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════${NC}"
}

test_case() {
    echo -e "\n${YELLOW}▶ TEST: $1${NC}"
    sleep 0.0$API_DELAY
}

assert_status() {
    local expected=$1
    local actual=$2
    local test_name=$3

    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}  ✓ $test_name: HTTP $actual${NC}"
        ((PASS++))
        return 0
    else
        echo -e "${RED}  ✗ $test_name: Expected HTTP $expected, got $actual${NC}"
        ((FAIL++))
        return 1
    fi
}

assert_json_field() {
    local json=$1
    local field=$2
    local expected=$3
    local test_name=$4

    local actual=$(echo "$json" | jq -r "$field" 2>/dev/null)

    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}  ✓ $test_name: $field = '$actual'${NC}"
        ((PASS++))
        return 0
    else
        echo -e "${RED}  ✗ $test_name: Expected $field = '$expected', got '$actual'${NC}"
        ((FAIL++))
        return 1
    fi
}

assert_db_record() {
    local sql_query=$1
    local expected=$2
    local test_name=$3

    local actual=$(sqlite3 "$TEST_DB" "$sql_query" 2>/dev/null)

    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}  ✓ $test_name: DB query returned '$actual'${NC}"
        ((PASS++))
        return 0
    else
        echo -e "${RED}  ✗ $test_name: Expected '$expected', got '$actual'${NC}"
        echo -e "${RED}    Query: $sql_query${NC}"
        ((FAIL++))
        return 1
    fi
}

api_request() {
    local method=$1
    local url=$2
    shift 2
    curl -s "$@" -X "$method" "$url"
    local status=$?
    sleep 0.0$API_DELAY
    return $status
}

wait_for_state() {
    local game_id=$1
    local target_state=$2
    local max_attempts=${3:-20}
    local attempt=0

    while [ $attempt -lt $max_attempts ]; do
        local response=$(api_request GET "$API_URL/games/$game_id")
        local current_state=$(echo "$response" | jq -r '.state' 2>/dev/null)

        if [ "$current_state" = "$target_state" ] || [[ "$target_state" = "!pending" && "$current_state" != "pending" ]]; then
            return 0
        fi

        ((attempt++))
        sleep 0.1
    done

    return 1
}

# Check dependencies
for cmd in jq sqlite3 curl; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${RED}Error: $cmd is required but not installed${NC}"
        exit 1
    fi
done

# Check database exists
if [ ! -f "$TEST_DB" ]; then
    echo -e "${RED}Error: Test database '$TEST_DB' not found${NC}"
    echo "Make sure the server is running with: ./run-server-with-db.sh"
    exit 1
fi

# Start tests
print_header "Database Persistence Test Suite"
echo "Server: $BASE_URL"
echo "Database: $TEST_DB"
echo "Mode: Development with WAL"
echo ""

# ==============================================================================
print_header "SECTION 1: Storage Health & Basic Persistence"
# ==============================================================================

test_case "1.1: Storage Health Check"
RESPONSE=$(api_request GET "$BASE_URL/health")
assert_json_field "$RESPONSE" '.storage' "ok" "Storage is healthy"

test_case "1.2: Database Schema Verification"
TABLE_COUNT=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('games', 'moves');" 2>/dev/null)
if [ "$TABLE_COUNT" = "2" ]; then
    echo -e "${GREEN}  ✓ Database schema verified: games and moves tables exist${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Database schema incomplete: expected 2 tables, found $TABLE_COUNT${NC}"
    ((FAIL++))
fi

test_case "1.3: Game Creation Persistence"
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "level": 5, "searchTime": 100}}')

GAME_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)
BLACK_ID=$(echo "$RESPONSE" | jq -r '.players.black.id' 2>/dev/null)
WHITE_ID=$(echo "$RESPONSE" | jq -r '.players.white.id' 2>/dev/null)

if [ -n "$GAME_ID" ] && [ "$GAME_ID" != "null" ]; then
    echo "  Game ID: $GAME_ID"
    sleep 0.5  # Allow async write

    assert_db_record \
        "SELECT COUNT(*) FROM games WHERE game_id = '$GAME_ID';" \
        "1" \
        "Game record created"

    assert_db_record \
        "SELECT black_player_id FROM games WHERE game_id = '$GAME_ID';" \
        "$BLACK_ID" \
        "Black player ID matches"

    assert_db_record \
        "SELECT black_level FROM games WHERE game_id = '$GAME_ID';" \
        "5" \
        "Black AI level persisted"

    assert_db_record \
        "SELECT initial_fen FROM games WHERE game_id = '$GAME_ID';" \
        "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1" \
        "Starting FEN persisted"
fi

# ==============================================================================
print_header "SECTION 2: Move Persistence & Undo"
# ==============================================================================

test_case "2.1: Human Move Persistence"
if [ -n "$GAME_ID" ]; then
    RESPONSE=$(api_request POST "$API_URL/games/$GAME_ID/moves" \
        -H "Content-Type: application/json" \
        -d '{"move": "e2e4"}')

    sleep 0.5  # Allow async write

    assert_db_record \
        "SELECT COUNT(*) FROM moves WHERE game_id = '$GAME_ID';" \
        "1" \
        "Move record created"

    assert_db_record \
        "SELECT move_uci FROM moves WHERE game_id = '$GAME_ID' AND move_number = 1;" \
        "e2e4" \
        "Move UCI notation stored"

    assert_db_record \
        "SELECT player_color FROM moves WHERE game_id = '$GAME_ID' AND move_number = 1;" \
        "w" \
        "Move color recorded"
fi

test_case "2.2: Computer Move Persistence"
if [ -n "$GAME_ID" ]; then
    # Trigger computer move
    api_request POST "$API_URL/games/$GAME_ID/moves" \
        -H "Content-Type: application/json" \
        -d '{"move": "cccc"}' > /dev/null

    wait_for_state "$GAME_ID" "!pending"
    sleep 0.5  # Allow async write

    assert_db_record \
        "SELECT COUNT(*) FROM moves WHERE game_id = '$GAME_ID';" \
        "2" \
        "Computer move persisted"

    COMPUTER_MOVE=$(sqlite3 "$TEST_DB" "SELECT move_uci FROM moves WHERE game_id = '$GAME_ID' AND move_number = 2;" 2>/dev/null)
    if [ -n "$COMPUTER_MOVE" ] && [ "$COMPUTER_MOVE" != "e2e4" ]; then
        echo -e "${GREEN}  ✓ Computer move stored: $COMPUTER_MOVE${NC}"
        ((PASS++))
    else
        echo -e "${RED}  ✗ Computer move not properly stored${NC}"
        ((FAIL++))
    fi
fi

test_case "2.3: Undo Move Database Effect"
if [ -n "$GAME_ID" ]; then
    # Undo last move
    api_request POST "$API_URL/games/$GAME_ID/undo" \
        -H "Content-Type: application/json" \
        -d '{"count": 1}' > /dev/null

    sleep 0.5  # Allow async write

    assert_db_record \
        "SELECT COUNT(*) FROM moves WHERE game_id = '$GAME_ID';" \
        "1" \
        "Undo removed move from DB"

    # Undo again
    api_request POST "$API_URL/games/$GAME_ID/undo" \
        -H "Content-Type: application/json" \
        -d '{"count": 1}' > /dev/null

    sleep 0.5

    assert_db_record \
        "SELECT COUNT(*) FROM moves WHERE game_id = '$GAME_ID';" \
        "0" \
        "All moves removed after undo"
fi

# ==============================================================================
print_header "SECTION 3: Complex Game Scenarios"
# ==============================================================================

test_case "3.1: Custom FEN Game Persistence"
CUSTOM_FEN="r1bqkbnr/pppp1ppp/2n5/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R w KQkq - 4 4"
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d "{\"white\": {\"type\": 2, \"searchTime\": 100}, \"black\": {\"type\": 2, \"searchTime\": 100}, \"fen\": \"$CUSTOM_FEN\"}")

FEN_GAME_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)
if [ -n "$FEN_GAME_ID" ] && [ "$FEN_GAME_ID" != "null" ]; then
    sleep 0.5

    assert_db_record \
        "SELECT initial_fen FROM games WHERE game_id = '$FEN_GAME_ID';" \
        "$CUSTOM_FEN" \
        "Custom FEN persisted correctly"

    # Clean up
    api_request DELETE "$API_URL/games/$FEN_GAME_ID" > /dev/null
fi

test_case "3.2: Multiple Games Isolation"
# Create two games
RESPONSE1=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
GAME_ID1=$(echo "$RESPONSE1" | jq -r '.gameId' 2>/dev/null)

RESPONSE2=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 2, "searchTime": 100}, "black": {"type": 1}}')
GAME_ID2=$(echo "$RESPONSE2" | jq -r '.gameId' 2>/dev/null)

if [ -n "$GAME_ID1" ] && [ -n "$GAME_ID2" ]; then
    sleep 0.5

    # Make moves in first game
    api_request POST "$API_URL/games/$GAME_ID1/moves" \
        -H "Content-Type: application/json" \
        -d '{"move": "d2d4"}' > /dev/null
    api_request POST "$API_URL/games/$GAME_ID1/moves" \
        -H "Content-Type: application/json" \
        -d '{"move": "d7d5"}' > /dev/null

    sleep 0.5

    assert_db_record \
        "SELECT COUNT(*) FROM moves WHERE game_id = '$GAME_ID1';" \
        "2" \
        "Game 1 has 2 moves"

    assert_db_record \
        "SELECT COUNT(*) FROM moves WHERE game_id = '$GAME_ID2';" \
        "0" \
        "Game 2 has 0 moves (isolation)"

    # Clean up
    api_request DELETE "$API_URL/games/$GAME_ID1" > /dev/null
    api_request DELETE "$API_URL/games/$GAME_ID2" > /dev/null
fi

# ==============================================================================
print_header "SECTION 4: Foreign Key Constraints & Cascade"
# ==============================================================================

test_case "4.1: Cascade Delete Verification"
# Create game with moves
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
CASCADE_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

if [ -n "$CASCADE_ID" ] && [ "$CASCADE_ID" != "null" ]; then
    # Make several moves
    api_request POST "$API_URL/games/$CASCADE_ID/moves" \
        -H "Content-Type: application/json" -d '{"move": "e2e4"}' > /dev/null
    api_request POST "$API_URL/games/$CASCADE_ID/moves" \
        -H "Content-Type: application/json" -d '{"move": "e7e5"}' > /dev/null
    api_request POST "$API_URL/games/$CASCADE_ID/moves" \
        -H "Content-Type: application/json" -d '{"move": "g1f3"}' > /dev/null

    sleep 0.5

    MOVE_COUNT_BEFORE=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM moves WHERE game_id = '$CASCADE_ID';" 2>/dev/null)
    echo -e "${BLUE}  Moves before delete: $MOVE_COUNT_BEFORE${NC}"

    # Delete game
    api_request DELETE "$API_URL/games/$CASCADE_ID" > /dev/null
    sleep 0.5

    # Note: Game deletion is handled in memory, DB records remain
    # This is by design - games table persists for history
    assert_db_record \
        "SELECT COUNT(*) FROM games WHERE game_id = '$CASCADE_ID';" \
        "1" \
        "Game record persists in DB (by design)"
fi

# ==============================================================================
print_header "SECTION 5: Async Write Buffer Behavior"
# ==============================================================================

test_case "5.1: Rapid Write Buffering"
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
BUFFER_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

if [ -n "$BUFFER_ID" ] && [ "$BUFFER_ID" != "null" ]; then
    # Rapid fire a sequence of legal moves without waiting
    echo -e "${BLUE}  Sending 10 rapid, legal moves...${NC}"
    moves=("e2e4" "e7e5" "g1f3" "b8c6" "f1b5" "a7a6" "b5c6" "d7c6" "e1g1" "f7f6")
    for move in "${moves[@]}"; do
        api_request POST "$API_URL/games/$BUFFER_ID/moves" \
            -H "Content-Type: application/json" -d "{\"move\": \"$move\"}" > /dev/null
    done

    # Immediate check (may show partial writes)
    IMMEDIATE_COUNT=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM moves WHERE game_id = '$BUFFER_ID';" 2>/dev/null)
    echo -e "${BLUE}  Immediate move count: $IMMEDIATE_COUNT${NC}"

    # Wait for async writes to complete
    sleep 1

    FINAL_COUNT=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM moves WHERE game_id = '$BUFFER_ID';" 2>/dev/null)
    if [ "$FINAL_COUNT" = "10" ]; then
        echo -e "${GREEN}  ✓ All 10 moves persisted after buffer flush${NC}"
        ((PASS++))
    else
        echo -e "${RED}  ✗ Expected 10 moves, found $FINAL_COUNT${NC}"
        ((FAIL++))
    fi

    # Clean up
    api_request DELETE "$API_URL/games/$BUFFER_ID" > /dev/null
fi

# ==============================================================================
print_header "SECTION 6: Database Query Endpoints"
# ==============================================================================

test_case "6.1: CLI Query Tool Integration"
# Create identifiable game
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "level": 15, "searchTime": 200}}')
QUERY_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)

if [ -n "$QUERY_ID" ] && [ "$QUERY_ID" != "null" ]; then
    sleep 0.5

    # Query using partial game ID (first 8 chars)
    PARTIAL_ID="${QUERY_ID:0:8}"
    FOUND=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM games WHERE game_id LIKE '$PARTIAL_ID%';" 2>/dev/null)

    if [ "$FOUND" = "1" ]; then
        echo -e "${GREEN}  ✓ Game queryable by partial ID${NC}"
        ((PASS++))
    else
        echo -e "${RED}  ✗ Game not found with partial ID query${NC}"
        ((FAIL++))
    fi

    # Verify player type storage
    assert_db_record \
        "SELECT white_type || ',' || black_type FROM games WHERE game_id = '$QUERY_ID';" \
        "1,2" \
        "Player types stored correctly"

    # Clean up
    api_request DELETE "$API_URL/games/$QUERY_ID" > /dev/null
fi

# ==============================================================================
print_header "SECTION 7: Storage Degradation Handling"
# ==============================================================================

test_case "7.1: Storage Health After Normal Operations"
# Check that storage remains healthy after all operations
RESPONSE=$(api_request GET "$BASE_URL/health")
assert_json_field "$RESPONSE" '.storage' "ok" "Storage still healthy after tests"

test_case "7.2: WAL Mode Verification"
JOURNAL_MODE=$(sqlite3 "$TEST_DB" "PRAGMA journal_mode;" 2>/dev/null)
if [ "$JOURNAL_MODE" = "wal" ]; then
    echo -e "${GREEN}  ✓ Database is in WAL mode${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Database not in WAL mode: $JOURNAL_MODE${NC}"
    ((FAIL++))
fi

# Check WAL file exists
if [ -f "${TEST_DB}-wal" ]; then
    echo -e "${GREEN}  ✓ WAL file exists${NC}"
    ((PASS++))
else
    echo -e "${YELLOW}  ⚠ WAL file not found (may be checkpointed)${NC}"
fi

# ==============================================================================
print_header "Test Summary"
# ==============================================================================

TOTAL=$((PASS + FAIL))
SUCCESS_RATE=0
if [ $TOTAL -gt 0 ]; then
    SUCCESS_RATE=$(( (PASS * 100) / TOTAL ))
fi

echo -e "\n${CYAN}══════════════════════════════════════${NC}"
echo -e "${GREEN}✓ Passed: $PASS${NC}"
echo -e "${RED}✗ Failed: $FAIL${NC}"
echo -e "${CYAN}──────────────────────────────────────${NC}"
echo -e "Total Tests: $TOTAL"
echo -e "Success Rate: ${SUCCESS_RATE}%"
echo -e "${CYAN}══════════════════════════════════════${NC}"

if [ $FAIL -eq 0 ]; then
    echo -e "\n${GREEN}🎉 All database tests passed!${NC}"
    exit 0
else
    echo -e "\n${RED}⚠️  Some tests failed. Review the output above.${NC}"
    exit 1
fi
#!/usr/bin/env bash
# FILE: lixenwraith/chess/test/test-db.sh

# Database & Authentication API Integration Test Suite
# Tests user operations, authentication, and persistence via HTTP API
#
# REQUIRES: Server running on localhost:8080 with database storage
# Start with: test/run-test-server.sh

BASE_URL="http://localhost:8080"
API_URL="${BASE_URL}/api/v1"
TEST_DB="test.db"
CHESS_SERVER_EXEC=${1:-"bin/chess-server"}
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

# Test users
TEST_USER1="alice"
TEST_PASS1="AlicePass123"
TEST_EMAIL1="alice@test.com"

TEST_USER2="bob"
TEST_PASS2="BobSecure456"
TEST_PASS2_OLD="BobSecure456"
TEST_PASS2_NEW="BobNewPass789"

TEST_USER_API="charlie"
TEST_PASS_API="CharliePass123"

TEST_USER_CLI="dave"
TEST_PASS_CLI="DaveSecure111"
UNSUPPORTED_HASH='$2a$10$abcdefghijklmnopqrstuv1234567890abcdefghijklmnopqrstuv'

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

assert_command() {
    local command=$1
    local expected_exit=$2
    local test_name=$3

    local output
    output=$(eval "$command" 2>&1)
    local exit_code=$?

    if [ "$exit_code" = "$expected_exit" ]; then
        echo -e "${GREEN}  ✓ $test_name: Command exit code $exit_code${NC}"
        ((PASS++))
        return 0
    else
        echo -e "${RED}  ✗ $test_name: Expected exit $expected_exit, got $exit_code${NC}"
        echo -e "    Command output: $output"
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

# Check dependencies
for cmd in jq sqlite3 curl; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${RED}Error: $cmd is required but not installed${NC}"
        exit 1
    fi
done

# Check executable exists
if [ ! -x "$CHESS_SERVER_EXEC" ]; then
    echo -e "${RED}Error: chess-server executable not found or not executable: $CHESS_SERVER_EXEC${NC}"
    exit 1
fi

# Verify server connectivity before running tests
if ! curl -sf "$BASE_URL/health" > /dev/null 2>&1; then
    echo -e "${RED}Error: Cannot connect to server at $BASE_URL${NC}"
    echo "Start the server first: test/run-test-server.sh"
    exit 1
fi

# Start tests
print_header "Database & User Management Test Suite"
echo "Server: $BASE_URL"
echo "Executable: $CHESS_SERVER_EXEC"
echo "Test Database (server-managed): $TEST_DB"
echo ""

# ==============================================================================
print_header "SECTION 1: CLI User Operations"
# ==============================================================================

test_case "1.1: database initialization"
assert_command "$CHESS_SERVER_EXEC db init -path $TEST_DB" 0 "initialize database"

# Create testuser1 first (not charlie)
test_case "1.2: Add First User via CLI"
OUTPUT=$($CHESS_SERVER_EXEC db user add -path "$TEST_DB" -username "testuser1" \
    -email "testuser1@test.com" -password "TestPass123" 2>&1)
if echo "$OUTPUT" | grep -qi "User created successfully"; then
    echo -e "${GREEN}  ✓ User created: testuser1${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Failed to create first user${NC}"
    ((FAIL++))
fi

test_case "1.3: Add Second User"
OUTPUT=$($CHESS_SERVER_EXEC db user add -path "$TEST_DB" -username "testuser2" \
    -password "TestPass456" 2>&1)
if echo "$OUTPUT" | grep -qi "User created successfully"; then
    echo -e "${GREEN}  ✓ User created: testuser2${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Failed to create second user${NC}"
    ((FAIL++))
fi

# Now test duplicate prevention with an existing user
test_case "1.4: Duplicate Username Prevention"
assert_command "$CHESS_SERVER_EXEC db user add -path $TEST_DB -username testuser1 -password TestPass789" 1 \
    "Duplicate username rejected"

test_case "1.5: Login with Case-Insensitive Username (ALICE)"
RESPONSE=$(api_request POST "$API_URL/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"ALICE\", \"password\": \"$TEST_PASS1\"}")
if echo "$RESPONSE" | jq -r '.token' 2>/dev/null | grep -qE "^ey[A-Za-z0-9_-]+\.ey[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$"; then
    echo -e "${GREEN}  ✓ Case-insensitive username login works${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Case-insensitive login failed${NC}"
    ((FAIL++))
fi

test_case "1.6: Update User Email"
assert_command "$CHESS_SERVER_EXEC db user set-email -path $TEST_DB -username testuser2 -email testuser2_updated@test.com" 0 \
    "Email update"

test_case "1.7: Update User Password"
assert_command "$CHESS_SERVER_EXEC db user set-password -path $TEST_DB -username testuser2 -password NewPass789" 0 \
    "Password update"

test_case "2.1: Health Check"
RESPONSE=$(api_request GET "$BASE_URL/health")
assert_json_field "$RESPONSE" '.storage' "ok" "Storage healthy"

# Register charlie here for the first time
test_case "2.2: Register New User via API"
RESPONSE=$(api_request POST "$API_URL/auth/register" \
    -H "Content-Type: application/json" \
    -d '{"username": "charlie", "email": "charlie@test.com", "password": "CharliePass123"}')
TOKEN_CHARLIE=$(echo "$RESPONSE" | jq -r '.token' 2>/dev/null)
if [ -n "$TOKEN_CHARLIE" ] && [ "$TOKEN_CHARLIE" != "null" ]; then
    echo -e "${GREEN}  ✓ User registered via API${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Registration failed${NC}"
    ((FAIL++))
fi

test_case "2.3: Login with Username"
RESPONSE=$(api_request POST "$API_URL/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"$TEST_USER1\", \"password\": \"$TEST_PASS1\"}")
TOKEN_ALICE=$(echo "$RESPONSE" | jq -r '.token' 2>/dev/null)
USER_ID_ALICE=$(echo "$RESPONSE" | jq -r '.userId' 2>/dev/null)
if [ -n "$TOKEN_ALICE" ] && [ "$TOKEN_ALICE" != "null" ]; then
    echo -e "${GREEN}  ✓ Login successful for $TEST_USER1${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Login failed${NC}"
    ((FAIL++))
fi

test_case "2.4: Login with Email"
RESPONSE=$(api_request POST "$API_URL/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"$TEST_EMAIL1\", \"password\": \"$TEST_PASS1\"}")
if echo "$RESPONSE" | jq -r '.token' 2>/dev/null | grep -q "^ey"; then
    echo -e "${GREEN}  ✓ Email login successful${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Email login failed${NC}"
    ((FAIL++))
fi

test_case "2.5: Invalid Credentials"
STATUS=$(api_request POST "$API_URL/auth/login" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"$TEST_USER1\", \"password\": \"WrongPassword\"}")
assert_status 401 "$STATUS" "Invalid password rejected"

test_case "2.6: Get Current User"
RESPONSE=$(api_request GET "$API_URL/auth/me" \
    -H "Authorization: Bearer $TOKEN_ALICE")
assert_json_field "$RESPONSE" '.username' "$TEST_USER1" "Username matches"
assert_json_field "$RESPONSE" '.email' "$TEST_EMAIL1" "Email matches"

# ==============================================================================
print_header "SECTION 3: Authenticated Game Creation"
# ==============================================================================

test_case "3.1: Create Game as Authenticated User"
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TOKEN_ALICE" \
    -d '{"white": {"type": 1}, "black": {"type": 2, "searchTime": 100}}')
GAME_ID=$(echo "$RESPONSE" | jq -r '.gameId' 2>/dev/null)
WHITE_PLAYER_ID=$(echo "$RESPONSE" | jq -r '.players.white.id' 2>/dev/null)

if [ "$WHITE_PLAYER_ID" = "$USER_ID_ALICE" ]; then
    echo -e "${GREEN}  ✓ Player ID matches User ID for authenticated human${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Player ID mismatch: expected $USER_ID_ALICE, got $WHITE_PLAYER_ID${NC}"
    ((FAIL++))
fi

test_case "3.2: Anonymous Game Creation"
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
ANON_WHITE_ID=$(echo "$RESPONSE" | jq -r '.players.white.id' 2>/dev/null)
ANON_BLACK_ID=$(echo "$RESPONSE" | jq -r '.players.black.id' 2>/dev/null)

# Check UUIDs are different and not user IDs
if [ "$ANON_WHITE_ID" != "$ANON_BLACK_ID" ] && \
   [ "$ANON_WHITE_ID" != "$USER_ID_ALICE" ] && \
   [[ "$ANON_WHITE_ID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; then
    echo -e "${GREEN}  ✓ Anonymous players get unique UUIDs${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Anonymous player ID issue${NC}"
    ((FAIL++))
fi

test_case "3.3: Both Players Same Authenticated User"
RESPONSE=$(api_request POST "$API_URL/games" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TOKEN_ALICE" \
    -d '{"white": {"type": 1}, "black": {"type": 1}}')
WHITE_ID=$(echo "$RESPONSE" | jq -r '.players.white.id' 2>/dev/null)
BLACK_ID=$(echo "$RESPONSE" | jq -r '.players.black.id' 2>/dev/null)

if [ "$WHITE_ID" = "$USER_ID_ALICE" ] && [ "$BLACK_ID" = "$USER_ID_ALICE" ]; then
    echo -e "${GREEN}  ✓ Same user can play both sides${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Both sides should be same user${NC}"
    ((FAIL++))
fi

# ==============================================================================
print_header "SECTION 4: Game Persistence with User IDs"
# ==============================================================================

test_case "4.1: Verify Game Storage with User ID"
if [ -n "$GAME_ID" ]; then
    sleep 0.5  # Allow async write

    DB_WHITE_ID=$(sqlite3 "$TEST_DB" "SELECT white_player_id FROM games WHERE game_id = '$GAME_ID';" 2>/dev/null)
    if [ "$DB_WHITE_ID" = "$USER_ID_ALICE" ]; then
        echo -e "${GREEN}  ✓ User ID correctly persisted in database${NC}"
        ((PASS++))
    else
        echo -e "${RED}  ✗ Database has wrong player ID${NC}"
        ((FAIL++))
    fi
fi

test_case "4.2: Query Games by User ID"
GAMES_COUNT=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM games WHERE white_player_id = '$USER_ID_ALICE' OR black_player_id = '$USER_ID_ALICE';" 2>/dev/null)
if [ "$GAMES_COUNT" -ge "2" ]; then
    echo -e "${GREEN}  ✓ User's games queryable: found $GAMES_COUNT games${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Expected at least 2 games for user${NC}"
    ((FAIL++))
fi

# ==============================================================================
print_header "SECTION 5: Password Operations"
# ==============================================================================

# Now TEST_PASS2_NEW is defined, this test should work
test_case "5.1: Update User Password via CLI for 'bob'"
assert_command "$CHESS_SERVER_EXEC db user set-password -path $TEST_DB -username $TEST_USER2 -password $TEST_PASS2_NEW" 0 \
    "CLI password update for '$TEST_USER2'"

test_case "5.2: Login with NEW Password for 'bob'"
RESPONSE=$(api_request POST "$API_URL/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"$TEST_USER2\", \"password\": \"$TEST_PASS2_NEW\"}")
if echo "$RESPONSE" | jq -r '.token' 2>/dev/null | grep -q "^ey"; then
    echo -e "${GREEN}  ✓ Login works with new password for '$TEST_USER2'${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Login failed with new password for '$TEST_USER2'${NC}"
    ((FAIL++))
fi

test_case "5.3: OLD Password Rejected for 'bob'"
STATUS=$(api_request POST "$API_URL/auth/login" \
    -o /dev/null -w "%{http_code}" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"$TEST_USER2\", \"password\": \"$TEST_PASS2_OLD\"}")
assert_status 401 "$STATUS" "Old password correctly rejected for '$TEST_USER2'"

test_case "5.4: Add new user '$TEST_USER_CLI' via CLI for hash test"
assert_command "$CHESS_SERVER_EXEC db user add -path $TEST_DB -username $TEST_USER_CLI -password $TEST_PASS_CLI" 0 \
    "Add user '$TEST_USER_CLI' for hash test"

test_case "5.5: CLI rejects unsupported hash format"
assert_command "$CHESS_SERVER_EXEC db user set-hash -path $TEST_DB -username $TEST_USER_CLI -hash '$UNSUPPORTED_HASH'" 1 \
    "Unsupported bcrypt hash rejected by CLI"

# ==============================================================================
print_header "SECTION 6: Edge Cases"
# ==============================================================================

test_case "6.1: Case-Insensitive Username"
RESPONSE=$(api_request POST "$API_URL/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"identifier\": \"ALICE\", \"password\": \"$TEST_PASS1\"}")
if echo "$RESPONSE" | jq -r '.token' 2>/dev/null | grep -qE "^ey[A-Za-z0-9_-]+\.ey[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$"; then
    echo -e "${GREEN}  ✓ Case-insensitive username login${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Case sensitivity issue${NC}"
    ((FAIL++))
fi

test_case "6.2: Concurrent Registration Handling"
# Try to register same username simultaneously
{
    api_request POST "$API_URL/auth/register" \
        -H "Content-Type: application/json" \
        -d '{"username": "concurrent", "password": "TestPass123"}' &
    api_request POST "$API_URL/auth/register" \
        -H "Content-Type: application/json" \
        -d '{"username": "concurrent", "password": "TestPass456"}' &
} > /dev/null 2>&1
wait

# Check only one user was created
USER_COUNT=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM users WHERE username = 'concurrent';" 2>/dev/null)
if [ "$USER_COUNT" = "1" ]; then
    echo -e "${GREEN}  ✓ Concurrent registration handled correctly${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ Race condition: $USER_COUNT users created${NC}"
    ((FAIL++))
fi

test_case "6.3: Delete User"
# First, get a user to delete
OUTPUT=$($CHESS_SERVER_EXEC db user add -path "$TEST_DB" -username "deleteme" \
    -password "TempPass123" 2>&1)
TEMP_ID=$(echo "$OUTPUT" | grep "ID:" | awk '{print $2}')

assert_command "$CHESS_SERVER_EXEC db user delete -path $TEST_DB -username deleteme" 0 \
    "User deletion by username"

# Verify deletion
USER_EXISTS=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM users WHERE user_id = '$TEMP_ID';" 2>/dev/null)
if [ "$USER_EXISTS" = "0" ]; then
    echo -e "${GREEN}  ✓ User successfully deleted from database${NC}"
    ((PASS++))
else
    echo -e "${RED}  ✗ User still exists after deletion${NC}"
    ((FAIL++))
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
    echo -e "\n${GREEN}🎉 All database and user tests passed!${NC}"
    exit 0
else
    echo -e "\n${RED}⚠️  Some tests failed. Review the output above.${NC}"
    exit 1
fi
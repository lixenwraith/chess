#!/usr/bin/env bash
BASE_URL="${BASE_URL:-http://localhost:8080}"
API_URL="${BASE_URL}/api"
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
PASS=0; FAIL=0

t()  { echo -e "\n${YELLOW}▶ TEST: $1${NC}"; }
ok() { echo -e "${GREEN}  ✓ $1${NC}"; ((PASS++)); }
ko() { echo -e "${RED}  ✗ $1${NC}"; ((FAIL++)); }
req(){ local m=$1 u=$2; shift 2; curl -s "$@" -X "$m" "$u"; }

assert_field() { # json field expected name
    local a
    a=$(echo "$1" | jq -r "$2" 2>/dev/null)
    if [ "$a" = "$3" ]; then
        ok "$4: $2 = '$a'"
    else
        ko "$4: expected $2 = '$3', got '$a'"
    fi
}

MATE_FEN="rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 1 3"   # fool's mate: white to move, mated
STALE_FEN="7k/5Q2/6K1/8/8/8/8/8 b - - 0 1"                                  # black to move, stalemate
PREMATE_W="7k/5Q2/5K2/8/8/8/8/8 w - - 0 1"                                  # f7g7 mates
PREMATE_B="rnbqkbnr/pppp1ppp/8/4p3/6P1/5P2/PPPPP2P/RNBQKBNR b KQkq - 0 2"   # black computer: d8h4#

t "E1: Terminal FEN at creation (HvH)"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d "{\"white\":{\"type\":1},\"black\":{\"type\":1},\"fen\":\"$MATE_FEN\"}")
assert_field "$R" '.state' "black wins" "Creation response carries mate"
G=$(echo "$R" | jq -r '.gameId'); [ "$G" != "null" ] && req DELETE "$API_URL/games/$G" >/dev/null

t "E2: Terminal FEN at creation (human white vs computer) — original repro case 1"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d "{\"white\":{\"type\":1},\"black\":{\"type\":2,\"searchTime\":100},\"fen\":\"$MATE_FEN\"}")
assert_field "$R" '.state' "black wins" "Mate detected, no client trigger needed"
G=$(echo "$R" | jq -r '.gameId'); [ "$G" != "null" ] && req DELETE "$API_URL/games/$G" >/dev/null

t "E3: Terminal FEN at creation (computer white vs human) — original repro case 2"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d "{\"white\":{\"type\":2,\"searchTime\":100},\"black\":{\"type\":1},\"fen\":\"$MATE_FEN\"}")
assert_field "$R" '.state' "black wins" "Mate detected at creation, not via cccc path"
G=$(echo "$R" | jq -r '.gameId'); [ "$G" != "null" ] && req DELETE "$API_URL/games/$G" >/dev/null

t "E4: Stalemate FEN at creation"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d "{\"white\":{\"type\":1},\"black\":{\"type\":1},\"fen\":\"$STALE_FEN\"}")
assert_field "$R" '.state' "stalemate" "Stalemate not misclassified as mate"
G=$(echo "$R" | jq -r '.gameId'); [ "$G" != "null" ] && req DELETE "$API_URL/games/$G" >/dev/null

t "E5: Mate delivered by human move"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d "{\"white\":{\"type\":1},\"black\":{\"type\":1},\"fen\":\"$PREMATE_W\"}")
G=$(echo "$R" | jq -r '.gameId')
R=$(req POST "$API_URL/games/$G/moves" -H "Content-Type: application/json" -d '{"move":"f7g7"}')
assert_field "$R" '.state' "white wins" "Move response carries terminal state"
assert_field "$R" '.lastMove.move' "f7g7" "LastMove present in terminal response"
req DELETE "$API_URL/games/$G" >/dev/null

t "E6: Mate delivered by computer + long-poll wakes with settled state (<5s)"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d "{\"white\":{\"type\":1},\"black\":{\"type\":2,\"level\":20,\"searchTime\":1000},\"fen\":\"$PREMATE_B\"}")
G=$(echo "$R" | jq -r '.gameId')
N=$(echo "$R" | jq -r '.moves | length')
req POST "$API_URL/games/$G/moves" -H "Content-Type: application/json" -d '{"move":"cccc"}' >/dev/null
S=$(date +%s)
R=$(req GET "$API_URL/games/$G?wait=true&moveCount=$N")
E=$(( $(date +%s) - S ))
assert_field "$R" '.state' "black wins" "Computer mate classified"
[ "$E" -lt 5 ] && ok "Poll woke in ${E}s (state-aware notify, no 30s stall)" \
               || ko "Poll took ${E}s — state-only/atomic wake regressed"
req DELETE "$API_URL/games/$G" >/dev/null

t "E7: Delete during long-poll wakes waiter promptly"
R=$(req POST "$API_URL/games" -H "Content-Type: application/json" \
    -d '{"white":{"type":1},"black":{"type":1}}')
G=$(echo "$R" | jq -r '.gameId')
S=$(date +%s)
req GET "$API_URL/games/$G?wait=true&moveCount=0" > /tmp/es_poll.json &
P=$!
sleep 1
req DELETE "$API_URL/games/$G" >/dev/null
wait $P
E=$(( $(date +%s) - S ))
CODE=$(jq -r '.code' /tmp/es_poll.json 2>/dev/null)
if [ "$E" -lt 5 ]; then
    ok "Poll woke in ${E}s (state-aware notify, no 30s stall)"
else
    ko "Poll took ${E}s — state-only/atomic wake regressed"
fi

echo -e "\n${CYAN}Passed: $PASS  Failed: $FAIL${NC}"
[ $FAIL -eq 0 ]


#!/usr/bin/env bash

# Draw and resignation test suite: automatic draws (dead material, threefold
# repetition, fifty-move rule), draw offers between humans and to the
# computer, resignation with slot authorization, undo after a result,
# long-poll wake-up on offers, concessions (final between humans, on record
# against the computer), claim checks on undo, players, and unload, and a game
# whose row is deleted by hand.
# Requires: curl, jq, and a server from test/run-test-server.sh; psql and
# CHESS_TEST_DSN for the deleted-row case (skipped without them).

BASE_URL=${BASE_URL:-"http://localhost:8080"}
API_URL="${BASE_URL}/api"
# Pause between requests; the dev-mode limiter allows 20 per second.
API_DELAY=${API_DELAY:-0.06}

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

PASS=0
FAIL=0
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

section() { echo -e "\n${CYAN}== $1 ==${NC}"; }
ok() { echo -e "${GREEN}  ✓ $1${NC}"; ((PASS++)); }
bad() { echo -e "${RED}  ✗ $1${NC}"; ((FAIL++)); }
skip() { echo -e "${YELLOW}  - $1${NC}"; }

check() { # check <description> <expected> <actual>
    if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: expected '$2', got '$3'"; fi
}

# request <method> <path> [curl args...]: body to $TMP/body, status printed.
request() {
    local method=$1 path=$2
    shift 2
    sleep "$API_DELAY"
    : >"$TMP/body"
    curl -s -o "$TMP/body" -w '%{http_code}' -X "$method" "$@" "$API_URL$path"
}

json() { jq -r "$1" "$TMP/body"; }

# movetext prints the PGN movetext in $TMP/body on one line (PGN wraps at 80).
movetext() { sed -n '/^$/,$p' "$TMP/body" | sed '/^$/d' | tr '\n' ' ' | sed 's/ $//'; }

post() { # post <path> <json> [token]
    local auth=()
    [ -n "$3" ] && auth=(-H "Authorization: Bearer $3")
    request POST "$1" -H 'Content-Type: application/json' "${auth[@]}" -d "$2"
}

new_game() { # new_game <white type> <black type> [fen]: prints the game ID
    local body
    body=$(jq -nc --argjson w "$1" --argjson b "$2" --arg fen "${3:-}" \
        '{white:{type:$w}, black:{type:$b, level:5, searchTime:100}} + (if $fen == "" then {} else {fen:$fen} end)')
    post /games "$body" >/dev/null
    json .gameId
}

play() { # play <gameId> <token|""> <uci>...
    local id=$1 token=$2 status
    shift 2
    for move in "$@"; do
        status=$(post "/games/$id/moves" "{\"move\":\"$move\"}" "$token")
        if [ "$status" != 200 ]; then
            bad "move $move: HTTP $status $(cat "$TMP/body")"
            return 1
        fi
    done
}

register() { # register <user> <password>: prints the session token
    post /auth/register "{\"username\":\"$1\",\"password\":\"$2\"}" >/dev/null
    json .token
}

if ! curl -sf "$BASE_URL/health" >/dev/null; then
    echo -e "${RED}Server not reachable at $BASE_URL; start test/run-test-server.sh${NC}"
    exit 1
fi

SYMMETRIC="g1f3 g8f6 b1c3 b8c6 e2e3 e7e6 d2d3 d7d6 f1e2 f8e7 c1d2 c8d7 e1g1 e8g8 a2a3 a7a6 h2h3 h7h6 a1b1 a8b8"
QUEEN_UP="e2e4 e7e5 g1f3 d8h4 f3h4 b8c6 h4f3 g8f6 b1c3 f8c5 f1e2 d7d6 e1g1 e8g8 d2d3 c8e6 c1e3 c5e3 f2e3 h7h6"

section "1. Automatic draws"
GAME=$(new_game 1 1 "4k3/8/8/8/8/8/3r4/4KB2 w - - 0 1")
play "$GAME" "" e1d2
check "Kxd2 leaves K+B v K: state" "draw" "$(json .state)"
check "termination" "insufficient_material" "$(json .termination)"
check "no move after the draw" 400 "$(post "/games/$GAME/moves" '{"move":"e8d8"}')"
check "error code" "GAME_OVER" "$(json .code)"

GAME=$(new_game 1 1)
play "$GAME" "" g1f3 g8f6 f3g1 f6g8 g1f3 g8f6 f3g1
check "second occurrence plays on" "ongoing" "$(json .state)"
play "$GAME" "" f6g8
check "third occurrence: threefold" "threefold_repetition" "$(json .termination)"
check "history termination" "threefold_repetition" \
    "$(request GET "/games/$GAME/history" >/dev/null; json .termination)"
check "PGN comment" "1. Nf3 Nf6 2. Ng1 Ng8 3. Nf3 Nf6 4. Ng1 Ng8 { Draw by threefold repetition. } 1/2-1/2" \
    "$(request GET "/games/$GAME/pgn" >/dev/null; movetext)"
check "undo reopens the game" 200 "$(post "/games/$GAME/undo" '{"count":1}')"
check "state after undo" "ongoing/null" "$(json '.state + "/" + (.termination // "null")')"

GAME=$(new_game 1 1 "4k3/8/8/8/8/8/8/R3K3 w - - 99 80")
play "$GAME" "" a1a2
check "hundredth quiet ply: fifty-move rule" "draw/fifty_move_rule" "$(json '.state + "/" + .termination')"

post /games '{"white":{"type":1},"black":{"type":1},"fen":"4k3/8/8/8/8/8/8/4K3 w - - 0 1"}' >/dev/null
check "bare kings are drawn at creation" "draw/insufficient_material" "$(json '.state + "/" + .termination')"

section "2. Resignation"
GAME=$(new_game 1 2)
play "$GAME" "" e2e4
check "resign infers the one human side" 200 "$(post "/games/$GAME/resign" '{}')"
check "result" "black wins/resignation" "$(json '.state + "/" + .termination')"
check "resigning twice" 400 "$(post "/games/$GAME/resign" '{}')"
check "PGN comment" "1. e4 { Black wins by resignation. } 0-1" \
    "$(request GET "/games/$GAME/pgn" >/dev/null; movetext)"

GAME=$(new_game 1 1)
check "hot seat needs a color" 400 "$(post "/games/$GAME/resign" '{}')"
check "bad color" 400 "$(post "/games/$GAME/resign" '{"color":"red"}')"

GAME=$(new_game 2 2)
check "computer-only game cannot resign" 400 "$(post "/games/$GAME/resign" '{}')"

PLAYER="draw$(date +%s)$RANDOM"
TOKEN=$(register "$PLAYER" DrawPass1234)
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] && ok "$PLAYER registered" || bad "register: $(cat "$TMP/body")"
GAME=$(new_game 1 1)
play "$GAME" "$TOKEN" e2e4 # claims white
check "anonymous caller cannot resign a claimed side" 403 "$(post "/games/$GAME/resign" '{"color":"w"}')"
check "claimant resigns without naming the side" 200 "$(post "/games/$GAME/resign" '{}' "$TOKEN")"
check "claimant's side lost" "black wins" "$(json .state)"
check "game listed for the claimant" "0-1" "$(request GET "/users/me/games?limit=1" \
    -H "Authorization: Bearer $TOKEN" >/dev/null; json '.games[0].pgnResult')"

section "3. Draw offers between humans"
GAME=$(new_game 1 1)
play "$GAME" "" e2e4
check "black offers" "offered/b" "$(post "/games/$GAME/draw" '{"action":"offer","color":"b"}' >/dev/null; json '.drawOutcome + "/" + .drawOffer')"
check "repeating a standing offer" "offered" "$(post "/games/$GAME/draw" '{"action":"offer","color":"b"}' >/dev/null; json .drawOutcome)"
check "the offerer cannot accept" 400 "$(post "/games/$GAME/draw" '{"action":"accept","color":"b"}')"
check "white declines" "declined/null" "$(post "/games/$GAME/draw" '{"action":"decline","color":"w"}' >/dev/null; json '.drawOutcome + "/" + (.drawOffer // "null")')"
check "second offer in one move" 409 "$(post "/games/$GAME/draw" '{"action":"offer","color":"b"}')"
play "$GAME" "" e7e5 g1f3
post "/games/$GAME/draw" '{"action":"offer","color":"b"}' >/dev/null
check "offer allowed after a move" "b" "$(json .drawOffer)"

# A waiting client wakes up when the offer is answered.
MOVES=$(json '.moves | length')
START=$(date +%s)
curl -s -o "$TMP/poll" "$API_URL/games/$GAME?wait=true&moveCount=$MOVES" &
POLL=$!
sleep 0.5
post "/games/$GAME/draw" '{"action":"accept","color":"w"}' >/dev/null
check "accepting ends the game" "draw/agreement" "$(json '.state + "/" + .termination')"
wait "$POLL"
ELAPSED=$(($(date +%s) - START))
[ "$ELAPSED" -lt 10 ] && ok "long-poll woke after ${ELAPSED}s" || bad "long-poll waited ${ELAPSED}s"
check "long-poll saw the result" "draw" "$(jq -r .state "$TMP/poll")"

GAME=$(new_game 1 1)
play "$GAME" "" e2e4
post "/games/$GAME/draw" '{"action":"offer","color":"w"}' >/dev/null
play "$GAME" "" e7e5
check "moving instead of answering declines" "null" "$(request GET "/games/$GAME" >/dev/null; json '.drawOffer // "null"')"
check "accepting a lapsed offer" 400 "$(post "/games/$GAME/draw" '{"action":"accept","color":"b"}')"
check "unknown action" 400 "$(post "/games/$GAME/draw" '{"action":"maybe","color":"w"}')"

section "4. Draw offers to the computer"
GAME=$(new_game 1 2)
check "declined before move 10" "declined" "$(post "/games/$GAME/draw" '{"action":"offer"}' >/dev/null; json .drawOutcome)"
check "one offer per move" 409 "$(post "/games/$GAME/draw" '{"action":"offer"}')"

# Level position: play 20 plies as humans, then hand Black to the computer.
GAME=$(new_game 1 1)
play "$GAME" "" $SYMMETRIC
request PUT "/games/$GAME/players" -H 'Content-Type: application/json' \
    -d '{"white":{"type":1},"black":{"type":2,"level":5,"searchTime":100}}' >/dev/null
check "computer accepts in a level position" "accepted/draw/agreement" \
    "$(post "/games/$GAME/draw" '{"action":"offer"}' >/dev/null; json '.drawOutcome + "/" + .state + "/" + .termination')"

# White a queen up: the computer playing White declines.
GAME=$(new_game 1 1)
play "$GAME" "" $QUEEN_UP
request PUT "/games/$GAME/players" -H 'Content-Type: application/json' \
    -d '{"white":{"type":2,"level":5,"searchTime":100},"black":{"type":1}}' >/dev/null
check "computer a queen up declines" "declined/ongoing" \
    "$(post "/games/$GAME/draw" '{"action":"offer"}' >/dev/null; json '.drawOutcome + "/" + .state')"

section "5. Concessions"
GAME=$(new_game 1 1)
play "$GAME" "" e2e4
post "/games/$GAME/draw" '{"action":"offer","color":"w"}' >/dev/null
post "/games/$GAME/draw" '{"action":"accept","color":"b"}' >/dev/null
check "hot seat: undo after an agreed draw" 400 "$(post "/games/$GAME/undo" '{"count":1}')"
check "error code" "GAME_OVER" "$(json .code)"

OPPONENT="draw$(date +%s)$RANDOM"
OPPONENT_TOKEN=$(register "$OPPONENT" DrawPass1234)
[ -n "$OPPONENT_TOKEN" ] && [ "$OPPONENT_TOKEN" != null ] && ok "$OPPONENT registered" || bad "register: $(cat "$TMP/body")"
GAME=$(new_game 1 1)
play "$GAME" "$TOKEN" e2e4 # claims white
play "$GAME" "$OPPONENT_TOKEN" e7e5 # claims black
check "two players: no takeback by white" 403 "$(post "/games/$GAME/undo" '{"count":1}' "$TOKEN")"
check "two players: no anonymous undo" 403 "$(post "/games/$GAME/undo" '{"count":1}')"
check "two players: players cannot be changed" 403 "$(request PUT "/games/$GAME/players" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
    -d '{"white":{"type":1},"black":{"type":2,"level":5,"searchTime":100}}')"
check "white resigns" "black wins/resignation" \
    "$(post "/games/$GAME/resign" '{}' "$TOKEN" >/dev/null; json '.state + "/" + .termination')"
check "the resignation stands for black too" 403 "$(post "/games/$GAME/undo" '{"count":1}' "$OPPONENT_TOKEN")"
check "an outsider cannot unload the game" 403 "$(request DELETE "/games/$GAME")"
check "a player can" 204 "$(request DELETE "/games/$GAME" -H "Authorization: Bearer $OPPONENT_TOKEN")"

GAME=$(new_game 1 2)
play "$GAME" "$TOKEN" e2e4
post "/games/$GAME/resign" '{}' "$TOKEN" >/dev/null
check "computer game: anonymous undo of a claimed side" 403 "$(post "/games/$GAME/undo" '{"count":1}')"
check "computer game: the claimant undoes the resignation" 200 "$(post "/games/$GAME/undo" '{"count":1}' "$TOKEN")"
check "play continues, the concession is kept" "ongoing/black wins/resignation/1" \
    "$(json '.state + "/" + .concession.result + "/" + .concession.termination + "/" + (.concession.ply | tostring)')"
play "$GAME" "$TOKEN" d2d4
check "history keeps the concession" "black_wins/resignation/1/none" \
    "$(request GET "/games/$GAME/history" >/dev/null; json '.concession.result + "/" + .concession.termination + "/" + (.concession.ply | tostring) + "/" + (.result // "none")')"
check "PGN notes it" "1. d4 { White resigned at ply 1; play continued. } *" \
    "$(request GET "/games/$GAME/pgn" >/dev/null; movetext)"

section "6. Server"
check "an unknown route is not a missing game" "404/NOT_FOUND" \
    "$(status=$(post "/games/$GAME/surrender" '{}'); echo "$status/$(json .code)")"
check "health reports the build" "true" "$(curl -s "$BASE_URL/health" | jq '.version | length > 0')"

section "7. A game whose row is deleted by hand"
if [ -z "$CHESS_TEST_DSN" ] || ! command -v psql >/dev/null; then
    skip "CHESS_TEST_DSN or psql missing; skipped"
else
    GAME=$(new_game 1 1)
    play "$GAME" "" e2e4
    request GET "/games/$GAME/history" >/dev/null # flush the writes
    psql "$CHESS_TEST_DSN" -qAtc "DELETE FROM games WHERE game_id = '$GAME'" >/dev/null
    check "move after the deletion is accepted in memory" 200 "$(post "/games/$GAME/moves" '{"move":"e7e5"}')"
    for _ in $(seq 1 20); do
        [ "$(request GET "/games/$GAME")" = 404 ] && break
        sleep 0.1
    done
    check "the game is unloaded" 404 "$(request GET "/games/$GAME")"
    check "storage stays healthy" "ok" "$(curl -s "$BASE_URL/health" | jq -r .storage)"
fi

echo -e "\n${CYAN}Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}"
[ "$FAIL" -eq 0 ]

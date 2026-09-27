#!/usr/bin/env bash

# Replay API test suite: SAN in history, PGN export, ETag revalidation,
# promotion handling, FEN validation, and the cursor-paged game listing.
# Requires: curl, jq, and a server from test/run-test-server.sh. Games are
# played human-vs-human, so no engine search is involved.

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

check() { # check <description> <expected> <actual>
    if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: expected '$2', got '$3'"; fi
}

# request <method> <path> [curl args...]: body to $TMP/body, headers to
# $TMP/headers, prints the status code.
request() {
    local method=$1 path=$2
    shift 2
    sleep "$API_DELAY"
    : >"$TMP/body" # curl leaves the file untouched when there is no body (304)
    curl -s -o "$TMP/body" -D "$TMP/headers" -w '%{http_code}' -X "$method" "$@" "$API_URL$path"
}

header() { grep -i "^$1:" "$TMP/headers" | head -1 | cut -d' ' -f2- | tr -d '\r'; }

new_game() { # new_game [fen] [token]
    local body='{"white":{"type":1},"black":{"type":1}}'
    [ -n "$1" ] && body=$(jq -nc --arg fen "$1" '{white:{type:1},black:{type:1},fen:$fen}')
    local auth=()
    [ -n "$2" ] && auth=(-H "Authorization: Bearer $2")
    request POST /games -H 'Content-Type: application/json' "${auth[@]}" -d "$body" >/dev/null
    jq -r '.gameId' "$TMP/body"
}

play() { # play <gameId> <token|""> <uci>...
    local id=$1 token=$2 status
    shift 2
    local auth=()
    [ -n "$token" ] && auth=(-H "Authorization: Bearer $token")
    for move in "$@"; do
        status=$(request POST "/games/$id/moves" -H 'Content-Type: application/json' "${auth[@]}" \
            -d "{\"move\":\"$move\"}")
        if [ "$status" != 200 ]; then
            bad "move $move: HTTP $status $(cat "$TMP/body")"
            return 1
        fi
    done
}

register() { # register <user> <password>: prints the session token
    request POST /auth/register -H 'Content-Type: application/json' \
        -d "{\"username\":\"$1\",\"password\":\"$2\"}" >/dev/null
    jq -r '.token' "$TMP/body"
}

if ! curl -sf "$BASE_URL/health" >/dev/null; then
    echo -e "${RED}Server not reachable at $BASE_URL; start test/run-test-server.sh${NC}"
    exit 1
fi

section "1. Checkmate: SAN, result, termination"
GAME=$(new_game)
play "$GAME" "" f2f3 e7e5 g2g4 d8h4
check "history status" 200 "$(request GET "/games/$GAME/history")"
check "SAN line" "f3 e5 g4 Qh4#" "$(jq -r '[.moves[].san] | join(" ")' "$TMP/body")"
check "pgnResult" "0-1" "$(jq -r .pgnResult "$TMP/body")"
check "termination" "checkmate" "$(jq -r .termination "$TMP/body")"
check "UCI kept" "d8h4" "$(jq -r '.moves[3].moveUci' "$TMP/body")"

section "2. ETag revalidation"
request GET "/games/$GAME/history" >/dev/null
ETAG=$(header ETag)
[ -n "$ETAG" ] && ok "ETag present ($ETAG)" || bad "ETag missing"
check "Cache-Control" "private, no-cache" "$(header Cache-Control)"
check "If-None-Match -> 304" 304 "$(request GET "/games/$GAME/history" -H "If-None-Match: $ETAG")"
check "304 has no body" 0 "$(wc -c <"$TMP/body" | tr -d ' ')"
check "stale ETag -> 200" 200 "$(request GET "/games/$GAME/history" -H 'If-None-Match: "stale"')"

section "3. PGN export"
check "pgn status" 200 "$(request GET "/games/$GAME/pgn")"
check "content type" "application/x-chess-pgn; charset=utf-8" "$(header Content-Type)"
case "$(header Content-Disposition)" in
    "attachment; filename=\"chess-"*"-${GAME:0:8}.pgn\"") ok "attachment filename" ;;
    *) bad "Content-Disposition: $(header Content-Disposition)" ;;
esac
check "seven tag roster order" "Event Site Date Round White Black Result" \
    "$(head -7 "$TMP/body" | sed -E 's/^\[([A-Za-z]+) .*/\1/' | tr '\n' ' ' | sed 's/ $//')"
check "movetext" "1. f3 e5 2. g4 Qh4# 0-1" "$(sed -n '/^$/,$p' "$TMP/body" | sed '/^$/d')"
grep -q '^\[Termination "normal"\]$' "$TMP/body" && ok "Termination tag" || bad "Termination tag missing"
PGN_ETAG=$(header ETag)
check "pgn 304" 304 "$(request GET "/games/$GAME/pgn" -H "If-None-Match: $PGN_ETAG")"
check "pgn ?ply=2" 200 "$(request GET "/games/$GAME/pgn?ply=2")"
check "truncated movetext" "1. f3 e5 *" "$(sed -n '/^$/,$p' "$TMP/body" | sed '/^$/d')"
check "?ply=5 rejected" 400 "$(request GET "/games/$GAME/pgn?ply=5")"
check "?ply=-1 rejected" 400 "$(request GET "/games/$GAME/pgn?ply=-1")"
check "unknown game" 404 "$(request GET "/games/00000000-0000-4000-8000-000000000000/pgn")"
check "malformed ID" 400 "$(request GET "/games/not-a-uuid/pgn")"

section "4. Castling and en passant"
GAME=$(new_game)
play "$GAME" "" e2e4 d7d5 e4e5 f7f5 e5f6 g8f6 g1f3 e7e6 f1e2 f8d6 e1g1 e8g8
request GET "/games/$GAME/history" >/dev/null
check "SAN line" "e4 d5 e5 f5 exf6 Nxf6 Nf3 e6 Be2 Bd6 O-O O-O" \
    "$(jq -r '[.moves[].san] | join(" ")' "$TMP/body")"
check "ongoing pgnResult" "*" "$(jq -r .pgnResult "$TMP/body")"
check "no termination while ongoing" "null" "$(jq -r .termination "$TMP/body")"

section "5. Promotion (the reported position)"
FEN="8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55"
GAME=$(new_game "$FEN")
[ "$GAME" != null ] && ok "custom FEN game created" || bad "custom FEN game: $(cat "$TMP/body")"
check "g7g8 without piece rejected" 400 \
    "$(request POST "/games/$GAME/moves" -H 'Content-Type: application/json' -d '{"move":"g7g8"}')"
case "$(jq -r .error "$TMP/body")" in
    "promotion piece required"*) ok "error names the missing piece" ;;
    *) bad "error: $(cat "$TMP/body")" ;;
esac
play "$GAME" "" g7g8n && ok "g7g8n accepted"
request GET "/games/$GAME/history" >/dev/null
check "promotion SAN" "g8=N" "$(jq -r '.moves[0].san' "$TMP/body")"
check "knight on g8" "6N1/5K2/5n2/p3k3/1p3p2/1P6/P7/8 b - - 0 55" "$(jq -r '.moves[0].fenAfterMove' "$TMP/body")"
request GET "/games/$GAME/pgn" >/dev/null
grep -q '^\[SetUp "1"\]$' "$TMP/body" && grep -q "^\[FEN \"$FEN\"\]$" "$TMP/body" \
    && ok "SetUp/FEN tags" || bad "SetUp/FEN tags missing"
check "numbering from move 55" "55. g8=N *" "$(sed -n '/^$/,$p' "$TMP/body" | sed '/^$/d')"

section "6. Invalid starting positions"
for fen in "8/8/8/8/8/8/8/8 w - - 0 1" "4k3/8/8/8/8/8/8/4K2r b - - 0 1" "4k3/8/8/8/8/8/8/P3K3 w - - 0 1"; do
    status=$(request POST /games -H 'Content-Type: application/json' \
        -d "$(jq -nc --arg fen "$fen" '{white:{type:1},black:{type:1},fen:$fen}')")
    check "rejects $fen ($(jq -r .error "$TMP/body"))" 400 "$status"
done

section "7. My games: cursor paging and filters"
# A fresh account per run, so games from earlier runs do not interfere.
PLAYER="replay$(date +%s)$RANDOM"
TOKEN=$(register "$PLAYER" ReplayPass123)
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] && ok "$PLAYER registered" || bad "register failed: $(cat "$TMP/body")"
IDS=()
for i in 1 2 3; do
    GAME=$(new_game "" "$TOKEN")
    play "$GAME" "$TOKEN" e2e4
    IDS=("$GAME" "${IDS[@]}")
done
# The player takes White only; Black's moves are anonymous and leave that
# slot unclaimed.
GAME=$(new_game "" "$TOKEN")
play "$GAME" "$TOKEN" f2f3 && play "$GAME" "" e7e5 && play "$GAME" "$TOKEN" g2g4 && play "$GAME" "" d8h4
IDS=("$GAME" "${IDS[@]}")

SEEN=()
CURSOR=""
for page in 1 2 3; do
    path="/users/me/games?limit=2"
    [ -n "$CURSOR" ] && path="$path&cursor=$CURSOR"
    check "page $page status" 200 "$(request GET "$path" -H "Authorization: Bearer $TOKEN")"
    mapfile -t PAGE < <(jq -r '.games[].gameId' "$TMP/body")
    SEEN+=("${PAGE[@]}")
    CURSOR=$(jq -r '.nextCursor // empty' "$TMP/body")
    [ -z "$CURSOR" ] && break
done
check "cursor pages cover the games newest first" "${IDS[*]}" "${SEEN[*]}"
check "finished filter" "${IDS[0]}" "$(request GET "/users/me/games?status=finished" \
    -H "Authorization: Bearer $TOKEN" >/dev/null; jq -r '[.games[].gameId] | join(" ")' "$TMP/body")"
check "summary pgnResult" "0-1" "$(jq -r '.games[0].pgnResult' "$TMP/body")"
check "black filter (White only)" 0 "$(request GET "/users/me/games?color=black" \
    -H "Authorization: Bearer $TOKEN" >/dev/null; jq '.games | length' "$TMP/body")"
check "bad cursor" 400 "$(request GET "/users/me/games?cursor=@@" -H "Authorization: Bearer $TOKEN")"
check "cursor with offset" 400 "$(request GET "/users/me/games?offset=2&cursor=$(jq -rn '"x"')" \
    -H "Authorization: Bearer $TOKEN")"
check "bad status" 400 "$(request GET "/users/me/games?status=paused" -H "Authorization: Bearer $TOKEN")"
check "listing needs auth" 401 "$(request GET "/users/me/games")"

echo -e "\n${CYAN}Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}"
[ "$FAIL" -eq 0 ]

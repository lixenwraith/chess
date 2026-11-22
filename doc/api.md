# API Reference

Base URL: `http://localhost:8080/api`

The API is unversioned; `/health` is served at the root, outside `/api`.

Content-Type: `application/json` (required for POST/PUT)

## Authentication

The API supports optional JWT authentication for user accounts. When authenticated, games are associated with the user account.

### Register User
`POST /auth/register`

Creates new user account and returns JWT token.

**Request:**
```json
{
  "username": "alice",
  "email": "alice@example.com",
  "password": "SecurePass123"
}
```

- `username` (string, required): 1-40 characters, alphanumeric and underscore only
- `email` (string, optional): Valid email address
- `password` (string, required): Minimum 8 characters, must contain letter and number

Accounts created here are identical to CLI-created accounts and do not
expire. Returns 409 when the username or email is taken, and 503 when the
registration limit (`-max-users`) or password-hashing capacity is exhausted.

**Response (201):**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "username": "alice",
  "email": "alice@example.com",
  "expiresAt": "2025-01-14T10:30:00Z"
}
```

### Login
`POST /auth/login`

Authenticates user and returns JWT token.

**Request:**
```json
{
  "identifier": "alice",
  "password": "SecurePass123"
}
```

- `identifier` (string, required): Username or email address
- `password` (string, required): User password

**Response (200):**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "username": "alice",
  "email": "alice@example.com",
  "expiresAt": "2025-01-14T10:30:00Z"
}
```

### Get Current User
`GET /auth/me`

Returns authenticated user information. Requires authentication.

**Headers:**
```
Authorization: Bearer <token>
```

**Response (200):**
```json
{
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "username": "alice",
  "email": "alice@example.com",
  "createdAt": "2025-01-07T10:30:00Z"
}
```

### Delete Account
`DELETE /auth/me`

Deletes the caller's own account. Requires authentication and the account
password again, so a token left on an unattended device is not enough.

**Request:**
```json
{"password": "SecurePass123"}
```

**Response (204):** no body.

What happens, in order: the session ends, so the token stops working at
once; the account's claims are released on every game in memory; then, behind
any gameplay write already queued, the account's claims and name snapshots are
cleared from every stored game and the account is deleted. A game no other
user claims is anonymous from then on and is purged 24 hours after its last
activity; a game another user claims stays with that user, with this side
unnamed. (The administrative `db user delete` keeps claims and names for the
record instead.)

Errors: 400 `INVALID_REQUEST` without a password, 401 for a wrong password or
token, 429 past 5 attempts a minute per IP, 503 `RESOURCE_LIMIT` when password
hashing is saturated or `STORAGE_UNAVAILABLE` when storage is down.

### Request a Password Reset
`POST /auth/recover`

Emails a one-time reset link to the account's address. The request is
answered before the account is looked up, so the response, and how soon it
comes, is the same whether or not the account exists or has an email. Mail
is the server's to ration, since an account's address is unverified:

- An address gets one message at once, a second no sooner than a minute
  after it, and then none until 24 hours after the second. A request in
  between mails nothing and leaves the last link working. The address is
  counted, not the account: deleting an account and registering its address
  again buys no further mail.
- All addresses together get 50 messages in 24 hours; past that nothing is
  mailed and the server logs a warning.
- The message carries the link and nothing its requester chose: no name and
  no text of theirs.

A newer link replaces a pending one. A message the relay refuses is logged,
its link revoked, and its place in the limits given back.

**Request:**
```json
{"identifier": "alice"}
```

- `identifier` (string, required): Username or email address

**Response (202):**
```json
{"message": "If the account has an email address, a reset link is on its way to it."}
```

Errors: 429 past 3 requests a minute per IP, 503 `NOT_CONFIGURED` when the
server has no mail transport (`/health` reports `"mail": "disabled"`), 503
`RESOURCE_LIMIT` when 64 requests already wait for the sender.

### Reset a Password
`POST /auth/reset`

Sets a new password with the token from the link (`?reset=<token>` on the web
client) and signs the user in; the token works once, for an hour, and every
other session of the account ends.

**Request:**
```json
{"token": "…", "password": "NewPass123"}
```

**Response (200):** as login (`token`, `userId`, `username`, `email`,
`expiresAt`).

Errors: 400 `INVALID_REQUEST` for a weak password or an invalid, used or
expired token (`"invalid or expired reset link"`), 429 past 10 a minute per
IP, 503 when storage or password hashing is unavailable.

## Game Endpoints

### Health Check
`GET /health`

Returns server and storage status, and the build the server runs.

**Response (200):**
```json
{
  "status": "healthy",
  "time": 1699123456,
  "storage": "ok",
  "mail": "disabled",
  "version": "v0.14.0-3-g1a2b3c4"
}
```

`mail` is `"ok"` when the server can email password-reset links (the web
client then offers **Forgot password?**), otherwise `"disabled"`.

`version` is `git describe` of the source the binary was built from (`make`
sets it), or `dev` for a plain `go build`. Compare it after a deployment: a
client newer than the server gets 404 `NOT_FOUND` for endpoints the server
predates.

Storage states:
- `"disabled"` - No database configured (`-dsn` or `CHESS_DSN`)
- `"ok"` - Database operational with auth enabled
- `"degraded"` - A persistence write failed or the write queue filled; live games continue in memory, and history, listings and resuming a stored game answer 503. The server tries the database every 5 seconds; when it answers, the games that changed are written whole and the state returns to `"ok"`

The top-level `status` is also `"degraded"` when storage is degraded.

### Create Game
`POST /games`

Creates new game with specified players. Optional authentication associates game with user.

**Headers (optional):**
```
Authorization: Bearer <token>
```

`side` (`w`, `b`, `white`, `black`; optional) is the human side the creator
takes: a claim when signed in, a guest's seat otherwise (see [Claims, seats
and control](#claims-seats-and-control)). Without it a signed-in creator
claims White if human, else Black, and a guest takes nothing. The response
then carries `side`, and for a guest `seat`, the secret of that seat, which
no later response repeats.

**Request:**
```json
{
  "white": {
    "type": 1,
    "level": 0,
    "searchTime": 0
  },
  "black": {
    "type": 2,
    "level": 15,
    "searchTime": 1000
  },
  "fen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
}
```

**Response (201):**
```json
{
  "gameId": "a1b2c3d4-e5f6-7890-1234-567890abcdef",
  "initialFen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
  "fen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
  "turn": "w",
  "state": "ongoing",
  "moves": [],
  "san": [],
  "players": {
    "white": {"id": "550e8400-...", "color": 1, "type": 1},
    "black": {"id": "ai-player-...", "color": 2, "type": 2, "level": 15, "searchTime": 1000}
  }
}
```

Note: When authenticated, human player IDs match the user's ID. Anonymous players receive unique UUIDs.
A claimed side also carries `claimedBy` and `name`, the claimant's username
when the claim was made; a side a guest holds carries `"seated": true`.

A custom `fen` must describe a position that can arise in a game: one king
per side, no pawn on the first or last rank, and the side not to move not in
check; otherwise the response is 400 `INVALID_FEN` with the reason. Castling
rights whose king or rook has left its original square are dropped.

### Get Game
`GET /games/{gameId}`

Returns current game state. An unfinished game that is stored but not in
memory, after a restart or a week idle, is loaded from its rows by the first
request that names it (this one, a move, undo, resign, draw, board or player
change) and plays on. What the rows do not hold starts fresh: a standing
draw offer and the one-offer-per-move allowance lapse, `lastMove` is empty,
and a game whose computer was thinking is `ongoing` with the computer to
move. Finished and imported games are not loaded: they answer 404 here and
stay available through the history. A miss while storage cannot be read
answers 503 `STORAGE_UNAVAILABLE`.

**Long-polling support:**
Add query parameters for real-time updates:
- `wait=true` - Enable long-polling (waits up to 30 seconds)
- `moveCount=N` - Last known move count

Returns immediately if game state changed, otherwise waits for updates:
```
GET /games/{gameId}?wait=true&moveCount=5
```

Response includes all game data. Compare `moves` array length to detect changes.
`moves` holds the UCI line and `san` the same moves in standard algebraic
notation, derived by the rules core from the position before each move (an
entry is empty only for a move it cannot notate, which the server logs).

**Timeout behavior:**
- Returns current state after 30 seconds even if no changes
- Game deletion notifies all waiting clients; server shutdown ends the wait
- A client that disconnects is not noticed: the server holds its wait until
  one of the above

### Get Durable Game History
`GET /games/{gameId}/history`

Returns the persisted replay line even after the live game has been unloaded
from memory or the server has restarted. Games without a registered player
are deleted 24 hours after their last activity; their history then returns
404. History is public to anyone who knows
the game ID, matching the existing public live-game read model. Persistent
storage must be enabled.

The response contains the initial FEN and an ordered FEN after every move, so a
client can replay the game without running a chess engine. Each move also
carries its SAN, derived on read from the stored position before it.

**Response (200):**
```json
{
  "gameId": "a1b2c3d4-e5f6-7890-1234-567890abcdef",
  "initialFen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
  "result": "white_wins",
  "pgnResult": "1-0",
  "termination": "checkmate",
  "startTimeUtc": "2026-09-07T12:00:00Z",
  "endTimeUtc": "2026-09-07T12:15:00Z",
  "origin": "played",
  "players": {
    "white": {"id": "user-id", "color": 1, "type": 1, "claimedBy": "user-id", "name": "alice"},
    "black": {"id": "player-id", "color": 2, "type": 1}
  },
  "moves": [
    {
      "moveNumber": 1,
      "moveUci": "e2e4",
      "san": "e4",
      "fenAfterMove": "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1",
      "playerColor": "w",
      "moveTimeUtc": "2026-09-07T12:00:05Z"
    }
  ]
}
```

`concession` (present only when there was one) is the game's first
resignation or agreed draw, in stored-result form (`"result": "black_wins"`);
see [Continuing after a resignation or agreed
draw](#continuing-after-a-resignation-or-agreed-draw).

`origin` is `played`, `imported` or `archive`; a game read from PGN also
carries the `event`, `site` and `round` it had. An archive game adds
`archive`: its `collection` with the `title`, `sourceUrl`, `licence` and
`attribution` it is published under, and the `whiteElo`, `blackElo` and `eco`
its PGN gave.
`result` is omitted while a game is ongoing. Persisted terminal values are
`white_wins`, `black_wins`, `draw`, and `stalemate`. `pgnResult` is the PGN
token for the same outcome (`1-0`, `0-1`, `1/2-1/2`, or `*` while ongoing),
and `termination` names how it was reached (see
[Game End](#game-end-draws-and-resignation); omitted while ongoing).
`moveNumber` counts plies from 1. `san` is omitted only for a stored move the
rules core cannot notate, which the server logs. A claimed player's `name` is
their username when the claim was recorded; it survives later renames and
`db user delete` (self-service deletion clears it), and is omitted for
anonymous and computer players.

Undo stays available after most results (a finished game can be rewound and
played on), so a stored game is never final. Responses carry a strong `ETag`
and `Cache-Control: private, no-cache`: send `If-None-Match` to get 304 while
nothing changed.

Returns 400 for a non-canonical game ID, 404 when the game has no durable
record, and 503 when persistence is disabled or degraded.

### Export PGN
`GET /games/{gameId}/pgn?ply=N`

Returns the stored game in PGN export format as
`application/x-chess-pgn; charset=utf-8` with
`Content-Disposition: attachment; filename="chess-<yyyymmdd>-<id8>.pgn"`.
Same visibility, `ETag`, and error responses as the history. `ply` (optional,
0 up to the number of stored plies) exports only the first N plies; the
result is then `*`, the `Termination` tag is left out, and the filename ends
`-ply<N>.pgn`. A `fetch` of the URL returns the text, so clients can copy it
as well as download it. `Site` is the host of the server's `-public-url` (`?`
without one); a game read from PGN keeps its own. An archive game adds, where
known, `WhiteElo`, `BlackElo` and `ECO`, and its collection's `Source`,
`Licence` and `Attribution`.

```
[Event "Casual game"]
[Site "example.com"]
[Date "2026.09.27"]
[Round "-"]
[White "alice"]
[Black "Stockfish level 3"]
[Result "0-1"]
[GameId "68007fd1-8970-4d84-950d-2f6ec6375c0b"]
[UTCDate "2026.09.27"]
[UTCTime "08:07:00"]
[WhiteType "human"]
[BlackType "program"]
[PlyCount "4"]
[Termination "normal"]

1. f3 e5 2. g4 Qh4# 0-1
```

The movetext of a finished game ends with a comment naming the outcome
before the result, e.g. `{ Black wins by resignation. } 0-1` or
`{ Draw by threefold repetition. } 1/2-1/2`.

Players are the name snapshot, `Anonymous`, or `Stockfish level N`. A game
from a custom position adds `SetUp "1"` and `FEN` tags, and its movetext
starts at that position's move number (`55. g8=Q` or `40... Kd8`).
Movetext wraps at 80 columns. `Termination` is `normal` for a finished game
and `unterminated` otherwise.

### List My Stored Games
`GET /users/me/games?limit=50&cursor=<nextCursor>&status=finished&color=white`

Returns games associated with the authenticated user at creation time, by a
later first-move slot claim, or by import, newest first. Requires
`Authorization: Bearer <token>` and persistent storage.

- `limit`: 1-100; defaults to 50
- `cursor`: the previous page's `nextCursor`; continues after that game, so
  games created meanwhile do not shift the page. Opaque; do not construct it.
- `status`: `ongoing` or `finished`
- `color`: `white` or `black`, the side the user claimed
- `offset`: 0-1,000,000; position-based paging for older clients. It cannot
  be combined with `cursor`, and pages shift when games are added.

Each item contains game ID, initial FEN, final FEN (after the last stored move,
or the initial FEN when there is none), result, its `pgnResult` token and
`termination` (as in the history; both omitted while ongoing), timestamps,
players, move count, `origin` (`played` or `imported`) and, for an imported
game, its `event`. `nextCursor` is present only when another
page exists; `nextOffset` also, on requests without a cursor. A game belongs
to a user who created it while authenticated, claimed a slot with a first
move, or imported it; `color` matches claims only. The association survives
later player reconfiguration.

Returns 400 `INVALID_REQUEST` for a malformed cursor or filter.

**Response (200):**
```json
{
  "games": [
    {
      "gameId": "a1b2c3d4-e5f6-7890-1234-567890abcdef",
      "initialFen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
      "finalFen": "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4",
      "result": "white_wins",
      "pgnResult": "1-0",
      "termination": "checkmate",
      "startTimeUtc": "2026-09-07T12:00:00Z",
      "endTimeUtc": "2026-09-07T12:15:00Z",
      "moveCount": 7,
      "origin": "played",
      "players": {
        "white": {"id": "user-id", "color": 1, "type": 1, "claimedBy": "user-id", "name": "alice"},
        "black": {"id": "player-id", "color": 2, "type": 1}
      }
    }
  ],
  "limit": 50,
  "offset": 0,
  "nextCursor": "MTc5MDQ5NjQyMDU4MTg2NS42ODAwN2ZkMS04OTcwLTRkODQtOTUwZC0yZjZlYzYzNzVjMGI"
}
```

### Browse the Archive
`GET /archive?collection=<name>&eco=<prefix>&player=<prefix>&cursor=<nextCursor>&limit=20`

The curated archive: games read from PGN that belong to a collection, not an
account. Lists the games of published collections, newest first, to anyone;
no token. Needs persistent storage.

- `limit`: 1-100; defaults to 20
- `cursor`: the previous page's `nextCursor`, as in the list of your games
- `collection`: a collection's name
- `eco`: the start of an opening code, in any case: `B`, `B9`, `B90`
- `player`: the start of either player's name, in any case, at least two
  characters; names read as the PGN wrote them (`Fischer, Robert`)
- `featured=true`: the featured games in their order, at most `limit`, with
  no cursor

Each item is a game summary as in [List My Stored Games](#list-my-stored-games)
with `"origin": "archive"` and an `archive` object: `collection`, and
`whiteElo`, `blackElo` and `eco` where the PGN gave them.

**Response (200):**
```json
{
  "games": [
    {
      "gameId": "a1b2c3d4-e5f6-7890-1234-567890abcdef",
      "initialFen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
      "finalFen": "1n1Rkb1r/p4ppp/4q3/4p1B1/4P3/8/PPP2PPP/2K5 b k - 1 17",
      "result": "white_wins",
      "pgnResult": "1-0",
      "termination": "checkmate",
      "startTimeUtc": "1858-01-01T00:00:00Z",
      "endTimeUtc": "1858-01-01T00:00:00Z",
      "moveCount": 33,
      "origin": "archive",
      "event": "Paris Opera",
      "archive": {"collection": "classics", "eco": "C41"},
      "players": {
        "white": {"id": "player-id", "color": 1, "type": 1, "name": "Morphy, Paul"},
        "black": {"id": "player-id", "color": 2, "type": 1, "name": "Duke Karl / Count Isouard"}
      }
    }
  ],
  "limit": 20,
  "nextCursor": "LTM1MzQyNzIwMDAwMDAwMDAwMC5hMWIyYzNkNC1lNWY2LTc4OTAtMTIzNC01Njc4OTBhYmNkZWY"
}
```

An archive game replays through its [history](#get-durable-game-history) and
[PGN](#export-pgn) like any stored game, and is never live:
`GET /games/{gameId}` answers 404. A game of an unpublished collection is
absent from this list only; its history and PGN answer to its ID.

Responses carry `Cache-Control: public, max-age=300`: the list changes only
with an import or a publication, which a cache shows up to five minutes late.
Returns 400 `INVALID_REQUEST` for a malformed cursor or filter, 503 without
storage.

### List Archive Collections
`GET /archive/collections`

The published collections, featured ones first, then by title; no token, and
cached like the list.

**Response (200):**
```json
{
  "collections": [
    {
      "name": "classics",
      "title": "Classic games",
      "sourceUrl": "https://example.org/classics",
      "licence": "Public domain",
      "attribution": "Compiled by the example project",
      "games": 3
    }
  ]
}
```

### Import a Game from PGN
`POST /games/import`

Stores one PGN game under the signed-in account, for replay and export.
Requires `Authorization: Bearer <token>` and persistent storage; 5 requests a
minute per IP. The PGN is read strictly: well-formed tag pairs, printable
text, balanced comments and variations (which are dropped, with annotations
and numeric glyphs), one game, at most 64 KiB and 1000 plies. Every move is
replayed by the rules core; an illegal move is refused with its ply. The
result comes from the PGN's result token and must agree with the final
position (a `1-0` after Black mates is refused); the stored termination is
checkmate or stalemate when the position says so, a draw rule when one
applies, otherwise resignation or agreement. `White`, `Black`, `Event`,
`Site`, `Round` and `Date`/`UTCDate`/`UTCTime` tags are kept; `SetUp`/`FEN`
set the starting position.

**Request:**
```json
{"pgn": "[White \"Spassky, Boris\"]\n[Black \"Fischer, Robert\"]\n[Result \"1-0\"]\n\n1. d4 Nf6 2. c4 e6 3. Nf3 d5 1-0\n"}
```

**Response (201):**
```json
{"gameId": "a1b2c3d4-e5f6-7890-1234-567890abcdef", "moveCount": 6, "result": "white_wins", "pgnResult": "1-0"}
```

Errors: 400 `INVALID_REQUEST` with `details` saying what is wrong with the
PGN, 401 without a token, 429 past the limit, 503 `STORAGE_UNAVAILABLE`.

An imported game has no claimed side: it is listed for its importer with
`"origin": "imported"` and its `event`, is excluded from the `color` filter,
and is never purged as anonymous. Its history and PGN carry the tags.

### Analyse a Position
`POST /analysis`

The engine's view of one position: its best variations, best first. Requires
`Authorization: Bearer <token>`; 30 requests a minute per IP. Stockfish
searches at full strength within 500,000 nodes, about a second, on an engine
of its own that no computer move waits for. One position is searched at a
time and four requests may wait; an answer is kept in memory by position, so
asking again costs nothing.

**Request:** `lines` is 1 to 3 (default 1), and at most the legal moves.
```json
{"fen": "rnbqkbnr/pppp1ppp/8/4p3/6P1/5P2/PPPPP2P/RNBQKBNR b KQkq - 0 2", "lines": 2}
```

**Response (200):**
```json
{
  "fen": "rnbqkbnr/pppp1ppp/8/4p3/6P1/5P2/PPPPP2P/RNBQKBNR b KQkq - 0 2",
  "depth": 19,
  "lines": [
    {"score": 0, "mate": -1, "moves": ["d8h4"], "san": ["Qh4#"]},
    {"score": -260, "moves": ["h7h5", "g4g5", "d8g5"], "san": ["h5", "g5", "Qxg5"]}
  ]
}
```

- `score` is White's, in centipawns. `mate`, when present, replaces it: the
  moves to mate, negative when Black mates.
- `moves` holds at most 12 plies in UCI (the second line above is cut short),
  `san` the same moves in notation.
- `fen` is the position as the rules core writes it.
- Where the game is over (mate, stalemate, dead material, the fifty-move
  rule) the rules answer without the engine: `lines` is empty, and `state`
  and `termination` say how, as in a game response.

Errors: 400 `INVALID_FEN` with `details`, 400 `INVALID_REQUEST`, 401 without
a token, 429 past the limit, 503 `RESOURCE_LIMIT` when too many are waiting.

### Remove a Stored Game
`DELETE /users/me/games/{gameId}`

Takes a stored game off the signed-in user's list. A game the user alone
holds (the only claimant, or its importer) is deleted with its moves, and
unloaded if it was still in memory; a game another user also claims is kept
for them, and only the caller's claim and name are cleared from it.

**Response (200):**
```json
{"removed": "game"}
```

`removed` is `game` or `claim`. Errors: 400 for a malformed ID, 404
`GAME_NOT_FOUND` when the user holds nothing in that game, 503 when storage
is unavailable.

### Make Move
`POST /games/{gameId}/moves`

Submits human move or triggers computer move.

**Human move:**
```json
{"move": "e2e4"}
```

Moves are UCI: castling is the king's two-square move (`e1g1`), and a
promotion appends the piece (`e7e8q`, `r`, `b`, or `n`). A pawn move to the
last rank without the piece returns 400 `INVALID_MOVE` with
`promotion piece required`.

**Computer move trigger:**
```json
{"move": "cccc"}
```

When the engine already has two moves a worker waiting, the trigger returns
503 `RESOURCE_LIMIT` and the game stays `ongoing`; repeat it shortly.

A move that ends the game (mate, stalemate, or an automatic draw) returns the
terminal state and `termination` in the same response.

### Game End: Draws and Resignation

A live game (`GET /games/{gameId}` and every game response) carries:

| Field | Meaning |
|---|---|
| `state` | `ongoing`, `pending` (computer thinking), `stuck` (engine failed), `white wins`, `black wins`, `stalemate`, or `draw` |
| `termination` | How a finished game ended: `checkmate`, `resignation`, `stalemate`, `insufficient_material`, `threefold_repetition`, `fifty_move_rule`, or `agreement`; omitted while unfinished |
| `drawOffer` | `w` or `b`: that side's draw offer awaits an answer; omitted when none |
| `drawOutcome` | Only on responses to `POST .../draw`: `offered`, `accepted`, or `declined` |
| `concession` | The game's first resignation or agreed draw: `{"result": "black wins", "termination": "resignation", "ply": 24}`, `ply` being the moves played when it was made; kept when an undo continues play |

**Automatic draws.** After every move the game is drawn, without a claim, as
on most online servers, when:

- neither side can mate: bare kings, one minor piece, or only bishops all on
  squares of one color (two knights do not count: mate is possible);
- the position occurs for the third time with the same side to move,
  castling rights, and en-passant possibility (threefold repetition);
- 50 moves by each side pass without a capture or pawn move (the halfmove
  clock reaches 100).

Mate and stalemate take precedence. A custom starting position that is
already dead, or whose halfmove clock is at 100, is created drawn.

#### Resign
`POST /games/{gameId}/resign`

```json
{"color": "w"}
```

Ends the game in the other side's favor, with `termination: "resignation"`.
`color` (`w`, `b`, `white`, `black`) may be omitted when the server can infer
the side: the one side the caller holds, else the only human side. Allowed while the game is unfinished, including while the computer is
thinking (its move is discarded) or the engine is stuck.

#### Offer, accept, or decline a draw
`POST /games/{gameId}/draw`

```json
{"action": "offer", "color": "b"}
```

`action` is `offer`, `accept`, or `decline`; `color` works as for resign.
Allowed only while `state` is `ongoing`.

- **To a human:** the offer stands (`drawOffer`) until the opponent accepts,
  declines, or makes a move instead, which declines it; the offerer's own
  move keeps it. Offering while the opponent's offer stands accepts it.
  Repeating a standing offer is a no-op. Waiting long-poll clients are woken
  on offers and answers.
- **To the computer:** answered at once in `drawOutcome`. The computer
  declines before both sides have made ten moves; after that it accepts when
  a short full-strength search rates its own side level or worse.
- **One offer per move:** after an offer is declined, the same side must make
  a move before offering again (409 `GAME_CONFLICT`).

Resign and draw share the move endpoint's authorization: a held side acts
only for its holder (403 `UNAUTHORIZED`), and an authenticated resignation
or acceptance claims an open side as a first move would.
Other errors are 400: `GAME_OVER` for a finished game, `INVALID_REQUEST` for
an ambiguous or computer side, a missing offer, or a game that is not
ongoing. Both return the game.

#### Continuing after a resignation or agreed draw

A resignation or agreed draw is a *concession*: a result the players chose.

- **Both sides human:** it is final. `undo` returns 400 `GAME_OVER`, for a
  hot-seat game too.
- **Against the computer:** the claimant may `undo` it and play on. The live
  `state` and `termination` (and the stored `result` and `termination`)
  follow the game; `concession` keeps the first one, in memory and in the
  database (`concession_*` columns), through later undos, moves, and results.
  History reports it, and the full PGN notes it in the final comment, such as
  `{ White resigned at ply 24; play continued. }`.

### Claims, seats and control

A human side is open, or held: by an account's **claim**, or by a guest's
**seat**.

- A claim is made by creating the game while signed in, by joining, or by a
  first authenticated move, resignation or draw acceptance on an open side.
  Requests act for it with the bearer token.
- A seat is taken by a guest creating the game with `side`, or joining. The
  answer carries its secret once, in `seat`; requests act for it with
  `X-Chess-Seat: <seat>`. The server keeps only the secret's SHA-256.
- An open side takes a move from anyone holding the game ID, and a guest's
  move leaves it open.

Moves, resignation, and draws act for one side and need to be its holder (or
the side open). The requests below affect both sides, so:

- undo, configure players: allowed when nobody else holds a side (the caller
  holds every held side, or none is held)
- delete (unload): allowed when the caller holds a side, or none is held

Otherwise they return 403 `UNAUTHORIZED`. Two players who each hold a side
therefore cannot take back each other's moves or swap the opponent for the
computer: takebacks between players are not supported.

A seat does not make a game a registered player's: a game no account claims
is purged 24 hours after its last activity, seats or not.

### Join a Game
`POST /games/{gameId}/join`

```json
{"color": "b"}
```

Takes an open human side without a move: `color`, or the only open one (400
when both are open and none is named). A signed-in caller claims it; a guest
gets a seat. Returns the game with `side`, and for a guest `seat`. A caller
who already holds a side gets it back unchanged. Waiting long-polls wake.

Errors: 409 `GAME_CONFLICT` when the side is taken, 400 `GAME_OVER` for a
finished game, 400 `INVALID_REQUEST` for a computer's side, 404 for an
unknown game.

### Undo Moves
`POST /games/{gameId}/undo`

```json
{"count": 2}
```

Reverts `count` plies (default 1). Allowed after a result: it rewinds the
result and clears draw offers. Refused while the computer is thinking, after
a resignation or agreed draw between two humans (400 `GAME_OVER`), and when
another player holds a side (403, see [Claims, seats and
control](#claims-seats-and-control)).

### Configure Players
`PUT /games/{gameId}/players`

Changes player configuration mid-game; subject to [Claims, seats and
control](#claims-seats-and-control). A claim or a seat stays with its side.

### Get Board
`GET /games/{gameId}/board`

Returns ASCII board visualization.

### Delete Game
`DELETE /games/{gameId}`

Unloads the live game from memory. Its persisted game and move history remain
available through the history endpoint, and an unfinished one resumes on its
next request. Returns 204 on success, 404 when it is not in memory, 403 to a
caller holding none of the game's held sides.

## Error Format
```json
{
  "error": "Description",
  "code": "ERROR_CODE",
  "details": "Additional context"
}
```

Error codes:
- `GAME_NOT_FOUND` - No live game with that ID, and no unfinished stored one to resume
- `NOT_FOUND` - No such endpoint (for example a server older than the client)
- `UNAUTHORIZED` - A side or the game is held by another player
- `INVALID_MOVE` - Illegal chess move
- `NOT_HUMAN_TURN` - Wrong player type for turn
- `GAME_OVER` - Game already ended
- `GAME_CONFLICT` - Game changed while a move was being validated (refresh and retry), or the side to join is taken
- `STORAGE_UNAVAILABLE` - Durable history/list storage is disabled or degraded, or a stored game could not be read to resume it
- `RATE_LIMIT_EXCEEDED` - Request limit exceeded
- `INVALID_REQUEST` - Malformed request
- `INVALID_CONTENT_TYPE` - Missing/wrong Content-Type header
- `INVALID_FEN` - Invalid FEN format
- `INTERNAL_ERROR` - Server error

## Rate Limiting

- Standard: 10 requests/second/IP (general endpoints)
- Development (`-dev`): 20 requests/second/IP
- Registration: 5 requests/minute/IP
- Login: 10 requests/minute/IP
- Account deletion: 5 requests/minute/IP
- Password recovery: 3 requests/minute/IP; reset: 10 requests/minute/IP
- PGN import: 5 requests/minute/IP
- Analysis: 30 requests/minute/IP

Exceeding limit returns 429 status. Behind a reverse proxy the client IP is the
proxy's `X-Real-IP` header, trusted only from addresses listed in
`-trusted-proxies`.

Password hashing is bounded to four concurrent operations. When all are busy
for five seconds, registration, login and account deletion return 503 with
`RESOURCE_LIMIT`.

## JWT Token Format

Tokens are HS256-signed JWTs valid for 7 days. Include in Authorization header:
```
Authorization: Bearer <token>
```

The scheme is case-insensitive and the token must use RFC 6750 syntax; a
malformed header returns 401 even on endpoints where authentication is
optional. Claims are `iss` (`chess-server`), `aud` (`chess-api`), `sub` (user
ID), `iat`, `nbf`, `exp`, and `extra.session_id`. Tokens carry no profile data;
use `/auth/me`. Authentication requires the session to exist, be unexpired, and
belong to the JWT subject. Logging in again replaces the previous session.

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
  "version": "v0.14.0-3-g1a2b3c4"
}
```

`version` is `git describe` of the source the binary was built from (`make`
sets it), or `dev` for a plain `go build`. Compare it after a deployment: a
client newer than the server gets 404 `NOT_FOUND` for endpoints the server
predates.

Storage states:
- `"disabled"` - No database configured (`-dsn` or `CHESS_DSN`)
- `"ok"` - Database operational with auth enabled
- `"degraded"` - A persistence write failed or the write queue filled; live games continue in memory, but durable history is no longer complete

The top-level `status` is also `"degraded"` when storage is degraded.

### Create Game
`POST /games`

Creates new game with specified players. Optional authentication associates game with user.

**Headers (optional):**
```
Authorization: Bearer <token>
```

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
  "fen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
  "turn": "w",
  "state": "ongoing",
  "moves": [],
  "players": {
    "white": {"id": "550e8400-...", "color": 1, "type": 1},
    "black": {"id": "ai-player-...", "color": 2, "type": 2, "level": 15, "searchTime": 1000}
  }
}
```

Note: When authenticated, human player IDs match the user's ID. Anonymous players receive unique UUIDs.

A custom `fen` must describe a position that can arise in a game: one king
per side, no pawn on the first or last rank, and the side not to move not in
check; otherwise the response is 400 `INVALID_FEN` with the reason. Castling
rights whose king or rook has left its original square are dropped.

### Get Game
`GET /games/{gameId}`

Returns current game state.

**Long-polling support:**
Add query parameters for real-time updates:
- `wait=true` - Enable long-polling (waits up to 30 seconds)
- `moveCount=N` - Last known move count

Returns immediately if game state changed, otherwise waits for updates:
```
GET /games/{gameId}?wait=true&moveCount=5
```

Response includes all game data. Compare `moves` array length to detect changes.

**Timeout behavior:**
- Returns current state after 30 seconds even if no changes
- Client disconnection cancels wait immediately
- Game deletion notifies all waiting clients

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

`result` is omitted while a game is ongoing. Persisted terminal values are
`white_wins`, `black_wins`, `draw`, and `stalemate`. `pgnResult` is the PGN
token for the same outcome (`1-0`, `0-1`, `1/2-1/2`, or `*` while ongoing),
and `termination` names how it was reached (see
[Game End](#game-end-draws-and-resignation); omitted while ongoing).
`moveNumber` counts plies from 1. `san` is omitted only for a stored move the
rules core cannot notate, which the server logs. A claimed player's `name` is
their username when the claim was recorded; it survives later renames and
account deletion, and is omitted for anonymous and computer players.

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
as well as download it.

```
[Event "Casual game"]
[Site "?"]
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

Returns games associated with the authenticated user at creation time or by a
later first-move slot claim, newest first. Requires
`Authorization: Bearer <token>` and persistent storage.

- `limit`: 1-100; defaults to 50
- `cursor`: the previous page's `nextCursor`; continues after that game, so
  games created meanwhile do not shift the page. Opaque; do not construct it.
- `status`: `ongoing` or `finished`
- `color`: `white` or `black`, the side the user claimed
- `offset`: 0-1,000,000; position-based paging for older clients. It cannot
  be combined with `cursor`, and pages shift when games are added.

Each item contains game ID, initial FEN, final FEN (after the last stored move,
or the initial FEN when there is none), result and its `pgnResult` token,
timestamps, players, and move count. `nextCursor` is present only when another
page exists; `nextOffset` also, on requests without a cursor. A game belongs
to a user who created it while authenticated or claimed a slot with a first
move; the association survives later player reconfiguration.

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
      "startTimeUtc": "2026-09-07T12:00:00Z",
      "endTimeUtc": "2026-09-07T12:15:00Z",
      "moveCount": 7,
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
the side: the one side the authenticated caller claimed, else the only human
side. Allowed while the game is unfinished, including while the computer is
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

Resign and draw share the move endpoint's authorization: a side claimed by a
user acts only for that user (403 `UNAUTHORIZED`), and an authenticated
resignation or acceptance claims an unclaimed side as a first move would.
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

### Claims and control

Anyone holding the game ID may move for, undo, reconfigure, or unload an
unclaimed side; a claim is what protects a player. Moves, resignation, and
draws act for one side and need that side's claim (or none). The requests
below affect both sides, so:

| Request | Allowed when |
|---|---|
| undo, configure players | no side is claimed by another user: the caller holds every claim, or nothing is claimed |
| delete (unload) | the caller holds a claim, or nothing is claimed |

Otherwise they return 403 `UNAUTHORIZED`. Two players who each claimed a
side therefore cannot take back each other's moves or swap the opponent for
the computer: takebacks between players are not supported. Send the bearer
token (optional authentication) with these requests.

### Undo Moves
`POST /games/{gameId}/undo`

```json
{"count": 2}
```

Reverts `count` plies (default 1). Allowed after a result: it rewinds the
result and clears draw offers. Refused while the computer is thinking, after
a resignation or agreed draw between two humans (400 `GAME_OVER`), and by
another user's claim (403, see [Claims and control](#claims-and-control)).

### Configure Players
`PUT /games/{gameId}/players`

Changes player configuration mid-game; subject to [Claims and
control](#claims-and-control).

### Get Board
`GET /games/{gameId}/board`

Returns ASCII board visualization.

### Delete Game
`DELETE /games/{gameId}`

Unloads the live game from memory. Its persisted game and move history remain
available through the history endpoint. Returns 204 on success, 403 to a
caller holding none of the game's claims.

## Error Format
```json
{
  "error": "Description",
  "code": "ERROR_CODE",
  "details": "Additional context"
}
```

Error codes:
- `GAME_NOT_FOUND` - No live game with that ID
- `NOT_FOUND` - No such endpoint (for example a server older than the client)
- `UNAUTHORIZED` - A side or the game is claimed by another user
- `INVALID_MOVE` - Illegal chess move
- `NOT_HUMAN_TURN` - Wrong player type for turn
- `GAME_OVER` - Game already ended
- `GAME_CONFLICT` - Game changed while a move was being validated; refresh and retry
- `STORAGE_UNAVAILABLE` - Durable history/list storage is disabled or degraded
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

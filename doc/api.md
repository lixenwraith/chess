# API Reference

Base URL: `http://localhost:8080/api/v1`

Content-Type: `application/json` (required for POST/PUT)

## Endpoints

### Create Game
`POST /games`

Creates new game with specified players.

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

- `type` (integer, required): 1=human, 2=computer
- `level` (integer, 0-20): AI skill level for computer players
- `searchTime` (integer, 100-10000ms): AI thinking time for computer players
- `fen` (string): Starting position in FEN notation (default: standard position)

**Response (201):**
```json
{
  "gameId": "a1b2c3d4-e5f6-7890-1234-567890abcdef",
  "fen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
  "turn": "w",
  "state": "ongoing",
  "moves": [],
  "players": {
    "white": {"id": "...", "color": 1, "type": 1},
    "black": {"id": "...", "color": 2, "type": 2, "level": 15, "searchTime": 1000}
  }
}
```

### Get Game
`GET /games/{gameId}`

Returns current game state.

**Response (200):**
```json
{
  "gameId": "...",
  "fen": "...",
  "turn": "w",
  "state": "ongoing",
  "moves": ["e2e4", "e7e5"],
  "players": {...},
  "lastMove": {
    "move": "e7e5",
    "playerColor": "b",
    "score": 25,
    "depth": 12
  }
}
```

States: `ongoing`, `pending` (computer thinking), `white wins`, `black wins`, `draw`, `stalemate`

### Make Move
`POST /games/{gameId}/moves`

Submits human move or triggers computer move.

**Human move:**
```json
{"move": "e2e4"}
```

**Computer move trigger:**
```json
{"move": "cccc"}
```

Returns updated game state (200) or error (400).

### Undo Moves
`POST /games/{gameId}/undo`

Reverts moves from history.

**Request:**
```json
{"count": 2}
```

- `count` (integer, 1-300): Number of moves to undo (default: 1)

### Configure Players
`PUT /games/{gameId}/players`

Changes player configuration mid-game.

**Request:**
```json
{
  "white": {"type": 2, "level": 5, "searchTime": 100},
  "black": {"type": 1}
}
```

### Get Board
`GET /games/{gameId}/board`

Returns ASCII board visualization.

**Response (200):**
```json
{
  "fen": "...",
  "board": "  a b c d e f g h\n8 r n b q k b n r 8\n..."
}
```

### Delete Game
`DELETE /games/{gameId}`

Removes game from memory. Returns 204 on success.

## Error Format

```json
{
  "error": "Description",
  "code": "ERROR_CODE",
  "details": "Additional context"
}
```

Error codes:
- `GAME_NOT_FOUND` - Invalid game ID
- `INVALID_MOVE` - Illegal chess move
- `NOT_HUMAN_TURN` - Wrong player type for turn
- `GAME_OVER` - Game already ended
- `RATE_LIMIT_EXCEEDED` - Request limit exceeded
- `INVALID_REQUEST` - Malformed request
- `INVALID_CONTENT_TYPE` - Missing/wrong Content-Type header

## Rate Limiting

- Standard: 10 request/second/IP
- Development (`-dev`): 20 requests/second/IP

Exceeding limit returns 429 status.
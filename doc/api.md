# API Reference

Base URL: `http://localhost:8080/api/v1`

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

## Game Endpoints

### Health Check
`GET /health`

Returns server and storage status.

**Response (200):**
```json
{
  "status": "healthy",
  "time": 1699123456,
  "storage": "ok"
}
```

Storage states:
- `"disabled"` - No storage path configured
- `"ok"` - Database operational with auth enabled
- `"degraded"` - Write failures detected, operating memory-only

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

### Get Game
`GET /games/{gameId}`

Returns current game state.

**Long-polling support:**
Add query parameters for real-time updates:
- `wait=true` - Enable long-polling (waits up to 25 seconds)
- `moveCount=N` - Last known move count

Returns immediately if game state changed, otherwise waits for updates:
```
GET /games/{gameId}?wait=true&moveCount=5
```

Response includes all game data. Compare `moves` array length to detect changes.

**Timeout behavior:**
- Returns current state after 25 seconds even if no changes
- Client disconnection cancels wait immediately
- Game deletion notifies all waiting clients

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

### Undo Moves
`POST /games/{gameId}/undo`

Reverts moves from history.

### Configure Players
`PUT /games/{gameId}/players`

Changes player configuration mid-game.

### Get Board
`GET /games/{gameId}/board`

Returns ASCII board visualization.

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
- `INVALID_FEN` - Invalid FEN format
- `INTERNAL_ERROR` - Server error

## Rate Limiting

- Standard: 10 requests/second/IP (general endpoints)
- Development (`-dev`): 20 requests/second/IP
- Registration: 5 requests/minute/IP
- Login: 10 requests/minute/IP

Exceeding limit returns 429 status.

## JWT Token Format

Tokens are HS256-signed JWTs valid for 7 days. Include in Authorization header:
```
Authorization: Bearer <token>
```

Token claims include `sub` (user ID), `username`, `email`, and `exp` (expiration).
# Architecture

## Components

### Transport Layer (`internal/server/http`)
Fiber web server handling HTTP requests/responses. Implements routing, rate limiting, content-type validation, JWT authentication middleware, request parsing. Translates HTTP to internal Command objects.

### Processing Layer (`internal/server/processor`)
Central command handler containing business logic. Single `Execute(Command)` entry point decouples transport from logic. Uses synchronous UCI engine for validation, asynchronous EngineQueue for computer moves. Commands include optional user context for authenticated operations.

### Service Layer (`internal/server/service`)
In-memory live-game storage with authentication support. A mutex protects each
game transition and callers receive immutable game views rather than mutable
game pointers. The service manages game lifecycle, snapshots, player
configuration, user accounts, JWT tokens, persistence, and cleanup. Finished
games leave memory after an hour while their durable rows remain; games with
no registered player leave memory and the database 24 hours after their last
activity (see Retention below).

#### Long-Polling Registry (`internal/service/waiter.go`)
Manages clients waiting for game state changes via HTTP long-polling. Tracks move counts per client, sends notifications on state changes, enforces 30-second timeout. Non-blocking notification pattern handles slow clients gracefully. Coordinates with service layer for game updates and deletion events.

#### Authentication Module (`internal/service/user.go`, `internal/http/auth.go`)
- **Password Hashing**: Argon2id (`lixenwraith/auth`), at most four concurrent
  derivations; unknown accounts verify against a dummy hash for equal timing
- **JWT Management**: one scoped `auth.JWT` manager (HS256, issuer
  `chess-server`, audience `chess-api`, 7-day lifetime, zero leeway); the key
  comes from `-jwt-secret-file`, a fixed dev key, or a per-process random key
- **Bearer Parsing**: `auth.ParseBearerToken` (RFC 6750 syntax)
- **User Operations**: Registration, login, profile management
- **Session Tracking**: One persisted session per user, with JWT subject/session
  binding and last-login timestamps

### Storage Layer (`internal/server/storage`)
PostgreSQL 18 persistence through pgx v5 (`database/sql` via `pgx/stdlib`),
with ordered asynchronous writes for gameplay and synchronous writes for
accounts. A bounded channel (1,000 operations) feeds one transactional writer,
which preserves per-game ordering (a move never overtakes its game's insert or
a rewind). Move persistence groups the move, first-move slot claim, and
terminal result in one transaction. Replay reads insert a barrier behind
accepted writes and then read the game and moves in one REPEATABLE READ
snapshot.

The writer retries a transaction that failed before COMMIT with a transient
error (lost connection, administrator shutdown, serialization failure,
deadlock) with doubling backoff for about three seconds, so a PostgreSQL
restart does not degrade storage. A COMMIT error (unknown outcome), a permanent
error, or a full queue marks storage degraded; live play remains available in
memory and `/health` reports that durable history may be incomplete.

Synchronous operations carry a five-second deadline. The pool holds at most ten
connections (five idle). The schema is resolved through `search_path`, so the
role default (`chess`) or a DSN parameter selects it. `InitDB` applies ordered,
transactional migrations under an advisory lock and issues no DDL when the
schema is current, which lets a DML-only runtime role start the server.

### Supporting Modules
- **Engine** (`internal/engine`): UCI protocol wrapper for Stockfish process communication
- **Game** (`internal/game`): Game state with snapshot history and player associations
- **Board** (`internal/board`): FEN parsing and ASCII generation
- **Rules core** (`internal/server/chess`): dependency-free position model,
  legal move generation, SAN, PGN, and draw-rule detection. It validates
  custom starting FENs, notates stored games, and shadows the engine on every
  live move; Stockfish stays the authority for live play
- **Core** (`internal/core`): Shared types, API models, error constants
- **CLI** (`cmd/chess-server/cli`): Database and user management commands
- **Client** (`cmd/chess-client-cli`, `internal/client`): Interactive debugging client with command registry, session management, and colored terminal output

## Request Flow

### User Registration
1. HTTP handler receives `POST /auth/register` with credentials
2. Validates username format and password strength
3. Service layer hashes password with Argon2id (bounded concurrency)
4. Creates the user and its initial session in one transaction (random UUIDv4 ID)
5. Generates a scoped JWT bound to that session
6. Returns token and user information

### Authenticated Game Creation
1. HTTP handler receives `POST /games` with optional Bearer token
2. Middleware validates JWT if present
3. Creates CreateGameCommand with user ID context
4. Processor creates game with user ID for human players
5. Service associates game with authenticated user
6. Returns game with player IDs matching user

### Human Move (Authenticated)
1. HTTP handler receives `POST /games/{id}/moves` with move
2. Optional JWT validation for user verification
3. Creates MakeMoveCommand, calls `processor.Execute()`
4. The rules core checks the move first only to name a missing promotion
   piece; the processor then validates it via the locked validation engine
5. If legal, gets new FEN from engine, and logs a warning if the rules core
   disagrees on legality or the resulting position
6. Calls `service.ApplyMoveWithState()` with the FEN, turn, and state that were validated
7. Service rejects a stale concurrent commit or atomically updates the move, optional slot claim, and terminal result
8. The same logical mutation is queued as one database transaction
9. Returns GameResponse

### Computer Move
1. HTTP handler receives `POST /games/{id}/moves` with `{"move": "cccc"}`
2. Processor sets game state to `pending`
3. Submits task to EngineQueue, returns immediately
4. Worker goroutine calculates move with dedicated Stockfish instance
5. Callback updates game state via service
6. Client polls for completion
7. Returns GameResponse

### Long-Polling Flow
1. Client sends `GET /games/{id}?wait=true&moveCount=N`
2. Handler creates context from HTTP connection
3. Registers wait with WaitRegistry using game ID and move count
4. If game state unchanged, blocks up to 30 seconds
5. On any game update, NotifyGame sends to all waiters
6. Returns immediately with current state
7. Client disconnection cancels wait via context
8. Game deletion notifies and removes all waiters

### Durable Replay Read
1. Client requests `GET /api/games/{id}/history`
2. Storage queues a barrier after all previously accepted gameplay writes
3. The writer reaches the barrier only after those transactions finish
4. Storage reads the game row and ordered moves in one REPEATABLE READ transaction
5. The service derives SAN for each ply from the stored FEN before it
6. The API returns the initial FEN plus every UCI move, its SAN, and the
   resulting FEN, with a strong ETag; `/pgn` renders the same read as PGN
7. This path works after terminal-memory eviction or a server restart

## Persistence Flow

### User Write Operations (Synchronous)
1. The password is hashed outside any lock, within the KDF concurrency bound
2. Storage takes a transaction-scoped advisory lock, so account creation is
   serialized across the server and CLI processes
3. Uniqueness is checked first, so a duplicate is reported even when registration is full
4. Public registration is refused once `-max-users` accounts exist; nothing is evicted
5. The new user and initial session commit together; any failure rolls back the entire operation
6. Login replaces the user's single session and records `last_login_at` in one statement
7. Other account mutations commit before success is returned; unique violations map to "already exists"
8. Registered and CLI-created accounts are identical and never expire

### Game Write Operations (Asynchronous)
1. Service layer calls a storage method (`RecordNewGame`, `RecordMove`, `RecordPlayers`, or `RewindGame`)
2. Operation queued to buffered channel (non-blocking)
3. Writer goroutine processes queue sequentially
4. Each logical mutation commits in one transaction
5. A full queue or write failure is logged and triggers degraded memory-only mode
6. Shutdown rejects new writes and drains every write already accepted

### Query Operations
1. Replay-sensitive game reads wait on the async-write barrier
2. "My games" matches claims (`white_claimed_by`/`black_claimed_by`) through two
   composite partial indexes; authenticated creators and first movers always
   record a claim, and claims survive player reconfiguration
3. The listing reads each game's move count and final FEN from its last move
   with a LATERAL probe of the `moves` primary key, not a per-row count
4. Move history is read in primary-key order `(game_id, move_number)`
5. Usernames and emails are stored lowercase; lookups lower their input
6. Malformed IDs are treated as "not found" before reaching the database

## Concurrency

- **HTTP Server**: Fiber handles concurrent connections
- **Game State**: Single RWMutex protects game map (concurrent reads, serial writes)
- **Move Validation**: Optimistic FEN/state/turn checks reject a result if the game changed while Stockfish was validating
- **Engine Workers**: Fixed pool (2 workers) with dedicated Stockfish processes
- **Validation Engine**: Single mutex-protected instance for synchronous validation
- **Storage Writer**: Single goroutine processes game write queue sequentially
- **User Operations**: Direct database access; account creation serialized by a PostgreSQL advisory lock
- **Password Hashing**: Four Argon2id slots; waiters give up after five seconds (HTTP 503)
- **PID Lock**: File-based exclusive lock prevents multiple instances

## Data Structures

### User Record
```go
type UserRecord struct {
    UserID       string
    Username     string
    Email        string
    PasswordHash string
    CreatedAt    time.Time
    LastLoginAt  *time.Time
}
```

### Game Snapshot with User Context
```go
type Snapshot struct {
    FEN           string
    PreviousMove  string
    NextTurnColor Color
    PlayerID      string  // User ID or generated UUID
}
```

### JWT Claims
```json
{
    "iss": "chess-server",
    "aud": ["chess-api"],
    "sub": "user-id",
    "iat": 1234567890,
    "nbf": 1234567890,
    "exp": 1235172690,
    "extra": {"session_id": "session-id"}
}
```
Profile data is not placed in the token; clients read it from `/auth/me`.

### Command Pattern with User Context
Commands encapsulate operations with type, arguments, and optional user ID for authenticated requests.

### Player Configuration
Players identified by UUID (authenticated users) or generated IDs (anonymous), configured with type (human/computer), skill level, and search time.

### Storage Schema (version 1)
```sql
users (
    user_id uuid PRIMARY KEY,
    username text NOT NULL UNIQUE,          -- lowercase, 1-64 characters
    email text UNIQUE,                      -- lowercase or NULL
    password_hash text NOT NULL,            -- Argon2id PHC string
    created_at timestamptz NOT NULL,
    last_login_at timestamptz
)

sessions (
    session_id uuid PRIMARY KEY,
    user_id uuid NOT NULL UNIQUE REFERENCES users ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
)

games (
    game_id uuid PRIMARY KEY,
    initial_fen text NOT NULL,
    white_player_id uuid NOT NULL,          -- user ID or generated UUID
    white_type smallint NOT NULL,           -- 1 human, 2 computer
    white_level smallint NOT NULL,          -- 0-20
    white_search_time integer NOT NULL,
    white_claimed_by uuid,                  -- user that owns the slot
    white_name text,                        -- claimant's username at claim time
    black_... (same columns),
    start_time_utc timestamptz NOT NULL,
    result text,                            -- white_wins, black_wins, draw, stalemate
    end_time_utc timestamptz,               -- set exactly when result is set
)

moves (
    game_id uuid REFERENCES games ON DELETE CASCADE,
    move_number integer,                    -- 1..n without gaps
    move_uci text NOT NULL,                 -- [a-h][1-8][a-h][1-8][qrbn]?
    fen_after_move text NOT NULL,
    player_color text NOT NULL,             -- 'w' | 'b'
    move_time_utc timestamptz NOT NULL,
    PRIMARY KEY (game_id, move_number)
)
```

`schema_version` records the applied version. Indexes cover session expiry,
each claim column ordered by `(start_time_utc DESC, game_id DESC)` for the game
listing, and the start time of unclaimed games for the anonymous-game purge.
Claims and player IDs deliberately have no foreign key to `users`: history
outlives deleted accounts, and the name snapshot keeps it readable.

### Retention
- Sessions are deleted after expiry (7 days after the last login).
- Finished games leave memory after `-finished-game-ttl` (1 hour); their rows stay.
- Games with no claimed slot are unloaded from memory and deleted from the
  database 24 hours after their last activity (`-anonymous-game-ttl`). The
  hourly cleanup evicts idle games first, then queues the delete through the
  ordered writer while excluding every game still loaded, so no live game can
  lose its row.

See [`deploy/postgresql`](../deploy/postgresql) for provisioning and
[deployment.md](./deployment.md) for the jail service.

## Security Architecture

### Authentication Flow
1. Password validation enforces minimum complexity
2. Argon2id hashing prevents rainbow table attacks
3. JWT tokens expire after 7 days and must name this service as issuer/audience
4. Case-insensitive username/email matching prevents duplicate accounts
5. Unknown accounts and wrong passwords cost the same Argon2id work and
   return the same error

### Rate Limiting Strategy
- Client IP: the `-proxy-header` value (default `X-Real-IP`) only on connections
  from `-trusted-proxies`; otherwise the TCP peer
- General API: 10 req/s per IP (20 in dev mode)
- Registration: 5 req/min per IP (prevent spam accounts)
- Login: 10 req/min per IP (prevent brute force)
- Game operations unaffected for authenticated users

### Data Protection
- Passwords never stored in plaintext
- JWT key from a 0600 key file, or rotated on restart (fixed in dev mode)
- Database access by Unix-socket peer authentication; no stored DB password
- User IDs use UUIDs with collision detection
- Transactions keep registration, sessions, moves, claims, results, and rewinds internally consistent
- Case-insensitive queries prevent duplicate accounts

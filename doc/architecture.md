# Architecture

## Components

### Transport Layer (`internal/server/http`)
Fiber web server handling HTTP requests/responses. Implements routing, rate limiting, content-type validation, JWT authentication middleware, request parsing. Translates HTTP to internal Command objects.
Each request carries a context that ends 35 seconds after it starts, or when
shutdown stops what outlived its drain; it reaches every synchronous storage
call. At shutdown long-polls end first, then running requests get five seconds
to finish.

### Processing Layer (`internal/server/processor`)
Central command handler containing business logic. Single `Execute(ctx, Command)` entry point decouples transport from logic; a command that names a stored game loads it first. The rules core validates moves; the asynchronous EngineQueue computes the computer's, and one more engine answers draw offers and re-checks the core's rulings off the request path. Commands include optional user context for authenticated operations.
Analysis (`POST /api/analysis`) has an engine of its own, started on its first request: one position at a time within a node budget, four requests waiting at most, answers kept in memory by position.

### Service Layer (`internal/server/service`)
In-memory live-game storage with authentication support. A mutex protects each
game transition and callers receive immutable game views rather than mutable
game pointers. The service manages game lifecycle, snapshots, player
configuration, user accounts, JWT tokens, persistence, and cleanup. Finished
games leave memory after an hour and unfinished ones a registered player
claimed after a week idle, while their durable rows remain; games with no
registered player leave memory and the database 24 hours after their last
activity (see Retention below). An unfinished game that is not in memory is
loaded from its rows by its next request (see Resume below).

#### Long-Polling Registry (`internal/server/service/waiter.go`)
Manages clients waiting for game state changes via HTTP long-polling. Tracks move counts per client, sends notifications on state changes, enforces 30-second timeout. Non-blocking notification pattern handles slow clients gracefully. Coordinates with service layer for game updates and deletion events.

#### Authentication Module (`internal/server/service/user.go`, `internal/server/http/auth.go`)
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

While degraded, every write is refused and its game remembered. Every five
seconds the service asks the store to recover: once the queue has settled and
the database answers a ping, the writer hands the service the games whose
writes were lost. Under its lock the service readmits writes and queues each
of those games whole (row upserted, moves past the line removed, the line
written over), so a later write for a game follows its whole row, and
whether the failed COMMIT landed no longer matters. A whole row the database
refuses for good drops that game alone. Nothing leaves memory by cleanup
while storage is degraded; a restart then loses what only memory held.

Synchronous operations run under the caller's context (a request's, the
cleanup job's): five seconds a query, ten for the sweep's flush and account
purge. Queued writes keep the writer's own deadline. A caller that has ended
queues nothing; once queued, a write runs and its caller (import, removal,
account deletion) waits up to fifteen seconds for the outcome. The pool holds
at most ten connections (five idle). The schema is resolved through
`search_path`, so the role default (`chess`) or a DSN parameter selects it.
`InitDB` applies ordered, transactional migrations under an advisory lock and
issues no DDL when the schema is current, which lets a DML-only runtime role
start the server.

### Supporting Modules
- **Engine** (`internal/server/engine`): UCI protocol wrapper for Stockfish process communication
- **Game** (`internal/server/game`): Game state with snapshot history and player associations
- **Rules core** (`internal/server/chess`): dependency-free position model,
  legal move generation, SAN, PGN, ASCII board, dead-material and repetition
  detection. It is the move authority: it validates custom starting FENs and
  every live move, gives the resulting position, classifies mate and
  stalemate, and applies the automatic draw rules. Stockfish replays each
  ruling in the background, and a disagreement is logged
- **Replay** (`internal/server/replay`): works on stored game records: SAN
  for every ply, PGN assembly with the termination comment, and verification
  against the rules (used by `db verify`, `db pgn`, and the integrity sweep)
- **Mail** (`internal/server/mail`): password-reset email through an SMTP
  relay, or `.eml` files in development
- **Metrics** (`internal/server/metrics`): one `msg=metrics` log line every
  `-metrics-interval` ([Metrics](#metrics))
- **Web server** (`internal/server/webserver`): serves the embedded web client
  (`-serve`)
- **Core** (`internal/server/core`): Shared types, API models, error constants
- **CLI** (`cmd/chess-server/cli`): Database and user management commands
- **Client** (`cmd/chess-client-cli`, `internal/client`): Interactive debugging client with command registry, session management, and colored terminal output

### Metrics
There is no scrape endpoint: every `-metrics-interval` (default one minute,
`0` off) the server logs its measures as one INFO line, `msg=metrics`, which a
log shipper filters out of the log. Counters run from the start; a timer
gives `count`, `avg` and `max` since the last line.

- `games`: `loaded`, `with_computer`, `longpoll_waiters`
- `auth.kdf_wait`: a password's wait for an Argon2id slot
- `replay.read`: a stored game's read, its barrier included
- `storage`: `write_queue`, `write_retries`, `degraded` and `recovered`
  (transitions), `flush` (a barrier's wait behind queued writes)
- `engine`: `queue` (computer moves waiting), `restarts`
- `rules.disagreements`: rulings the engine shadow contradicted

## Request Flow

### User Registration
1. HTTP handler receives `POST /auth/register` with credentials
2. Validates username format and password strength
3. Service layer hashes password with Argon2id (bounded concurrency)
4. Creates the user and its initial session in one transaction (random UUIDv4 ID)
5. Generates a scoped JWT bound to that session
6. Returns token and user information

### Game Creation and Sides
1. HTTP handler receives `POST /games` with optional Bearer token
2. Middleware validates JWT if present; the session lookup names the user
3. The handler builds the actor: user ID and name, and the hash of the seat
   secret the request presents (`X-Chess-Seat`), either of which may be absent
4. The creator takes one human side: the `side` asked for, else White (or
   Black) when signed in. A signed-in creator claims it by name; a guest
   gets a seat, whose secret only this answer carries
5. `POST /games/{id}/join` gives an open side the same way, without a move,
   and wakes long-polls
6. A held side acts only for its holder; an open one for anyone with the ID

### Human Move (Authenticated)
1. HTTP handler receives `POST /games/{id}/moves` with move
2. Optional JWT validation for user verification
3. Creates MakeMoveCommand, calls `processor.Execute()`
4. The rules core checks the move against the game's position: an illegal
   one is refused, a missing promotion piece named. No engine and no lock
   are involved
5. The core gives the new FEN and classifies it: mate or stalemate, else the
   automatic draws over the game's position history (dead material,
   threefold repetition, fifty-move rule)
6. The ruling is handed to the engine shadow, which replays the move on
   Stockfish later and logs `rules core and engine disagree` on legality or
   on the resulting position; a full backlog skips the check
7. Calls `service.ApplyMoveWithState()` with the FEN, turn, state, and
   termination that were decided
8. Service rejects a stale concurrent commit or atomically updates the move,
   optional slot claim, terminal result, and termination; a move by the
   recipient of a draw offer declines it
9. The same logical mutation is queued as one database transaction
10. Returns GameResponse

### Resignation and Draw Agreement
1. `POST /games/{id}/resign` or `/draw` with an optional side
2. The processor resolves the acting side (requested, else the caller's one
   claimed side, else the only human side) and checks slot ownership
3. A draw offer to a human is recorded on the game and wakes long-polls; an
   offer to a computer is answered at once from a short full-strength search
   on the processor's engine (after twenty plies; accept when level or worse
   for the computer); one offer per own move
4. `service.EndGame()` re-checks under its lock that the game is unfinished,
   unchanged, and the accepted offer still stands, then sets result and
   termination, claims an empty acting side for an authenticated caller, and
   queues one transaction

### Computer Move
1. HTTP handler receives `POST /games/{id}/moves` with `{"move": "cccc"}`
2. Processor queues a task on EngineQueue and sets the game `pending`, under
   the service lock; a full queue (two tasks a worker) answers 503
   `RESOURCE_LIMIT` and leaves the game `ongoing`
3. Returns immediately
4. Worker goroutine calculates move with dedicated Stockfish instance
5. Callback checks the move with the rules core, classifies the position as
   for a human move, and commits via the service; a worker that reports no
   move is believed only when the rules find none
6. Client polls for completion
7. Returns GameResponse

### Long-Polling Flow
1. Client sends `GET /games/{id}?wait=true&moveCount=N`
2. Registers wait with WaitRegistry using game ID and move count
3. If game state unchanged, blocks up to 30 seconds
4. On any game update, NotifyGame sends to all waiters
5. Returns immediately with current state
6. Game deletion notifies and removes all waiters; server shutdown ends the
   wait
7. A client that disconnects is not noticed (fasthttp reports no disconnect
   to a running handler): its wait ends by one of the above

### Resume
1. A game command, or a long-poll, names a game that is not in memory
2. The service registers the load, so concurrent misses share it and the
   purge and the integrity sweep treat the game as loaded
3. It reads the row and moves behind the write barrier, outside its lock
4. Only an unfinished game that was played here and passes verification is
   rebuilt; a finished or imported one stays replay-only
5. The game is inserted under the lock; a removal or a released claim that
   crossed the read makes the load read again
6. The command runs on the loaded game; a miss while storage is degraded or
   slow answers 503

### Durable Replay Read
1. Client requests `GET /api/games/{id}/history`
2. Storage queues a barrier after all previously accepted gameplay writes
3. The writer reaches the barrier only after those transactions finish
4. Storage reads the game row and ordered moves in one REPEATABLE READ transaction
5. The replay package derives SAN for each ply from the stored FEN before it
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
6. Recovery readmits writes and stores each game that changed meanwhile whole
7. Shutdown rejects new writes and drains every write already accepted

### Query Operations
1. Replay-sensitive game reads wait on the async-write barrier
2. "My games" matches claims and imports (`white_claimed_by`,
   `black_claimed_by`, `imported_by`) through three composite partial
   indexes, a colour filter that claim only; authenticated creators and first
   movers always record a claim, and claims survive player reconfiguration
3. The listing reads each game's move count and final FEN from its last move
   with a LATERAL probe of the `moves` primary key, not a per-row count
4. Move history is read in primary-key order `(game_id, move_number)`
5. Usernames and emails are stored lowercase; lookups lower their input
6. Malformed IDs are treated as "not found" before reaching the database

## Concurrency

- **HTTP Server**: Fiber handles concurrent connections
- **Game State**: Single RWMutex protects game map (concurrent reads, serial writes)
- **Move Validation**: The rules core, on the request's goroutine; optimistic FEN/state/turn checks reject a result if the game changed meanwhile
- **Engine Workers**: Fixed pool (2 workers) with dedicated Stockfish processes
- **Processor Engine**: Single mutex-protected instance for draw-offer evaluation and the shadow check (64 rulings may wait)
- **Analysis Engine**: A fourth Stockfish process, started by the first analysis; one search at a time, four requests waiting
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
Players identified by UUID (authenticated users) or generated IDs (anonymous), configured with type (human/computer), skill level, and search time. A human side is held by an account's claim or a guest's seat, or is open.

### Storage Schema (version 6)
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
    white_seat bytea,                       -- SHA-256 of a guest's seat secret;
                                            --   never beside a claim
    black_... (same columns),
    start_time_utc timestamptz NOT NULL,
    result text,                            -- white_wins, black_wins, draw, stalemate
    termination text,                       -- how: checkmate, resignation, stalemate,
                                            -- insufficient_material, threefold_repetition,
                                            -- fifty_move_rule, agreement
    end_time_utc timestamptz,               -- set exactly when result is set
    concession_result text,                 -- first resignation (win) or agreed draw,
    concession_termination text,            --   kept when an undo continues play
    concession_ply integer,                 --   against the computer; all four set
    concession_time_utc timestamptz,        --   together or all NULL
    imported_by uuid,                       -- account that imported it from PGN
    event text, site text, round text       -- the imported PGN's tags
)

password_resets (
    user_id uuid PRIMARY KEY REFERENCES users ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,       -- SHA-256 of the emailed token
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL         -- an hour after creation
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

`schema_version` records the applied version; version 2 added
`termination`, constrained to be set exactly with `result` and to fit it
(wins by checkmate or resignation, draws by a draw rule or agreement).
Version 3 added the `concession_*` columns: a rewind clears `result` and
`termination` but keeps them, and the first concession is never overwritten.
Version 4 added `password_resets`, one pending reset per account; version 5
the import columns; version 6 the guest seats. Indexes cover session and reset expiry, each claim column
and `imported_by` ordered by `(start_time_utc DESC, game_id DESC)` for the
game listing, and the start time of games with neither claim nor importer for
the anonymous-game purge.
Claims and player IDs deliberately have no foreign key to `users`: history
outlives deleted accounts, and the name snapshot keeps it readable.

### Retention
- Sessions are deleted after expiry (7 days after the last login).
- Finished games leave memory after `-finished-game-ttl` (1 hour); their rows stay.
- Unfinished games a registered player claimed leave memory after a week
  without activity (`-idle-game-ttl`); their rows stay, and the next request
  for one loads it again.
- Games with no claimed slot are unloaded from memory and deleted from the
  database 24 hours after their last activity (`-anonymous-game-ttl`). The
  hourly cleanup evicts idle games first, then queues the delete through the
  ordered writer while excluding every game still loaded or being loaded, so
  no live game can lose its row.
- A write whose game row was deleted by hand fails only that game: the
  writer drops it and the service unloads the game, instead of degrading
  storage for everyone. A whole row refused during recovery does the same.
- With `-db-cleanup report|delete`, the hourly cleanup also runs an integrity
  sweep: loaded games without a row, games with missing plies, stored games
  failing verification (200 per run, resuming in ID order), games whose
  registered players were all deleted, and accounts with unusable password
  hashes. `report` only logs; `delete` unloads and removes them.

See [`deploy/postgresql`](../deploy/postgresql) for provisioning,
[deployment.md](./deployment.md) for the jail service, and
[deployment-linux.md](./deployment-linux.md) for systemd.

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
- Analysis: 30 req/min per IP, signed-in only (a search costs a second of CPU)
- Game operations unaffected for authenticated users

### Data Protection
- Passwords never stored in plaintext
- A guest's seat secret is shown once and stored as its SHA-256, compared in constant time
- JWT key from a 0600 key file, or rotated on restart (fixed in dev mode)
- Database access by Unix-socket peer authentication; no stored DB password
- User IDs use UUIDs with collision detection
- Transactions keep registration, sessions, moves, claims, results, and rewinds internally consistent
- Case-insensitive queries prevent duplicate accounts

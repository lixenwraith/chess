# Architecture

## Components

### Transport Layer (`internal/http`)
Fiber web server handling HTTP requests/responses. Implements routing, rate limiting, content-type validation, JWT authentication middleware, request parsing. Translates HTTP to internal Command objects.

### Processing Layer (`internal/processor`)
Central command handler containing business logic. Single `Execute(Command)` entry point decouples transport from logic. Uses synchronous UCI engine for validation, asynchronous EngineQueue for computer moves. Commands include optional user context for authenticated operations.

### Service Layer (`internal/service`)
In-memory state storage with authentication support. Thread-safe game map protected by RWMutex. Manages game lifecycle, snapshots, player configuration, user accounts, and JWT token generation. Coordinates with storage layer for persistence of both games and users.

#### Long-Polling Registry (`internal/service/waiter.go`)
Manages clients waiting for game state changes via HTTP long-polling. Tracks move counts per client, sends notifications on state changes, enforces 25-second timeout. Non-blocking notification pattern handles slow clients gracefully. Coordinates with service layer for game updates and deletion events.

#### Authentication Module (`internal/service/user.go`, `internal/http/auth.go`)
- **Password Hashing**: Argon2id for secure password storage
- **JWT Management**: HS256 tokens with 7-day expiration
- **User Operations**: Registration, login, profile management
- **Session Tracking**: Last login timestamps

### Storage Layer (`internal/storage`)
SQLite persistence with async writes for games, synchronous writes for authentication operations. Buffered channel (1000 ops) processes game writes sequentially in background. User operations use direct database access for consistency. Graceful degradation on write failures. WAL mode for development environments.

### Supporting Modules
- **Engine** (`internal/engine`): UCI protocol wrapper for Stockfish process communication
- **Game** (`internal/game`): Game state with snapshot history and player associations
- **Board** (`internal/board`): FEN parsing and ASCII generation
- **Core** (`internal/core`): Shared types, API models, error constants
- **CLI** (`cmd/chessd/cli`): Database and user management commands

## Request Flow

### User Registration
1. HTTP handler receives `POST /auth/register` with credentials
2. Validates username format and password strength
3. Service layer hashes password with Argon2id
4. Creates user record with unique ID (collision detection)
5. Generates JWT token
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
4. Processor validates move via locked validation engine
5. If legal, gets new FEN from engine
6. Calls `service.ApplyMove()` to update state
7. Persists move with player identification
8. Returns GameResponse

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
4. If game state unchanged, blocks up to 25 seconds
5. On any game update, NotifyGame sends to all waiters
6. Returns immediately with current state
7. Client disconnection cancels wait via context
8. Game deletion notifies and removes all waiters

## Persistence Flow

### User Write Operations (Synchronous)
1. Service layer calls storage method directly (CreateUser, UpdateUserPassword, etc.)
2. Operations use database transactions for consistency
3. Unique constraint checks within transaction
4. Immediate commit or rollback
5. Returns success or specific error (duplicate username, etc.)

### Game Write Operations (Asynchronous)
1. Service layer calls storage method (RecordNewGame, RecordMove, DeleteUndoneMoves)
2. Operation queued to buffered channel (non-blocking)
3. Writer goroutine processes queue sequentially
4. Transactions ensure atomicity
5. Failures trigger degradation to memory-only mode

### Query Operations
1. CLI invokes Store.QueryGames or Store.GetUserByUsername with filters
2. Direct database read (no queue)
3. Case-insensitive matching for usernames/emails
4. Results formatted as tabular output

## Concurrency

- **HTTP Server**: Fiber handles concurrent connections
- **Game State**: Single RWMutex protects game map (concurrent reads, serial writes)
- **Engine Workers**: Fixed pool (2 workers) with dedicated Stockfish processes
- **Validation Engine**: Single mutex-protected instance for synchronous validation
- **Storage Writer**: Single goroutine processes game write queue sequentially
- **User Operations**: Direct database access with transaction isolation
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
```go
{
    "sub": "user-id",
    "username": "alice",
    "email": "alice@example.com",
    "exp": 1234567890
}
```

### Command Pattern with User Context
Commands encapsulate operations with type, arguments, and optional user ID for authenticated requests.

### Player Configuration
Players identified by UUID (authenticated users) or generated IDs (anonymous), configured with type (human/computer), skill level, and search time.

### Storage Schema
```sql
-- User authentication table
users (
    user_id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL COLLATE NOCASE,
    email TEXT COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_login_at DATETIME
)

-- Game storage with player associations
games (
    game_id TEXT PRIMARY KEY,
    initial_fen TEXT,
    white_player_id TEXT,  -- User ID or generated UUID
    white_type INTEGER,
    white_level INTEGER,
    white_search_time INTEGER,
    black_player_id TEXT,  -- User ID or generated UUID
    black_type INTEGER,
    black_level INTEGER,
    black_search_time INTEGER,
    start_time_utc DATETIME
)

-- Move history
moves (
    move_id INTEGER PRIMARY KEY,
    game_id TEXT,
    move_number INTEGER,
    move_uci TEXT,
    fen_after_move TEXT,
    player_color TEXT,
    move_time_utc DATETIME,
    FOREIGN KEY (game_id) REFERENCES games(game_id)
)
```

## Security Architecture

### Authentication Flow
1. Password validation enforces minimum complexity
2. Argon2id hashing prevents rainbow table attacks
3. JWT tokens expire after 7 days
4. Case-insensitive username/email matching prevents enumeration
5. Constant-time password verification prevents timing attacks

### Rate Limiting Strategy
- General API: 10 req/s per IP (20 in dev mode)
- Registration: 5 req/min per IP (prevent spam accounts)
- Login: 10 req/min per IP (prevent brute force)
- Game operations unaffected for authenticated users

### Data Protection
- Passwords never stored in plaintext
- JWT secret rotates on restart (or fixed in dev mode)
- User IDs use UUIDs with collision detection
- Transactions ensure data consistency
- Case-insensitive queries prevent duplicate accounts
package storage

import "time"

// UserRecord represents a user account in the database. Accounts created by
// public registration and by the CLI are identical and do not expire.
type UserRecord struct {
	UserID       string
	Username     string
	Email        string // empty when the account has no email (stored as NULL)
	PasswordHash string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// SessionRecord represents an active user session
type SessionRecord struct {
	SessionID string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// GameRecord represents a row in the games table
type GameRecord struct {
	GameID          string
	InitialFEN      string
	WhitePlayerID   string
	WhiteType       int
	WhiteLevel      int
	WhiteSearchTime int
	WhiteClaimedBy  string
	WhiteName       string // claimant's username when the claim was recorded
	BlackPlayerID   string
	BlackType       int
	BlackLevel      int
	BlackSearchTime int
	BlackClaimedBy  string
	BlackName       string
	Result          string
	StartTimeUTC    time.Time
	EndTimeUTC      *time.Time
}

// GameSummaryRecord is a game row plus its replay extent. MoveCount and
// FinalFEN come from the last move, so a picker needs no move-list read.
type GameSummaryRecord struct {
	GameRecord
	MoveCount int
	FinalFEN  string
}

// MoveRecord represents a row in the moves table
type MoveRecord struct {
	GameID       string
	MoveNumber   int
	MoveUCI      string
	FENAfterMove string
	PlayerColor  string
	MoveTimeUTC  time.Time
}

// MovePersistence groups changes caused by one accepted move so the move,
// first-move slot claim, and terminal result commit in one transaction.
type MovePersistence struct {
	Move       MoveRecord
	ClaimColor string
	ClaimedBy  string
	Result     string
	EndTimeUTC *time.Time
}

// schemaVersion is the newest schema this binary understands. Migrations are
// applied in order inside one transaction; PostgreSQL DDL is transactional, so
// a failed upgrade leaves the previous version intact.
const schemaVersion = 1

// migrations[v-1] upgrades the schema from version v-1 to v. Never edit a
// released migration; append a new one.
var migrations = []string{
	// v1: initial PostgreSQL schema.
	//
	// Identifiers are uuid (16 bytes, validated on input). Usernames and emails
	// are stored lowercase, so plain unique constraints give case-insensitive
	// uniqueness without citext or a nondeterministic collation. Game-to-user
	// association is by claim: every authenticated human slot records its
	// claimant, and claims survive player reconfiguration. The claimant's
	// username is copied into the game when the claim is written, so replays
	// and PGN keep the name after a rename or account deletion. Games with no
	// claim belong to anonymous players and are purged after inactivity.
	`
CREATE TABLE schema_version (
	singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
	version    integer NOT NULL CHECK (version > 0),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
	user_id       uuid PRIMARY KEY,
	username      text NOT NULL,
	email         text,
	password_hash text NOT NULL,
	created_at    timestamptz NOT NULL DEFAULT now(),
	last_login_at timestamptz,
	CONSTRAINT users_username_key UNIQUE (username),
	CONSTRAINT users_email_key UNIQUE (email),
	CONSTRAINT users_username_check
		CHECK (username = lower(username) AND char_length(username) BETWEEN 1 AND 64),
	CONSTRAINT users_email_check
		CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 254)
);

CREATE TABLE sessions (
	session_id uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL,
	CONSTRAINT sessions_user_id_key UNIQUE (user_id)
);

CREATE TABLE games (
	game_id           uuid PRIMARY KEY,
	initial_fen       text NOT NULL CHECK (initial_fen <> ''),
	white_player_id   uuid NOT NULL,
	white_type        smallint NOT NULL CHECK (white_type IN (1, 2)),
	white_level       smallint NOT NULL DEFAULT 0 CHECK (white_level BETWEEN 0 AND 20),
	white_search_time integer NOT NULL DEFAULT 0 CHECK (white_search_time >= 0),
	white_claimed_by  uuid,
	white_name        text CHECK (char_length(white_name) BETWEEN 1 AND 64),
	black_player_id   uuid NOT NULL,
	black_type        smallint NOT NULL CHECK (black_type IN (1, 2)),
	black_level       smallint NOT NULL DEFAULT 0 CHECK (black_level BETWEEN 0 AND 20),
	black_search_time integer NOT NULL DEFAULT 0 CHECK (black_search_time >= 0),
	black_claimed_by  uuid,
	black_name        text CHECK (char_length(black_name) BETWEEN 1 AND 64),
	start_time_utc    timestamptz NOT NULL DEFAULT now(),
	result            text CHECK (result IN ('white_wins', 'black_wins', 'draw', 'stalemate')),
	end_time_utc      timestamptz,
	CONSTRAINT games_result_end_time_check CHECK ((result IS NULL) = (end_time_utc IS NULL))
);

-- Moves are numbered 1..n without gaps; the primary key serves ordered replay
-- reads and the last-move probe used by game listings.
CREATE TABLE moves (
	game_id        uuid NOT NULL REFERENCES games (game_id) ON DELETE CASCADE,
	move_number    integer NOT NULL CHECK (move_number > 0),
	move_uci       text NOT NULL CHECK (move_uci ~ '^[a-h][1-8][a-h][1-8][qrbn]?$'),
	fen_after_move text NOT NULL CHECK (fen_after_move <> ''),
	player_color   text NOT NULL CHECK (player_color IN ('w', 'b')),
	move_time_utc  timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (game_id, move_number)
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
-- Candidates for the anonymous-game purge: a game that started after the
-- cutoff cannot have been idle since it.
CREATE INDEX games_anonymous_start_idx ON games (start_time_utc)
	WHERE white_claimed_by IS NULL AND black_claimed_by IS NULL;
CREATE INDEX games_white_claimed_idx ON games (white_claimed_by, start_time_utc DESC, game_id DESC)
	WHERE white_claimed_by IS NOT NULL;
CREATE INDEX games_black_claimed_idx ON games (black_claimed_by, start_time_utc DESC, game_id DESC)
	WHERE black_claimed_by IS NOT NULL;
`,
}

// ownedTables lists every table created by migrations, children first.
var ownedTables = []string{"moves", "games", "sessions", "users", "schema_version"}

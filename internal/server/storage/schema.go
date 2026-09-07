package storage

import "time"

// UserRecord represents a user account in the database
type UserRecord struct {
	UserID       string     `db:"user_id"`
	Username     string     `db:"username"`
	Email        string     `db:"email"`
	PasswordHash string     `db:"password_hash"`
	AccountType  string     `db:"account_type"` // "permanent" or "temp"
	CreatedAt    time.Time  `db:"created_at"`
	ExpiresAt    *time.Time `db:"expires_at"` // nil for permanent
	LastLoginAt  *time.Time `db:"last_login_at"`
}

// SessionRecord represents an active user session
type SessionRecord struct {
	SessionID string    `db:"session_id"`
	UserID    string    `db:"user_id"`
	CreatedAt time.Time `db:"created_at"`
	ExpiresAt time.Time `db:"expires_at"`
}

// GameRecord represents a row in the games table
type GameRecord struct {
	GameID          string     `db:"game_id"`
	InitialFEN      string     `db:"initial_fen"`
	WhitePlayerID   string     `db:"white_player_id"`
	WhiteType       int        `db:"white_type"`
	WhiteLevel      int        `db:"white_level"`
	WhiteSearchTime int        `db:"white_search_time"`
	WhiteClaimedBy  string     `db:"white_claimed_by"`
	BlackPlayerID   string     `db:"black_player_id"`
	BlackType       int        `db:"black_type"`
	BlackLevel      int        `db:"black_level"`
	BlackSearchTime int        `db:"black_search_time"`
	BlackClaimedBy  string     `db:"black_claimed_by"`
	Result          string     `db:"result"`
	StartTimeUTC    time.Time  `db:"start_time_utc"`
	EndTimeUTC      *time.Time `db:"end_time_utc"`
}

type GameSummaryRecord struct {
	GameRecord
	MoveCount int `db:"move_count"`
}

// MoveRecord represents a row in the moves table
type MoveRecord struct {
	MoveID       int64     `db:"move_id"`
	GameID       string    `db:"game_id"`
	MoveNumber   int       `db:"move_number"`
	MoveUCI      string    `db:"move_uci"`
	FENAfterMove string    `db:"fen_after_move"`
	PlayerColor  string    `db:"player_color"`
	MoveTimeUTC  time.Time `db:"move_time_utc"`
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

// Schema defines tables only. Indexes are applied after legacy column
// migrations so upgrading an older games table never references a missing
// column.
const Schema = `
CREATE TABLE IF NOT EXISTS users (
	user_id TEXT PRIMARY KEY,
	username TEXT UNIQUE NOT NULL COLLATE NOCASE,
	email TEXT COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	account_type TEXT NOT NULL DEFAULT 'temp' CHECK(account_type IN ('permanent', 'temp')),
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at DATETIME,
	last_login_at DATETIME
);

CREATE TABLE IF NOT EXISTS sessions (
	session_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL UNIQUE,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at DATETIME NOT NULL,
	FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS games (
	game_id TEXT PRIMARY KEY,
	initial_fen TEXT NOT NULL,
	white_player_id TEXT NOT NULL,
	white_type INTEGER NOT NULL,
	white_level INTEGER NOT NULL DEFAULT 0,
	white_search_time INTEGER NOT NULL DEFAULT 1000,
	black_player_id TEXT NOT NULL,
	black_type INTEGER NOT NULL,
	black_level INTEGER NOT NULL DEFAULT 0,
	black_search_time INTEGER NOT NULL DEFAULT 1000,
	start_time_utc DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	result TEXT CHECK(result IS NULL OR result IN ('white_wins', 'black_wins', 'draw', 'stalemate')),
	end_time_utc DATETIME,
	white_claimed_by TEXT,
	black_claimed_by TEXT
);

CREATE TABLE IF NOT EXISTS moves (
	move_id INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id TEXT NOT NULL,
	move_number INTEGER NOT NULL,
	move_uci TEXT NOT NULL,
	fen_after_move TEXT NOT NULL,
	player_color TEXT NOT NULL CHECK(player_color IN ('w', 'b')),
	move_time_utc DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (game_id) REFERENCES games(game_id) ON DELETE CASCADE,
	UNIQUE(game_id, move_number)
);
`

const Indexes = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_unique
	ON users(email) WHERE email IS NOT NULL AND email != '';
CREATE INDEX IF NOT EXISTS idx_users_temp_created_at
	ON users(created_at) WHERE account_type = 'temp';
CREATE INDEX IF NOT EXISTS idx_users_temp_expires_at
	ON users(expires_at) WHERE account_type = 'temp' AND expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_games_white_player ON games(white_player_id);
CREATE INDEX IF NOT EXISTS idx_games_black_player ON games(black_player_id);
CREATE INDEX IF NOT EXISTS idx_games_white_claimed ON games(white_claimed_by)
	WHERE white_claimed_by IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_games_black_claimed ON games(black_claimed_by)
	WHERE black_claimed_by IS NOT NULL;
`

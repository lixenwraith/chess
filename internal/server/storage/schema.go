// FILE: lixenwraith/chess/internal/server/storage/schema.go
package storage

import "time"

// UserRecord represents a user account in the database
type UserRecord struct {
	UserID       string     `db:"user_id"`
	Username     string     `db:"username"`
	Email        string     `db:"email"`
	PasswordHash string     `db:"password_hash"`
	CreatedAt    time.Time  `db:"created_at"`
	LastLoginAt  *time.Time `db:"last_login_at"`
}

// GameRecord represents a row in the games table
type GameRecord struct {
	GameID          string    `db:"game_id"`
	InitialFEN      string    `db:"initial_fen"`
	WhitePlayerID   string    `db:"white_player_id"`
	WhiteType       int       `db:"white_type"`
	WhiteLevel      int       `db:"white_level"`
	WhiteSearchTime int       `db:"white_search_time"`
	BlackPlayerID   string    `db:"black_player_id"`
	BlackType       int       `db:"black_type"`
	BlackLevel      int       `db:"black_level"`
	BlackSearchTime int       `db:"black_search_time"`
	StartTimeUTC    time.Time `db:"start_time_utc"`
}

// MoveRecord represents a row in the moves table
type MoveRecord struct {
	MoveID       int64     `db:"move_id"`
	GameID       string    `db:"game_id"`
	MoveNumber   int       `db:"move_number"`
	MoveUCI      string    `db:"move_uci"`
	FENAfterMove string    `db:"fen_after_move"`
	PlayerColor  string    `db:"player_color"` // "w" or "b"
	MoveTimeUTC  time.Time `db:"move_time_utc"`
}

// Schema defines the SQLite database structure
const Schema = `
CREATE TABLE IF NOT EXISTS users (
	user_id TEXT PRIMARY KEY,
	username TEXT UNIQUE NOT NULL COLLATE NOCASE,
	email TEXT COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_login_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_unique ON users(email) WHERE email IS NOT NULL AND email != '';

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
	start_time_utc DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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

CREATE INDEX IF NOT EXISTS idx_moves_game_id ON moves(game_id);
CREATE INDEX IF NOT EXISTS idx_games_white_player ON games(white_player_id);
CREATE INDEX IF NOT EXISTS idx_games_black_player ON games(black_player_id);
`
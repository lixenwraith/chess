// FILE: lixenwraith/chess/internal/client/api/types.go
package api

import "time"

// Request types
type CreateGameRequest struct {
	White PlayerConfig `json:"white"`
	Black PlayerConfig `json:"black"`
	FEN   string       `json:"fen,omitempty"`
}

type PlayerConfig struct {
	Type       int `json:"type"` // 1=human, 2=computer
	Level      int `json:"level,omitempty"`
	SearchTime int `json:"searchTime,omitempty"`
}

type MoveRequest struct {
	Move string `json:"move"`
}

type UndoRequest struct {
	Count int `json:"count"`
}

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// Response types
type GameResponse struct {
	GameID   string          `json:"gameId"`
	FEN      string          `json:"fen"`
	Turn     string          `json:"turn"`
	State    string          `json:"state"`
	Moves    []string        `json:"moves"`
	Players  PlayersResponse `json:"players"`
	LastMove *MoveInfo       `json:"lastMove,omitempty"`
}

type PlayersResponse struct {
	White PlayerInfo `json:"white"`
	Black PlayerInfo `json:"black"`
}

type PlayerInfo struct {
	ID         string `json:"id"`
	Type       int    `json:"type"`
	Level      int    `json:"level,omitempty"`
	SearchTime int    `json:"searchTime,omitempty"`
}

type MoveInfo struct {
	Move        string `json:"move"`
	PlayerColor string `json:"playerColor"`
	Score       int    `json:"score,omitempty"`
	Depth       int    `json:"depth,omitempty"`
}

type BoardResponse struct {
	FEN   string `json:"fen"`
	Board string `json:"board"`
}

type AuthResponse struct {
	Token    string `json:"token"`
	UserID   string `json:"userId"`
	Username string `json:"username"`
}

type UserResponse struct {
	UserID    string     `json:"userId"`
	Username  string     `json:"username"`
	Email     string     `json:"email,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	LastLogin *time.Time `json:"lastLoginAt,omitempty"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details string `json:"details,omitempty"`
}

type HealthResponse struct {
	Status  string `json:"status"`
	Time    int64  `json:"time"`
	Storage string `json:"storage,omitempty"`
}
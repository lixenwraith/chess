// FILE: internal/core/api.go
package core

// Request types

type CreateGameRequest struct {
	White PlayerConfig `json:"white" validate:"required"`
	Black PlayerConfig `json:"black" validate:"required"`
	FEN   string       `json:"fen,omitempty" validate:"omitempty,max=100"`
}

type ConfigurePlayersRequest struct {
	White PlayerConfig `json:"white" validate:"required"`
	Black PlayerConfig `json:"black" validate:"required"`
}

type MoveRequest struct {
	Move string `json:"move" validate:"required,min=4,max=5"` // "cccc" for computer move, 4-5 chars for UCI moves
}

type UndoRequest struct {
	Count int `json:"count" validate:"required,min=1,max=300"` // Max based on longest games in history (272), theoretical max 5949
}

// Response types

type GameResponse struct {
	GameID   string          `json:"gameId"`
	FEN      string          `json:"fen"`
	Turn     string          `json:"turn"`  // "w" or "b"
	State    string          `json:"state"` // "ongoing", "white_wins", etc
	Moves    []string        `json:"moves"`
	Players  PlayersResponse `json:"players"`
	LastMove *MoveInfo       `json:"lastMove,omitempty"`
}

type MoveInfo struct {
	Move        string `json:"move"`
	PlayerColor string `json:"playerColor"` // "w" or "b"
	Score       int    `json:"score,omitempty"`
	Depth       int    `json:"depth,omitempty"`
}

type BoardResponse struct {
	FEN   string `json:"fen"`
	Board string `json:"board"` // ASCII representation
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details string `json:"details,omitempty"`
}
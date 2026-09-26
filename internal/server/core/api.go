package core

import "time"

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
	State    string          `json:"state"` // "ongoing", "white wins", etc
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

// GameHistoryResponse is the durable replay representation of a game. Moves
// are ordered and include the resulting FEN so clients do not need an engine
// to replay a stored game.
type GameHistoryResponse struct {
	GameID       string          `json:"gameId"`
	InitialFEN   string          `json:"initialFen"`
	Result       string          `json:"result,omitempty"`
	StartTimeUTC time.Time       `json:"startTimeUtc"`
	EndTimeUTC   *time.Time      `json:"endTimeUtc,omitempty"`
	Players      PlayersResponse `json:"players"`
	Moves        []HistoryMove   `json:"moves"`
}

type HistoryMove struct {
	MoveNumber   int       `json:"moveNumber"`
	MoveUCI      string    `json:"moveUci"`
	FENAfterMove string    `json:"fenAfterMove"`
	PlayerColor  string    `json:"playerColor"`
	MoveTimeUTC  time.Time `json:"moveTimeUtc"`
}

// GameSummary is intentionally sufficient for a client-side game picker; the
// full move list remains on the per-game history endpoint. FinalFEN is the
// position after the last stored move (the initial FEN when there is none).
type GameSummary struct {
	GameID       string          `json:"gameId"`
	InitialFEN   string          `json:"initialFen"`
	FinalFEN     string          `json:"finalFen"`
	Result       string          `json:"result,omitempty"`
	StartTimeUTC time.Time       `json:"startTimeUtc"`
	EndTimeUTC   *time.Time      `json:"endTimeUtc,omitempty"`
	MoveCount    int             `json:"moveCount"`
	Players      PlayersResponse `json:"players"`
}

type GameListResponse struct {
	Games      []GameSummary `json:"games"`
	Limit      int           `json:"limit"`
	Offset     int           `json:"offset"`
	NextOffset *int          `json:"nextOffset,omitempty"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details string `json:"details,omitempty"`
}

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
//
// PGNResult is the PGN result token ("1-0", "0-1", "1/2-1/2", or "*") and
// Termination names how a result was reached ("checkmate", "stalemate",
// "draw"; omitted while the game is unfinished).
type GameHistoryResponse struct {
	GameID       string          `json:"gameId"`
	InitialFEN   string          `json:"initialFen"`
	Result       string          `json:"result,omitempty"`
	PGNResult    string          `json:"pgnResult"`
	Termination  string          `json:"termination,omitempty"`
	StartTimeUTC time.Time       `json:"startTimeUtc"`
	EndTimeUTC   *time.Time      `json:"endTimeUtc,omitempty"`
	Players      PlayersResponse `json:"players"`
	Moves        []HistoryMove   `json:"moves"`
}

// HistoryMove is one ply. SAN is derived on read and omitted only when the
// stored move cannot be notated.
type HistoryMove struct {
	MoveNumber   int       `json:"moveNumber"`
	MoveUCI      string    `json:"moveUci"`
	SAN          string    `json:"san,omitempty"`
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
	PGNResult    string          `json:"pgnResult"`
	StartTimeUTC time.Time       `json:"startTimeUtc"`
	EndTimeUTC   *time.Time      `json:"endTimeUtc,omitempty"`
	MoveCount    int             `json:"moveCount"`
	Players      PlayersResponse `json:"players"`
}

// GameListResponse pages newest first. NextCursor continues the listing and
// stays stable while new games are added; NextOffset is kept for clients
// that page by position and is omitted on cursor requests.
type GameListResponse struct {
	Games      []GameSummary `json:"games"`
	Limit      int           `json:"limit"`
	Offset     int           `json:"offset"`
	NextOffset *int          `json:"nextOffset,omitempty"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details string `json:"details,omitempty"`
}

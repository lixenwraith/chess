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

// ResignRequest ends the game in the opponent's favor. Color may be omitted
// when the caller controls exactly one human side.
type ResignRequest struct {
	Color string `json:"color,omitempty" validate:"omitempty,oneof=w b white black"`
}

// DrawRequest offers, accepts, or declines a draw by agreement. Offering to a
// computer opponent is answered at once.
type DrawRequest struct {
	Action string `json:"action" validate:"required,oneof=offer accept decline"`
	Color  string `json:"color,omitempty" validate:"omitempty,oneof=w b white black"`
}

type UndoRequest struct {
	Count int `json:"count" validate:"required,min=1,max=300"` // Max based on longest games in history (272), theoretical max 5949
}

// Response types

// GameResponse is the live game. Termination names how a finished game ended;
// DrawOffer is the color whose draw offer awaits an answer; DrawOutcome
// reports what a draw request did ("offered", "accepted", "declined") and is
// set only on responses to that request. Concession is the game's first
// resignation or agreed draw, kept after an undo continues play. SAN runs
// parallel to Moves, derived by the rules core from the position before each
// move; an entry is empty only for a move the core cannot notate.
type GameResponse struct {
	GameID      string          `json:"gameId"`
	FEN         string          `json:"fen"`
	Turn        string          `json:"turn"`  // "w" or "b"
	State       string          `json:"state"` // "ongoing", "white wins", etc
	Termination string          `json:"termination,omitempty"`
	DrawOffer   string          `json:"drawOffer,omitempty"`
	DrawOutcome string          `json:"drawOutcome,omitempty"`
	Concession  *Concession     `json:"concession,omitempty"`
	Moves       []string        `json:"moves"`
	SAN         []string        `json:"san"`
	Players     PlayersResponse `json:"players"`
	LastMove    *MoveInfo       `json:"lastMove,omitempty"`
}

// Concession records a resignation or agreed draw. Result uses the enclosing
// response's vocabulary: a state ("black wins") in GameResponse, a stored
// result ("black_wins") in GameHistoryResponse. Ply is the number of moves
// played when it was made.
type Concession struct {
	Result      string `json:"result"`
	Termination string `json:"termination"`
	Ply         int    `json:"ply"`
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

// ImportGameRequest is a PGN game to store for the signed-in user.
type ImportGameRequest struct {
	PGN string `json:"pgn" validate:"required,max=65536"`
}

// ImportGameResponse names the stored game.
type ImportGameResponse struct {
	GameID    string `json:"gameId"`
	MoveCount int    `json:"moveCount"`
	Result    string `json:"result,omitempty"`
	PGNResult string `json:"pgnResult"`
}

// RemoveGameResponse says what DELETE /users/me/games/{id} did: "game" (the
// game is gone) or "claim" (another player keeps it; the caller's side was
// released).
type RemoveGameResponse struct {
	Removed string `json:"removed"`
}

// GameHistoryResponse is the durable replay representation of a game. Moves
// are ordered and include the resulting FEN so clients do not need an engine
// to replay a stored game. Origin is "played" or "imported"; Event, Site and
// Round are set for imported games that carried them.
//
// PGNResult is the PGN result token ("1-0", "0-1", "1/2-1/2", or "*") and
// Termination names how a result was reached (see core.Termination; omitted
// while the game is unfinished). Concession is the game's first resignation
// or agreed draw, kept when an undo continued play past it.
type GameHistoryResponse struct {
	GameID       string          `json:"gameId"`
	InitialFEN   string          `json:"initialFen"`
	Result       string          `json:"result,omitempty"`
	PGNResult    string          `json:"pgnResult"`
	Termination  string          `json:"termination,omitempty"`
	StartTimeUTC time.Time       `json:"startTimeUtc"`
	EndTimeUTC   *time.Time      `json:"endTimeUtc,omitempty"`
	Concession   *Concession     `json:"concession,omitempty"`
	Origin       string          `json:"origin"`
	Event        string          `json:"event,omitempty"`
	Site         string          `json:"site,omitempty"`
	Round        string          `json:"round,omitempty"`
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
// position after the last stored move (the initial FEN when there is none);
// Termination says how a finished game ended, as in the history; Origin is
// "played" or "imported", and Event is the imported game's event tag.
type GameSummary struct {
	GameID       string          `json:"gameId"`
	InitialFEN   string          `json:"initialFen"`
	FinalFEN     string          `json:"finalFen"`
	Result       string          `json:"result,omitempty"`
	PGNResult    string          `json:"pgnResult"`
	Termination  string          `json:"termination,omitempty"`
	StartTimeUTC time.Time       `json:"startTimeUtc"`
	EndTimeUTC   *time.Time      `json:"endTimeUtc,omitempty"`
	MoveCount    int             `json:"moveCount"`
	Origin       string          `json:"origin"`
	Event        string          `json:"event,omitempty"`
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

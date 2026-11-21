package core

import "time"

// Request types

// CreateGameRequest creates a game. Side is the human side its creator takes:
// a claim when signed in, a guest's seat otherwise. Without it a signed-in
// creator claims White if human, else Black, and a guest takes nothing.
type CreateGameRequest struct {
	White PlayerConfig `json:"white" validate:"required"`
	Black PlayerConfig `json:"black" validate:"required"`
	FEN   string       `json:"fen,omitempty" validate:"omitempty,max=100"`
	Side  string       `json:"side,omitempty" validate:"omitempty,oneof=w b white black"`
}

// JoinRequest takes an open human side: the one named, or the only one.
type JoinRequest struct {
	Color string `json:"color,omitempty" validate:"omitempty,oneof=w b white black"`
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

// AnalysisRequest asks for the engine's best Lines variations of a position:
// 1 to 3, one when omitted.
type AnalysisRequest struct {
	FEN   string `json:"fen" validate:"required,max=100"`
	Lines int    `json:"lines,omitempty" validate:"omitempty,min=1,max=3"`
}

// Response types

// AnalysisResponse is the engine's view of a position, best line first. A
// position where the game is over has no lines and names its State and
// Termination instead, as the rules give them.
type AnalysisResponse struct {
	FEN         string         `json:"fen"`
	Depth       int            `json:"depth,omitempty"`
	State       string         `json:"state,omitempty"`
	Termination string         `json:"termination,omitempty"`
	Lines       []AnalysisLine `json:"lines"`
}

// AnalysisLine is a variation in UCI with its SAN. Score is White's, in
// centipawns; Mate, when present, is the moves to mate instead, negative when
// Black mates.
type AnalysisLine struct {
	Score int      `json:"score"`
	Mate  int      `json:"mate,omitempty"`
	Moves []string `json:"moves"`
	SAN   []string `json:"san"`
}

// GameResponse is the live game. DrawOutcome ("offered", "accepted",
// "declined") is set only on the answer to a draw request; Side and Seat only
// on the answer to the create or join that gave the caller a side, Seat being
// a guest's secret, shown once. SAN runs parallel to Moves; an entry is empty
// only for a move the rules core cannot notate.
type GameResponse struct {
	GameID      string          `json:"gameId"`
	InitialFEN  string          `json:"initialFen"`
	FEN         string          `json:"fen"`
	Turn        string          `json:"turn"`  // "w" or "b"
	State       string          `json:"state"` // "ongoing", "white wins", etc
	Termination string          `json:"termination,omitempty"`
	DrawOffer   string          `json:"drawOffer,omitempty"`
	DrawOutcome string          `json:"drawOutcome,omitempty"`
	Side        string          `json:"side,omitempty"`
	Seat        string          `json:"seat,omitempty"`
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

// ArchiveInfo is what the archive says of a game: its collection, and the
// ratings and opening code its PGN gave. A history adds the collection's
// title and the rights it is published under.
type ArchiveInfo struct {
	Collection  string `json:"collection"`
	Title       string `json:"title,omitempty"`
	SourceURL   string `json:"sourceUrl,omitempty"`
	Licence     string `json:"licence,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	WhiteElo    int    `json:"whiteElo,omitempty"`
	BlackElo    int    `json:"blackElo,omitempty"`
	ECO         string `json:"eco,omitempty"`
}

// GameHistoryResponse is a stored game for replay: ordered moves, each with
// the FEN after it, so a client needs no engine. Origin is "played",
// "imported" or "archive", with the Event, Site and Round a PGN carried.
// PGNResult is the PGN token ("1-0", "0-1", "1/2-1/2", "*"); Concession is the
// first resignation or agreed draw, kept when an undo played on past it.
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
	Archive      *ArchiveInfo    `json:"archive,omitempty"`
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
// Termination says how a finished game ended, and Origin how it was stored,
// as in the history; Event is the event tag of a game read from PGN.
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
	Archive      *ArchiveInfo    `json:"archive,omitempty"`
	Players      PlayersResponse `json:"players"`
}

// ArchiveListResponse pages the published archive newest first; NextCursor
// continues it. A featured list has no cursor.
type ArchiveListResponse struct {
	Games      []GameSummary `json:"games"`
	Limit      int           `json:"limit"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// Collection is a published collection of archive games.
type Collection struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	SourceURL   string `json:"sourceUrl"`
	Licence     string `json:"licence"`
	Attribution string `json:"attribution"`
	Games       int    `json:"games"`
}

type CollectionListResponse struct {
	Collections []Collection `json:"collections"`
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

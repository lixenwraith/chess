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
	Color      int    `json:"color"`
	Type       int    `json:"type"`
	Level      int    `json:"level,omitempty"`
	SearchTime int    `json:"searchTime,omitempty"`
	ClaimedBy  string `json:"claimedBy,omitempty"`
	Name       string `json:"name,omitempty"`
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
	Token     string    `json:"token"`
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
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

type HistoryMove struct {
	MoveNumber   int       `json:"moveNumber"`
	MoveUCI      string    `json:"moveUci"`
	SAN          string    `json:"san,omitempty"`
	FENAfterMove string    `json:"fenAfterMove"`
	PlayerColor  string    `json:"playerColor"`
	MoveTimeUTC  time.Time `json:"moveTimeUtc"`
}

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

type GameListResponse struct {
	Games      []GameSummary `json:"games"`
	Limit      int           `json:"limit"`
	Offset     int           `json:"offset"`
	NextOffset *int          `json:"nextOffset,omitempty"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

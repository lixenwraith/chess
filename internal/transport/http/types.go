// FILE: internal/transport/http/types.go
package http

import (
	"chess/internal/core"
)

// Request types

type CreateGameRequest struct {
	White PlayerType `json:"white"` // 0=human, 1=computer
	Black PlayerType `json:"black"` // 0=human, 1=computer
	FEN   string     `json:"fen,omitempty"`
}

type MoveRequest struct {
	Move string `json:"move"` // UCI format: "e2e4"
}

type UndoRequest struct {
	Count int `json:"count,omitempty"` // default: 1
}

// Response types

type GameResponse struct {
	GameID   string      `json:"gameId"`
	FEN      string      `json:"fen"`
	Turn     string      `json:"turn"`  // "w" or "b"
	State    string      `json:"state"` // "ongoing", "white_wins", etc
	Moves    []string    `json:"moves"`
	Players  PlayersInfo `json:"players"`
	LastMove *MoveInfo   `json:"lastMove,omitempty"`
}

type PlayersInfo struct {
	White PlayerType `json:"white"`
	Black PlayerType `json:"black"`
}

type MoveInfo struct {
	Move   string `json:"move"`
	Player string `json:"player"` // "w" or "b"
	Score  int    `json:"score,omitempty"`
	Depth  int    `json:"depth,omitempty"`
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

// Custom type for JSON marshaling of PlayerType
type PlayerType core.PlayerType

func (p PlayerType) MarshalJSON() ([]byte, error) {
	// Map to int for JSON: 0=human, 1=computer
	return []byte(string('0' + p)), nil
}

func (p *PlayerType) UnmarshalJSON(data []byte) error {
	if len(data) == 1 && data[0] >= '0' && data[0] <= '1' {
		*p = PlayerType(data[0] - '0')
		return nil
	}
	// Also accept string format for compatibility
	str := string(data)
	if str == `"human"` || str == "human" {
		*p = PlayerType(core.PlayerHuman)
	} else if str == `"computer"` || str == "computer" {
		*p = PlayerType(core.PlayerComputer)
	} else if str == "0" {
		*p = PlayerType(core.PlayerHuman)
	} else if str == "1" {
		*p = PlayerType(core.PlayerComputer)
	}
	return nil
}

// Helper functions

func colorToString(c core.Color) string {
	return string(c)
}

func stateToString(s core.State) string {
	switch s {
	case core.StateOngoing:
		return "ongoing"
	case core.StateWhiteWins:
		return "white_wins"
	case core.StateBlackWins:
		return "black_wins"
	case core.StateDraw:
		return "draw"
	case core.StateStalemate:
		return "stalemate"
	default:
		return "unknown"
	}
}

// Error codes
const (
	ErrGameNotFound      = "GAME_NOT_FOUND"
	ErrInvalidMove       = "INVALID_MOVE"
	ErrNotHumanTurn      = "NOT_HUMAN_TURN"
	ErrGameOver          = "GAME_OVER"
	ErrRateLimitExceeded = "RATE_LIMIT_EXCEEDED"
	ErrInvalidContent    = "INVALID_CONTENT_TYPE"
	ErrInvalidRequest    = "INVALID_REQUEST"
	ErrInternalError     = "INTERNAL_ERROR"
)
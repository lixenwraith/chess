package core

import (
	"crypto/subtle"

	"github.com/google/uuid"
)

type PlayerType int

const (
	PlayerHuman PlayerType = iota + 1
	PlayerComputer
)

// Player is one side of a game. An account holds it by a claim, a guest by a
// seat: the secret whose SHA-256 is Seat, which no response carries.
type Player struct {
	ID         string     `json:"id"`
	Color      Color      `json:"color"`
	Type       PlayerType `json:"type"`
	Level      int        `json:"level,omitempty"`      // Only for computer
	SearchTime int        `json:"searchTime,omitempty"` // Only for computer
	ClaimedBy  string     `json:"claimedBy,omitempty"`  // UserID that claimed this slot
	Name       string     `json:"name,omitempty"`       // Claimant's username when the claim was made
	Seated     bool       `json:"seated,omitempty"`     // a guest holds this side
	Seat       []byte     `json:"-"`
}

// Actor is who a request acts as: a signed-in user, the guest whose seat
// secret hashes to Seat, both, or neither. Name is the user's name.
type Actor struct {
	UserID string
	Name   string
	Seat   []byte
}

// Held reports whether an account or a guest holds this side; an open side
// takes a move from anyone who knows the game.
func (p *Player) Held() bool {
	return p.ClaimedBy != "" || p.Seated
}

// HeldBy reports whether a is the one who holds this side.
func (p *Player) HeldBy(a Actor) bool {
	if p.ClaimedBy != "" {
		return a.UserID == p.ClaimedBy
	}
	return p.Seated && subtle.ConstantTimeCompare(p.Seat, a.Seat) == 1
}

// Admits reports whether a may act for this side: a holds it, or nobody does.
func (p *Player) Admits(a Actor) bool {
	return !p.Held() || p.HeldBy(a)
}

// PlayerConfig is a side's configuration in a request.
type PlayerConfig struct {
	Type       PlayerType `json:"type" validate:"required,oneof=1 2"`
	Level      int        `json:"level,omitempty" validate:"omitempty,min=0,max=20"`
	SearchTime int        `json:"searchTime,omitempty" validate:"omitempty,min=100,max=10000"` // Processor sets the min value
}

// PlayersResponse for API responses
type PlayersResponse struct {
	White *Player `json:"white"`
	Black *Player `json:"black"`
}

// NewPlayer creates a Player from PlayerConfig
func NewPlayer(config PlayerConfig, color Color) *Player {
	player := &Player{
		ID:    uuid.New().String(),
		Color: color,
		Type:  config.Type,
	}

	if config.Type == PlayerComputer {
		player.Level = config.Level
		player.SearchTime = config.SearchTime
	}

	return player
}

type Color byte

const (
	ColorWhite Color = iota + 1
	ColorBlack
)

func (c Color) String() string {
	if c == ColorWhite {
		return "w"
	} else if c == ColorBlack {
		return "b"
	} else {
		return "-"
	}
}

func OppositeColor(c Color) Color {
	if c == ColorWhite {
		return ColorBlack
	}
	return ColorWhite
}

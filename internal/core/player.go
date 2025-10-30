// FILE: internal/core/player.go
package core

import (
	"github.com/google/uuid"
)

type PlayerType int

const (
	PlayerHuman PlayerType = iota + 1
	PlayerComputer
)

// Player is the complete game entity with all state
type Player struct {
	ID         string     `json:"id"`
	Color      Color      `json:"color"`
	Type       PlayerType `json:"type"`
	Level      int        `json:"level,omitempty"`      // Only for computer
	SearchTime int        `json:"searchTime,omitempty"` // Only for computer
}

// PlayerConfig for API requests and configuration
type PlayerConfig struct {
	Type       PlayerType `json:"type" validate:"required,oneof=1 2"`
	Level      int        `json:"level,omitempty" validate:"omitempty,min=0,max=20"`
	SearchTime int        `json:"searchTime,omitempty" validate:"omitempty,min=100,max=10000"` // Processor sets the min value
}

// PlayersResponse for API responses - now contains full Player structs
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
	ColorWhite = iota + 1
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
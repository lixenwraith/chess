// FILE: lixenwraith/chess/internal/server/processor/command.go
package processor

import (
	"chess/internal/server/core"
)

// CommandType defines the type of command being executed
type CommandType int

const (
	CmdCreateGame CommandType = iota
	CmdConfigurePlayers
	CmdGetGame
	CmdDeleteGame
	CmdMakeMove
	CmdUndoMove
	CmdGetBoard
)

// Command is a unified structure for all processor operations
type Command struct {
	Type   CommandType
	UserID string
	GameID string // For game-specific commands
	Args   any    // Command-specific arguments
}

// ProcessorResponse wraps the response with metadata
type ProcessorResponse struct {
	Success bool                `json:"success"`
	Pending bool                `json:"pending,omitempty"` // For async operations
	Data    any                 `json:"data,omitempty"`
	Error   *core.ErrorResponse `json:"error,omitempty"`
}

func NewCreateGameCommand(req core.CreateGameRequest) Command {
	return Command{
		Type: CmdCreateGame,
		Args: req,
	}
}

func NewConfigurePlayersCommand(gameID string, req core.ConfigurePlayersRequest) Command {
	return Command{
		Type:   CmdConfigurePlayers,
		GameID: gameID,
		Args:   req,
	}
}

func NewGetGameCommand(gameID string) Command {
	return Command{
		Type:   CmdGetGame,
		GameID: gameID,
	}
}

func NewMakeMoveCommand(gameID string, req core.MoveRequest) Command {
	return Command{
		Type:   CmdMakeMove,
		GameID: gameID,
		Args:   req,
	}
}

func NewUndoMoveCommand(gameID string, req core.UndoRequest) Command {
	return Command{
		Type:   CmdUndoMove,
		GameID: gameID,
		Args:   req,
	}
}

func NewDeleteGameCommand(gameID string) Command {
	return Command{
		Type:   CmdDeleteGame,
		GameID: gameID,
	}
}

func NewGetBoardCommand(gameID string) Command {
	return Command{
		Type:   CmdGetBoard,
		GameID: gameID,
	}
}
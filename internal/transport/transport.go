// FILE: internal/transport/transport.go
package transport

import (
	"chess/internal/board"
	"chess/internal/core"
	"chess/internal/game"
)

// Handler processes user commands independent of transport medium
type Handler interface {
	HandleNewGame(id string, fen string, whiteType, blackType core.PlayerType) error
	HandleMove(gameID, move string) error
	HandleUndo(gameID string) error
	HandleGetBoard(gameID string) (*board.Board, error)
	HandleGetGame(gameID string) (*game.Game, error)
}

// View abstracts display/output operations
type View interface {
	DisplayBoard(b *board.Board)
	ShowMessage(msg string)
	ShowError(err error)
	ShowGameHistory(g *game.Game)
	ShowComputerMove(player core.Color, move string, depth, score int)
	ShowHumanMove(move string)
	ShowGameOver(state core.State)
	ShowPrompt(prompt string)
}

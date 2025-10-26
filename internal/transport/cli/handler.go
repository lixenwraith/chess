// FILE: internal/transport/cli/handler.go
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"chess/internal/cli"
	"chess/internal/core"
	"chess/internal/service"

	"github.com/google/uuid"
)

type CLIHandler struct {
	svc    *service.Service
	view   *cli.CLI
	gameID string
}

func New(svc *service.Service, view *cli.CLI) *CLIHandler {
	return &CLIHandler{
		svc:  svc,
		view: view,
	}
}

// Main game loop - simple command processing
func (h *CLIHandler) Run() {
	for {
		// Generate prompt based on current game state
		prompt := h.getPrompt()
		h.view.ShowPrompt(prompt)

		// Get command (blocking)
		cmd, err := h.view.GetCommand()
		if err != nil {
			break
		}

		// Process command - returns false to exit
		if !h.ProcessCommand(cmd) {
			break
		}
	}
}

// Generates the appropriate command prompt
func (h *CLIHandler) getPrompt() string {
	prompt := "> "
	if h.gameID != "" {
		g, err := h.svc.GetGame(h.gameID)
		if err == nil && g.State() == core.StateOngoing {
			// Always show whose turn it is
			prompt = fmt.Sprintf("[%c]> ", g.NextTurn())
			if g.NextPlayer().Type == core.PlayerComputer {
				prompt = "ENTER to execute computer move\n" + prompt
			}
		}
	}
	return prompt
}

// Handles user commands - returns false to exit
func (h *CLIHandler) ProcessCommand(cmd *cli.Command) bool {
	switch cmd.Type {
	case cli.CmdQuit:
		return false

	case cli.CmdNone:
		// Empty command triggers computer move if it's computer's turn
		if h.gameID != "" {
			g, err := h.svc.GetGame(h.gameID)
			if err == nil && g.State() == core.StateOngoing &&
				g.NextPlayer().Type == core.PlayerComputer {
				h.executeComputerMove()
			}
		}
		return true

	case cli.CmdNew:
		return h.handleNewGame("")

	case cli.CmdResume:
		if len(cmd.Args) < 1 {
			h.view.ShowMessage("Usage: resume <FEN string>")
			return true
		}
		fen := strings.Join(cmd.Args, " ")
		return h.handleNewGame(fen)

	case cli.CmdMove:
		if h.gameID == "" {
			h.view.ShowMessage("No active game. Use 'new' or 'resume <FEN>'.")
			return true
		}

		g, _ := h.svc.GetGame(h.gameID)
		if g.NextPlayer().Type != core.PlayerHuman {
			h.view.ShowMessage("It's not a human player's turn. Press ENTER to execute computer move.")
			return true
		}

		if err := h.svc.MakeHumanMove(h.gameID, cmd.Args[0]); err != nil {
			h.view.ShowError(fmt.Errorf("invalid move: %v", err))
			return true
		}

		// Get result and display human move
		g, _ = h.svc.GetGame(h.gameID)
		result := g.LastResult()
		if result != nil {
			h.view.ShowHumanMove(result.Move)
		}

		board, _ := h.svc.GetCurrentBoard(h.gameID)
		h.view.DisplayBoard(board)

		if result != nil && result.GameState != core.StateOngoing {
			h.view.ShowGameOver(result.GameState)
			h.gameID = ""
		}

	case cli.CmdUndo:
		if h.gameID == "" {
			h.view.ShowMessage("No active game.")
			return true
		}

		// Parse undo count
		count := 1
		if len(cmd.Args) > 0 {
			if n, err := strconv.Atoi(cmd.Args[0]); err == nil && n > 0 {
				count = n
			} else {
				h.view.ShowMessage("Invalid undo count. Usage: undo [count]")
				return true
			}
		}

		if err := h.svc.Undo(h.gameID, count); err != nil {
			h.view.ShowError(err)
		} else {
			if count == 1 {
				h.view.ShowMessage("Move undone")
			} else {
				h.view.ShowMessage(fmt.Sprintf("%d moves undone", count))
			}

			board, _ := h.svc.GetCurrentBoard(h.gameID)
			h.view.DisplayBoard(board)
		}

	case cli.CmdColor:
		if len(cmd.Args) < 1 {
			h.view.ShowMessage("Usage: color <off|brown|green|gray>")
			return true
		}

		theme := cli.ColorTheme(cmd.Args[0])
		if err := h.view.SetTheme(theme); err != nil {
			h.view.ShowError(err)
		} else {
			h.view.ShowMessage(fmt.Sprintf("Color theme set to: %s", theme))
			if h.gameID != "" {
				board, _ := h.svc.GetCurrentBoard(h.gameID)
				h.view.DisplayBoard(board)
			}
		}

	case cli.CmdVerbose:
		verbose := h.view.ToggleVerbose()
		h.view.ShowMessage(fmt.Sprintf("Verbose mode: %t", verbose))

	case cli.CmdHistory:
		if h.gameID == "" {
			h.view.ShowMessage("No active game.")
			return true
		}
		g, _ := h.svc.GetGame(h.gameID)
		h.view.ShowGameHistory(g)

	case cli.CmdHelp:
		h.view.ShowHelp()
	}

	return true
}

func (h *CLIHandler) executeComputerMove() {
	result, err := h.svc.MakeComputerMove(h.gameID)
	if err != nil {
		h.view.ShowError(fmt.Errorf("engine error: %v", err))
		h.gameID = ""
		return
	}

	h.view.ShowComputerMove(result)
	board, _ := h.svc.GetCurrentBoard(h.gameID)
	h.view.DisplayBoard(board)

	if result.GameState != core.StateOngoing {
		h.view.ShowGameOver(result.GameState)
		h.gameID = ""
	}
}

// Starts a new game with player type selection
func (h *CLIHandler) handleNewGame(fen string) bool {
	// Get player types
	h.view.ShowPrompt("Select White player (h/c): ")
	whiteInput := h.view.ReadLine()
	var whiteType core.PlayerType
	if whiteInput == "c" || whiteInput == "computer" {
		whiteType = core.PlayerComputer
	} else {
		whiteType = core.PlayerHuman
	}

	h.view.ShowPrompt("Select Black player (h/c): ")
	blackInput := h.view.ReadLine()
	var blackType core.PlayerType
	if blackInput == "c" || blackInput == "computer" {
		blackType = core.PlayerComputer
	} else {
		blackType = core.PlayerHuman
	}

	// Create new game
	h.gameID = uuid.New().String()
	var fenArray []string
	if fen != "" {
		fenArray = []string{fen}
	}

	if err := h.svc.NewGame(h.gameID, whiteType, blackType, fenArray...); err != nil {
		h.view.ShowError(fmt.Errorf("could not start the game: %v", err))
		h.gameID = ""
		return true
	}

	h.view.ShowMessage("Game started.")
	board, _ := h.svc.GetCurrentBoard(h.gameID)
	h.view.DisplayBoard(board)

	return true
}
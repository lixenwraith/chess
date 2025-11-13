// FILE: lixenwraith/chess/internal/client/command/game.go
package command

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"chess/internal/client/api"
	"chess/internal/client/display"
	"chess/internal/client/session"
)

func (r *Registry) registerGameCommands() {
	r.Register(&Command{
		Name:        "new",
		ShortName:   "n",
		Description: "Create a new game",
		Usage:       "new",
		Handler:     newGameHandler,
	})

	r.Register(&Command{
		Name:        "join",
		ShortName:   "j",
		Description: "Join/set current game ID",
		Usage:       "join <gameId>",
		Handler:     joinGameHandler,
	})

	r.Register(&Command{
		Name:        "move",
		ShortName:   "m",
		Description: "Make a move",
		Usage:       "move <uci-move>",
		Handler:     moveHandler,
	})

	r.Register(&Command{
		Name:        "computer",
		ShortName:   "c",
		Description: "Trigger computer move",
		Usage:       "computer",
		Handler:     computerMoveHandler,
	})

	r.Register(&Command{
		Name:        "undo",
		ShortName:   "u",
		Description: "Undo moves",
		Usage:       "undo [count]",
		Handler:     undoHandler,
	})

	r.Register(&Command{
		Name:        "show",
		ShortName:   "h",
		Description: "Show board and game state",
		Usage:       "show",
		Handler:     showBoardHandler,
	})

	r.Register(&Command{
		Name:        "state",
		ShortName:   "s",
		Description: "Show raw game JSON",
		Usage:       "state",
		Handler:     gameStateHandler,
	})

	r.Register(&Command{
		Name:        "delete",
		ShortName:   "d",
		Description: "Delete a game",
		Usage:       "delete [gameId]",
		Handler:     deleteGameHandler,
	})

	r.Register(&Command{
		Name:        "poll",
		ShortName:   "p",
		Description: "Long-poll for game updates",
		Usage:       "poll",
		Handler:     pollHandler,
	})
}

func newGameHandler(s *session.Session, args []string) error {
	scanner := bufio.NewScanner(os.Stdin)
	c := s.Client

	display.Println(display.Cyan, "\nCreating new game...")

	// White player
	display.Print(display.Yellow, "White player type (h/c) [h]: ")
	scanner.Scan()
	whiteType := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if whiteType == "" {
		whiteType = "h"
	}

	white := api.PlayerConfig{Type: 1}
	if whiteType == "c" {
		white.Type = 2

		display.Print(display.Yellow, "Computer level (0-20) [10]: ")
		scanner.Scan()
		levelStr := strings.TrimSpace(scanner.Text())
		if levelStr == "" {
			white.Level = 10
		} else {
			level, _ := strconv.Atoi(levelStr)
			white.Level = level
		}

		display.Print(display.Yellow, "Search time (100-10000ms) [1000]: ")
		scanner.Scan()
		timeStr := strings.TrimSpace(scanner.Text())
		if timeStr == "" {
			white.SearchTime = 1000
		} else {
			searchTime, _ := strconv.Atoi(timeStr)
			white.SearchTime = searchTime
		}
	}

	// Black player (same pattern)
	display.Print(display.Yellow, "Black player type (h/c) [h]: ")
	scanner.Scan()
	blackType := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if blackType == "" {
		blackType = "h"
	}

	black := api.PlayerConfig{Type: 1}
	if blackType == "c" {
		black.Type = 2

		display.Print(display.Yellow, "Computer level (0-20) [10]: ")
		scanner.Scan()
		levelStr := strings.TrimSpace(scanner.Text())
		if levelStr == "" {
			black.Level = 10
		} else {
			level, _ := strconv.Atoi(levelStr)
			black.Level = level
		}

		display.Print(display.Yellow, "Search time (100-10000ms) [1000]: ")
		scanner.Scan()
		timeStr := strings.TrimSpace(scanner.Text())
		if timeStr == "" {
			black.SearchTime = 1000
		} else {
			searchTime, _ := strconv.Atoi(timeStr)
			black.SearchTime = searchTime
		}
	}

	// Starting position
	display.Print(display.Yellow, "Starting position (FEN) [default]: ")
	scanner.Scan()
	fen := strings.TrimSpace(scanner.Text())

	req := &api.CreateGameRequest{
		White: white,
		Black: black,
		FEN:   fen,
	}

	resp, err := c.CreateGame(req)
	if err != nil {
		return err
	}

	s.CurrentGame = resp.GameID
	s.LastMoveCount = len(resp.Moves)
	s.CurrentGameState = resp

	// Determine player color if authenticated
	if s.CurrentUser != "" {
		if resp.Players.White.ID == s.CurrentUser {
			s.PlayerColor = "w"
		} else if resp.Players.Black.ID == s.CurrentUser {
			s.PlayerColor = "b"
		}
	}

	display.Println(display.Green, "Game created: %s", resp.GameID)
	display.Println(display.Cyan, "Current game set to: %s", resp.GameID)

	// If white is computer, inform user to trigger move
	if white.Type == 2 {
		display.Println(display.Magenta, "\nWhite is computer. Use 'computer' or 'c' to trigger first move.")
	}

	return nil
}

func joinGameHandler(s *session.Session, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: join <gameId>")
	}

	gameID := args[0]
	c := s.GetClient().(*api.Client)

	// Verify game exists
	resp, err := c.GetGame(gameID)
	if err != nil {
		return err
	}

	s.SetCurrentGame(gameID)
	s.SetLastMoveCount(len(resp.Moves))
	s.SetGameState(resp)

	// Determine player color if authenticated
	if s.GetCurrentUser() != "" {
		if resp.Players.White.ID == s.GetCurrentUser() {
			s.SetPlayerColor("w")
		} else if resp.Players.Black.ID == s.GetCurrentUser() {
			s.SetPlayerColor("b")
		} else {
			s.SetPlayerColor("")
		}
	}

	fmt.Printf("%sJoined game: %s%s\n", display.Green, gameID, display.Reset)
	fmt.Printf("Turn: %s | State: %s | Moves: %d\n", resp.Turn, resp.State, len(resp.Moves))

	return nil
}

func moveHandler(s *session.Session, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: move <uci-move>")
	}

	gameID := s.CurrentGame
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	move := args[0]
	c := s.Client

	resp, err := c.MakeMove(gameID, move)
	if err != nil {
		return err
	}

	s.LastMoveCount = len(resp.Moves)
	s.CurrentGameState = resp
	display.Println(display.Green, "Move accepted")

	// Check if game ended
	switch resp.State {
	case "checkmate":
		winner := "Black"
		if resp.Turn == "b" { // Turn switches after move, so if black's turn after checkmate, white won
			winner = "White"
		}
		display.Println(display.Green, "\nCHECKMATE! %s wins!", winner)
	case "stalemate":
		display.Println(display.Yellow, "\nSTALEMATE! Game drawn.")
	case "draw":
		display.Println(display.Yellow, "\nDRAW! Game drawn.")
	case "ongoing":
		// Check if computer needs to play
		currentTurn := resp.Turn
		var computerPlayer *api.PlayerInfo
		if currentTurn == "w" && resp.Players.White.Type == 2 {
			computerPlayer = &resp.Players.White
		} else if currentTurn == "b" && resp.Players.Black.Type == 2 {
			computerPlayer = &resp.Players.Black
		}

		if computerPlayer != nil {
			display.Println(display.Magenta, "\nComputer's turn. Use 'computer' or 'c' to trigger move.")
		}
	}

	return nil
}

func computerMoveHandler(s *session.Session, args []string) error {
	gameID := s.CurrentGame
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	c := s.Client

	resp, err := c.MakeMove(gameID, "cccc")
	if err != nil {
		return err
	}

	if resp.State == "pending" {
		display.Println(display.Magenta, "Computer is thinking...")

		// Poll for completion
		for i := 0; i < 50; i++ {
			time.Sleep(200 * time.Millisecond)
			resp2, err := c.GetGame(gameID)
			if err == nil && resp2.State != "pending" {
				s.LastMoveCount = len(resp2.Moves)
				s.CurrentGameState = resp2
				if resp2.LastMove != nil {
					display.Print(display.Magenta, "Computer played: %s", resp2.LastMove.Move)
					if resp2.LastMove.Depth > 0 {
						fmt.Printf(" (depth %d, score %d)", resp2.LastMove.Depth, resp2.LastMove.Score)
					}
					fmt.Println()
				}

				// Check if game ended after computer move
				switch resp2.State {
				case "checkmate":
					winner := "Black"
					if resp2.Turn == "b" {
						winner = "White"
					}
					display.Println(display.Green, "\nCHECKMATE! %s wins!", winner)
				case "stalemate":
					display.Println(display.Yellow, "\nSTALEMATE! Game drawn.")
				case "draw":
					display.Println(display.Yellow, "\nDRAW! Game drawn.")
				}

				return nil
			}
		}
		return fmt.Errorf("timeout waiting for computer move")
	}

	s.LastMoveCount = len(resp.Moves)
	s.CurrentGameState = resp
	display.Println(display.Green, "Move triggered")
	return nil
}

func undoHandler(s *session.Session, args []string) error {
	gameID := s.GetCurrentGame()
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	count := 1
	if len(args) > 0 {
		var err error
		count, err = strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid count: %s", args[0])
		}
	}

	c := s.GetClient().(*api.Client)
	resp, err := c.UndoMoves(gameID, count)
	if err != nil {
		return err
	}

	s.SetLastMoveCount(len(resp.Moves))
	s.SetGameState(resp)
	display.Println(display.Green, "Undid %d move(s)", count)
	return nil
}

func showBoardHandler(s *session.Session, args []string) error {
	gameID := s.GetCurrentGame()
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	c := s.GetClient().(*api.Client)

	// Get full game state
	game, err := c.GetGame(gameID)
	if err != nil {
		return err
	}

	// Get ASCII board
	board, err := c.GetBoard(gameID)
	if err != nil {
		return err
	}

	s.SetLastMoveCount(len(game.Moves))
	s.SetGameState(game)

	// Display board with colors
	fmt.Println()
	display.RenderBoard(board.Board)

	// Display game info
	fmt.Printf("\nFEN: %s\n", game.FEN)
	fmt.Printf("Turn: %s | State: %s | Moves: %d\n",
		display.ColorForTurn(game.Turn), game.State, len(game.Moves))

	// Display move history
	if len(game.Moves) > 0 {
		fmt.Printf("\nHistory: ")
		for i, move := range game.Moves {
			if i > 0 {
				fmt.Print(" ")
			}
			if i%2 == 0 {
				fmt.Printf("%d.%s", (i/2)+1, move)
			} else {
				fmt.Printf(" %s", move)
			}
		}
		fmt.Println()
	}

	// Display last move info
	if game.LastMove != nil {
		color := "White"
		if game.LastMove.PlayerColor == "b" {
			color = "Black"
		}
		fmt.Printf("Last move: %s by %s", game.LastMove.Move, color)
		if game.LastMove.Depth > 0 {
			fmt.Printf(" (depth %d, score %d)", game.LastMove.Depth, game.LastMove.Score)
		}
		fmt.Println()
	}

	return nil
}

func gameStateHandler(s *session.Session, args []string) error {
	gameID := s.GetCurrentGame()
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	c := s.GetClient().(*api.Client)
	resp, err := c.GetGame(gameID)
	if err != nil {
		return err
	}

	s.SetLastMoveCount(len(resp.Moves))

	// Pretty print JSON
	display.Println(display.Cyan, "Game State:")
	display.PrettyPrintJSON(resp)

	return nil
}

func deleteGameHandler(s *session.Session, args []string) error {
	gameID := s.GetCurrentGame()
	if len(args) > 0 {
		gameID = args[0]
	}

	if gameID == "" {
		return fmt.Errorf("specify game ID or set current game")
	}

	c := s.GetClient().(*api.Client)
	err := c.DeleteGame(gameID)
	if err != nil {
		return err
	}

	if gameID == s.GetCurrentGame() {
		s.SetCurrentGame("")
		s.SetLastMoveCount(0)
	}

	fmt.Printf("%sGame deleted: %s%s\n", display.Green, gameID, display.Reset)
	return nil
}

func pollHandler(s *session.Session, args []string) error {
	gameID := s.GetCurrentGame()
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	c := s.GetClient().(*api.Client)
	moveCount := s.GetLastMoveCount()

	display.Println(display.Cyan, "Long-polling for updates (move count: %d)...", moveCount)
	display.Println(display.Cyan, "This may take up to 25 seconds")

	resp, err := c.GetGameWithPoll(gameID, moveCount)
	if err != nil {
		return err
	}

	s.SetLastMoveCount(len(resp.Moves))
	s.SetGameState(resp)

	if len(resp.Moves) > moveCount {
		display.Println(display.Green, "Game updated! New moves detected")
		if resp.LastMove != nil {
			fmt.Printf("Last move: %s\n", resp.LastMove.Move)
		}
	} else {
		display.Println(display.Yellow, "No updates (timeout)")
	}

	return nil
}
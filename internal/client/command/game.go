package command

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

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
		Usage:       "move <uci-move>  (e2e4; castling e1g1; promotion e7e8q, e7e8n)",
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
		Description: "Unload a live game (history retained)",
		Usage:       "delete [gameId]",
		Handler:     deleteGameHandler,
	})

	r.Register(&Command{
		Name:        "games",
		ShortName:   "g",
		Description: "List your stored games, newest first (login required)",
		Usage:       "games [more]",
		Handler:     gamesHandler,
	})

	r.Register(&Command{
		Name:        "pgn",
		ShortName:   "",
		Description: "Print a stored game as PGN",
		Usage:       "pgn [gameId] [ply]  (default: current game, all plies)",
		Handler:     pgnHandler,
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

// moveHandler submits a human move. Terminal/error outcomes are reported via
// the shared printOutcome (no-op on "ongoing"/"pending"); the computer-turn
// hint stays local since it only applies after a successful human move.
func moveHandler(s *session.Session, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: move <uci-move> (promotion appends q, r, b, or n: e7e8q)")
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

	printOutcome(resp)

	// Hint to trigger the computer if the game continues on a computer's turn
	if resp.State == "ongoing" {
		isComputerTurn := (resp.Turn == "w" && resp.Players.White.Type == 2) ||
			(resp.Turn == "b" && resp.Players.Black.Type == 2)
		if isComputerTurn {
			display.Println(display.Magenta, "\nComputer's turn. Use 'computer' or 'c' to trigger move.")
		}
	}

	return nil
}

// computerMoveHandler triggers a computer move and waits for the result via
// the server's long-poll. With the state-aware waiter, a single poll wakes on
// either the applied move (move count delta) or a state-only settle
// (mate-without-move, stuck) — no fixed-interval GET hammering, no hard cap
// below the server's max searchTime. Polls loop only if the wake races the
// pending window (e.g. queue wait), each round costing at most WaitTimeout.
func computerMoveHandler(s *session.Session, args []string) error {
	gameID := s.CurrentGame
	if gameID == "" {
		return fmt.Errorf("no current game, use 'new' or 'join <gameId>'")
	}

	c := s.Client

	// Baseline BEFORE triggering: the long-poll returns immediately if the
	// move count already differs from this value.
	baselineMoves := s.LastMoveCount
	if s.CurrentGameState != nil {
		baselineMoves = len(s.CurrentGameState.Moves)
	}

	resp, err := c.MakeMove(gameID, "cccc")
	if err != nil {
		return err
	}

	if resp.State != "pending" {
		// Server resolved synchronously (shouldn't normally happen)
		s.LastMoveCount = len(resp.Moves)
		s.CurrentGameState = resp
		display.Println(display.Green, "Move triggered")
		printOutcome(resp)
		return nil
	}

	display.Println(display.Magenta, "Computer is thinking...")

	// Up to 3 long-poll rounds (~90s ceiling) covers max searchTime (10s)
	// plus pathological queue wait, without hanging indefinitely.
	const maxPolls = 3
	var final *api.GameResponse
	for i := 0; i < maxPolls; i++ {
		polled, err := c.GetGameWithPoll(gameID, baselineMoves)
		if err != nil {
			return err
		}
		if polled.State != "pending" {
			final = polled
			break
		}
		// Woke on timeout while still pending; poll again.
	}
	if final == nil {
		return fmt.Errorf("computer move still pending after %d poll rounds", maxPolls)
	}

	s.LastMoveCount = len(final.Moves)
	s.CurrentGameState = final

	// A move may legitimately be absent: mate-without-move detection or a
	// stuck transition settle the state without applying anything.
	if final.LastMove != nil && len(final.Moves) > baselineMoves {
		display.Print(display.Magenta, "Computer played: %s", final.LastMove.Move)
		if final.LastMove.Depth > 0 {
			fmt.Printf(" (depth %d, score %d)", final.LastMove.Depth, final.LastMove.Score)
		}
		fmt.Println()
	}

	printOutcome(final)
	return nil
}

// printOutcome reports terminal or error states using the server's actual
// State.String() values ("white wins"/"black wins", not "checkmate").
func printOutcome(resp *api.GameResponse) {
	switch resp.State {
	case "white wins":
		display.Println(display.Green, "\nCHECKMATE! White wins!")
	case "black wins":
		display.Println(display.Green, "\nCHECKMATE! Black wins!")
	case "stalemate":
		display.Println(display.Yellow, "\nSTALEMATE! Game drawn.")
	case "draw":
		display.Println(display.Yellow, "\nDRAW! Game drawn.")
	case "stuck":
		display.Println(display.Yellow, "\nEngine error — 'undo' to recover, or 'new'/'delete'.")
	}
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

	fmt.Printf("%sLive game unloaded (history retained): %s%s\n", display.Green, gameID, display.Reset)
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
	display.Println(display.Cyan, "This may take up to 30 seconds")

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

// gamesHandler lists the stored games of the signed-in user, 20 at a time;
// "games more" continues from the previous page.
func gamesHandler(s *session.Session, args []string) error {
	if s.AuthToken == "" {
		return fmt.Errorf("login required")
	}
	cursor := ""
	if len(args) > 0 && args[0] == "more" {
		if s.GamesCursor == "" {
			return fmt.Errorf("no further games; run 'games' to list from the newest")
		}
		cursor = s.GamesCursor
	}

	resp, err := s.Client.GetMyGames(20, cursor)
	if err != nil {
		return err
	}
	s.GamesCursor = resp.NextCursor
	if len(resp.Games) == 0 {
		display.Println(display.Yellow, "No stored games")
		return nil
	}

	fmt.Printf("\n%-36s  %-20s  %-20s  %-7s  %5s  %s\n", "Game ID", "White", "Black", "Result", "Plies", "Started (UTC)")
	for _, g := range resp.Games {
		fmt.Printf("%-36s  %-20s  %-20s  %-7s  %5d  %s\n", g.GameID,
			playerLabel(g.Players.White), playerLabel(g.Players.Black), g.PGNResult, g.MoveCount,
			g.StartTimeUTC.UTC().Format("2006-01-02 15:04"))
	}
	if resp.NextCursor != "" {
		display.Println(display.Cyan, "\nMore games: games more")
	}
	return nil
}

func playerLabel(p api.PlayerInfo) string {
	switch {
	case p.Type == 2:
		return fmt.Sprintf("Stockfish L%d", p.Level)
	case p.Name != "":
		return p.Name
	}
	return "anonymous"
}

// pgnHandler prints the stored game as PGN: the current game unless an ID is
// given, and every ply unless a count is given.
func pgnHandler(s *session.Session, args []string) error {
	gameID, ply := s.CurrentGame, -1
	for _, arg := range args {
		if n, err := strconv.Atoi(arg); err == nil && n >= 0 {
			ply = n
		} else {
			gameID = arg
		}
	}
	if gameID == "" {
		return fmt.Errorf("no current game, use 'join <gameId>' or 'pgn <gameId>'")
	}
	text, err := s.Client.GetGamePGN(gameID, ply)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(text)
	return nil
}

package command

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lixenwraith/chess/internal/client/display"
	"github.com/lixenwraith/chess/internal/client/replay"
	"github.com/lixenwraith/chess/internal/client/session"
)

// Replay commands step through a stored game from its history. The replay
// state lives beside the live session, which keeps its game, user and poll
// position; live commands that would change the game are refused until
// 'live' ends the replay (see liveOnly).
func (r *Registry) registerReplayCommands() {
	r.Register(&Command{
		Name:        "replay",
		ShortName:   "",
		Description: "Step through a stored game",
		Usage:       "replay <gameId|index>  (index from the last 'games' listing)",
		Handler:     replayHandler,
	})
	r.Register(&Command{
		Name:        "next",
		ShortName:   ">",
		Description: "Replay: next ply",
		Usage:       "next [count]",
		Handler:     stepHandler(1),
	})
	r.Register(&Command{
		Name:        "prev",
		ShortName:   "<",
		Description: "Replay: previous ply",
		Usage:       "prev [count]",
		Handler:     stepHandler(-1),
	})
	r.Register(&Command{
		Name:        "first",
		ShortName:   "",
		Description: "Replay: initial position",
		Usage:       "first",
		Handler: func(s *session.Session, args []string) error {
			return gotoHandler(s, []string{"0"})
		},
	})
	r.Register(&Command{
		Name:        "last",
		ShortName:   "",
		Description: "Replay: final position",
		Usage:       "last",
		Handler: func(s *session.Session, args []string) error {
			if s.Replay == nil {
				return errNotReplaying
			}
			return gotoHandler(s, []string{strconv.Itoa(s.Replay.Len())})
		},
	})
	r.Register(&Command{
		Name:        "goto",
		ShortName:   "",
		Description: "Replay: show the position after N plies",
		Usage:       "goto <ply>",
		Handler:     gotoHandler,
	})
	r.Register(&Command{
		Name:        "auto",
		ShortName:   "",
		Description: "Replay: play forward to the end",
		Usage:       "auto [ms]  (delay per ply, default 1000)",
		Handler:     autoHandler,
	})
	r.Register(&Command{
		Name:        "fen",
		ShortName:   "",
		Description: "Replay: print the FEN of the current position",
		Usage:       "fen",
		Handler:     fenHandler,
	})
	r.Register(&Command{
		Name:        "live",
		ShortName:   "",
		Description: "Leave the replay and return to the live session",
		Usage:       "live",
		Handler:     liveHandler,
	})
}

var errNotReplaying = fmt.Errorf("no replay in progress; use 'replay <gameId|index>'")

// liveOnly names the commands a replay refuses: they would change the live
// game or poll it, while a replay only reads a stored one.
var liveOnly = map[string]bool{
	"new": true, "join": true, "move": true, "computer": true, "undo": true,
	"delete": true, "poll": true, "resign": true, "draw": true,
}

// replayHandler loads a stored game by ID, or by its position in the last
// 'games' listing, and shows its initial position.
func replayHandler(s *session.Session, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: replay <gameId|index>")
	}
	gameID := args[0]
	if index, err := strconv.Atoi(gameID); err == nil {
		if index < 1 || index > len(s.GamesPage) {
			if len(s.GamesPage) == 0 {
				return fmt.Errorf("no listing to index; run 'games' first or give a game ID")
			}
			return fmt.Errorf("index %d is outside the last listing (1-%d)", index, len(s.GamesPage))
		}
		gameID = s.GamesPage[index-1].GameID
	}

	history, err := s.Client.GetGameHistory(gameID)
	if err != nil {
		return err
	}
	s.Replay = replay.New(history)

	white, black := s.Replay.Players()
	display.Println(display.Cyan, "\nReplaying %s", history.GameID)
	fmt.Printf("%s vs %s  ·  %s  ·  %d plies  ·  started %s\n",
		display.C(display.Blue, white), display.C(display.Red, black), s.Replay.Result(), s.Replay.Len(),
		history.StartTimeUTC.UTC().Format("2006-01-02 15:04"))
	if c := history.Concession; c != nil {
		what := "a draw was agreed"
		switch c.Result {
		case "white_wins":
			what = "Black resigned"
		case "black_wins":
			what = "White resigned"
		}
		display.Println(display.Yellow, "%s at ply %d; play continued.", what, c.Ply)
	}
	fmt.Println()
	fmt.Println(s.Replay.Movetext(80))
	display.Println(display.Reset, "\nnext, prev, first, last, goto <ply>, auto [ms], fen, pgn [save <path>], live")
	return showReplayPosition(s)
}

func stepHandler(direction int) func(*session.Session, []string) error {
	return func(s *session.Session, args []string) error {
		if s.Replay == nil {
			return errNotReplaying
		}
		count := 1
		if len(args) > 0 {
			n, err := strconv.Atoi(args[0])
			if err != nil || n < 1 {
				return fmt.Errorf("invalid count: %s", args[0])
			}
			count = n
		}
		before := s.Replay.Ply
		s.Replay.Goto(before + direction*count)
		if s.Replay.Ply == before {
			if direction > 0 {
				display.Println(display.Yellow, "At the end of the game")
			} else {
				display.Println(display.Yellow, "At the initial position")
			}
		}
		return showReplayPosition(s)
	}
}

func gotoHandler(s *session.Session, args []string) error {
	if s.Replay == nil {
		return errNotReplaying
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: goto <ply> (0-%d)", s.Replay.Len())
	}
	ply, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid ply: %s", args[0])
	}
	if ply < 0 || ply > s.Replay.Len() {
		return fmt.Errorf("ply %d is outside 0-%d", ply, s.Replay.Len())
	}
	s.Replay.Goto(ply)
	return showReplayPosition(s)
}

// autoHandler plays forward from the current ply to the end, one board per
// ply, pausing between them.
func autoHandler(s *session.Session, args []string) error {
	if s.Replay == nil {
		return errNotReplaying
	}
	delay := 1000
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 || n > 60000 {
			return fmt.Errorf("delay must be 0-60000 ms")
		}
		delay = n
	}
	if s.Replay.AtEnd() {
		display.Println(display.Yellow, "At the end of the game; 'first' to start over")
		return nil
	}
	for !s.Replay.AtEnd() {
		time.Sleep(time.Duration(delay) * time.Millisecond)
		s.Replay.Next()
		if err := showReplayPosition(s); err != nil {
			return err
		}
	}
	return nil
}

func fenHandler(s *session.Session, args []string) error {
	if s.Replay == nil {
		return errNotReplaying
	}
	fmt.Println(s.Replay.FEN())
	return nil
}

func liveHandler(s *session.Session, args []string) error {
	if s.Replay == nil {
		display.Println(display.Yellow, "Not replaying")
		return nil
	}
	s.Replay = nil
	display.Println(display.Green, "Back to the live session")
	if s.CurrentGame != "" {
		display.Println(display.Cyan, "Current game: %s", s.CurrentGame)
	}
	return nil
}

// showReplayPosition prints the board at the current ply with its label.
func showReplayPosition(s *session.Session) error {
	r := s.Replay
	board, err := display.BoardFromFEN(r.FEN())
	if err != nil {
		return fmt.Errorf("stored position cannot be shown: %w", err)
	}
	fmt.Println()
	display.RenderBoard(board)
	label := r.Label(r.Ply)
	if r.Ply == 0 {
		label = "initial position"
	}
	fmt.Printf("\nPly %d/%d  %s\n", r.Ply, r.Len(), display.C(display.Cyan, label))
	if r.AtEnd() && r.History.Result != "" {
		display.Println(display.Green, "Result: %s", r.Result())
	}
	return nil
}

// replayPrompt is the prompt segment while replaying.
func ReplayPrompt(s *session.Session) string {
	if s.Replay == nil {
		return ""
	}
	return fmt.Sprintf(" replay %d/%d", s.Replay.Ply, s.Replay.Len())
}

// pgnSaveArgs splits "pgn save <path> [gameId] [ply]" style arguments:
// a "save" followed by a path, and the remaining arguments.
func pgnSaveArgs(args []string) (path string, rest []string, err error) {
	for i, arg := range args {
		if arg == "save" {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("usage: pgn save <path>")
			}
			rest = append(append([]string{}, args[:i]...), args[i+2:]...)
			return args[i+1], rest, nil
		}
	}
	return "", args, nil
}

func describePGNTarget(gameID string, ply int) string {
	if ply >= 0 {
		return fmt.Sprintf("%s (first %d plies)", gameID, ply)
	}
	return gameID
}

func trimPGN(text string) string {
	return strings.TrimRight(text, "\n") + "\n"
}

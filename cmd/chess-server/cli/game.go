package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"chess/internal/server/chess"
	"chess/internal/server/service"
	"chess/internal/server/storage"
)

// runPGN prints a stored game as PGN. The game may be named by the 8-digit
// prefix that `db query` prints.
func runPGN(args []string) error {
	fs, dsn := newFlagSet("pgn")
	gameID := fs.String("gameId", "", "Game ID or unique prefix of at least 8 hex digits (required)")
	ply := fs.Int("ply", -1, "Export only the first N plies (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *gameID == "" {
		return errors.New("-gameId is required")
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	id, err := resolveGameID(store, *gameID)
	if err != nil {
		return err
	}
	record, moves, err := store.GetGameHistory(id)
	if err != nil {
		return fmt.Errorf("load game %s: %w", id, err)
	}
	pgn, err := service.BuildPGN(record, moves, *ply)
	if err != nil {
		return err
	}
	fmt.Print(pgn.Text)
	return nil
}

func resolveGameID(store *storage.Store, prefix string) (string, error) {
	id, err := store.ResolveGameID(prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no game matches %q", prefix)
	}
	return id, err
}

// runVerify replays stored games with the rules core. Each stored move must
// be legal from the stored position before it and produce the stored
// position after it, and a stored result must match the final position.
func runVerify(args []string) error {
	fs, dsn := newFlagSet("verify")
	gameID := fs.String("gameId", "", "Verify one game (ID or unique prefix); default all")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	var ids []string
	if *gameID != "" {
		id, err := resolveGameID(store, *gameID)
		if err != nil {
			return err
		}
		ids = []string{id}
	} else {
		games, err := store.QueryGames("", "")
		if err != nil {
			return fmt.Errorf("list games: %w", err)
		}
		for _, g := range games {
			ids = append(ids, g.GameID)
		}
	}

	plies, problems := 0, 0
	for _, id := range ids {
		record, moves, err := store.GetGameHistory(id)
		if err != nil {
			return fmt.Errorf("load game %s: %w", id, err)
		}
		plies += len(moves)
		for _, problem := range verifyGame(record, moves) {
			fmt.Fprintf(os.Stdout, "%s: %s\n", id, problem)
			problems++
		}
	}
	fmt.Printf("Verified %d game(s), %d plies: %d problem(s)\n", len(ids), plies, problems)
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	return nil
}

func verifyGame(record *storage.GameRecord, moves []storage.MoveRecord) []string {
	var problems []string
	pos, err := chess.ParseFEN(record.InitialFEN)
	if err != nil {
		return []string{fmt.Sprintf("initial position: %v", err)}
	}
	for _, move := range moves {
		m, err := pos.ParseUCI(move.MoveUCI)
		if err != nil {
			problems = append(problems, fmt.Sprintf("ply %d %s: %v", move.MoveNumber, move.MoveUCI, err))
		} else if want := pos.Play(m).FEN(); !sameFEN(want, move.FENAfterMove) {
			problems = append(problems, fmt.Sprintf("ply %d %s: stored %q, rules give %q",
				move.MoveNumber, move.MoveUCI, move.FENAfterMove, want))
		}
		// Continue from the stored position so one fault is reported once.
		if pos, err = chess.ParseFEN(move.FENAfterMove); err != nil {
			return append(problems, fmt.Sprintf("ply %d stored position: %v", move.MoveNumber, err))
		}
	}

	final := pos
	mated := !final.HasLegalMoves() && final.InCheck()
	stalemated := !final.HasLegalMoves() && !final.InCheck()
	var want string
	switch {
	case mated && final.Turn() == chess.White:
		want = "black_wins"
	case mated:
		want = "white_wins"
	case stalemated:
		want = "stalemate"
	}
	if record.Result != want && !(record.Result == "draw" && want == "") {
		problems = append(problems, fmt.Sprintf("stored result %q, final position gives %q", record.Result, want))
	}
	return problems
}

func sameFEN(a, b string) bool {
	na, errA := chess.NormalizeFEN(a)
	nb, errB := chess.NormalizeFEN(b)
	return errA == nil && errB == nil && na == nb
}

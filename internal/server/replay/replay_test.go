package replay

import (
	"strings"
	"testing"
	"time"

	"chess/internal/server/chess"
	"chess/internal/server/storage"
)

// stored returns the stored form of a line played from start.
func stored(t *testing.T, start string, uci ...string) []storage.MoveRecord {
	t.Helper()
	line, err := chess.Replay(start, uci)
	if err != nil {
		t.Fatal(err)
	}
	moves := make([]storage.MoveRecord, len(uci))
	for i, u := range uci {
		moves[i] = storage.MoveRecord{
			MoveNumber: i + 1, MoveUCI: u, FENAfterMove: line.Position(i + 1).FEN(),
			PlayerColor: line.Position(i).Turn().String(),
		}
	}
	return moves
}

func record(start, result, termination string) *storage.GameRecord {
	return &storage.GameRecord{
		GameID: "68007fd1-8970-4d84-950d-2f6ec6375c0b", InitialFEN: start,
		Result: result, Termination: termination,
		StartTimeUTC: time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC),
	}
}

func TestVerifyConsistentGames(t *testing.T) {
	cycle := []string{"g1f3", "g8f6", "f3g1", "f6g8"}
	for name, tc := range map[string]struct {
		start, result, termination string
		uci                        []string
	}{
		"checkmate":   {chess.StartFEN, "black_wins", "checkmate", []string{"f2f3", "e7e5", "g2g4", "d8h4"}},
		"resignation": {chess.StartFEN, "white_wins", "resignation", []string{"e2e4"}},
		"agreement":   {chess.StartFEN, "draw", "agreement", []string{"e2e4", "e7e5"}},
		"stalemate":   {"7k/5Q2/5K2/8/8/8/8/8 w - - 0 1", "stalemate", "stalemate", []string{"f6g6"}},
		"dead":        {"4k3/8/8/8/8/8/3r4/4KB2 w - - 0 1", "draw", "insufficient_material", []string{"e1d2"}},
		"threefold":   {chess.StartFEN, "draw", "threefold_repetition", append(cycle, cycle...)},
		"fifty":       {"4k3/8/8/8/8/8/8/R3K3 w - - 99 80", "draw", "fifty_move_rule", []string{"a1a2"}},
		"ongoing":     {chess.StartFEN, "", "", []string{"e2e4"}},
		// A dead position left unfinished predates automatic draws.
		"legacy dead": {"4k3/8/8/8/8/8/8/4K3 w - - 0 1", "", "", nil},
	} {
		problems := Verify(record(tc.start, tc.result, tc.termination), stored(t, tc.start, tc.uci...))
		if len(problems) != 0 {
			t.Errorf("%s: problems %v", name, problems)
		}
	}
}

func TestVerifyFindsProblems(t *testing.T) {
	mate := stored(t, chess.StartFEN, "f2f3", "e7e5", "g2g4", "d8h4")
	gap := append(append([]storage.MoveRecord{}, mate[:1]...), mate[2:]...)
	corrupt := stored(t, chess.StartFEN, "e2e4", "e7e5")
	corrupt[1].FENAfterMove = chess.StartFEN
	illegal := stored(t, chess.StartFEN, "e2e4")
	illegal[0].MoveUCI = "e2e5"
	wrongSide := stored(t, chess.StartFEN, "e2e4")
	wrongSide[0].PlayerColor = "b"

	for name, tc := range map[string]struct {
		record *storage.GameRecord
		moves  []storage.MoveRecord
		want   string
	}{
		"deleted ply":       {record(chess.StartFEN, "black_wins", "checkmate"), gap, "ply 2 missing (next stored ply is 3)"},
		"wrong position":    {record(chess.StartFEN, "", ""), corrupt, "ply 2 e7e5: stored"},
		"illegal move":      {record(chess.StartFEN, "", ""), illegal, "ply 1 e2e5: illegal move"},
		"wrong side":        {record(chess.StartFEN, "", ""), wrongSide, `stored as played by "b"`},
		"wrong winner":      {record(chess.StartFEN, "white_wins", "checkmate"), mate, "does not hold"},
		"missing result":    {record(chess.StartFEN, "", ""), mate, "checkmate but no result"},
		"mismatched pair":   {record(chess.StartFEN, "draw", "checkmate"), mate, "cannot come from"},
		"orphan term":       {record(chess.StartFEN, "", "agreement"), mate[:1], "without a result"},
		"false repetition":  {record(chess.StartFEN, "draw", "threefold_repetition"), mate[:2], "does not hold"},
		"false dead":        {record(chess.StartFEN, "draw", "insufficient_material"), mate[:2], "does not hold"},
		"false fifty":       {record(chess.StartFEN, "draw", "fifty_move_rule"), mate[:2], "does not hold"},
		"bad initial":       {record("8/8/8/8/8/8/8/8 w - - 0 1", "", ""), nil, "initial position"},
		"premature mate":    {record(chess.StartFEN, "black_wins", "checkmate"), mate[:3], "does not hold"},
		"unknown result":    {record(chess.StartFEN, "abandoned", "resignation"), mate[:1], "cannot come from"},
		"stalemate as mate": {record("7k/5Q2/5K2/8/8/8/8/8 w - - 0 1", "white_wins", "checkmate"), stored(t, "7k/5Q2/5K2/8/8/8/8/8 w - - 0 1", "f6g6"), "does not hold"},
	} {
		problems := Verify(tc.record, tc.moves)
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), tc.want) {
			t.Errorf("%s: problems = %v, want %q", name, problems, tc.want)
		}
	}
}

func TestVerifyAcceptsEnginePassantConvention(t *testing.T) {
	// Stockfish prints an en-passant square whenever a pawn could capture,
	// even when the capture is illegal; the rules core prints it only when
	// legal. Both describe the same position.
	moves := stored(t, chess.StartFEN, "e2e4", "d7d5", "e4e5", "f7f5")
	moves[0].FENAfterMove = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	if problems := Verify(record(chess.StartFEN, "", ""), moves); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
}

func TestDescribe(t *testing.T) {
	for _, tc := range []struct{ result, termination, want string }{
		{"white_wins", "checkmate", "White wins by checkmate."},
		{"black_wins", "resignation", "Black wins by resignation."},
		{"stalemate", "stalemate", "Draw by stalemate."},
		{"draw", "agreement", "Draw by agreement."},
		{"draw", "threefold_repetition", "Draw by threefold repetition."},
		{"draw", "fifty_move_rule", "Draw by the fifty-move rule."},
		{"draw", "insufficient_material", "Draw by insufficient material."},
		{"", "", ""},
	} {
		if got := Describe(tc.result, tc.termination); got != tc.want {
			t.Errorf("Describe(%q, %q) = %q, want %q", tc.result, tc.termination, got, tc.want)
		}
	}
}

func TestBuildPGN(t *testing.T) {
	moves := stored(t, chess.StartFEN, "e2e4", "e7e5")
	rec := record(chess.StartFEN, "white_wins", "resignation")
	rec.BlackType, rec.BlackLevel, rec.WhiteType, rec.WhiteName = 2, 4, 1, "alice"

	pgn, err := BuildPGN(rec, moves, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[White \"alice\"]\n", "[Black \"Stockfish level 4\"]\n", "[Result \"1-0\"]\n",
		"[Termination \"normal\"]\n", "\n\n1. e4 e5 { White wins by resignation. } 1-0\n"} {
		if !strings.Contains(pgn.Text, want) {
			t.Errorf("PGN lacks %q:\n%s", want, pgn.Text)
		}
	}
	if pgn.Filename != "chess-20260928-68007fd1.pgn" {
		t.Errorf("filename %q", pgn.Filename)
	}

	partial, err := BuildPGN(rec, moves, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(partial.Text, "\n\n1. e4 *\n") || partial.Filename != "chess-20260928-68007fd1-ply1.pgn" {
		t.Errorf("partial %q:\n%s", partial.Filename, partial.Text)
	}
	if _, err := BuildPGN(rec, moves, 3); err == nil {
		t.Error("ply beyond the line accepted")
	}
}

// A resignation taken back by an undo is noted before the live result.
func TestBuildPGNNotesContinuedConcession(t *testing.T) {
	moves := stored(t, chess.StartFEN, "e2e4", "e7e5", "d2d4")
	rec := record(chess.StartFEN, "", "")
	rec.ConcessionResult, rec.ConcessionTermination, rec.ConcessionPly = "black_wins", "resignation", 2

	pgn, err := BuildPGN(rec, moves, -1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(pgn.Text, "\n\n1. e4 e5 2. d4 { White resigned at ply 2; play continued. } *\n") {
		t.Errorf("continued game:\n%s", pgn.Text)
	}

	// The concession that is still the result needs no note.
	rec.Result, rec.Termination, rec.ConcessionPly = "black_wins", "resignation", 3
	if pgn, _ = BuildPGN(rec, moves, -1); strings.Contains(pgn.Text, "continued") {
		t.Errorf("standing resignation noted as continued:\n%s", pgn.Text)
	}
}

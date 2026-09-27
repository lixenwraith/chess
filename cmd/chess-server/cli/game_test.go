package cli

import (
	"strings"
	"testing"

	"chess/internal/server/chess"
	"chess/internal/server/storage"
)

func storedLine(t *testing.T, uci ...string) []storage.MoveRecord {
	t.Helper()
	line, err := chess.Replay(chess.StartFEN, uci)
	if err != nil {
		t.Fatal(err)
	}
	moves := make([]storage.MoveRecord, len(uci))
	for i, u := range uci {
		moves[i] = storage.MoveRecord{MoveNumber: i + 1, MoveUCI: u, FENAfterMove: line.Position(i + 1).FEN()}
	}
	return moves
}

func TestVerifyGame(t *testing.T) {
	mate := storedLine(t, "f2f3", "e7e5", "g2g4", "d8h4")
	record := &storage.GameRecord{InitialFEN: chess.StartFEN, Result: "black_wins"}
	if problems := verifyGame(record, mate); len(problems) != 0 {
		t.Fatalf("consistent game reported %v", problems)
	}

	// A Stockfish-style FEN with a pseudo-legal en-passant square is equal.
	ep := storedLine(t, "e2e4", "d7d5", "e4e5", "f7f5")
	ep[3].FENAfterMove = "rnbqkbnr/ppp1p1pp/8/3pPp2/8/8/PPPP1PPP/RNBQKBNR w KQkq f6 0 3"
	ep[0].FENAfterMove = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	if problems := verifyGame(&storage.GameRecord{InitialFEN: chess.StartFEN}, ep); len(problems) != 0 {
		t.Fatalf("en-passant convention reported %v", problems)
	}

	wrongResult := &storage.GameRecord{InitialFEN: chess.StartFEN, Result: "white_wins"}
	missingResult := &storage.GameRecord{InitialFEN: chess.StartFEN}
	corrupt := storedLine(t, "e2e4", "e7e5")
	corrupt[1].FENAfterMove = chess.StartFEN
	illegal := storedLine(t, "e2e4")
	illegal[0].MoveUCI = "e2e5"

	for name, tc := range map[string]struct {
		record *storage.GameRecord
		moves  []storage.MoveRecord
		want   string
	}{
		"wrong result":   {wrongResult, mate, `stored result "white_wins", final position gives "black_wins"`},
		"missing result": {missingResult, mate, `stored result "", final position gives "black_wins"`},
		"wrong position": {missingResult, corrupt, "ply 2 e7e5: stored"},
		"illegal move":   {missingResult, illegal, "ply 1 e2e5: illegal move"},
	} {
		problems := verifyGame(tc.record, tc.moves)
		if len(problems) == 0 || !strings.Contains(problems[0], tc.want) {
			t.Errorf("%s: problems = %v, want %q", name, problems, tc.want)
		}
	}
}

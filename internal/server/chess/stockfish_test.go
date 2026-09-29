package chess

import (
	"bufio"
	"io"
	"math/rand/v2"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// stockfish drives a Stockfish process for cross-checks.
type stockfish struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Scanner
}

func startStockfish(t *testing.T) *stockfish {
	t.Helper()
	path, err := exec.LookPath("stockfish")
	if err != nil {
		if _, statErr := exec.LookPath("/usr/games/stockfish"); statErr != nil {
			t.Skip("stockfish not found; skipping engine cross-check")
		}
		path = "/usr/games/stockfish"
	}
	cmd := exec.Command(path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sf := &stockfish{cmd: cmd, in: in, out: bufio.NewScanner(outPipe)}
	t.Cleanup(func() {
		io.WriteString(in, "quit\n")
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
	})
	sf.send(t, "uci")
	sf.until(t, "uciok", nil)
	return sf
}

func (sf *stockfish) send(t *testing.T, cmd string) {
	t.Helper()
	if _, err := io.WriteString(sf.in, cmd+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (sf *stockfish) until(t *testing.T, prefix string, visit func(string)) string {
	t.Helper()
	for sf.out.Scan() {
		line := sf.out.Text()
		if visit != nil {
			visit(line)
		}
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("stockfish closed before %q: %v", prefix, sf.out.Err())
	return ""
}

// position returns Stockfish's FEN and sorted legal moves (from perft 1).
func (sf *stockfish) position(t *testing.T, fen string) (string, []string) {
	t.Helper()
	sf.send(t, "position fen "+fen)
	sf.send(t, "d")
	var sfFEN string
	sf.until(t, "Checkers:", func(l string) {
		if s, ok := strings.CutPrefix(l, "Fen: "); ok {
			sfFEN = s
		}
	})
	sf.send(t, "go perft 1")
	var moves []string
	sf.until(t, "Nodes searched:", func(l string) {
		if uci, count, ok := strings.Cut(l, ": "); ok && count == "1" && len(uci) >= 4 && len(uci) <= 5 {
			moves = append(moves, uci)
		}
	})
	slices.Sort(moves)
	return sfFEN, moves
}

// TestAgainstStockfish plays random legal games and compares, at every ply,
// the legal move set with Stockfish's perft and the FEN after each move with
// Stockfish's own (normalized for its en-passant convention).
func TestAgainstStockfish(t *testing.T) {
	sf := startStockfish(t)
	games, maxPlies := 40, 160
	if testing.Short() {
		games = 5
	}
	rng := rand.New(rand.NewPCG(20260927, 1))
	starts := []string{StartFEN}
	for _, tc := range perftCases[1:] {
		starts = append(starts, tc.fen)
	}
	checked := 0
	for g := 0; g < games; g++ {
		pos, err := ParseFEN(starts[g%len(starts)])
		if err != nil {
			t.Fatal(err)
		}
		for ply := 0; ply < maxPlies; ply++ {
			fen := pos.FEN()
			sfFEN, sfMoves := sf.position(t, fen)
			normalized, err := NormalizeFEN(sfFEN)
			if err != nil {
				t.Fatalf("stockfish FEN %q: %v", sfFEN, err)
			}
			if normalized != fen {
				t.Fatalf("game %d ply %d: FEN %q, stockfish %q", g, ply, fen, sfFEN)
			}
			legal := pos.LegalMoves()
			ours := make([]string, len(legal))
			for i, m := range legal {
				ours[i] = m.UCI()
			}
			slices.Sort(ours)
			if !slices.Equal(ours, sfMoves) {
				t.Fatalf("game %d ply %d %s:\n ours %v\n   sf %v", g, ply, fen, ours, sfMoves)
			}
			checked++
			if len(legal) == 0 {
				break
			}
			// Prefer captures and promotions now and then to reach endgames.
			m := legal[rng.IntN(len(legal))]
			if rng.IntN(3) == 0 {
				for _, c := range legal {
					if pos.board[c.To] != NoPiece || c.Promotion != NoKind {
						m = c
						break
					}
				}
			}
			pos = pos.Play(m)
		}
	}
	t.Logf("compared %d positions with stockfish", checked)
}

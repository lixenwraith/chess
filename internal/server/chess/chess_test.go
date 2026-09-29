package chess

import (
	"errors"
	"strings"
	"testing"
)

// Perft reference counts from the Chess Programming Wiki "Perft Results"
// page; together they cover castling, en passant, promotion, pins, and checks.
var perftCases = []struct {
	name   string
	fen    string
	counts []uint64 // depth 1..n
}{
	{"start", StartFEN, []uint64{20, 400, 8902, 197281}},
	{"kiwipete", "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
		[]uint64{48, 2039, 97862, 4085603}},
	{"position3", "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1", []uint64{14, 191, 2812, 43238, 674624}},
	{"position4", "r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1",
		[]uint64{6, 264, 9467, 422333}},
	{"position4-mirrored", "r2q1rk1/pP1p2pp/Q4n2/bbp1p3/Np6/1B3NBn/pPPP1PPP/R3K2R b KQ - 0 1",
		[]uint64{6, 264, 9467, 422333}},
	{"position5", "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8", []uint64{44, 1486, 62379, 2103487}},
	{"position6", "r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10",
		[]uint64{46, 2079, 89890, 3894594}},
}

func TestPerft(t *testing.T) {
	for _, tc := range perftCases {
		t.Run(tc.name, func(t *testing.T) {
			pos, err := ParseFEN(tc.fen)
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range tc.counts {
				depth := i + 1
				if testing.Short() && want > 500000 {
					break
				}
				if got := pos.Perft(depth); got != want {
					t.Fatalf("perft(%d) = %d, want %d", depth, got, want)
				}
			}
		})
	}
}

func TestFENRoundTrip(t *testing.T) {
	for _, tc := range perftCases {
		pos, err := ParseFEN(tc.fen)
		if err != nil {
			t.Fatal(err)
		}
		if got := pos.FEN(); got != tc.fen {
			t.Errorf("FEN() = %q, want %q", got, tc.fen)
		}
	}
}

func TestParseFENRejects(t *testing.T) {
	for _, fen := range []string{
		"",
		"8/8/8/8/8/8/8/8 w - - 0 1", // no kings
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0",     // 5 fields
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNRR w KQkq - 0 1",  // 9 files
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBN w KQkq - 0 1",    // 7 files
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR x KQkq - 0 1",   // side
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkqK - 0 1",  // castling length
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KK - 0 1",     // duplicate right
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq e9 0 1",  // ep square
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - -1 1",  // halfmove
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 0",   // fullmove
		"Pnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",   // pawn on rank 8
		"4k3/8/8/8/8/8/8/4K2r b - - 0 1",                             // side not to move in check
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1 x", // 7 fields
	} {
		if _, err := ParseFEN(fen); !errors.Is(err, ErrInvalidFEN) {
			t.Errorf("ParseFEN(%q) error = %v, want ErrInvalidFEN", fen, err)
		}
	}
}

func TestParseFENNormalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		// Castling rights without their rook are dropped.
		{"4k3/8/8/8/8/8/8/4K3 w KQkq - 0 1", "4k3/8/8/8/8/8/8/4K3 w - - 0 1"},
		// Right order is canonical.
		{"r3k2r/8/8/8/8/8/8/R3K2R w qkQK - 0 1", "r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1"},
		// Pseudo-legal en passant (no capturing pawn) is dropped.
		{"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
			"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1"},
		// A capture that would expose the king (pinned along the rank) is illegal.
		{"8/8/8/8/k2pP2R/8/8/4K3 b - e3 0 1", "8/8/8/8/k2pP2R/8/8/4K3 b - - 0 1"},
		// A legal en-passant capture is kept.
		{"4k3/8/8/8/3pP3/8/8/3K3R b - e3 0 1", "4k3/8/8/8/3pP3/8/8/3K3R b - e3 0 1"},
	}
	for _, tc := range cases {
		got, err := NormalizeFEN(tc.in)
		if err != nil {
			t.Fatalf("NormalizeFEN(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("NormalizeFEN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSAN(t *testing.T) {
	cases := []struct {
		fen, uci, san string
	}{
		{StartFEN, "e2e4", "e4"},
		{StartFEN, "g1f3", "Nf3"},
		{"r1bqkbnr/pppp1ppp/2n5/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R w KQkq - 2 3", "f1b5", "Bb5"},
		// Castling both ways.
		{"r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1", "e1g1", "O-O"},
		{"r3k2r/8/8/8/8/8/8/R3K2R b KQkq - 0 1", "e8c8", "O-O-O"},
		// File disambiguation: knights on b1 and f3 both reach d2.
		{"4k3/8/8/8/8/5N2/8/1N2K3 w - - 0 1", "b1d2", "Nbd2"},
		// Rank disambiguation: rooks on a1 and a5 both reach a3.
		{"4k3/8/8/R7/8/8/8/R3K3 w - - 0 1", "a1a3", "R1a3"},
		// Both: queens on e4, h4, and h1 all reach e1.
		{"1k6/8/8/8/4Q2Q/8/8/K6Q w - - 0 1", "h4e1", "Qh4e1"},
		// Pawn capture, en passant, promotion with check and capture.
		{"rnbqkbnr/ppp1pppp/8/3p4/4P3/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 2", "e4d5", "exd5"},
		{"rnbqkbnr/ppp1p1pp/8/3pPp2/8/8/PPPP1PPP/RNBQKBNR w KQkq f6 0 3", "e5f6", "exf6"},
		{"8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55", "g7g8q", "g8=Q"},
		{"8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55", "g7g8n", "g8=N"},
		{"3r3k/4P3/8/8/8/8/8/K7 w - - 0 1", "e7d8q", "exd8=Q+"},
		// Mate.
		{"rnbqkbnr/ppppp2p/5p2/6p1/4P3/8/PPPP1PPP/RNBQKBNR w KQkq g6 0 3", "d1h5", "Qh5#"},
	}
	for _, tc := range cases {
		pos, err := ParseFEN(tc.fen)
		if err != nil {
			t.Fatalf("%s: %v", tc.fen, err)
		}
		m, err := pos.ParseUCI(tc.uci)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.fen, tc.uci, err)
		}
		if got := pos.SAN(m); got != tc.san {
			t.Errorf("%s %s: SAN = %q, want %q", tc.fen, tc.uci, got, tc.san)
		}
		back, err := pos.ParseSAN(tc.san)
		if err != nil || back != m {
			t.Errorf("ParseSAN(%q) = %v, %v; want %v", tc.san, back, err, m)
		}
	}
}

func TestParseSANLenient(t *testing.T) {
	pos, err := ParseFEN("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"0-0": "e1g1", "O-O-O": "e1c1", "Kf1!?": "e1f1", "Ra1b1": "a1b1"} {
		m, err := pos.ParseSAN(in)
		if err != nil || m.UCI() != want {
			t.Errorf("ParseSAN(%q) = %v, %v; want %s", in, m.UCI(), err, want)
		}
	}
	promo, _ := ParseFEN("8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55")
	if m, err := promo.ParseSAN("g8Q"); err != nil || m.UCI() != "g7g8q" {
		t.Errorf("ParseSAN(g8Q) = %v, %v", m.UCI(), err)
	}
	if _, err := promo.ParseSAN("g8"); !errors.Is(err, ErrIllegalMove) {
		t.Errorf("ParseSAN(g8) error = %v, want ErrIllegalMove", err)
	}
	knights, _ := ParseFEN("4k3/8/8/8/8/5N2/8/1N2K3 w - - 0 1")
	if _, err := knights.ParseSAN("Nd2"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ParseSAN(Nd2) error = %v, want ambiguous", err)
	}
}

func TestParseUCIPromotionRequired(t *testing.T) {
	pos, err := ParseFEN("8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pos.ParseUCI("g7g8"); !errors.Is(err, ErrPromotionRequired) {
		t.Fatalf("ParseUCI(g7g8) error = %v, want ErrPromotionRequired", err)
	}
	if _, err := pos.ParseUCI("f7g8"); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("ParseUCI(f7g8) error = %v, want ErrIllegalMove", err)
	}
}

// The reported game: every stored FEN must match the replay, and the final
// position must accept the promotion that the web client could not send.
func TestReplayReportedGame(t *testing.T) {
	uci := strings.Fields(`e2e4 e7e6 d2d4 d7d5 b1c3 a7a6 g1f3 g8f6 f1d3 f6e4 c3e4 d5e4 d3e4 c7c5 c2c3 f8e7
		c1e3 c5d4 c3d4 b8d7 e1g1 d7f6 e3g5 f6e4 g5e7 d8e7 d1d3 f7f5 a1c1 e7d6 f3e5 c8d7 f2f3 e4f6 e5d7 d6d7
		f1e1 e8g8 d3e3 f8f7 e3e6 a8d8 e6d7 f7d7 c1d1 d7d4 d1d4 d8d4 e1e5 g8f7 e5f5 g7g6 f5a5 f7e7 b2b3 d4d5
		a5d5 f6d5 g1f2 e7d6 g2g4 d6e5 f2g3 b7b5 h2h4 d5f6 f3f4 e5e4 g4g5 f6d5 g3g4 a6a5 h4h5 d5e3 g4g3 b5b4
		h5g6 h7g6 g3h4 e4f3 h4h3 f3e4 h3g3 e3f5 g3g4 f5e7 g4g3 e7c6 g3g4 e4e3 f4f5 c6e5 g4h4 g6f5 h4h5 e3f4
		g5g6 e5d7 g6g7 d7f6 h5g6 f4e5 g6g5 f5f4 g5g6 f6g8 g6f7 g8f6`)
	line, err := Replay(StartFEN, uci)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := line.Final().FEN(), "8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55"; got != want {
		t.Fatalf("final FEN = %q, want %q", got, want)
	}
	if line.SAN[20] != "O-O" || line.SAN[37] != "O-O" {
		t.Errorf("castling SAN = %q, %q", line.SAN[20], line.SAN[37])
	}
	next, err := Replay(StartFEN, append(uci, "g7g8q"))
	if err != nil {
		t.Fatal(err)
	}
	if got := next.SAN[len(next.SAN)-1]; got != "g8=Q" {
		t.Errorf("promotion SAN = %q", got)
	}
}

func TestReplayReportsPly(t *testing.T) {
	_, err := Replay(StartFEN, []string{"e2e4", "e7e5", "e1e3"})
	var plyErr *PlyError
	if !errors.As(err, &plyErr) || plyErr.Ply != 3 || !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("error = %v, want illegal move at ply 3", err)
	}
}

func TestDrawPrimitives(t *testing.T) {
	for fen, want := range map[string]bool{
		"4k3/8/8/8/8/8/8/4K3 w - - 0 1":     true,  // bare kings
		"4k3/8/8/8/8/8/8/3NK3 w - - 0 1":    true,  // one knight
		"3bk3/8/8/8/8/8/8/2B1K3 w - - 0 1":  true,  // bishops on one color
		"2b1k3/8/8/8/8/8/8/2B1K3 w - - 0 1": false, // opposite-colored bishops
		"4k3/8/8/8/8/8/8/2NNK3 w - - 0 1":   false, // two knights can mate with help
		"4k3/8/8/8/8/8/P7/4K3 w - - 0 1":    false,
	} {
		pos, err := ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		if got := pos.InsufficientMaterial(); got != want {
			t.Errorf("%s: InsufficientMaterial = %v, want %v", fen, got, want)
		}
	}

	cycle := []string{"g1f3", "g8f6", "f3g1", "f6g8"}
	line, err := Replay(StartFEN, repeat(cycle, 2))
	if err != nil {
		t.Fatal(err)
	}
	for ply, want := range map[int]int{0: 1, 3: 1, 4: 2, 5: 2, 8: 3} {
		if got := line.Repetitions(ply); got != want {
			t.Errorf("Repetitions(%d) = %d, want %d", ply, got, want)
		}
	}
	// A pawn move in between resets the window: the start position cannot
	// recur after 1. e3.
	line, err = Replay(StartFEN, append([]string{"e2e3", "e7e6"}, repeat(cycle, 2)...))
	if err != nil {
		t.Fatal(err)
	}
	if got := line.Repetitions(10); got != 3 {
		t.Errorf("after pawn moves: Repetitions(10) = %d, want 3", got)
	}
	if got := Repetitions(nil); got != 0 {
		t.Errorf("Repetitions(nil) = %d", got)
	}
}

func repeat(moves []string, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, moves...)
	}
	return out
}

func TestPGN(t *testing.T) {
	line, err := Replay(StartFEN, []string{"f2f3", "e7e5", "g2g4", "d8h4"})
	if err != nil {
		t.Fatal(err)
	}
	got := PGN{
		Tags: []Tag{
			{"Event", "Casual game"}, {"Date", "2026.09.27"}, {"White", `Al "the" \ pawn`},
			{"Black", "Stockfish level 3"}, {"Termination", "normal"}, {"Bad Tag", "x"}, {"Evil", "a\"]\n[X \"y"},
		},
		StartFEN: StartFEN,
		SAN:      line.SAN,
		Result:   "0-1",
	}.String()
	want := `[Event "Casual game"]
[Site "?"]
[Date "2026.09.27"]
[Round "-"]
[White "Al \"the\" \\ pawn"]
[Black "Stockfish level 3"]
[Result "0-1"]
[Termination "normal"]
[Evil "a\"][X \"y"]

1. f3 e5 2. g4 Qh4# 0-1
`
	if got != want {
		t.Fatalf("PGN mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPGNCustomStartAndWrap(t *testing.T) {
	start := "4k3/8/8/8/8/8/8/R3K3 b - - 0 40"
	var uci []string
	for i := 0; i < 20; i++ {
		// Shuffle king and rook without repeating five times in a row.
		uci = append(uci, [][]string{{"e8d8", "a1a2"}, {"d8e8", "a2a1"}}[i%2]...)
	}
	line, err := Replay(start, uci[:30])
	if err != nil {
		t.Fatal(err)
	}
	text := PGN{Tags: []Tag{{"Date", "2026.01.02"}}, StartFEN: start, SAN: line.SAN, Result: "garbage"}.String()
	if !strings.Contains(text, "[SetUp \"1\"]\n[FEN \""+start+"\"]\n") {
		t.Errorf("missing SetUp/FEN tags:\n%s", text)
	}
	movetext := text[strings.Index(text, "\n\n")+2:]
	if !strings.HasPrefix(movetext, "40... Kd8 41. Ra2 Ke8 ") {
		t.Errorf("movetext starts %q", movetext[:30])
	}
	if !strings.HasSuffix(movetext, " *\n") {
		t.Errorf("result token missing: %q", movetext)
	}
	for _, l := range strings.Split(strings.TrimSuffix(movetext, "\n"), "\n") {
		if len(l) > pgnLineWidth {
			t.Errorf("line longer than %d: %q", pgnLineWidth, l)
		}
	}
}

func TestResultToken(t *testing.T) {
	for in, want := range map[string]string{"white_wins": "1-0", "black_wins": "0-1", "draw": "1/2-1/2",
		"stalemate": "1/2-1/2", "": "*", "other": "*"} {
		if got := ResultToken(in); got != want {
			t.Errorf("ResultToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestASCII(t *testing.T) {
	pos, err := ParseFEN(StartFEN)
	if err != nil {
		t.Fatal(err)
	}
	want := `  a b c d e f g h
8 r n b q k b n r 8
7 p p p p p p p p 7
6 . . . . . . . . 6
5 . . . . . . . . 5
4 . . . . . . . . 4
3 . . . . . . . . 3
2 P P P P P P P P 2
1 R N B Q K B N R 1
  a b c d e f g h`
	if got := pos.ASCII(); got != want {
		t.Fatalf("ASCII() =\n%s\nwant\n%s", got, want)
	}
}

func TestPGNComment(t *testing.T) {
	text := PGN{SAN: []string{"e4", "e5"}, Comment: "White {resigns}.\n", Result: "0-1"}.String()
	if !strings.HasSuffix(text, "\n\n1. e4 e5 { White resigns. } 0-1\n") {
		t.Fatalf("PGN comment:\n%s", text)
	}
}

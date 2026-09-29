package chess

import (
	"regexp"
	"strings"
	"testing"
)

func FuzzParseFEN(f *testing.F) {
	for _, tc := range perftCases {
		f.Add(tc.fen)
	}
	f.Add("8/8/8/8/k2pP2R/8/8/4K3 b - e3 0 1")
	f.Add("4k3/8/8/8/8/8/8/4K3 w KQkq - 0 1")
	f.Fuzz(func(t *testing.T, fen string) {
		pos, err := ParseFEN(fen)
		if err != nil {
			return
		}
		out := pos.FEN()
		again, err := ParseFEN(out)
		if err != nil {
			t.Fatalf("FEN() output %q does not parse: %v", out, err)
		}
		if again.FEN() != out {
			t.Fatalf("FEN not stable: %q -> %q", out, again.FEN())
		}
		for _, m := range pos.LegalMoves() {
			san := pos.SAN(m)
			back, err := pos.ParseSAN(san)
			if err != nil || back != m {
				t.Fatalf("%s: SAN %q of %s parses to %v, %v", out, san, m.UCI(), back.UCI(), err)
			}
			if u, err := pos.ParseUCI(m.UCI()); err != nil || u != m {
				t.Fatalf("%s: UCI %s parses to %v, %v", out, m.UCI(), u.UCI(), err)
			}
		}
	})
}

func FuzzParseSAN(f *testing.F) {
	positions := make([]*Position, 0, len(perftCases))
	for _, tc := range perftCases {
		pos, err := ParseFEN(tc.fen)
		if err != nil {
			f.Fatal(err)
		}
		positions = append(positions, pos)
	}
	for _, s := range []string{"e4", "Nf3", "O-O", "0-0-0", "exd8=Q+", "Qh4e1", "Rxa1#", "b8Q", "N", "x", "=Q"} {
		f.Add(s, uint8(0))
	}
	f.Fuzz(func(t *testing.T, san string, which uint8) {
		pos := positions[int(which)%len(positions)]
		m, err := pos.ParseSAN(san)
		if err != nil {
			return
		}
		found := false
		for _, legal := range pos.LegalMoves() {
			found = found || legal == m
		}
		if !found {
			t.Fatalf("ParseSAN(%q) returned non-legal move %s", san, m.UCI())
		}
	})
}

var tagLine = regexp.MustCompile(`^\[[A-Za-z][A-Za-z0-9_]* "([^"\\]|\\["\\])*"\]$`)

func FuzzPGN(f *testing.F) {
	f.Add("Event", "Casual game", "x\"]\n[Y \"z")
	f.Add("White", `a\b`, "Ω")
	f.Fuzz(func(t *testing.T, name, value, other string) {
		text := PGN{
			Tags:    []Tag{{name, value}, {"White", other}, {"Black", value}},
			SAN:     []string{"e4", "e5"},
			Comment: other,
			Result:  "*",
		}.String()
		header, movetext, ok := strings.Cut(text, "\n\n")
		if !ok {
			t.Fatalf("no blank line between tags and movetext:\n%s", text)
		}
		for _, l := range strings.Split(header, "\n") {
			if !tagLine.MatchString(l) {
				t.Fatalf("malformed tag line %q", l)
			}
		}
		// The comment must stay one brace comment: exactly one of each brace,
		// in order, before the result.
		if !strings.HasPrefix(movetext, "1. e4 e5 ") || !strings.HasSuffix(movetext, "*\n") ||
			strings.Count(movetext, "{") > 1 || strings.Count(movetext, "}") != strings.Count(movetext, "{") ||
			strings.Index(movetext, "}") < strings.Index(movetext, "{") {
			t.Fatalf("movetext = %q", movetext)
		}
	})
}

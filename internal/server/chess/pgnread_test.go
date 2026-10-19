package chess

import (
	"errors"
	"strings"
	"testing"
)

const scholarsMate = `[Event "Casual game"]
[Site "?"]
[Date "2026.01.02"]
[Round "-"]
[White "alice"]
[Black "bob"]
[Result "1-0"]
[Note "with \"quotes\" and a \\ backslash"]

1. e4 e5 2. Bc4 Nc6 3. Qh5 Nf6?? {a blunder} ( 3... g6 $1 4. Qf3 ) 4. Qxf7# 1-0
`

func TestParsePGN(t *testing.T) {
	game, err := ParsePGN(scholarsMate)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(game.SAN, " "); got != "e4 e5 Bc4 Nc6 Qh5 Nf6 Qxf7#" {
		t.Errorf("SAN = %q", got)
	}
	if game.Result != "1-0" || game.Tag("White") != "alice" || game.Tag("Round") != "-" {
		t.Errorf("result %q, tags %v", game.Result, game.Tags)
	}
	if game.Tag("Note") != `with "quotes" and a \ backslash` {
		t.Errorf("escaped value = %q", game.Tag("Note"))
	}
	if len(game.Tags) != 8 {
		t.Errorf("%d tags", len(game.Tags))
	}

	// Legal in the rules core, and the final position is mate.
	pos, _ := ParseFEN(StartFEN)
	for i, san := range game.SAN {
		m, err := pos.ParseSAN(san)
		if err != nil {
			t.Fatalf("ply %d %q: %v", i+1, san, err)
		}
		pos = pos.Play(m)
	}
	if !pos.InCheck() || pos.HasLegalMoves() {
		t.Error("final position is not mate")
	}
}

func TestParsePGNAcceptsLooseButValidForms(t *testing.T) {
	for name, text := range map[string]string{
		"no tags, no result":   "1.e4 e5 2.Nf3",
		"semicolon comment":    "1. e4 ; best by test\ne5 *",
		"black first":          "[FEN \"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1\"]\n[SetUp \"1\"]\n\n1... e5 2. Nf3",
		"castling zeros":       "1. e4 e5 2. Nf3 Nc6 3. Bc4 Bc5 4. 0-0 Nf6 5. 0-0-0",
		"escape line":          "%ignored line\n1. e4 e5 1/2-1/2",
		"nag and annotations":  "1. e4!! $14 e5?! 2. Nf3+ ",
		"windows line endings": "[Event \"x\"]\r\n\r\n1. e4 e5 0-1\r\n",
		"byte order mark":      "\uFEFF1. d4 *",
	} {
		if _, err := ParsePGN(text); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParsePGNRefusesWeirdness(t *testing.T) {
	for name, text := range map[string]string{
		"empty":                   "",
		"no moves":                "[Event \"x\"]\n\n*",
		"two games":               "1. e4 e5 1-0\n\n1. d4 d5 *",
		"text after result":       "1. e4 e5 * and then",
		"unterminated comment":    "1. e4 {oops e5",
		"unterminated variation":  "1. e4 (1. d4 e5",
		"stray paren":             "1. e4 ) e5",
		"stray brace":             "1. e4 } e5",
		"bad token":               "1. e4 e5 2. Nf3 hello",
		"bad move number":         "1x. e4",
		"long token":              "1. " + strings.Repeat("e", 40),
		"html":                    "1. e4 <script>alert(1)</script>",
		"bad tag name":            "[Ev ent \"x\"]\n\n1. e4 *",
		"unquoted tag":            "[Event x]\n\n1. e4 *",
		"unescaped quote":         "[Event \"a\"b\"]\n\n1. e4 *",
		"bad escape":              "[Event \"a\\n\"]\n\n1. e4 *",
		"duplicate tag":           "[Event \"x\"]\n[Event \"y\"]\n\n1. e4 *",
		"result mismatch":         "[Result \"1-0\"]\n\n1. e4 e5 0-1",
		"bad result tag":          "[Result \"won\"]\n\n1. e4 e5",
		"control character":       "1. e4\x07 e5",
		"invalid utf8":            "1. e4 \xff e5",
		"too many plies":          strings.Repeat("Nf3 Nf6 Ng1 Ng8 ", MaxPGNPlies/4+1),
		"deep variations":         strings.Repeat("(", maxVariationDepth+1) + "1. e4" + strings.Repeat(")", maxVariationDepth+1),
		"long tag value":          "[Event \"" + strings.Repeat("x", maxPGNTagValue+1) + "\"]\n\n1. e4 *",
		"nag without digits":      "1. e4 $ e5",
		"trailing garbage in tag": "[Event \"x\"] junk\n\n1. e4 *",
	} {
		_, err := ParsePGN(text)
		if !errors.Is(err, ErrInvalidPGN) {
			t.Errorf("%s: error = %v, want ErrInvalidPGN", name, err)
		}
	}
	if _, err := ParsePGN(strings.Repeat("1. e4 e5 ", MaxPGNSize)); !errors.Is(err, ErrInvalidPGN) {
		t.Errorf("oversized: %v", err)
	}
}

// Exported games read back unchanged.
func TestParsePGNRoundTripsExport(t *testing.T) {
	line, err := Replay(StartFEN, []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1b5", "a7a6", "b5a4", "g8f6", "e1g1"})
	if err != nil {
		t.Fatal(err)
	}
	text := PGN{
		Tags:     []Tag{{Name: "Event", Value: "Round trip \"test\""}, {Name: "White", Value: "a"}, {Name: "Black", Value: "b"}},
		StartFEN: StartFEN, SAN: line.SAN, Comment: "Still going.", Result: "*",
	}.String()
	game, err := ParsePGN(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if strings.Join(game.SAN, " ") != strings.Join(line.SAN, " ") || game.Tag("Event") != `Round trip "test"` || game.Result != "*" {
		t.Errorf("round trip: %v %q", game.SAN, game.Tag("Event"))
	}
}

func FuzzParsePGN(f *testing.F) {
	f.Add(scholarsMate)
	f.Add("1. e4 e5 (1... c5) {x} $1 2. Nf3 *")
	f.Add("[A \"b\"]\n\n")
	f.Fuzz(func(t *testing.T, text string) {
		game, err := ParsePGN(text)
		if err != nil {
			if !errors.Is(err, ErrInvalidPGN) {
				t.Fatalf("error %v is not ErrInvalidPGN", err)
			}
			return
		}
		if len(game.SAN) == 0 || len(game.SAN) > MaxPGNPlies {
			t.Fatalf("accepted %d plies", len(game.SAN))
		}
		for _, san := range game.SAN {
			if !pgnSAN.MatchString(san) {
				t.Fatalf("accepted token %q", san)
			}
		}
	})
}

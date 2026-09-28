package processor

import (
	"errors"
	"testing"

	"chess/internal/server/chess"
	"chess/internal/server/core"
	"chess/internal/server/game"
)

func TestRulesCheck(t *testing.T) {
	const promotion = "8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55"
	if r := rulesCheck(promotion, "g7g8"); !errors.Is(r.err, chess.ErrPromotionRequired) {
		t.Errorf("g7g8: error = %v, want ErrPromotionRequired", r.err)
	}
	if r := rulesCheck(promotion, "g7g8q"); r.err != nil || r.fen != "6Q1/5K2/5n2/p3k3/1p3p2/1P6/P7/8 b - - 0 55" {
		t.Errorf("g7g8q: %+v", r)
	}
	if r := rulesCheck(chess.StartFEN, "e2e5"); !errors.Is(r.err, chess.ErrIllegalMove) {
		t.Errorf("e2e5: error = %v, want ErrIllegalMove", r.err)
	}
	if r := rulesCheck("not a fen", "e2e4"); !errors.Is(r.err, chess.ErrInvalidFEN) {
		t.Errorf("bad FEN: error = %v, want ErrInvalidFEN", r.err)
	}
}

func TestAdjudicateDraw(t *testing.T) {
	cycle := []string{"g1f3", "g8f6", "f3g1", "f6g8"}
	line, err := chess.Replay(chess.StartFEN, append(cycle, cycle...))
	if err != nil {
		t.Fatal(err)
	}
	fens := make([]string, 9)
	for i := range fens {
		fens[i] = line.Position(i).FEN()
	}

	for name, tc := range map[string]struct {
		history []string
		fen     string
		want    core.Termination
	}{
		"start":             {nil, chess.StartFEN, core.TermNone},
		"bare kings":        {nil, "4k3/8/8/8/8/8/8/4K3 w - - 0 1", core.TermInsufficientMaterial},
		"knight":            {nil, "4k3/8/8/8/8/8/8/3NK3 b - - 3 40", core.TermInsufficientMaterial},
		"rook":              {nil, "4k3/8/8/8/8/8/8/R3K3 w - - 3 40", core.TermNone},
		"fifty moves":       {nil, "4k3/8/8/8/8/8/8/R3K3 w - - 100 90", core.TermFiftyMoveRule},
		"forty-nine":        {nil, "4k3/8/8/8/8/8/8/R3K3 w - - 99 90", core.TermNone},
		"second occurrence": {fens[:4], fens[4], core.TermNone},
		"third occurrence":  {fens[:8], fens[8], core.TermThreefoldRepetition},
		"unparsable":        {nil, "not a fen", core.TermNone},
	} {
		state, termination := adjudicateDraw(tc.history, tc.fen)
		if termination != tc.want || (tc.want == core.TermNone) != (state == core.StateOngoing) {
			t.Errorf("%s: got %v %q, want %q", name, state, termination, tc.want)
		}
	}
}

func TestActingColor(t *testing.T) {
	human := func(claimedBy string) *core.Player {
		return &core.Player{Type: core.PlayerHuman, ClaimedBy: claimedBy}
	}
	computer := &core.Player{Type: core.PlayerComputer}
	view := func(white, black *core.Player) game.View {
		return game.View{WhitePlayer: white, BlackPlayer: black}
	}

	for name, tc := range map[string]struct {
		view      game.View
		requested string
		user      string
		want      core.Color
		code      string
	}{
		"only human side":           {view(human(""), computer), "", "", core.ColorWhite, ""},
		"only human side, black":    {view(computer, human("")), "", "", core.ColorBlack, ""},
		"claimed side of caller":    {view(human("u1"), human("")), "", "u1", core.ColorWhite, ""},
		"explicit color":            {view(human(""), human("")), "b", "", core.ColorBlack, ""},
		"explicit long color":       {view(human(""), human("")), "white", "", core.ColorWhite, ""},
		"ambiguous hot seat":        {view(human(""), human("")), "", "", 0, core.ErrInvalidRequest},
		"self-play needs color":     {view(human("u1"), human("u1")), "", "u1", 0, core.ErrInvalidRequest},
		"computer only":             {view(computer, computer), "", "", 0, core.ErrInvalidRequest},
		"computer side requested":   {view(human(""), computer), "b", "", 0, core.ErrInvalidRequest},
		"claimed by another":        {view(human("u1"), computer), "", "u2", 0, core.ErrUnauthorized},
		"claimed, anonymous caller": {view(human("u1"), computer), "", "", 0, core.ErrUnauthorized},
	} {
		color, failure := actingColor(tc.view, tc.requested, tc.user)
		switch {
		case tc.code != "" && (failure == nil || failure.Error.Code != tc.code):
			t.Errorf("%s: got %v %+v, want error %s", name, color, failure, tc.code)
		case tc.code == "" && (failure != nil || color != tc.want):
			t.Errorf("%s: got %v %+v, want %v", name, color, failure, tc.want)
		}
	}
}

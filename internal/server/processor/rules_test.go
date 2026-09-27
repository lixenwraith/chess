package processor

import (
	"errors"
	"testing"

	"chess/internal/server/chess"
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

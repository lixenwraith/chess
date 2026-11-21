package processor

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
)

// TestAnalysisScoresForWhiteAndLeavesAFinishedPositionToTheRules also asks
// again with the engine taken away: a position seen before, however its FEN
// is spelled, is answered from memory.
func TestAnalysisScoresForWhiteAndLeavesAFinishedPositionToTheRules(t *testing.T) {
	if _, err := exec.LookPath("stockfish"); err != nil {
		t.Skip("stockfish is not in PATH")
	}
	p := &Processor{analyst: newAnalyst()}
	t.Cleanup(p.analyst.close)

	over, err := p.Analyse(t.Context(), "rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 1 3", 3)
	if err != nil || over.State != "black wins" || over.Termination != "checkmate" || len(over.Lines) != 0 {
		t.Fatalf("mated position: %+v, %v", over, err)
	}
	eng := <-p.analyst.engine
	p.analyst.engine <- eng
	if eng != nil {
		t.Error("a finished position started the engine")
	}

	const blackMates = "rnbqkbnr/pppp1ppp/8/4p3/6P1/5P2/PPPPP2P/RNBQKBNR b KQkq g3 0 2"
	got, err := p.Analyse(t.Context(), blackMates, 2)
	if err != nil || len(got.Lines) != 2 {
		t.Fatalf("mate in one: %+v, %v", got, err)
	}
	if best := got.Lines[0]; best.Mate != -1 || best.Moves[0] != "d8h4" || best.SAN[0] != "Qh4#" {
		t.Errorf("best line = %+v, want Black's Qh4# as mate -1", best)
	}

	eng = <-p.analyst.engine
	defer func() { p.analyst.engine <- eng }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	again, err := p.Analyse(ctx, strings.Replace(blackMates, " g3 ", " - ", 1), 2)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Errorf("asked again = %+v, %v; want the first answer", again, err)
	}
}

func TestAnalysisPastTheWaitingRoomIsRefused(t *testing.T) {
	p := &Processor{analyst: newAnalyst()}
	for range cap(p.analyst.admitted) {
		p.analyst.admitted <- struct{}{}
	}
	if _, err := p.Analyse(t.Context(), chess.StartFEN, 1); !errors.Is(err, ErrAnalysisBusy) {
		t.Errorf("error = %v, want ErrAnalysisBusy", err)
	}
}

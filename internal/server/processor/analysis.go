package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/engine"
)

const (
	analysisNodes   = 500_000 // about a second of one core
	analysisTimeout = 15 * time.Second
	analysisWaiting = 4 // requests admitted behind the one being searched
	analysisCached  = 4096
	analysisPlies   = 12 // of a variation, in a response
)

// ErrAnalysisBusy refuses an analysis past the few that may wait their turn.
var ErrAnalysisBusy = errors.New("analysis busy")

// analyst searches positions on an engine of its own, started on first use,
// so an analysis never delays a computer's move. One position is searched at
// a time; answers are kept by position and line count.
type analyst struct {
	admitted chan struct{}    // a token for each request searching or waiting
	engine   chan *engine.UCI // the idle engine, nil before its first use
	mu       sync.Mutex
	cache    map[string]core.AnalysisResponse
}

func newAnalyst() *analyst {
	a := &analyst{
		admitted: make(chan struct{}, analysisWaiting+1),
		engine:   make(chan *engine.UCI, 1),
		cache:    make(map[string]core.AnalysisResponse),
	}
	a.engine <- nil
	return a
}

func (a *analyst) cached(key string) (core.AnalysisResponse, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	resp, ok := a.cache[key]
	return resp, ok
}

// keep stores an answer; a full cache gives up any one entry for it.
func (a *analyst) keep(key string, resp core.AnalysisResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= analysisCached {
		for old := range a.cache {
			delete(a.cache, old)
			break
		}
	}
	a.cache[key] = resp
}

// close stops the engine unless a search holds it, which ends with the process.
func (a *analyst) close() {
	select {
	case eng := <-a.engine:
		if eng != nil {
			eng.Close()
		}
	case <-time.After(time.Second):
	}
}

// Analyse gives the engine's best lines of a position, scored for White. The
// rules answer where the game is over; a position seen before is answered
// from memory. The error wraps chess.ErrInvalidFEN or is ErrAnalysisBusy, the
// context's, or the engine's.
func (p *Processor) Analyse(ctx context.Context, fen string, lines int) (core.AnalysisResponse, error) {
	if !p.isFENSafe(fen) {
		return core.AnalysisResponse{}, fmt.Errorf("%w: format or characters", chess.ErrInvalidFEN)
	}
	pos, err := chess.ParseFEN(fen)
	if err != nil {
		return core.AnalysisResponse{}, err
	}
	resp := core.AnalysisResponse{FEN: pos.FEN(), Lines: []core.AnalysisLine{}}
	if state, termination := outcome(nil, pos); state != core.StateOngoing {
		resp.State, resp.Termination = state.String(), string(termination)
		return resp, nil
	}
	lines = min(max(lines, 1), len(pos.LegalMoves()))
	key := fmt.Sprintf("%d %s", lines, resp.FEN)
	a := p.analyst
	if kept, ok := a.cached(key); ok {
		return kept, nil
	}

	select {
	case a.admitted <- struct{}{}:
		defer func() { <-a.admitted }()
	default:
		return core.AnalysisResponse{}, ErrAnalysisBusy
	}
	var eng *engine.UCI
	select {
	case eng = <-a.engine:
		defer func() { a.engine <- eng }()
	case <-ctx.Done():
		return core.AnalysisResponse{}, ctx.Err()
	}
	// The same position may have been searched while this request waited.
	if kept, ok := a.cached(key); ok {
		return kept, nil
	}
	if eng == nil {
		if eng, err = engine.New(); err != nil {
			return core.AnalysisResponse{}, fmt.Errorf("start analysis engine: %w", err)
		}
	}
	found, err := eng.Analyse(resp.FEN, lines, analysisNodes, analysisTimeout)
	if err != nil {
		// The next request starts a fresh engine, whatever state this one is in.
		eng.Close()
		eng = nil
		slog.Warn("analysis failed", "fen", resp.FEN, "error", err)
		return core.AnalysisResponse{}, err
	}
	resp.Depth = found[0].Depth
	for _, line := range found {
		resp.Lines = append(resp.Lines, analysisLine(pos, line))
	}
	a.keep(key, resp)
	return resp, nil
}

// analysisLine turns the engine's line into White's score and the moves the
// rules can notate, which a variation ends at.
func analysisLine(pos *chess.Position, line engine.Line) core.AnalysisLine {
	out := core.AnalysisLine{Score: line.Score, Mate: line.Mate, Moves: []string{}, SAN: []string{}}
	if pos.Turn() == chess.Black {
		out.Score, out.Mate = -out.Score, -out.Mate
	}
	for _, uci := range line.Moves[:min(len(line.Moves), analysisPlies)] {
		move, err := pos.ParseUCI(uci)
		if err != nil {
			break
		}
		out.Moves, out.SAN = append(out.Moves, uci), append(out.SAN, pos.SAN(move))
		pos = pos.Play(move)
	}
	return out
}

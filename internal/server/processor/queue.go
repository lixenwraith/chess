package processor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"chess/internal/server/core"
	"chess/internal/server/engine"
)

// EngineTask contains computer move calculation request and response channel
type EngineTask struct {
	GameID   string
	FEN      string
	Color    core.Color
	Player   *core.Player // Full player config including engine configuration
	Response chan<- EngineResult
}

// EngineResult contains the outcome of an engine calculation
type EngineResult struct {
	GameID string
	Move   string
	Score  int
	Depth  int
	IsMate bool
	MateIn int
	Error  error
}

// EngineQueue manages async engine computations
type EngineQueue struct {
	tasks        chan EngineTask
	workers      int
	wg           sync.WaitGroup
	callbackWG   sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	submitMu     sync.RWMutex
	closed       bool
	shutdownOnce sync.Once
}

// NewEngineQueue creates a queue with specified worker count
func NewEngineQueue(workerCount int) *EngineQueue {
	if workerCount < 1 {
		workerCount = 2 // Default
	}

	ctx, cancel := context.WithCancel(context.Background())

	q := &EngineQueue{
		tasks:   make(chan EngineTask, 100), // Buffered for queueing
		workers: workerCount,
		ctx:     ctx,
		cancel:  cancel,
	}

	q.start()
	return q
}

// start initializes the worker pool
func (q *EngineQueue) start() {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker(i)
	}
}

// worker processes engine tasks
func (q *EngineQueue) worker(id int) {
	defer q.wg.Done()
	var eng *engine.UCI
	for {
		var err error
		if eng, err = engine.New(); err == nil {
			break
		}
		slog.Warn("engine worker initialization failed; retrying", "worker", id, "error", err)
		select {
		case <-q.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	defer eng.Close()
	slog.Debug("engine worker started", "worker", id)
	for {
		select {
		case task, ok := <-q.tasks:
			if !ok {
				return
			}
			task.Response <- q.processTask(eng, task) // Response is buffered(1); never blocks
		case <-q.ctx.Done():
			return
		}
	}
}

// processTask executes a single engine calculation
func (q *EngineQueue) processTask(eng *engine.UCI, task EngineTask) EngineResult {
	started := time.Now()
	defer func() {
		slog.Debug("engine task completed", "game_id", task.GameID, "duration", time.Since(started))
	}()
	result := EngineResult{GameID: task.GameID}
	if err := eng.NewGame(); err != nil {
		result.Error = err
		return result
	}
	if task.Player.Type == core.PlayerComputer {
		if err := eng.SetSkillLevel(task.Player.Level); err != nil {
			result.Error = err
			return result
		}
	}
	if err := eng.SetPosition(task.FEN, nil); err != nil {
		result.Error = err
		return result
	}
	searchTime := 1000
	if task.Player.Type == core.PlayerComputer && task.Player.SearchTime > 0 {
		searchTime = task.Player.SearchTime
	}
	search, err := eng.Search(searchTime)
	if err != nil {
		result.Error = fmt.Errorf("engine search failed: %w", err)
		return result
	}
	if search.BestMove == "" || search.BestMove == "(none)" {
		result.IsMate, result.MateIn = search.IsMate, search.MateIn
		return result
	}
	result.Move, result.Score, result.Depth = search.BestMove, search.Score, search.Depth
	result.IsMate, result.MateIn = search.IsMate, search.MateIn
	return result
}

// Submit adds a task to the queue
func (q *EngineQueue) Submit(task EngineTask) error {
	q.submitMu.RLock()
	defer q.submitMu.RUnlock()
	return q.submitLocked(task)
}

func (q *EngineQueue) submitLocked(task EngineTask) error {
	if q.closed {
		return fmt.Errorf("queue is shutting down")
	}
	select {
	case q.tasks <- task:
		slog.Debug("engine task queued", "game_id", task.GameID, "queue_depth", len(q.tasks))
		return nil
	case <-q.ctx.Done():
		return fmt.Errorf("queue is shutting down")
	default:
		return fmt.Errorf("queue is full")
	}
}

// SubmitAsync submits a task without blocking for result
func (q *EngineQueue) SubmitAsync(gameID, fen string, color core.Color, player *core.Player, callback func(EngineResult)) error {
	if player == nil {
		return fmt.Errorf("computer player is missing")
	}
	if callback == nil {
		return fmt.Errorf("engine callback is missing")
	}
	respChan := make(chan EngineResult, 1)
	q.submitMu.RLock()
	err := q.submitLocked(EngineTask{
		GameID: gameID, FEN: fen, Color: color, Player: player, Response: respChan,
	})
	if err == nil {
		// Registered while the submit lock is held, so Shutdown cannot begin
		// waiting between a successful send and this Add.
		q.callbackWG.Add(1)
	}
	q.submitMu.RUnlock()
	if err != nil {
		return err
	}
	budget := 1000
	if player.Type == core.PlayerComputer && player.SearchTime > 0 {
		budget = player.SearchTime
	}
	wait := time.Duration(budget)*time.Millisecond*2 + 30*time.Second // search budget + queue-wait headroom
	go func() {
		defer q.callbackWG.Done()
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case result := <-respChan:
			callback(result)
		case <-timer.C:
			callback(EngineResult{GameID: gameID, Error: fmt.Errorf("engine timeout")})
		case <-q.ctx.Done():
			// Live state is intentionally abandoned during server shutdown. The
			// last fully committed position remains the durable replay boundary.
			return
		}
	}()
	return nil
}

// Shutdown gracefully stops the queue
func (q *EngineQueue) Shutdown(timeout time.Duration) error {
	q.shutdownOnce.Do(func() {
		q.submitMu.Lock()
		q.closed = true
		q.cancel()
		close(q.tasks)
		q.submitMu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		q.callbackWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("shutdown timeout exceeded")
	}
}

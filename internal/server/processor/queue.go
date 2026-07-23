package processor

import (
	"context"
	"fmt"
	"log"
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
	tasks   chan EngineTask
	workers int
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
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
		log.Printf("worker %d: engine init failed: %v; retrying", id, err)
		select {
		case <-q.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	defer eng.Close()
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
	select {
	case q.tasks <- task:
		return nil
	case <-q.ctx.Done():
		return fmt.Errorf("queue is shutting down")
	default:
		return fmt.Errorf("queue is full")
	}
}

// SubmitAsync submits a task without blocking for result
func (q *EngineQueue) SubmitAsync(gameID, fen string, color core.Color, player *core.Player, callback func(EngineResult)) error {
	respChan := make(chan EngineResult, 1)
	if err := q.Submit(EngineTask{GameID: gameID, FEN: fen, Color: color, Player: player, Response: respChan}); err != nil {
		return err
	}
	budget := 1000
	if player.Type == core.PlayerComputer && player.SearchTime > 0 {
		budget = player.SearchTime
	}
	wait := time.Duration(budget)*time.Millisecond*2 + 30*time.Second // search budget + queue-wait headroom
	go func() {
		select {
		case result := <-respChan:
			callback(result)
		case <-time.After(wait):
			callback(EngineResult{GameID: gameID, Error: fmt.Errorf("engine timeout")})
		}
	}()
	return nil
}

// Shutdown gracefully stops the queue
func (q *EngineQueue) Shutdown(timeout time.Duration) error {
	q.cancel()
	close(q.tasks)

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("shutdown timeout exceeded")
	}
}

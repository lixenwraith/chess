// FILE: lixenwraith/chess/internal/server/processor/queue.go
package processor

import (
	"context"
	"fmt"
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

	// Each worker gets its own engine instance
	eng, err := engine.New()
	if err != nil {
		fmt.Printf("Worker %d failed to initialize engine: %v\n", id, err)
		return
	}
	defer eng.Close()

	for {
		select {
		case task, ok := <-q.tasks:
			if !ok {
				return // Channel closed
			}

			result := q.processTask(eng, task)

			// Send result if receiver still listening
			select {
			case task.Response <- result:
			case <-time.After(100 * time.Millisecond):
				// Receiver abandoned, discard result
			}

		case <-q.ctx.Done():
			return
		}
	}
}

// processTask executes a single engine calculation
func (q *EngineQueue) processTask(eng *engine.UCI, task EngineTask) EngineResult {
	result := EngineResult{
		GameID: task.GameID,
	}

	// Apply computer configuration if provided
	if task.Player.Type == core.PlayerComputer {
		eng.SetSkillLevel(task.Player.Level)
	}

	// Setup position
	eng.SetPosition(task.FEN, []string{})

	// Determine search time
	searchTime := 1000 // Default 1 second
	if task.Player.Type == core.PlayerComputer && task.Player.SearchTime > 0 {
		searchTime = task.Player.SearchTime
	}

	// Search for best move
	search, err := eng.Search(searchTime)
	if err != nil {
		result.Error = fmt.Errorf("engine search failed: %v", err)
		return result
	}

	// Check for no legal moves
	if search.BestMove == "" || search.BestMove == "(none)" {
		result.Move = ""
		result.IsMate = search.IsMate
		result.MateIn = search.MateIn
		return result
	}

	result.Move = search.BestMove
	result.Score = search.Score
	result.Depth = search.Depth
	result.IsMate = search.IsMate
	result.MateIn = search.MateIn

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

	task := EngineTask{
		GameID:   gameID,
		FEN:      fen,
		Color:    color,
		Player:   player,
		Response: respChan,
	}

	if err := q.Submit(task); err != nil {
		return err
	}

	// Handle result in background
	go func() {
		select {
		case result := <-respChan:
			callback(result)
		case <-time.After(5 * time.Second):
			callback(EngineResult{
				GameID: gameID,
				Error:  fmt.Errorf("engine timeout"),
			})
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
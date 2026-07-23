package engine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	enginePath       = "stockfish"
	handshakeTimeout = 5 * time.Second
	barrierTimeout   = 5 * time.Second
	diagnoseTimeout  = 3 * time.Second
	probeTimeout     = 3 * time.Second
	lineBuffer       = 512
)

var ErrEngineTimeout = errors.New("engine timeout")

// UCI wraps a stockfish process. All engine dialogue is a serialized
// request/response transaction under mu; a single reader goroutine owns stdout
// for the life of the process. Any timeout/EOF kills and respawns the process:
// output desync cannot survive into the next call.
type UCI struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	alive bool
}

type SearchResult struct {
	BestMove string
	Score    int
	Depth    int
	IsMate   bool
	MateIn   int
}

type Diagnosis struct {
	FEN     string
	InCheck bool
}

func New() (*UCI, error) {
	u := &UCI{}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.spawnLocked(); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *UCI) spawnLocked() error {
	cmd := exec.Command(enginePath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}

	lines := make(chan string, lineBuffer)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines) // EOF: process exited or was killed
	}()

	u.cmd, u.stdin, u.lines, u.alive = cmd, stdin, lines, true

	if _, err := u.txLocked(handshakeTimeout, []string{"uci"}, "uciok", nil); err != nil {
		u.killLocked()
		return fmt.Errorf("uci handshake: %w", err)
	}
	if _, err := u.txLocked(handshakeTimeout, []string{"isready"}, "readyok", nil); err != nil {
		u.killLocked()
		return fmt.Errorf("uci handshake: %w", err)
	}
	return nil
}

// killLocked hard-stops the process. Reaping is deferred to a goroutine that
// first drains the line channel to completion, so cmd.Wait never races the
// reader's final reads on the stdout pipe.
func (u *UCI) killLocked() {
	u.alive = false
	if u.cmd != nil && u.cmd.Process != nil {
		u.cmd.Process.Kill()
	}
	if u.stdin != nil {
		u.stdin.Close()
	}
	if u.lines != nil {
		go func(ch chan string, cmd *exec.Cmd) {
			for range ch {
			}
			cmd.Wait()
		}(u.lines, u.cmd)
	}
}

func (u *UCI) restartLocked() {
	u.killLocked()
	_ = u.spawnLocked() // on failure alive stays false; next tx errors immediately
}

func (u *UCI) drainLocked() {
	for {
		select {
		case _, ok := <-u.lines:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// txLocked: drain stale lines, send commands, read to the terminal prefix.
// visit observes every line including the terminal one. Timeout is
// per-transaction total.
func (u *UCI) txLocked(timeout time.Duration, cmds []string, terminal string, visit func(string)) (string, error) {
	if !u.alive {
		return "", errors.New("engine not running")
	}
	u.drainLocked()
	for _, c := range cmds {
		if _, err := fmt.Fprintln(u.stdin, c); err != nil {
			u.restartLocked()
			return "", fmt.Errorf("engine write: %w", err)
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ln, ok := <-u.lines:
			if !ok {
				u.restartLocked()
				return "", errors.New("engine closed unexpectedly")
			}
			if visit != nil {
				visit(ln)
			}
			if strings.HasPrefix(ln, terminal) {
				return ln, nil
			}
		case <-deadline.C:
			u.restartLocked()
			return "", fmt.Errorf("%w awaiting %q", ErrEngineTimeout, terminal)
		}
	}
}

func (u *UCI) tx(timeout time.Duration, cmds []string, terminal string, visit func(string)) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.txLocked(timeout, cmds, terminal, visit)
}

func (u *UCI) NewGame() error {
	_, err := u.tx(barrierTimeout, []string{"ucinewgame", "isready"}, "readyok", nil)
	return err
}

func (u *UCI) SetSkillLevel(level int) error {
	if level < 0 {
		level = 0
	} else if level > 20 {
		level = 20
	}
	_, err := u.tx(barrierTimeout,
		[]string{fmt.Sprintf("setoption name Skill Level value %d", level), "isready"},
		"readyok", nil)
	return err
}

func (u *UCI) SetPosition(fen string, moves []string) error {
	cmd := "position fen " + fen
	if len(moves) > 0 {
		cmd += " moves " + strings.Join(moves, " ")
	}
	_, err := u.tx(barrierTimeout, []string{cmd, "isready"}, "readyok", nil)
	return err
}

// Diagnose runs `d` and consumes its full output. Terminal line is "Checkers:"
// (last line of `d` in current Stockfish; verify against the jailed build —
// see context requests).
func (u *UCI) Diagnose() (Diagnosis, error) {
	var d Diagnosis
	last, err := u.tx(diagnoseTimeout, []string{"d"}, "Checkers:", func(ln string) {
		if s, ok := strings.CutPrefix(ln, "Fen: "); ok {
			d.FEN = strings.TrimSpace(s)
		}
	})
	if err != nil {
		return Diagnosis{}, err
	}
	if d.FEN == "" {
		return Diagnosis{}, errors.New("d output missing Fen line")
	}
	d.InCheck = strings.TrimSpace(strings.TrimPrefix(last, "Checkers:")) != ""
	return d, nil
}

// HasLegalMoves probes with a depth-1 search: deterministic, milliseconds.
func (u *UCI) HasLegalMoves() (bool, error) {
	last, err := u.tx(probeTimeout, []string{"go depth 1"}, "bestmove ", nil)
	if err != nil {
		return false, err
	}
	f := strings.Fields(last)
	return len(f) >= 2 && f[1] != "(none)", nil
}

func (u *UCI) Search(timeMs int) (*SearchResult, error) {
	r := &SearchResult{}
	timeout := time.Duration(timeMs)*time.Millisecond + 5*time.Second
	last, err := u.tx(timeout, []string{fmt.Sprintf("go movetime %d", timeMs)}, "bestmove ", func(ln string) {
		if !strings.HasPrefix(ln, "info ") {
			return
		}
		f := strings.Fields(ln)
		for i := 0; i < len(f)-1; i++ {
			switch f[i] {
			case "depth":
				fmt.Sscanf(f[i+1], "%d", &r.Depth)
			case "cp":
				fmt.Sscanf(f[i+1], "%d", &r.Score)
				r.IsMate = false
			case "mate":
				fmt.Sscanf(f[i+1], "%d", &r.MateIn)
				r.IsMate = true
				if r.MateIn > 0 {
					r.Score = 100000 - r.MateIn
				} else {
					r.Score = -100000 - r.MateIn
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	f := strings.Fields(last)
	if len(f) >= 2 {
		r.BestMove = f[1]
	}
	return r, nil
}

func (u *UCI) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.alive {
		fmt.Fprintln(u.stdin, "quit")
		done := make(chan struct{})
		go func() { u.cmd.Wait(); close(done) }()
		u.alive = false
		select {
		case <-done:
			u.stdin.Close()
			return nil
		case <-time.After(1 * time.Second):
		}
	}
	u.killLocked()
	return nil
}


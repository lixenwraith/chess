// FILE: internal/engine/engine.go
package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const enginePath = "stockfish"

type UCI struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex
}

type SearchResult struct {
	BestMove string
	Score    int
	Depth    int
	IsMate   bool
	MateIn   int
}

func New() (*UCI, error) {
	cmd := exec.Command(enginePath)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start engine: %v", err)
	}

	uci := &UCI{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
	}

	if err := uci.initialize(); err != nil {
		uci.Close()
		return nil, err
	}

	return uci, nil
}

// SetSkillLevel sets the Stockfish skill level (0-20)
func (u *UCI) SetSkillLevel(level int) {
	if level < 0 {
		level = 0
	} else if level > 20 {
		level = 20
	}
	u.sendCommand(fmt.Sprintf("setoption name Skill Level value %d", level))
}

// Get FEN from Stockfish's debug ('d') command
func (u *UCI) GetFEN() (string, error) {
	u.sendCommand("d")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		for u.stdout.Scan() {
			line := u.stdout.Text()
			if strings.HasPrefix(line, "Fen: ") {
				done <- strings.TrimPrefix(line, "Fen: ")
				return
			}
		}
		done <- ""
	}()

	select {
	case fen := <-done:
		if fen == "" {
			return "", fmt.Errorf("failed to get FEN from engine")
		}
		return fen, nil
	case <-ctx.Done():
		return "", fmt.Errorf("timeout getting FEN")
	}
}

func (u *UCI) initialize() error {
	u.sendCommand("uci")

	// Wait for uciok with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan bool)
	go func() {
		for u.stdout.Scan() {
			if u.stdout.Text() == "uciok" {
				done <- true
				return
			}
		}
		done <- false
	}()

	select {
	case success := <-done:
		if !success {
			return fmt.Errorf("engine closed unexpectedly")
		}
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for uciok")
	}

	u.sendCommand("isready")
	return u.waitReady()
}

func (u *UCI) waitReady() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error)
	go func() {
		for u.stdout.Scan() {
			if u.stdout.Text() == "readyok" {
				done <- nil
				return
			}
		}
		done <- fmt.Errorf("engine closed unexpectedly")
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for readyok")
	}
}

func (u *UCI) sendCommand(cmd string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fmt.Fprintln(u.stdin, cmd)
}

func (u *UCI) NewGame() {
	u.sendCommand("ucinewgame")
	u.sendCommand("isready")
	u.waitReady()
}

func (u *UCI) SetPosition(fen string, moves []string) {
	cmd := fmt.Sprintf("position fen %s", fen)
	if len(moves) > 0 {
		cmd += " moves " + strings.Join(moves, " ")
	}
	u.sendCommand(cmd)
}

func (u *UCI) Search(timeMs int) (*SearchResult, error) {
	u.sendCommand(fmt.Sprintf("go movetime %d", timeMs))

	result := &SearchResult{}

	// Add timeout protection (2x the search time + buffer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeMs*2+1000)*time.Millisecond)
	defer cancel()

	done := make(chan error)
	go func() {
		for u.stdout.Scan() {
			line := u.stdout.Text()

			if strings.HasPrefix(line, "info ") {
				fields := strings.Fields(line)
				for i := 0; i < len(fields)-1; i++ {
					switch fields[i] {
					case "depth":
						fmt.Sscanf(fields[i+1], "%d", &result.Depth)
					case "cp":
						fmt.Sscanf(fields[i+1], "%d", &result.Score)
						result.IsMate = false
					case "mate":
						fmt.Sscanf(fields[i+1], "%d", &result.MateIn)
						result.IsMate = true
						// Convert mate score to centipawn equivalent for backwards compatibility
						if result.MateIn > 0 {
							result.Score = 100000 - result.MateIn
						} else {
							result.Score = -100000 - result.MateIn
						}
					}
				}
			}

			if strings.HasPrefix(line, "bestmove ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					result.BestMove = parts[1]
				}
				done <- nil
				return
			}
		}
		done <- fmt.Errorf("engine closed unexpectedly")
	}()

	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
		return result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout waiting for bestmove")
	}
}

func (u *UCI) Close() error {
	u.sendCommand("quit")
	time.Sleep(100 * time.Millisecond)

	// Try graceful shutdown first
	done := make(chan error, 1)
	go func() {
		done <- u.cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(1 * time.Second):
		// Force kill if doesn't exit gracefully
		return u.cmd.Process.Kill()
	}
}
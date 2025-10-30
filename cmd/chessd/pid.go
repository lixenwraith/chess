// FILE: cmd/chessd/pid.go
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// managePIDFile creates and manages a PID file with optional locking.
// Returns a cleanup function that must be called on exit.
func managePIDFile(path string, lock bool) (func(), error) {
	// Open/create PID file with exclusive create first attempt
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if !os.IsExist(err) {
			return nil, fmt.Errorf("cannot create PID file: %w", err)
		}

		// File exists - check if stale
		if lock {
			if err := checkStalePID(path); err != nil {
				return nil, err
			}
		}

		// Reopen for writing (truncate existing content)
		file, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("cannot open PID file: %w", err)
		}
	}

	// Acquire exclusive lock if requested
	if lock {
		if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			file.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("cannot acquire lock: another instance is running")
			}
			return nil, fmt.Errorf("lock failed: %w", err)
		}
	}

	// Write current PID
	pid := os.Getpid()
	if _, err = fmt.Fprintf(file, "%d\n", pid); err != nil {
		file.Close()
		os.Remove(path)
		return nil, fmt.Errorf("cannot write PID: %w", err)
	}

	// Sync to ensure PID is written
	if err = file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return nil, fmt.Errorf("cannot sync PID file: %w", err)
	}

	// Return cleanup function
	cleanup := func() {
		if lock {
			// Release lock explicitly, file close works too
			syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		}
		file.Close()
		os.Remove(path)
	}

	return cleanup, nil
}

// checkStalePID reads an existing PID file and checks if the process is running
func checkStalePID(path string) error {
	// Try to read existing PID
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read existing PID file: %w", err)
	}

	pidStr := string(data)
	pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
	if err != nil {
		// Corrupted PID file
		return fmt.Errorf("corrupted PID file (contains: %q)", pidStr)
	}

	// Check if process exists using kill(0), never errors on Unix
	proc, _ := os.FindProcess(pid)

	// Send signal 0 to check if process exists
	if err = proc.Signal(syscall.Signal(0)); err != nil {
		// Process doesn't exist or we don't have permission
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stale PID file found for defunct process %d", pid)
		}
		// Process exists but we can't signal it (different user?)
		return fmt.Errorf("process %d exists but cannot verify ownership: %v", pid, err)
	}

	// Process is running
	return fmt.Errorf("stale PID file: process %d is running but not holding lock", pid)
}
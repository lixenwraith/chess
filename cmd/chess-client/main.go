// FILE: lixenwraith/chess/cmd/chess-client/main.go
// Package main implements an interactive debugging client for the chess server API.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"chess/internal/client/api"
	"chess/internal/client/commands"
	"chess/internal/client/display"
	"chess/internal/client/session"

	"github.com/chzyer/readline"
)

func main() {
	s := &session.Session{
		APIBaseURL: "http://localhost:8080",
		Client:     api.New("http://localhost:8080"),
		Verbose:    false,
	}

	// Initialize readline
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          display.Prompt("chess"),
		HistoryFile:     ".chess_history",
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Printf("%s%s%s\n", display.Red, err.Error(), display.Reset)
		os.Exit(1)
	}
	defer rl.Close()

	fmt.Printf("%sChess Debug Client%s\n", display.Cyan, display.Reset)
	fmt.Printf("%sAPI: %s%s\n", display.Cyan, s.APIBaseURL, display.Reset)
	fmt.Printf("Type 'help' for commands\n\n")

	registry := commands.NewRegistry(s)

	for {
		// Build enhanced prompt
		prompt := buildPrompt(s)
		rl.SetPrompt(prompt)

		line, err := rl.Readline()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if line == "exit" || line == "quit" || line == "x" {
			break
		}

		// Check for verbose flag
		if strings.HasSuffix(line, " -v") {
			s.Verbose = true
			line = strings.TrimSuffix(line, " -v")
		} else {
			s.Verbose = false
		}

		registry.Execute(line)
	}
}

func buildPrompt(s *session.Session) string {
	parts := []string{}

	// Base
	base := "chess"

	// Add user/game context
	if s.Username != "" {
		parts = append(parts, fmt.Sprintf("%s%s%s", display.Magenta, s.Username, display.Reset))
	}
	if s.Username != "" && s.CurrentGame != "" {
		parts = append(parts, fmt.Sprintf("%s - %s", display.Yellow, display.Reset))
	}
	if s.CurrentGame != "" {
		parts = append(parts, fmt.Sprintf("%s%s%s", display.White, s.CurrentGame[:8], display.Reset))
	}

	// Add player color if in game
	if s.CurrentGameState != nil && s.PlayerColor != "" {
		colorText := ""
		if s.PlayerColor == "w" {
			colorText = display.Blue + "White" + display.Reset
		} else {
			colorText = display.Red + "Black" + display.Reset
		}
		parts = append(parts, colorText)
	}

	// Build first part
	promptStr := base
	if len(parts) > 0 {
		promptStr += display.Yellow + " [" + display.Reset + strings.Join(parts, "") + display.Yellow + "]"
	}

	// Add game state if available
	if s.CurrentGameState != nil {
		turnInfo := ""
		if s.CurrentGameState.Turn == "w" {
			turnPlayer := "White"
			playerType := "h"
			if s.CurrentGameState.Players.White.Type == 2 {
				playerType = "c"
			}
			turnInfo = fmt.Sprintf(" - Turn:%s(%s)",
				fmt.Sprintf("%s%s%s", display.Blue, turnPlayer, display.Reset),
				playerType)
		} else {
			turnPlayer := "Black"
			playerType := "h"
			if s.CurrentGameState.Players.Black.Type == 2 {
				playerType = "c"
			}
			turnInfo = fmt.Sprintf(" - Turn:%s(%s)",
				fmt.Sprintf("%s%s%s", display.Red, turnPlayer, display.Reset),
				playerType)
		}
		promptStr += turnInfo
	}

	return display.Prompt(promptStr)
}
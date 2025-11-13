// FILE: lixenwraith/chess/cmd/chess-client/main.go
// Package main implements an interactive debugging client for the chess server API.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"chess/internal/client/api"
	"chess/internal/client/command"
	"chess/internal/client/display"
	"chess/internal/client/session"
)

func main() {
	s := &session.Session{
		APIBaseURL: "http://localhost:8080",
		Client:     api.New("http://localhost:8080"),
		Verbose:    false,
	}

	// Initialize simple input scanner
	scanner := bufio.NewScanner(os.Stdin)

	display.Println(display.Cyan, "Chess Debug Client")
	display.Println(display.Cyan, "API: %s", s.APIBaseURL)
	fmt.Println("Type 'help' for commands\n")

	registry := command.NewRegistry(s)

	for {
		// Build enhanced prompt
		prompt := buildPrompt(s)
		fmt.Print(prompt)

		// Read input
		if !scanner.Scan() {
			// EOF or error
			if err := scanner.Err(); err != nil {
				display.Println(display.Red, "\nError reading input: %s", err.Error())
			}
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Check for exit commands
		if line == "exit" || line == "quit" || line == "x" {
			display.Println(display.Cyan, "Goodbye!")
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
	var b display.Builder
	b.Add("", "chess")

	// Add user/game context
	if s.Username != "" {
		b.Add("", " [").Add(display.Magenta, s.Username)
		if s.CurrentGame != "" {
			b.Add(display.Yellow, " - ")
		} else {
			b.Add("", "]")
		}
	}

	if s.CurrentGame != "" {
		if s.Username == "" {
			b.Add("", " [")
		}
		b.Add(display.White, s.CurrentGame[:8])
		b.Add("", "]")
	}

	// Add player color if in game
	if s.CurrentGameState != nil && s.PlayerColor != "" {
		if s.PlayerColor == "w" {
			b.Add("", " ").Add(display.Blue, "White")
		} else {
			b.Add("", " ").Add(display.Red, "Black")
		}
	}

	// Add game state if available
	if s.CurrentGameState != nil {
		turnInfo := " - Turn:"
		if s.CurrentGameState.Turn == "w" {
			playerType := "h"
			if s.CurrentGameState.Players.White.Type == 2 {
				playerType = "c"
			}
			b.Add("", turnInfo).Add(display.Blue, "White").Add("", fmt.Sprintf("(%s)", playerType))
		} else {
			playerType := "h"
			if s.CurrentGameState.Players.Black.Type == 2 {
				playerType = "c"
			}
			b.Add("", turnInfo).Add(display.Red, "Black").Add("", fmt.Sprintf("(%s)", playerType))
		}
	}

	return display.Prompt(b.String())
}
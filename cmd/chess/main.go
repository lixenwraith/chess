// FILE: cmd/chess/main.go
package main

import (
	"fmt"
	"os"

	"chess/internal/cli"
	"chess/internal/service"
	clitransport "chess/internal/transport/cli"
)

func main() {
	svc, err := service.New()
	if err != nil {
		fmt.Printf("Failed to start: %v\n", err)
		os.Exit(1)
	}
	defer svc.Close()

	view := cli.New(os.Stdin, os.Stdout)
	handler := clitransport.New(svc, view)

	view.ShowWelcome()
	handler.Run() // All game loop logic is in the handler
}

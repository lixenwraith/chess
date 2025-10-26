// FILE: cmd/chessd/main.go
package main

import (
	"fmt"
	"log"
	"os"

	"chess/internal/service"
)

func main() {
	// Placeholder for future API server implementation
	svc, err := service.New()
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}
	defer svc.Close()

	// TODO: Phase 2 - Add HTTP/WebSocket server here
	fmt.Println("Chess server daemon - not yet implemented")
	fmt.Println("This will host the API in Phase 2")
	os.Exit(0)
}

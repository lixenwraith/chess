// FILE: cmd/chessd/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chess/internal/service"
	"chess/internal/transport/http"
)

func main() {
	// Command-line flags
	var (
		host = flag.String("host", "localhost", "Server host")
		port = flag.Int("port", 8080, "Server port")
		dev  = flag.Bool("dev", false, "Development mode (relaxed rate limits)")
	)
	flag.Parse()

	// Initialize service (includes engine)
	svc, err := service.New()
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}
	defer func() {
		if err := svc.Close(); err != nil {
			log.Printf("Warning: failed to close service cleanly: %v", err)
		}
	}()

	// Create Fiber app with dev mode flag
	app := http.NewFiberApp(svc, *dev)

	// Server configuration
	addr := fmt.Sprintf("%s:%d", *host, *port)

	// Start server in goroutine
	go func() {
		log.Printf("Chess API Server starting...")
		log.Printf("Listening on: http://%s", addr)
		log.Printf("API Version: v1")
		log.Printf("Rate Limit: 1 request/second per IP")
		log.Printf("Endpoints: http://%s/api/v1/games", addr)
		log.Printf("Health: http://%s/health", addr)

		if err := app.Listen(addr); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.ShutdownWithTimeout(5 * time.Second); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
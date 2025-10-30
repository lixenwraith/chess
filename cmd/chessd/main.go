// FILE: cmd/chessd/main.go
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chess/internal/http"
	"chess/internal/processor"
	"chess/internal/service"
)

func main() {
	// Command-line flags
	var (
		host = flag.String("host", "localhost", "Server host")
		port = flag.Int("port", 8080, "Server port")
		dev  = flag.Bool("dev", false, "Development mode (relaxed rate limits)")
	)
	flag.Parse()

	// 1. Initialize the Service (Pure State Manager)
	svc, err := service.New()
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}
	defer svc.Close()

	// 2. Initialize the Processor (Orchestrator), injecting the service
	proc, err := processor.New(svc)
	if err != nil {
		log.Fatalf("Failed to initialize processor: %v", err)
	}
	defer func() {
		if err := proc.Close(); err != nil {
			log.Printf("Warning: failed to close processor cleanly: %v", err)
		}
	}()

	// 3. Initialize the Fiber App/HTTP Handler, injecting the processor
	app := http.NewFiberApp(proc, *dev)

	// Server configuration
	addr := fmt.Sprintf("%s:%d", *host, *port)

	// Start server in a goroutine
	go func() {
		log.Printf("Chess API Server starting...")
		log.Printf("Listening on: http://%s", addr)
		log.Printf("API Version: v1")
		if *dev {
			log.Printf("Rate Limit: 10 requests/second per IP (DEV MODE)")
		} else {
			log.Printf("Rate Limit: 1 request/second per IP")
		}
		log.Printf("Endpoints: http://%s/api/v1/games", addr)
		log.Printf("Health: http://%s/health", addr)

		if err := app.Listen(addr); err != nil {
			// This log often prints on graceful shutdown, which is normal.
			log.Printf("Server listen error: %v", err)
		}
	}()

	// Wait for an interrupt signal to gracefully shut down
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Graceful shutdown with a timeout
	if err := app.ShutdownWithTimeout(5 * time.Second); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
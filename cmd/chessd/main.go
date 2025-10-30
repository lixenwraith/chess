// FILE: cmd/chessd/main.go
package main

import (
	"chess/cmd/chessd/cli"
	"chess/internal/http"
	"chess/internal/processor"
	"chess/internal/service"
	"chess/internal/storage"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Check for CLI database commands
	if len(os.Args) > 1 && os.Args[1] == "db" {
		if err := cli.Run(os.Args[2:]); err != nil {
			log.Fatalf("CLI error: %v", err)
		}
		os.Exit(0)
	}

	// Command-line flags
	var (
		host        = flag.String("host", "localhost", "Server host")
		port        = flag.Int("port", 8080, "Server port")
		dev         = flag.Bool("dev", false, "Development mode (relaxed rate limits)")
		storagePath = flag.String("storage-path", "", "Path to SQLite database file (disables persistence if empty)")
		pidPath     = flag.String("pid", "", "Optional path to write PID file")
		pidLock     = flag.Bool("pid-lock", false, "Lock PID file to allow only one instance (requires -pid)")
	)
	flag.Parse()

	// Validate PID flags
	if *pidLock && *pidPath == "" {
		log.Fatal("Error: -pid-lock flag requires the -pid flag to be set")
	}

	// Manage PID file if requested
	if *pidPath != "" {
		cleanup, err := managePIDFile(*pidPath, *pidLock)
		if err != nil {
			log.Fatalf("Failed to manage PID file: %v", err)
		}
		defer cleanup()
		log.Printf("PID file created at: %s (lock: %v)", *pidPath, *pidLock)
	}

	// 1. Initialize Storage (optional)
	var store *storage.Store
	if *storagePath != "" {
		log.Printf("Initializing persistent storage at: %s", *storagePath)
		var err error
		store, err = storage.NewStore(*storagePath, *dev) // CHANGED: Added *dev parameter
		if err != nil {
			log.Fatalf("Failed to initialize storage: %v", err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				log.Printf("Warning: failed to close storage cleanly: %v", err)
			}
		}()
	} else {
		log.Printf("Persistent storage disabled (use -storage-path to enable)")
	}

	// 2. Initialize the Service with optional storage
	svc, err := service.New(store)
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}
	defer svc.Close()

	// 3. Initialize the Processor (Orchestrator), injecting the service
	proc, err := processor.New(svc)
	if err != nil {
		log.Fatalf("Failed to initialize processor: %v", err)
	}
	defer func() {
		if err := proc.Close(); err != nil {
			log.Printf("Warning: failed to close processor cleanly: %v", err)
		}
	}()

	// 4. Initialize the Fiber App/HTTP Handler, injecting processor and service
	app := http.NewFiberApp(proc, svc, *dev)

	// Server configuration
	addr := fmt.Sprintf("%s:%d", *host, *port)

	// Start server in a goroutine
	go func() {
		log.Printf("Chess API Server starting...")
		log.Printf("Listening on: http://%s", addr)
		log.Printf("API Version: v1")
		if *dev {
			log.Printf("Rate Limit: 20 requests/second per IP (DEV MODE)")
		} else {
			log.Printf("Rate Limit: 10 requests/second per IP")
		}
		if *storagePath != "" {
			log.Printf("Storage: Enabled (%s)", *storagePath)
		} else {
			log.Printf("Storage: Disabled")
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
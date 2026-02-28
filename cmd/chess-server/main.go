// Package main implements the chess server application with RESTful API,
// user authentication, and optional web UI serving capabilities.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chess/cmd/chess-server/cli"
	"chess/internal/server/http"
	"chess/internal/server/processor"
	"chess/internal/server/service"
	"chess/internal/server/storage"
	"chess/internal/server/webserver"
)

const (
	gracefulShutdownTimeout = time.Second * 5
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
		// API server flags (renamed)
		apiHost     = flag.String("api-host", "localhost", "API server host")
		apiPort     = flag.Int("api-port", 8080, "API server port")
		dev         = flag.Bool("dev", false, "Development mode (relaxed rate limits)")
		storagePath = flag.String("storage-path", "", "Path to SQLite database file (disables persistence if empty)")
		pidPath     = flag.String("pid", "", "Optional path to write PID file")
		pidLock     = flag.Bool("pid-lock", false, "Lock PID file to allow only one instance (requires -pid)")

		// Web UI server flags
		serve   = flag.Bool("serve", false, "Enable web UI server")
		webHost = flag.String("web-host", "localhost", "Web UI server host")
		webPort = flag.Int("web-port", 9090, "Web UI server port")
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
		store, err = storage.NewStore(*storagePath, *dev)
		if err != nil {
			log.Fatalf("Failed to initialize storage: %v", err)
		}
		if err := store.InitDB(); err != nil {
			log.Fatalf("Failed to initialize schema: %v", err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				log.Printf("Warning: failed to close storage cleanly: %v", err)
			}
		}()
	} else {
		log.Printf("Persistent storage disabled (use -storage-path to enable)")
	}

	// JWT secret management
	var jwtSecret []byte
	if *dev {
		// Fixed secret in dev mode for testing consistency
		jwtSecret = []byte("dev-secret-minimum-32-characters-long")
		log.Printf("Using fixed JWT secret (dev mode)")
	} else {
		// Generate cryptographically secure secret
		jwtSecret = make([]byte, 32)
		if _, err := rand.Read(jwtSecret); err != nil {
			log.Fatalf("Failed to generate JWT secret: %v", err)
		}
		log.Printf("JWT secret generated (sessions valid until restart)")
	}

	// 2. Initialize the Service with optional storage and auth
	svc := service.New(store, jwtSecret)

	// Start cleanup job for expired users/sessions
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	go svc.RunCleanupJob(cleanupCtx, service.CleanupJobInterval)

	// 3. Initialize the Processor (Orchestrator), injecting the service
	proc, err := processor.New(svc)
	if err != nil {
		svc.Shutdown(gracefulShutdownTimeout)
		log.Fatalf("Failed to initialize processor: %v", err)
	}

	// 4. Initialize the Fiber App/HTTP Handler, injecting processor and service
	app := http.NewFiberApp(proc, svc, *dev)

	// API Server configuration
	apiAddr := fmt.Sprintf("%s:%d", *apiHost, *apiPort)

	// Start API server in a goroutine
	go func() {
		log.Printf("Chess API Server starting...")
		log.Printf("API Listening on: http://%s", apiAddr)
		log.Printf("API Version: v1")
		log.Printf("Authentication: Enabled (JWT)")
		if *dev {
			log.Printf("Rate Limit: 20 requests/second per IP (DEV MODE)")
		} else {
			log.Printf("Rate Limit: 10 requests/second per IP")
		}
		if *storagePath != "" {
			log.Printf("Storage: Enabled (%s)", *storagePath)
		} else {
			log.Printf("Storage: Disabled (auth features unavailable)")
		}
		log.Printf("API Endpoints: http://%s/api/v1/games", apiAddr)
		log.Printf("Auth Endpoints: http://%s/api/v1/auth/[register|login|me]", apiAddr)
		log.Printf("Health: http://%s/health", apiAddr)

		if err := app.Listen(apiAddr); err != nil {
			log.Printf("API server listen error: %v", err)
		}
	}()

	// 5. Start Web UI server (optional)
	if *serve {
		webAddr := fmt.Sprintf("%s:%d", *webHost, *webPort)
		apiURL := fmt.Sprintf("http://%s", apiAddr)

		go func() {
			log.Printf("Web UI Server starting...")
			log.Printf("Web UI Listening on: http://%s", webAddr)
			log.Printf("Web UI API target: %s", apiURL)

			if err := webserver.Start(*webHost, *webPort, apiURL); err != nil {
				log.Printf("Web UI server error: %v", err)
			}
		}()
	}

	// Wait for an interrupt signal to gracefully shut down
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down servers...")

	// Graceful shutdown of service (includes wait registry)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer shutdownCancel()

	// Graceful shutdown of HTTP server with timeout
	if err = app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	// Close processor after service shutdown
	if err = proc.Close(); err != nil {
		log.Printf("Processor close error: %v", err)
	}

	cleanupCancel() // Stop cleanup job

	// Shutdown service first (includes wait registry cleanup)
	if err = svc.Shutdown(gracefulShutdownTimeout); err != nil {
		log.Printf("Service shutdown error: %v", err)
	}

	log.Println("Servers exited")
}


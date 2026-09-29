// Package main implements the chess server application with RESTful API,
// user authentication, and optional web UI serving capabilities.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"log/slog"
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
		dsn         = flag.String("dsn", "", "PostgreSQL connection string (default $CHESS_DSN; persistence is disabled if empty)")
		pidPath     = flag.String("pid", "", "Optional path to write PID file")
		pidLock     = flag.Bool("pid-lock", false, "Lock PID file to allow only one instance (requires -pid)")
		logLevel    = flag.String("log-level", "info", "Log level: debug, info, warn, or error")
		logHTTP     = flag.Bool("log-http", true, "Log HTTP requests")
		proxies     = flag.String("trusted-proxies", "", "Comma-separated reverse-proxy IPs/CIDRs whose -proxy-header is trusted for client IPs")
		proxyHeader = flag.String("proxy-header", "X-Real-IP", "Header carrying the client IP from a trusted proxy")
		finishedTTL = flag.Duration("finished-game-ttl", service.FinishedGameTTL, "How long completed games remain in memory (0 disables eviction)")
		anonTTL     = flag.Duration("anonymous-game-ttl", service.AnonymousGameTTL, "How long games without a registered player survive after their last activity, in memory and in the database (0 keeps them)")
		maxUsers    = flag.Int("max-users", service.DefaultMaxUsers, "Accounts at which public registration closes (0 = no limit; CLI-created accounts are not limited)")
		jwtFile     = flag.String("jwt-secret-file", "", "File holding a stable JWT signing key, mode 0600, at least 32 bytes (default $CHESS_JWT_SECRET_FILE)")
		dbCleanup   = flag.String("db-cleanup", "off", "Hourly integrity sweep of stored games and accounts: off, report (log only), or delete")

		// Web UI server flags
		serve     = flag.Bool("serve", false, "Enable web UI server")
		webHost   = flag.String("web-host", "localhost", "Web UI server host")
		webPort   = flag.Int("web-port", 9090, "Web UI server port")
		webAPIURL = flag.String("web-api-url", "", "Browser-visible API base URL (defaults to the API listen address)")
	)
	flag.Parse()
	// Environment fallbacks are applied after parsing so -h never prints a
	// DSN, which may contain a password.
	if *dsn == "" {
		*dsn = os.Getenv("CHESS_DSN")
	}
	if *jwtFile == "" {
		*jwtFile = os.Getenv("CHESS_JWT_SECRET_FILE")
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		log.Fatalf("Invalid -log-level %q: %v", *logLevel, err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				attr.Value = slog.TimeValue(attr.Value.Time().UTC())
			}
			return attr
		},
	})))
	// slog.SetDefault bridges the standard logger through the structured
	// handler. Avoid embedding a second timestamp inside its message.
	log.SetFlags(0)

	trustedProxies, err := parseTrustedProxies(*proxies)
	if err != nil {
		log.Fatalf("Invalid -trusted-proxies: %v", err)
	}

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

	// 1. Initialize Storage (optional). The DSN is never logged: it may carry a
	// password when peer or .pgpass authentication is not used.
	var store *storage.Store
	if *dsn != "" {
		log.Printf("Initializing PostgreSQL storage")
		var err error
		store, err = storage.NewStore(*dsn)
		if err != nil {
			log.Fatalf("Failed to initialize storage: %v", err)
		}
		if err := store.InitDB(); err != nil {
			log.Fatalf("Failed to initialize schema: %v", err)
		}
	} else {
		log.Printf("Persistent storage disabled (use -dsn or CHESS_DSN to enable)")
	}

	// JWT signing key: a key file keeps sessions valid across restarts; without
	// one, dev mode uses a fixed key and production generates a per-process key.
	var jwtSecret []byte
	switch {
	case *jwtFile != "":
		var err error
		if jwtSecret, err = loadJWTSecret(*jwtFile); err != nil {
			log.Fatalf("Failed to load JWT secret: %v", err)
		}
		log.Printf("JWT secret loaded from file (sessions survive restarts)")
	case *dev:
		jwtSecret = []byte("dev-secret-minimum-32-characters-long")
		log.Printf("Using fixed JWT secret (dev mode)")
	default:
		jwtSecret = make([]byte, 32)
		rand.Read(jwtSecret)
		log.Printf("JWT secret generated (sessions valid until restart; use -jwt-secret-file to persist)")
	}

	// 2. Initialize the Service with optional storage and auth
	svc, err := service.New(store, jwtSecret)
	clear(jwtSecret) // the JWT manager keeps its own copy
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}
	svc.SetFinishedGameTTL(*finishedTTL)
	svc.SetAnonymousGameTTL(*anonTTL)
	svc.SetMaxUsers(*maxUsers)
	integrity, err := service.ParseIntegrityMode(*dbCleanup)
	if err != nil {
		log.Fatalf("Invalid -db-cleanup: %v", err)
	}
	if integrity != service.IntegrityOff && store == nil {
		log.Fatalf("-db-cleanup %s requires a database (-dsn)", integrity)
	}
	svc.SetIntegrityMode(integrity)

	// Start cleanup job for expired users/sessions
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		svc.RunCleanupJob(cleanupCtx, service.CleanupJobInterval)
	}()

	// 3. Initialize the Processor (Orchestrator), injecting the service
	proc, err := processor.New(svc)
	if err != nil {
		cleanupCancel()
		<-cleanupDone
		svc.Shutdown(gracefulShutdownTimeout)
		log.Fatalf("Failed to initialize processor: %v", err)
	}

	// 4. Initialize the Fiber App/HTTP Handler, injecting processor and service
	app := http.NewFiberApp(proc, svc, http.Options{
		DevMode:        *dev,
		LogRequests:    *logHTTP,
		TrustedProxies: trustedProxies,
		ProxyHeader:    *proxyHeader,
	})

	// API Server configuration
	apiAddr := fmt.Sprintf("%s:%d", *apiHost, *apiPort)

	// Start API server in a goroutine
	go func() {
		log.Printf("Chess API Server starting...")
		log.Printf("API Listening on: http://%s", apiAddr)
		log.Printf("Authentication: Enabled (JWT)")
		if *dev {
			log.Printf("Rate Limit: 20 requests/second per IP (DEV MODE)")
		} else {
			log.Printf("Rate Limit: 10 requests/second per IP")
		}
		if len(trustedProxies) > 0 {
			log.Printf("Client IP: %s from trusted proxies %v", *proxyHeader, trustedProxies)
		} else {
			log.Printf("Client IP: TCP peer (behind a reverse proxy, set -trusted-proxies or all clients share one rate limit)")
		}
		if store != nil {
			log.Printf("Storage: Enabled (PostgreSQL)")
		} else {
			log.Printf("Storage: Disabled (auth features unavailable)")
		}
		log.Printf("API Endpoints: http://%s/api/games", apiAddr)
		log.Printf("Auth Endpoints: http://%s/api/auth/[register|login|me]", apiAddr)
		log.Printf("Health: http://%s/health", apiAddr)

		if err := app.Listen(apiAddr); err != nil {
			log.Printf("API server listen error: %v", err)
		}
	}()

	// 5. Start Web UI server (optional)
	if *serve {
		webAddr := fmt.Sprintf("%s:%d", *webHost, *webPort)
		apiURL := fmt.Sprintf("http://%s", apiAddr)
		if *webAPIURL != "" {
			apiURL = *webAPIURL
		}

		go func() {
			log.Printf("Web UI Server starting...")
			log.Printf("Web UI Listening on: http://%s", webAddr)
			log.Printf("Web UI API target: %s", apiURL)

			if err := webserver.Start(*webHost, *webPort, apiURL, *logHTTP); err != nil {
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

	cleanupCancel() // Stop cleanup before closing processor and storage.
	<-cleanupDone

	// Close processor before the service so engine callbacks have settled.
	if err = proc.Close(); err != nil {
		log.Printf("Processor close error: %v", err)
	}

	// Shutdown service (wait registry, accepted storage writes, database).
	if err = svc.Shutdown(gracefulShutdownTimeout); err != nil {
		log.Printf("Service shutdown error: %v", err)
	}

	log.Println("Servers exited")
}

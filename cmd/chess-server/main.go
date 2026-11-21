// Package main implements the chess server application with RESTful API,
// user authentication, and optional web UI serving capabilities.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lixenwraith/chess/cmd/chess-server/cli"
	"github.com/lixenwraith/chess/internal/server/http"
	"github.com/lixenwraith/chess/internal/server/mail"
	"github.com/lixenwraith/chess/internal/server/metrics"
	"github.com/lixenwraith/chess/internal/server/processor"
	"github.com/lixenwraith/chess/internal/server/service"
	"github.com/lixenwraith/chess/internal/server/storage"
	"github.com/lixenwraith/chess/internal/server/webserver"
)

const (
	gracefulShutdownTimeout = time.Second * 5
)

// version is set at link time (make: -X main.version=...); /health reports it
// so a deployment can be checked against the build it should be running.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "db" {
		if err := cli.Run(os.Args[2:]); err != nil {
			log.Fatalf("CLI error: %v", err)
		}
		os.Exit(0)
	}

	var (
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
		idleTTL     = flag.Duration("idle-game-ttl", service.IdleGameTTL, "How long an unfinished game with a registered player stays in memory after its last activity; it stays stored (0 keeps it)")
		maxUsers    = flag.Int("max-users", service.DefaultMaxUsers, "Accounts at which public registration closes (0 = no limit; CLI-created accounts are not limited)")
		jwtFile     = flag.String("jwt-secret-file", "", "File holding a stable JWT signing key, mode 0600, at least 32 bytes (default $CHESS_JWT_SECRET_FILE)")
		dbCleanup   = flag.String("db-cleanup", "off", "Hourly integrity sweep of stored games and accounts: off, report (log only), or delete")
		metricsTick = flag.Duration("metrics-interval", time.Minute, "How often the server's measures are logged as one \"metrics\" line (0 disables)")

		// Password recovery by email: a transport (SMTP relay, or a directory
		// of .eml files for development) and the web client's address, which
		// the emailed link opens. Without a transport, recovery is off. The
		// address's host also names this server in exported PGN (Site).
		smtpHost     = flag.String("smtp-host", "", "SMTP relay for password-reset email (recovery is off when empty)")
		smtpPort     = flag.Int("smtp-port", 25, "SMTP relay port")
		smtpUser     = flag.String("smtp-user", "", "SMTP user name (optional; the password comes from -smtp-password-file)")
		smtpPassFile = flag.String("smtp-password-file", "", "File holding the SMTP password, mode 0600")
		mailFrom     = flag.String("mail-from", "", "Sender address of server email, e.g. \"Chess <chess@example.com>\"")
		mailDir      = flag.String("mail-dir", "", "Write email to .eml files in this directory instead of sending (development)")
		publicURL    = flag.String("public-url", "", "Web client address: emailed links open it, and its host is the Site of exported games, e.g. https://example.com/chess/web-client/")

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

	if *pidLock && *pidPath == "" {
		log.Fatal("Error: -pid-lock flag requires the -pid flag to be set")
	}

	if *pidPath != "" {
		cleanup, err := managePIDFile(*pidPath, *pidLock)
		if err != nil {
			log.Fatalf("Failed to manage PID file: %v", err)
		}
		defer cleanup()
		log.Printf("PID file created at: %s (lock: %v)", *pidPath, *pidLock)
	}

	// The DSN is never logged: it may carry a password when peer or .pgpass
	// authentication is not used.
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

	svc, err := service.New(store, jwtSecret)
	clear(jwtSecret) // the JWT manager keeps its own copy
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}
	svc.SetFinishedGameTTL(*finishedTTL)
	svc.SetAnonymousGameTTL(*anonTTL)
	svc.SetIdleGameTTL(*idleTTL)
	svc.SetMaxUsers(*maxUsers)
	integrity, err := service.ParseIntegrityMode(*dbCleanup)
	if err != nil {
		log.Fatalf("Invalid -db-cleanup: %v", err)
	}
	if integrity != service.IntegrityOff && store == nil {
		log.Fatalf("-db-cleanup %s requires a database (-dsn)", integrity)
	}
	svc.SetIntegrityMode(integrity)

	if *publicURL != "" {
		u, err := url.Parse(*publicURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			log.Fatalf("Invalid -public-url %q: must be absolute (https://host/chess/web-client/)", *publicURL)
		}
		svc.SetSite(u.Hostname())
	}

	// Password recovery needs mail and the client's public address.
	sender, err := mail.Config{
		SMTPHost: *smtpHost, SMTPPort: *smtpPort, SMTPUser: *smtpUser, SMTPPasswordFile: *smtpPassFile,
		Dir: *mailDir, From: *mailFrom,
	}.Build()
	switch {
	case errors.Is(err, mail.ErrNoSender):
		log.Printf("Password recovery disabled (no -smtp-host or -mail-dir)")
	case err != nil:
		log.Fatalf("Invalid mail configuration: %v", err)
	case store == nil:
		log.Fatalf("Password recovery (-smtp-host or -mail-dir) requires a database (-dsn)")
	case *publicURL == "":
		log.Fatalf("Password recovery requires -public-url, the web client address that reset links open")
	default:
		if err := svc.SetMailer(sender, *publicURL); err != nil {
			log.Fatalf("Invalid -public-url: %v", err)
		}
		log.Printf("Password recovery enabled: %s; links open %s", sender.Describe(), *publicURL)
	}

	// The server's lifetime: requests and the cleanup job run within it.
	serverCtx, serverCancel := context.WithCancel(context.Background())
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		svc.RunCleanupJob(serverCtx, service.CleanupJobInterval)
	}()

	proc, err := processor.New(svc)
	if err != nil {
		serverCancel()
		<-cleanupDone
		svc.Shutdown(gracefulShutdownTimeout)
		log.Fatalf("Failed to initialize processor: %v", err)
	}

	if *metricsTick > 0 {
		go metrics.Report(serverCtx, *metricsTick, svc.Metrics, proc.Metrics)
	}

	app := http.NewFiberApp(proc, svc, http.Options{
		DevMode:        *dev,
		LogRequests:    *logHTTP,
		TrustedProxies: trustedProxies,
		ProxyHeader:    *proxyHeader,
		Version:        version,
		Context:        serverCtx,
	})

	apiAddr := fmt.Sprintf("%s:%d", *apiHost, *apiPort)

	go func() {
		log.Printf("Chess API Server %s starting...", version)
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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down servers...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer shutdownCancel()

	// Long-polls hold no state: end them first, so the drain waits only for
	// requests doing work. Cancelling the server context then stops what
	// outlived the drain, and the cleanup job, before storage closes.
	if err = svc.EndWaits(gracefulShutdownTimeout); err != nil {
		log.Printf("Long-poll shutdown error: %v", err)
	}
	if err = app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}
	serverCancel()
	<-cleanupDone

	// Close processor before the service so engine callbacks have settled.
	if err = proc.Close(); err != nil {
		log.Printf("Processor close error: %v", err)
	}

	if err = svc.Shutdown(gracefulShutdownTimeout); err != nil {
		log.Printf("Service shutdown error: %v", err)
	}

	log.Println("Servers exited")
}

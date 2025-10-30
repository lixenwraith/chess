// FILE: cmd/chessd/cli/cli.go
package cli

import (
	"chess/internal/storage"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

// Run is the entry point for the CLI mini-app
func Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("subcommand required: init, delete, or query")
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "delete":
		return runDelete(args[1:])
	case "query":
		return runQuery(args[1:])
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}
	defer store.Close()

	if err := store.InitDB(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	fmt.Printf("Database initialized at: %s\n", *path)
	return nil
}

func runDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}

	if err := store.DeleteDB(); err != nil {
		return fmt.Errorf("failed to delete database: %w", err)
	}

	fmt.Printf("Database deleted: %s\n", *path)
	return nil
}

func runQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	gameID := fs.String("gameId", "", "Game ID to filter (optional, * for all)")
	playerID := fs.String("playerId", "", "Player ID to filter (optional, * for all)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	games, err := store.QueryGames(*gameID, *playerID)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}

	if len(games) == 0 {
		fmt.Println("No games found")
		return nil
	}

	// Print results in tabular format
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Game ID\tWhite Player\tBlack Player\tStart Time")
	fmt.Fprintln(w, strings.Repeat("-", 80))

	for _, g := range games {
		whiteInfo := fmt.Sprintf("%s (T%d)", g.WhitePlayerID[:8], g.WhiteType)
		blackInfo := fmt.Sprintf("%s (T%d)", g.BlackPlayerID[:8], g.BlackType)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			g.GameID[:8]+"...",
			whiteInfo,
			blackInfo,
			g.StartTimeUTC.Format("2006-01-02 15:04:05"),
		)
	}
	w.Flush()

	fmt.Printf("\nFound %d game(s)\n", len(games))
	return nil
}
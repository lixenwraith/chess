package cli

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"chess/internal/server/storage"

	"github.com/google/uuid"
	"github.com/lixenwraith/auth"
	"golang.org/x/term"
)

const dsnEnv = "CHESS_DSN"

// Run is the entry point for the CLI mini-app
func Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("subcommand required: init, delete, query, user")
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "delete":
		return runDelete(args[1:])
	case "query":
		return runQuery(args[1:])
	case "user":
		if len(args) < 2 {
			return fmt.Errorf("user subcommand required: add, delete, set-password, set-hash, set-email, set-username, list")
		}
		return runUser(args[1], args[2:])
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

// newFlagSet returns a flag set with the shared -dsn flag. An empty -dsn falls
// back to $CHESS_DSN (in openStore, so usage output never prints it), which
// keeps a password-bearing DSN out of process arguments.
func newFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "PostgreSQL connection string (default $"+dsnEnv+")")
	return fs, dsn
}

// openStore connects and, unless migrating, verifies the schema is current.
func openStore(dsn string, requireSchema bool) (*storage.Store, error) {
	if dsn == "" {
		dsn = os.Getenv(dsnEnv)
	}
	if dsn == "" {
		return nil, fmt.Errorf("database connection required: use -dsn or %s", dsnEnv)
	}
	store, err := storage.NewStore(dsn)
	if err != nil {
		return nil, err
	}
	if requireSchema {
		if err := store.CheckSchema(); err != nil {
			store.Close()
			return nil, err
		}
	}
	return store, nil
}

func runInit(args []string) error {
	fs, dsn := newFlagSet("init")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := openStore(*dsn, false)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.InitDB(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	version, err := store.SchemaVersion()
	if err != nil {
		return err
	}
	fmt.Printf("Database schema ready (version %d)\n", version)
	return nil
}

func runDelete(args []string) error {
	fs, dsn := newFlagSet("delete")
	confirm := fs.Bool("confirm", false, "Confirm dropping all chess tables and data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return fmt.Errorf("refusing to drop all chess tables without -confirm")
	}

	store, err := openStore(*dsn, false)
	if err != nil {
		return err
	}
	defer store.Close()

	// ☣ DESTRUCTIVE: drops every chess table in the connection's search_path
	if err := store.DropSchema(); err != nil {
		return err
	}
	fmt.Println("Chess tables dropped")
	return nil
}

func runQuery(args []string) error {
	fs, dsn := newFlagSet("query")
	gameID := fs.String("gameId", "", "Game ID to filter (optional, * for all)")
	playerID := fs.String("playerId", "", "Player or user ID to filter (optional, * for all)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
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
	fmt.Fprintln(w, "Game ID\tWhite Player\tBlack Player\tResult\tStarted\tEnded")
	fmt.Fprintln(w, strings.Repeat("-", 130))

	for _, g := range games {
		whiteInfo := formatStoredPlayer(g.WhitePlayerID, g.WhiteClaimedBy, g.WhiteType)
		blackInfo := formatStoredPlayer(g.BlackPlayerID, g.BlackClaimedBy, g.BlackType)
		result := g.Result
		if result == "" {
			result = "ongoing"
		}
		ended := "-"
		if g.EndTimeUTC != nil {
			ended = g.EndTimeUTC.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			abbreviateID(g.GameID),
			whiteInfo,
			blackInfo,
			result,
			g.StartTimeUTC.Format("2006-01-02 15:04:05"),
			ended,
		)
	}
	w.Flush()

	fmt.Printf("\nFound %d game(s)\n", len(games))
	return nil
}

func formatStoredPlayer(playerID, claimedBy string, playerType int) string {
	value := fmt.Sprintf("%s (T%d)", abbreviateID(playerID), playerType)
	if claimedBy != "" && claimedBy != playerID {
		value += " claim:" + abbreviateID(claimedBy)
	}
	return value
}

func abbreviateID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8] + "..."
}

func runUser(subcommand string, args []string) error {
	switch subcommand {
	case "add":
		return runUserAdd(args)
	case "delete":
		return runUserDelete(args)
	case "set-password":
		return runUserSetPassword(args)
	case "set-hash":
		return runUserSetHash(args)
	case "set-email":
		return runUserSetEmail(args)
	case "set-username":
		return runUserSetUsername(args)
	case "list":
		return runUserList(args)
	default:
		return fmt.Errorf("unknown user subcommand: %s", subcommand)
	}
}

// readPassword prompts on the terminal without echo.
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	pwBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}
	return string(pwBytes), nil
}

func hashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return hash, nil
}

func runUserAdd(args []string) error {
	fs, dsn := newFlagSet("user add")
	username := fs.String("username", "", "Username (required)")
	email := fs.String("email", "", "Email address (optional)")
	password := fs.String("password", "", "Password (optional, will prompt if not provided)")
	hash := fs.String("hash", "", "Pre-computed password hash (optional)")
	interactive := fs.Bool("interactive", false, "Interactive password prompt")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}
	if *password != "" && *hash != "" {
		return fmt.Errorf("cannot specify both -password and -hash")
	}

	var passwordHash string
	switch {
	case *interactive:
		if *password != "" || *hash != "" {
			return fmt.Errorf("cannot use -interactive with -password or -hash")
		}
		entered, err := readPassword("Enter password: ")
		if err != nil {
			return err
		}
		if passwordHash, err = hashPassword(entered); err != nil {
			return err
		}
	case *hash != "":
		if err := auth.ValidatePHCHashFormat(*hash); err != nil {
			return fmt.Errorf("invalid hash format: %w", err)
		}
		passwordHash = *hash
	case *password != "":
		var err error
		if passwordHash, err = hashPassword(*password); err != nil {
			return err
		}
	default:
		return fmt.Errorf("password required: use -password, -hash, or -interactive")
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	userID := uuid.NewString()
	record := storage.UserRecord{
		UserID:       userID,
		Username:     strings.ToLower(*username),
		Email:        strings.ToLower(*email),
		PasswordHash: passwordHash,
		CreatedAt:    time.Now().UTC(),
	}

	if err := store.CreateUser(record); err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	fmt.Printf("User created successfully:\n")
	fmt.Printf("  ID: %s\n", userID)
	fmt.Printf("  Username: %s\n", record.Username)
	if record.Email != "" {
		fmt.Printf("  Email: %s\n", record.Email)
	}
	return nil
}

// lookupUser resolves -username or -id to a user ID.
func lookupUser(store *storage.Store, username, userID string) (string, error) {
	switch {
	case username == "" && userID == "":
		return "", fmt.Errorf("either -username or -id required")
	case username != "" && userID != "":
		return "", fmt.Errorf("specify either -username or -id, not both")
	case userID != "":
		if _, err := store.GetUserByID(userID); err != nil {
			return "", userLookupError(userID, err)
		}
		return userID, nil
	default:
		user, err := store.GetUserByUsername(username)
		if err != nil {
			return "", userLookupError(username, err)
		}
		return user.UserID, nil
	}
}

func userLookupError(identifier string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("user not found: %s", identifier)
	}
	return fmt.Errorf("failed to look up user %s: %w", identifier, err)
}

func runUserDelete(args []string) error {
	fs, dsn := newFlagSet("user delete")
	username := fs.String("username", "", "Username to delete")
	userID := fs.String("id", "", "User ID to delete")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	targetID, err := lookupUser(store, *username, *userID)
	if err != nil {
		return err
	}
	if err := store.DeleteUser(targetID); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	fmt.Printf("User deleted: %s\n", targetID)
	return nil
}

func runUserSetPassword(args []string) error {
	fs, dsn := newFlagSet("user set-password")
	username := fs.String("username", "", "Username (required)")
	password := fs.String("password", "", "New password")
	interactive := fs.Bool("interactive", false, "Interactive password prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}

	var newPassword string
	switch {
	case *interactive:
		if *password != "" {
			return fmt.Errorf("cannot use -interactive with -password")
		}
		entered, err := readPassword("Enter new password: ")
		if err != nil {
			return err
		}
		newPassword = entered
	case *password != "":
		newPassword = *password
	default:
		return fmt.Errorf("password required: use -password or -interactive")
	}
	passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	targetID, err := lookupUser(store, *username, "")
	if err != nil {
		return err
	}
	if err := store.UpdateUserPassword(targetID, passwordHash); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	fmt.Printf("Password updated for user: %s\n", *username)
	return nil
}

func runUserSetHash(args []string) error {
	fs, dsn := newFlagSet("user set-hash")
	username := fs.String("username", "", "Username (required)")
	hash := fs.String("hash", "", "Password hash (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}
	if *hash == "" {
		return fmt.Errorf("password hash required")
	}
	if err := auth.ValidatePHCHashFormat(*hash); err != nil {
		return fmt.Errorf("invalid hash format: %w", err)
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	targetID, err := lookupUser(store, *username, "")
	if err != nil {
		return err
	}
	if err := store.UpdateUserPassword(targetID, *hash); err != nil {
		return fmt.Errorf("failed to update password hash: %w", err)
	}

	fmt.Printf("Password hash updated for user: %s\n", *username)
	return nil
}

func runUserSetEmail(args []string) error {
	fs, dsn := newFlagSet("user set-email")
	username := fs.String("username", "", "Username (required)")
	email := fs.String("email", "", "New email address (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}
	if *email == "" {
		return fmt.Errorf("email required")
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	targetID, err := lookupUser(store, *username, "")
	if err != nil {
		return err
	}
	if err := store.UpdateUserEmail(targetID, *email); err != nil {
		return fmt.Errorf("failed to update email: %w", err)
	}

	fmt.Printf("Email updated for user: %s\n", *username)
	return nil
}

func runUserSetUsername(args []string) error {
	fs, dsn := newFlagSet("user set-username")
	current := fs.String("current", "", "Current username (required)")
	newName := fs.String("new", "", "New username (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *current == "" {
		return fmt.Errorf("current username required")
	}
	if *newName == "" {
		return fmt.Errorf("new username required")
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	targetID, err := lookupUser(store, *current, "")
	if err != nil {
		return err
	}
	if err := store.UpdateUserUsername(targetID, *newName); err != nil {
		return fmt.Errorf("failed to update username: %w", err)
	}

	fmt.Printf("Username updated: %s -> %s\n", *current, strings.ToLower(*newName))
	return nil
}

func runUserList(args []string) error {
	fs, dsn := newFlagSet("user list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := openStore(*dsn, true)
	if err != nil {
		return err
	}
	defer store.Close()

	users, err := store.GetAllUsers()
	if err != nil {
		return fmt.Errorf("failed to list users: %w", err)
	}

	if len(users) == 0 {
		fmt.Println("No users found")
		return nil
	}

	// Print results in tabular format
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "User ID\tUsername\tEmail\tCreated\tLast Login")
	fmt.Fprintln(w, strings.Repeat("-", 100))

	for _, u := range users {
		lastLogin := "never"
		if u.LastLoginAt != nil {
			lastLogin = u.LastLoginAt.Format("2006-01-02 15:04")
		}
		email := u.Email
		if email == "" {
			email = "(none)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			abbreviateID(u.UserID),
			u.Username,
			email,
			u.CreatedAt.Format("2006-01-02 15:04"),
			lastLogin,
		)
	}
	w.Flush()

	fmt.Printf("\nTotal users: %d\n", len(users))
	return nil
}

package cli

import (
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

func runUserAdd(args []string) error {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	username := fs.String("username", "", "Username (required)")
	email := fs.String("email", "", "Email address (optional)")
	password := fs.String("password", "", "Password (optional, will prompt if not provided)")
	hash := fs.String("hash", "", "Pre-computed password hash (optional)")
	interactive := fs.Bool("interactive", false, "Interactive password prompt")
	temp := fs.Bool("temp", false, "Create as temporary user (24h TTL, default: permanent)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}

	// Validate password/hash options
	if *password != "" && *hash != "" {
		return fmt.Errorf("cannot specify both -password and -hash")
	}

	var passwordHash string

	if *interactive {
		if *password != "" || *hash != "" {
			return fmt.Errorf("cannot use -interactive with -password or -hash")
		}
		fmt.Print("Enter password: ")
		pwBytes, err := term.ReadPassword(syscall.Stdin)
		fmt.Println()
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}
		if len(pwBytes) < 8 {
			return fmt.Errorf("password must be at least 8 characters")
		}

		// Hash password (Argon2)
		passwordHash, err = auth.HashPassword(string(pwBytes))
		if err != nil {
			return fmt.Errorf("failed to hash password: %w", err)
		}
	} else if *hash != "" {
		passwordHash = *hash
	} else if *password != "" {
		if len(*password) < 8 {
			return fmt.Errorf("password must be at least 8 characters")
		}
		// Hash password (Argon2)
		var err error
		passwordHash, err = auth.HashPassword(*password)
		if err != nil {
			return fmt.Errorf("failed to hash password: %w", err)
		}
	} else {
		return fmt.Errorf("password required: use -password, -hash, or -interactive")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	// Generate user ID with conflict check
	var userID string
	for attempts := 0; attempts < 10; attempts++ {
		userID = uuid.New().String()
		if _, err := store.GetUserByID(userID); err != nil {
			// User doesn't exist, ID is unique
			break
		}
		if attempts == 9 {
			return fmt.Errorf("failed to generate unique user ID after 10 attempts")
		}
	}

	// Determine account type (CLI default = permanent)
	accountType := "permanent"
	var expiresAt *time.Time
	if *temp {
		accountType = "temp"
		expiry := time.Now().UTC().Add(24 * time.Hour)
		expiresAt = &expiry
	}

	record := storage.UserRecord{
		UserID:       userID,
		Username:     strings.ToLower(*username),
		Email:        strings.ToLower(*email),
		PasswordHash: passwordHash,
		AccountType:  accountType,
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    expiresAt,
	}

	if err := store.CreateUser(record); err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	fmt.Printf("User created successfully:\n")
	fmt.Printf("  ID: %s\n", userID)
	fmt.Printf("  Username: %s\n", *username)
	if *email != "" {
		fmt.Printf("  Email: %s\n", *email)
	}
	return nil
}

func runUserDelete(args []string) error {
	fs := flag.NewFlagSet("user delete", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	username := fs.String("username", "", "Username to delete")
	userID := fs.String("id", "", "User ID to delete")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}
	if *username == "" && *userID == "" {
		return fmt.Errorf("either -username or -id required")
	}
	if *username != "" && *userID != "" {
		return fmt.Errorf("specify either -username or -id, not both")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	var targetID string
	if *userID != "" {
		targetID = *userID
	} else {
		user, err := store.GetUserByUsername(*username)
		if err != nil {
			return fmt.Errorf("user not found: %s", *username)
		}
		targetID = user.UserID
	}

	if err := store.DeleteUser(targetID); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	fmt.Printf("User deleted: %s\n", targetID)
	return nil
}

func runUserSetPassword(args []string) error {
	fs := flag.NewFlagSet("user set-password", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	username := fs.String("username", "", "Username (required)")
	password := fs.String("password", "", "New password")
	interactive := fs.Bool("interactive", false, "Interactive password prompt")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}

	var newPassword string
	if *interactive {
		if *password != "" {
			return fmt.Errorf("cannot use -interactive with -password")
		}
		fmt.Print("Enter new password: ")
		pwBytes, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}
		newPassword = string(pwBytes)
	} else if *password != "" {
		newPassword = *password
	} else {
		return fmt.Errorf("password required: use -password or -interactive")
	}

	if len(newPassword) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	// Get user
	user, err := store.GetUserByUsername(*username)
	if err != nil {
		return fmt.Errorf("user not found: %s", *username)
	}

	// Hash password (Argon2)
	passwordHash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Update password
	if err := store.UpdateUserPassword(user.UserID, passwordHash); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	fmt.Printf("Password updated for user: %s\n", *username)
	return nil
}

func runUserSetHash(args []string) error {
	fs := flag.NewFlagSet("user set-hash", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	username := fs.String("username", "", "Username (required)")
	hash := fs.String("hash", "", "Password hash (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
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

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	// Get user
	user, err := store.GetUserByUsername(*username)
	if err != nil {
		return fmt.Errorf("user not found: %s", *username)
	}

	// Update password hash directly
	if err := store.UpdateUserPassword(user.UserID, *hash); err != nil {
		return fmt.Errorf("failed to update password hash: %w", err)
	}

	fmt.Printf("Password hash updated for user: %s\n", *username)
	return nil
}

func runUserSetEmail(args []string) error {
	fs := flag.NewFlagSet("user set-email", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	username := fs.String("username", "", "Username (required)")
	email := fs.String("email", "", "New email address (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}
	if *username == "" {
		return fmt.Errorf("username required")
	}
	if *email == "" {
		return fmt.Errorf("email required")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	// Get user
	user, err := store.GetUserByUsername(*username)
	if err != nil {
		return fmt.Errorf("user not found: %s", *username)
	}

	// Update email
	if err := store.UpdateUserEmail(user.UserID, strings.ToLower(*email)); err != nil {
		return fmt.Errorf("failed to update email: %w", err)
	}

	fmt.Printf("Email updated for user: %s\n", *username)
	return nil
}

func runUserSetUsername(args []string) error {
	fs := flag.NewFlagSet("user set-username", flag.ContinueOnError)
	path := fs.String("path", "", "Database file path (required)")
	current := fs.String("current", "", "Current username (required)")
	new := fs.String("new", "", "New username (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return fmt.Errorf("database path required")
	}
	if *current == "" {
		return fmt.Errorf("current username required")
	}
	if *new == "" {
		return fmt.Errorf("new username required")
	}

	store, err := storage.NewStore(*path, false)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer store.Close()

	// Get user
	user, err := store.GetUserByUsername(*current)
	if err != nil {
		return fmt.Errorf("user not found: %s", *current)
	}

	// Update username
	if err := store.UpdateUserUsername(user.UserID, strings.ToLower(*new)); err != nil {
		return fmt.Errorf("failed to update username: %w", err)
	}

	fmt.Printf("Username updated: %s -> %s\n", *current, *new)
	return nil
}

func runUserList(args []string) error {
	fs := flag.NewFlagSet("user list", flag.ContinueOnError)
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
	fmt.Fprintln(w, "User ID\tUsername\tType\tEmail\tCreated\tExpires\tLast Login")
	fmt.Fprintln(w, strings.Repeat("-", 120))

	for _, u := range users {
		lastLogin := "never"
		if u.LastLoginAt != nil {
			lastLogin = u.LastLoginAt.Format("2006-01-02 15:04")
		}
		email := u.Email
		if email == "" {
			email = "(none)"
		}
		expires := "never"
		if u.ExpiresAt != nil {
			expires = u.ExpiresAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			u.UserID[:8]+"...",
			u.Username,
			u.AccountType,
			email,
			u.CreatedAt.Format("2006-01-02 15:04"),
			expires,
			lastLogin,
		)
	}
	w.Flush()

	fmt.Printf("\nTotal users: %d\n", len(users))
	return nil
}
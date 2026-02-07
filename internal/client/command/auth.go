package command

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"chess/internal/client/api"
	"chess/internal/client/display"
	"chess/internal/client/session"
)

func (r *Registry) registerAuthCommands() {
	r.Register(&Command{
		Name:        "register",
		ShortName:   "r",
		Description: "Register a new user",
		Usage:       "register",
		Handler:     registerHandler,
	})

	r.Register(&Command{
		Name:        "login",
		ShortName:   "l",
		Description: "Login with credentials",
		Usage:       "login",
		Handler:     loginHandler,
	})

	r.Register(&Command{
		Name:        "logout",
		ShortName:   "o",
		Description: "Clear authentication",
		Usage:       "logout",
		Handler:     logoutHandler,
	})

	r.Register(&Command{
		Name:        "whoami",
		ShortName:   "i",
		Description: "Show current user",
		Usage:       "whoami",
		Handler:     whoamiHandler,
	})

	r.Register(&Command{
		Name:        "user",
		ShortName:   "e",
		Description: "Set user ID manually",
		Usage:       "user <userId>",
		Handler:     setUserHandler,
	})
}

func registerHandler(s *session.Session, args []string) error {
	scanner := bufio.NewScanner(os.Stdin)
	c := s.GetClient().(*api.Client)

	display.Print(display.Yellow, "Username: ")
	scanner.Scan()
	username := strings.TrimSpace(scanner.Text())

	password, err := readPassword(display.Yellow + "Password: " + display.Reset)
	if err != nil {
		return err
	}

	display.Print(display.Yellow, "Email (optional): ")
	scanner.Scan()
	email := strings.TrimSpace(scanner.Text())

	resp, err := c.Register(username, password, email)
	if err != nil {
		return err
	}

	s.SetAuthToken(resp.Token)
	s.SetCurrentUser(resp.UserID)
	s.SetUsername(resp.Username)
	c.SetToken(resp.Token)

	display.Println(display.Green, "Registered successfully")
	fmt.Printf("User ID: %s\n", resp.UserID)
	fmt.Printf("Username: %s\n", resp.Username)

	return nil
}

func loginHandler(s *session.Session, args []string) error {
	scanner := bufio.NewScanner(os.Stdin)
	c := s.GetClient().(*api.Client)

	display.Print(display.Yellow, "Username or Email: ")
	scanner.Scan()
	identifier := strings.TrimSpace(scanner.Text())

	password, err := readPassword(display.Yellow + "Password: " + display.Reset)
	if err != nil {
		return err
	}

	resp, err := c.Login(identifier, password)
	if err != nil {
		return err
	}

	s.SetAuthToken(resp.Token)
	s.SetCurrentUser(resp.UserID)
	s.SetUsername(resp.Username)
	c.SetToken(resp.Token)

	display.Println(display.Green, "Logged in successfully")
	fmt.Printf("User ID: %s\n", resp.UserID)
	fmt.Printf("Username: %s\n", resp.Username)

	return nil
}

func logoutHandler(s *session.Session, args []string) error {
	c := s.GetClient().(*api.Client)

	// Call server to invalidate session if authenticated
	if s.GetAuthToken() != "" {
		if err := c.Logout(); err != nil {
			// Log but don't fail - clear local state anyway
			display.Println(display.Yellow, "Server logout failed: %s", err.Error())
		}
	}

	// Clear local state
	s.SetAuthToken("")
	s.SetCurrentUser("")
	s.SetUsername("")
	c.SetToken("")

	display.Println(display.Green, "Logged out")
	return nil
}

func whoamiHandler(s *session.Session, args []string) error {
	if s.GetAuthToken() == "" {
		display.Println(display.Yellow, "Not authenticated")
		return nil
	}

	c := s.GetClient().(*api.Client)
	user, err := c.GetCurrentUser()
	if err != nil {
		return err
	}

	display.Println(display.Cyan, "Current User:")
	fmt.Printf("  User ID:  %s\n", user.UserID)
	fmt.Printf("  Username: %s\n", user.Username)
	if user.Email != "" {
		fmt.Printf("  Email:    %s\n", user.Email)
	}
	fmt.Printf("  Created:  %s\n", user.CreatedAt.Format("2006-01-02 15:04:05"))
	if user.LastLogin != nil {
		fmt.Printf("  Last Login: %s\n", user.LastLogin.Format("2006-01-02 15:04:05"))
	}

	return nil
}

func setUserHandler(s *session.Session, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: user <userId>")
	}

	userID := args[0]
	s.SetCurrentUser(userID)
	display.Println(display.Cyan, "User ID set to: %s", userID)
	fmt.Println("Note: This doesn't authenticate, just sets the ID for display")

	return nil
}
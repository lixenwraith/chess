// FILE: lixenwraith/chess/internal/client/commands/auth.go
package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"chess/internal/client/api"
	"chess/internal/client/display"

	"golang.org/x/term"
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

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(bytePassword), nil
}

func registerHandler(s Session, args []string) error {
	scanner := bufio.NewScanner(os.Stdin)
	c := s.GetClient().(*api.Client)

	fmt.Print(display.Yellow + "Username: " + display.Reset)
	scanner.Scan()
	username := strings.TrimSpace(scanner.Text())

	password, err := readPassword(display.Yellow + "Password: " + display.Reset)
	if err != nil {
		return err
	}

	fmt.Print(display.Yellow + "Email (optional): " + display.Reset)
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

	fmt.Printf("%sRegistered successfully%s\n", display.Green, display.Reset)
	fmt.Printf("User ID: %s\n", resp.UserID)
	fmt.Printf("Username: %s\n", resp.Username)

	return nil
}

func loginHandler(s Session, args []string) error {
	scanner := bufio.NewScanner(os.Stdin)
	c := s.GetClient().(*api.Client)

	fmt.Print(display.Yellow + "Username or Email: " + display.Reset)
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

	fmt.Printf("%sLogged in successfully%s\n", display.Green, display.Reset)
	fmt.Printf("User ID: %s\n", resp.UserID)
	fmt.Printf("Username: %s\n", resp.Username)

	return nil
}

func logoutHandler(s Session, args []string) error {
	s.SetAuthToken("")
	s.SetCurrentUser("")
	s.SetUsername("")
	c := s.GetClient().(*api.Client)
	c.SetToken("")

	fmt.Printf("%sLogged out%s\n", display.Green, display.Reset)
	return nil
}

func whoamiHandler(s Session, args []string) error {
	if s.GetAuthToken() == "" {
		fmt.Printf("%sNot authenticated%s\n", display.Yellow, display.Reset)
		return nil
	}

	c := s.GetClient().(*api.Client)
	user, err := c.GetCurrentUser()
	if err != nil {
		return err
	}

	fmt.Printf("%sCurrent User:%s\n", display.Cyan, display.Reset)
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

func setUserHandler(s Session, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: user <userId>")
	}

	userID := args[0]
	s.SetCurrentUser(userID)
	fmt.Printf("%sUser ID set to: %s%s\n", display.Cyan, userID, display.Reset)
	fmt.Println("Note: This doesn't authenticate, just sets the ID for display")

	return nil
}
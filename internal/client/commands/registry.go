// FILE: lixenwraith/chess/internal/client/commands/registry.go
package commands

import (
	"fmt"
	"os"
	"strings"

	"chess/internal/client/api"
	"chess/internal/client/display"
)

type Session interface {
	GetAPIBaseURL() string
	SetAPIBaseURL(string)
	GetCurrentGame() string
	SetCurrentGame(string)
	GetCurrentUser() string
	SetCurrentUser(string)
	GetAuthToken() string
	SetAuthToken(string)
	GetUsername() string
	SetUsername(string)
	GetLastMoveCount() int
	SetLastMoveCount(int)
	GetClient() interface{}
	IsVerbose() bool
	SetGameState(interface{})
	SetPlayerColor(string)
	GetPlayerColor() string
}

// Command defines a client command with its handler
type Command struct {
	Name        string
	ShortName   string
	Description string
	Usage       string
	Handler     func(Session, []string) error
}

type Registry struct {
	session  Session
	commands map[string]*Command
}

// Registry manages command registration and execution
func NewRegistry(session Session) *Registry {
	r := &Registry{
		session:  session,
		commands: make(map[string]*Command),
	}

	// Register all commands
	r.registerGameCommands()
	r.registerAuthCommands()
	r.registerDebugCommands()

	// Help command
	r.Register(&Command{
		Name:        "help",
		ShortName:   "?",
		Description: "Show available commands",
		Usage:       "help [command]",
		Handler:     r.helpHandler,
	})

	// Exit command
	r.Register(&Command{
		Name:        "exit",
		ShortName:   "x",
		Description: "Exit the client",
		Usage:       "exit",
		Handler:     exitHandler,
	})

	return r
}

func (r *Registry) Register(cmd *Command) {
	r.commands[cmd.Name] = cmd
	if cmd.ShortName != "" {
		r.commands[cmd.ShortName] = cmd
	}
}

func (r *Registry) Execute(input string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return
	}

	cmdName := parts[0]
	args := parts[1:]

	cmd, exists := r.commands[cmdName]
	if !exists {
		fmt.Printf("%sUnknown command: %s%s\n", display.Red, cmdName, display.Reset)
		fmt.Printf("Type 'help' for available commands\n")
		return
	}

	// Set verbose mode in client if session supports it
	if cl, ok := r.session.GetClient().(*api.Client); ok {
		cl.SetVerbose(r.session.IsVerbose())
	}

	if err := cmd.Handler(r.session, args); err != nil {
		fmt.Printf("%sError: %s%s\n", display.Red, err.Error(), display.Reset)
	}
}

func (r *Registry) helpHandler(s Session, args []string) error {
	if len(args) > 0 {
		// Show help for specific command
		cmd, exists := r.commands[args[0]]
		if !exists {
			return fmt.Errorf("unknown command: %s", args[0])
		}
		fmt.Printf("\n%s%s%s - %s\n", display.Cyan, cmd.Name, display.Reset, cmd.Description)
		if cmd.ShortName != "" {
			fmt.Printf("Short form: %s%s%s\n", display.Cyan, cmd.ShortName, display.Reset)
		}
		fmt.Printf("Usage: %s\n", cmd.Usage)
		return nil
	}

	// Show all commands
	fmt.Printf("\n%sAvailable Commands:%s\n\n", display.Cyan, display.Reset)

	// Group commands
	type cmdInfo struct {
		name      string
		shortName string
		desc      string
	}

	gameCommands := []cmdInfo{
		{"new", "n", ""},
		{"join", "j", ""},
		{"move", "m", ""},
		{"computer", "c", ""},
		{"undo", "u", ""},
		{"show", "h", ""},
		{"state", "s", ""},
		{"delete", "d", ""},
		{"poll", "p", ""},
	}

	authCommands := []cmdInfo{
		{"register", "r", ""},
		{"login", "l", ""},
		{"logout", "o", ""},
		{"whoami", "i", ""},
		{"user", "e", ""},
	}

	utilCommands := []cmdInfo{
		{"health", ".", ""},
		{"url", "/", ""},
		{"raw", ":", ""},
		{"clear", "-", ""},
		{"help", "?", ""},
		{"exit", "x", ""},
	}

	printCommandGroup := func(title string, cmds []cmdInfo) {
		fmt.Printf("%s%s:%s\n", display.Yellow, title, display.Reset)
		for _, info := range cmds {
			if cmd, exists := r.commands[info.name]; exists {
				shortPart := ""
				if info.shortName != "" {
					shortPart = fmt.Sprintf("[%s%s%s] ", display.Cyan, info.shortName, display.Reset)
				}
				fmt.Printf("  %s%-10s %s\n", shortPart, cmd.Name, cmd.Description)
			}
		}
	}

	printCommandGroup("Game Commands", gameCommands)
	fmt.Println()
	printCommandGroup("Auth Commands", authCommands)
	fmt.Println()
	printCommandGroup("Utility Commands", utilCommands)

	fmt.Printf("\nType 'help <command>' for detailed usage\n")
	fmt.Printf("Add '-v' to any command for verbose output\n")
	return nil
}
func exitHandler(s Session, args []string) error {
	fmt.Printf("%sGoodbye!%s\n", display.Cyan, display.Reset)
	os.Exit(0)
	return nil
}
// FILE: lixenwraith/chess/internal/client/command/registry.go
package command

import (
	"fmt"
	"strings"

	"chess/internal/client/api"
	"chess/internal/client/display"
	"chess/internal/client/session"
)

// Command defines a client command with its handler
type Command struct {
	Name        string
	ShortName   string
	Description string
	Usage       string
	Handler     func(*session.Session, []string) error
}

type Registry struct {
	session  *session.Session
	commands map[string]*Command
}

// Registry manages command registration and execution
func NewRegistry(s *session.Session) *Registry {
	r := &Registry{
		session:  s,
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

	// Exit command (handled in main loop, but registered for help display)
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
		display.Println(display.Red, "Unknown command: %s", cmdName)
		display.Println(display.Reset, "Type 'help' for available commands")
		return
	}

	// Set verbose mode in client if session supports it
	if cl, ok := r.session.GetClient().(*api.Client); ok {
		cl.SetVerbose(r.session.IsVerbose())
	}

	if err := cmd.Handler(r.session, args); err != nil {
		display.Println(display.Red, "Error: %s", err.Error())
	}
}

func (r *Registry) helpHandler(s *session.Session, args []string) error {
	if len(args) > 0 {
		// Show help for specific command
		cmd, exists := r.commands[args[0]]
		if !exists {
			return fmt.Errorf("unknown command: %s", args[0])
		}
		fmt.Println()
		display.Print(display.Cyan, cmd.Name)
		display.Println(display.Reset, " - %s", cmd.Description)
		if cmd.ShortName != "" {
			display.Println(display.Cyan, "Short form: %s", cmd.ShortName)
		}
		fmt.Printf("Usage: %s\n", cmd.Usage)
		return nil
	}

	// Show all commands
	display.Println(display.Cyan, "\nAvailable Commands:\n")

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
		display.Println(display.Yellow, "%s:", title)
		for _, info := range cmds {
			if cmd, exists := r.commands[info.name]; exists {
				shortPart := ""
				if info.shortName != "" {
					shortPart = fmt.Sprintf("[%s%s%s] ", display.Cyan, info.shortName, display.Reset)
				}
				fmt.Printf("  %s%-10s %s\n", shortPart, cmd.Name, cmd.Description)
			}
		}
		fmt.Println()
	}

	printCommandGroup("Game Commands", gameCommands)
	printCommandGroup("Auth Commands", authCommands)
	printCommandGroup("Utility Commands", utilCommands)

	display.Println(display.Reset, "Type 'help <command>' for detailed usage")
	display.Println(display.Reset, "Add '-v' to any command for verbose output\n")
	return nil
}

func exitHandler(s *session.Session, args []string) error {
	// Exit is handled in main loop, this is just for consistency
	display.Println(display.Cyan, "Goodbye!\n")
	return nil
}
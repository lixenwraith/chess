package command

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/lixenwraith/chess/internal/client/api"
	"github.com/lixenwraith/chess/internal/client/display"
	"github.com/lixenwraith/chess/internal/client/session"
)

type Command struct {
	Name        string
	ShortName   string
	Description string
	Usage       string
	Handler     func(*session.Session, []string) error
	// Paged commands print at length and read nothing: their output is
	// shown one screen at a time (display.Page).
	Paged bool
}

type Registry struct {
	session  *session.Session
	commands map[string]*Command
}

func NewRegistry(s *session.Session) *Registry {
	r := &Registry{
		session:  s,
		commands: make(map[string]*Command),
	}

	r.registerGameCommands()
	r.registerReplayCommands()
	r.registerAuthCommands()
	r.registerDebugCommands()

	r.Register(&Command{
		Name:        "help",
		ShortName:   "?",
		Description: "Show available commands",
		Usage:       "help [command]",
		Handler:     r.helpHandler,
		Paged:       true,
	})

	// The main loop handles exit; it is registered so help lists it
	r.Register(&Command{
		Name:        "exit",
		ShortName:   "x",
		Description: "Exit the client",
		Usage:       "exit",
		Handler:     exitHandler,
	})

	return r
}

// interruptible runs a command that waits under a context Ctrl-C cancels, so
// the wait ends and the session stays. Commands without it keep Ctrl-C's
// default, as the prompt does.
func interruptible(run func(context.Context, *session.Session, []string) error) func(*session.Session, []string) error {
	return func(s *session.Session, args []string) error {
		ctx, stop := interruptContext()
		defer stop()
		return run(ctx, s, args)
	}
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
	if r.session.Replay != nil && liveOnly[cmd.Name] {
		display.Println(display.Red, "'%s' is not available while replaying; 'live' returns to the live session", cmd.Name)
		return
	}

	if cl, ok := r.session.GetClient().(*api.Client); ok {
		cl.SetVerbose(r.session.IsVerbose())
	}

	if cmd.Paged && r.session.ReadLine != nil {
		var buf bytes.Buffer
		previous := display.Out
		display.Out = &buf
		err := cmd.Handler(r.session, args)
		display.Out = previous
		display.Page(buf.String(), r.session.ReadLine)
		if err != nil {
			display.Println(display.Red, "Error: %s", err.Error())
		}
		return
	}
	if err := cmd.Handler(r.session, args); err != nil {
		display.Println(display.Red, "Error: %s", err.Error())
	}
}

func (r *Registry) helpHandler(s *session.Session, args []string) error {
	if len(args) > 0 {
		cmd, exists := r.commands[args[0]]
		if !exists {
			return fmt.Errorf("unknown command: %s", args[0])
		}
		display.Line()
		display.Print(display.Cyan, cmd.Name)
		display.Println(display.Reset, " - %s", cmd.Description)
		if cmd.ShortName != "" {
			display.Println(display.Cyan, "Short form: %s", cmd.ShortName)
		}
		display.Printf("Usage: %s\n", cmd.Usage)
		return nil
	}

	display.Println(display.Cyan, "\nAvailable Commands:\n")

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
		{"resign", "", ""},
		{"draw", "", ""},
		{"games", "g", ""},
		{"archive", "", ""},
		{"pgn", "", ""},
		{"import", "", ""},
	}

	replayCommands := []cmdInfo{
		{"replay", "", ""},
		{"next", ">", ""},
		{"prev", "<", ""},
		{"first", "", ""},
		{"last", "", ""},
		{"goto", "", ""},
		{"auto", "", ""},
		{"fen", "", ""},
		{"live", "", ""},
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
				display.Printf("  %s%-10s %s\n", shortPart, cmd.Name, cmd.Description)
			}
		}
		display.Line()
	}

	printCommandGroup("Game Commands", gameCommands)
	printCommandGroup("Replay Commands", replayCommands)
	printCommandGroup("Auth Commands", authCommands)
	printCommandGroup("Utility Commands", utilCommands)

	display.Println(display.Reset, "Type 'help <command>' for detailed usage")
	display.Println(display.Reset, "Add '-v' to any command for verbose output")
	display.Println(display.Reset, "Long output pauses after each screen: Enter continues, q stops\n")
	return nil
}

func exitHandler(s *session.Session, args []string) error {
	display.Println(display.Cyan, "Goodbye!\n")
	return nil
}

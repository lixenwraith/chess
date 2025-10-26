// FILE: internal/cli/cli.go
package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"chess/internal/board"
	"chess/internal/core"
	"chess/internal/game"
)

type CommandType int

const (
	CmdNone CommandType = iota
	CmdNew
	CmdResume
	CmdMove
	CmdUndo
	CmdColor
	CmdVerbose
	CmdHistory
	CmdHelp
	CmdQuit
)

type Command struct {
	Type CommandType
	Args []string
	Raw  string
}

type ColorTheme string

const (
	ThemeOff   ColorTheme = "off"
	ThemeBrown ColorTheme = "brown"
	ThemeGreen ColorTheme = "green"
	ThemeGray  ColorTheme = "gray"
)

type themeColors struct {
	lightBg string
	darkBg  string
	white   string
	black   string
	reset   string
}

var themes = map[ColorTheme]themeColors{
	ThemeOff: {
		lightBg: "",
		darkBg:  "",
		white:   "",
		black:   "",
		reset:   "",
	},
	ThemeBrown: {
		lightBg: "\033[48;5;230m", // Beige
		darkBg:  "\033[48;5;94m",  // Brown
		white:   "\033[97m",
		black:   "\033[30m",
		reset:   "\033[0m",
	},
	ThemeGreen: {
		lightBg: "\033[48;5;157m", // Light green
		darkBg:  "\033[48;5;22m",  // Dark green
		white:   "\033[97m",
		black:   "\033[30m",
		reset:   "\033[0m",
	},
	ThemeGray: {
		lightBg: "\033[48;5;251m", // Light gray
		darkBg:  "\033[48;5;240m", // Dark gray
		white:   "\033[97m",
		black:   "\033[30m",
		reset:   "\033[0m",
	},
}

type CLI struct {
	input   *bufio.Scanner
	output  io.Writer
	theme   ColorTheme
	verbose bool
}

func New(input io.Reader, output io.Writer) *CLI {
	return &CLI{
		input:   bufio.NewScanner(input),
		output:  output,
		theme:   ThemeOff,
		verbose: false,
	}
}

// Reads a command synchronously
func (c *CLI) GetCommand() (*Command, error) {
	if !c.input.Scan() {
		if err := c.input.Err(); err != nil {
			return nil, err
		}
		return &Command{Type: CmdQuit}, nil
	}

	input := strings.TrimSpace(c.input.Text())
	if input == "" {
		return &Command{Type: CmdNone}, nil
	}

	return c.parseCommand(input), nil
}

func (c *CLI) parseCommand(input string) *Command {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return &Command{Type: CmdNone}
	}

	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "new":
		return &Command{Type: CmdNew, Args: args}
	case "resume":
		return &Command{Type: CmdResume, Args: args, Raw: input}
	case "undo":
		return &Command{Type: CmdUndo, Args: args}
	case "color":
		return &Command{Type: CmdColor, Args: args}
	case "verbose":
		return &Command{Type: CmdVerbose}
	case "history":
		return &Command{Type: CmdHistory}
	case "help", "?":
		return &Command{Type: CmdHelp}
	case "quit", "exit":
		return &Command{Type: CmdQuit}
	default:
		// Assume it's a move
		return &Command{Type: CmdMove, Args: []string{cmd}}
	}
}

func (c *CLI) SetTheme(theme ColorTheme) error {
	if _, ok := themes[theme]; !ok {
		return fmt.Errorf("invalid theme: %s (use: off, brown, green, gray)", theme)
	}
	c.theme = theme
	return nil
}

func (c *CLI) ToggleVerbose() bool {
	c.verbose = !c.verbose
	return c.verbose
}

func (c *CLI) IsVerbose() bool {
	return c.verbose
}

func (c *CLI) ShowMessage(msg string) {
	fmt.Fprintln(c.output, msg)
}

func (c *CLI) ShowError(err error) {
	c.ShowMessage(fmt.Sprintf("Error: %v\n", err))
}

func (c *CLI) ShowPrompt(prompt string) {
	fmt.Fprint(c.output, prompt)
}

func (c *CLI) ReadLine() string {
	if c.input.Scan() {
		return strings.TrimSpace(c.input.Text())
	}
	return ""
}

func (c *CLI) DisplayBoard(b *board.Board) {
	theme := themes[c.theme]
	var sb strings.Builder

	sb.WriteString("\n  a b c d e f g h\n")

	for r := 0; r < 8; r++ {
		sb.WriteString(fmt.Sprintf("%d ", 8-r))
		for f := 0; f < 8; f++ {
			// Get piece at position
			square := fmt.Sprintf("%c%c", 'a'+f, '8'-r)
			piece := b.GetPieceAt(square)

			if c.theme == ThemeOff {
				// No colors, just show piece or space
				if piece == 0 {
					sb.WriteString("  ")
				} else {
					sb.WriteString(fmt.Sprintf("%c ", piece))
				}
			} else {
				// Apply theme colors
				bg := theme.darkBg
				if (r+f)%2 == 0 {
					bg = theme.lightBg
				}

				if piece == 0 {
					sb.WriteString(fmt.Sprintf("%s  %s", bg, theme.reset))
				} else {
					color := theme.black
					if piece >= 'A' && piece <= 'Z' {
						color = theme.white
					}
					sb.WriteString(fmt.Sprintf("%s%s%c %s", bg, color, piece, theme.reset))
				}
			}
		}
		sb.WriteString(fmt.Sprintf(" %d\n", 8-r))
	}
	sb.WriteString("  a b c d e f g h\n")

	c.ShowMessage(sb.String())
}

func (c *CLI) ShowHelp() {
	help := `Commands:
  new              - Start a new game with player type selection
  resume <FEN>     - Resume from a specific board position
  <move>           - Make a move (e.g., e2e4, g1f3)
  undo [count]     - Undo last move(s), default 1
  color <theme>    - Set board color theme (off|brown|green|gray)
  verbose          - Toggle detailed move information
  history          - Show game move history and positions
  quit/exit        - Exit the program
  help/?           - Show this help message

During any game:
  Press ENTER      - Execute computer move (when it's computer's turn)`

	c.ShowMessage(help)
}

func (c *CLI) ShowWelcome() {
	c.ShowMessage("Welcome to Chess!")
	c.ShowMessage("Commands: new, resume <FEN>, <move>, undo, quit/exit, verbose, history, help/?")
	c.ShowMessage("Example: 'resume 4k3/8/8/8/8/8/8/4K2R w K - 0 1' to start from a puzzle.")
	c.ShowMessage("Press ENTER to execute computer moves when it's computer's turn.")
	c.ShowMessage("")
}

func (c *CLI) ShowGameHistory(g *game.Game) {
	c.ShowMessage(fmt.Sprintf("Starting FEN: %s\n", g.InitialFEN()))

	moves := g.Moves()
	for i := 0; i < len(moves); i += 2 {
		moveNum := i/2 + 1
		white := moves[i]
		if i+1 < len(moves) {
			black := moves[i+1]
			c.ShowMessage(fmt.Sprintf("%d. %s | %s\n", moveNum, white, black))
		} else {
			c.ShowMessage(fmt.Sprintf("%d. %s | ...\n", moveNum, white))
		}
	}
	c.ShowMessage(fmt.Sprintf("Current FEN: %s\n", g.CurrentFEN()))
	c.ShowMessage(fmt.Sprintf("Game state: %s\n", g.State()))
}

func (c *CLI) ShowComputerMove(result *game.MoveResult) {
	if c.verbose {
		c.ShowMessage(fmt.Sprintf("Computer (%c): %s (depth=%d, score=%d)\n",
			result.Player, result.Move, result.Depth, result.Score))
	} else {
		// Always show computer moves in non-verbose mode too
		c.ShowMessage(fmt.Sprintf("Computer (%c): %s\n", result.Player, result.Move))
	}
}

func (c *CLI) ShowHumanMove(move string) {
	if c.verbose {
		c.ShowMessage(fmt.Sprintf("Your move: %s\n", move))
	}
}

func (c *CLI) ShowGameOver(state core.State) {
	c.ShowMessage(fmt.Sprintf("\nGame Over: %s\n", state))
	c.ShowMessage("Start a new game with 'new' or 'resume'.")
}
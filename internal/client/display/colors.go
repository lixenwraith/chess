// FILE: lixenwraith/chess/internal/client/display/colors.go
package display

// Terminal color codes
const (
	Reset   = "\033[0m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
)

// Prompt returns a colored prompt string
func Prompt(text string) string {
	return Yellow + text + Yellow + " > " + Reset
}
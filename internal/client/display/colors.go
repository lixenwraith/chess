// FILE: lixenwraith/chess/internal/client/display/colors.go
package display

import (
	"fmt"
	"strings"
)

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

// C wraps text with color and reset codes
func C(color, text string) string {
	return color + text + Reset
}

// Print outputs colored text immediately
func Print(color, format string, args ...any) {
	fmt.Printf(C(color, format), args...)
}

// Println outputs colored text with newline
func Println(color, format string, args ...any) {
	fmt.Println(C(color, fmt.Sprintf(format, args...)))
}

// Build creates a multi-colored string
type Builder struct {
	parts []string
}

func (b *Builder) Add(color, text string) *Builder {
	b.parts = append(b.parts, C(color, text))
	return b
}

func (b *Builder) String() string {
	return strings.Join(b.parts, "")
}

// Prompt returns a colored prompt string
func Prompt(text string) string {
	return Yellow + text + Yellow + " > " + Reset
}
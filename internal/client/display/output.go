package display

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Out receives everything the client prints. A paged command runs with Out
// swapped for a buffer, which Page then shows one screen at a time.
var Out io.Writer = os.Stdout

// Printf, Line and Write are fmt.Printf, fmt.Println and fmt.Print to Out.
func Printf(format string, args ...any) { fmt.Fprintf(Out, format, args...) }
func Line(args ...any)                  { fmt.Fprintln(Out, args...) }
func Write(args ...any)                 { fmt.Fprint(Out, args...) }

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// TermSize returns the terminal's rows and columns, or zeros when the output
// is not a terminal (then nothing is paged). Platform files set it.
var TermSize = func() (rows, cols int) { return 0, 0 }

// Page prints text to Out one screen at a time. After each full screen it
// asks to continue; readLine supplies the answer (an empty line continues,
// "q" stops). Without a terminal, or when the text fits, it prints at once.
func Page(text string, readLine func() (string, bool)) {
	rows, cols := TermSize()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if rows < 4 || readLine == nil || visualRows(lines, cols) <= rows-1 {
		if text != "" {
			fmt.Fprint(Out, strings.TrimRight(text, "\n")+"\n")
		}
		return
	}
	pageRows := rows - 1 // one line for the prompt
	used, i := 0, 0
	for i < len(lines) {
		need := wrappedRows(lines[i], cols)
		if used > 0 && used+need > pageRows {
			remaining := len(lines) - i
			fmt.Fprintf(Out, "%s-- More (%d lines left; Enter continues, q quits) --%s", Yellow, remaining, Reset)
			answer, ok := readLine()
			// Clear the prompt and the echoed answer line.
			fmt.Fprint(Out, "\r\x1b[2K\x1b[1A\r\x1b[2K")
			if !ok || strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "q") {
				return
			}
			used = 0
			continue
		}
		fmt.Fprintln(Out, lines[i])
		used += need
		i++
	}
}

func visualRows(lines []string, cols int) int {
	total := 0
	for _, line := range lines {
		total += wrappedRows(line, cols)
	}
	return total
}

// wrappedRows is how many terminal rows a line takes, ignoring colour codes.
func wrappedRows(line string, cols int) int {
	if cols <= 0 {
		return 1
	}
	width := utf8.RuneCountInString(ansiPattern.ReplaceAllString(line, ""))
	if width == 0 {
		return 1
	}
	return (width + cols - 1) / cols
}

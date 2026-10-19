package display

import (
	"bytes"
	"strings"
	"testing"
)

func TestPagePrintsScreensAndStopsOnQ(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevSize := Out, TermSize
	Out = &buf
	TermSize = func() (int, int) { return 5, 80 } // 4 lines a page
	defer func() { Out, TermSize = prevOut, prevSize }()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line "+string(rune('0'+i%10)))
	}
	answers := []string{"", "q"}
	reads := 0
	readLine := func() (string, bool) {
		if reads >= len(answers) {
			return "", false
		}
		reads++
		return answers[reads-1], true
	}
	Page(strings.Join(lines, "\n")+"\n", readLine)
	out := buf.String()
	if reads != 2 {
		t.Errorf("prompted %d times, want 2", reads)
	}
	if !strings.Contains(out, "line 1\nline 2\nline 3\nline 4\n") || !strings.Contains(out, "More (6 lines left") {
		t.Errorf("first page = %q", out)
	}
	if !strings.Contains(out, "line 5\nline 6\nline 7\nline 8\n") || strings.Contains(out, "line 9") {
		t.Errorf("second page / stop = %q", out)
	}

	// Fits on one screen: no prompt. A wrapped long line counts as two rows.
	buf.Reset()
	reads = 0
	Page("a\nb\n", readLine)
	if buf.String() != "a\nb\n" || reads != 0 {
		t.Errorf("short text = %q, prompts %d", buf.String(), reads)
	}
	if wrappedRows(Red+strings.Repeat("x", 100)+Reset, 80) != 2 || wrappedRows("", 80) != 1 {
		t.Error("wrapped rows")
	}
	// No terminal: everything at once.
	TermSize = func() (int, int) { return 0, 0 }
	buf.Reset()
	Page(strings.Join(lines, "\n"), readLine)
	if strings.Count(buf.String(), "\n") != 10 || reads != 0 {
		t.Errorf("unpaged = %q", buf.String())
	}
}

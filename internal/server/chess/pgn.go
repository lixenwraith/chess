package chess

import (
	"strconv"
	"strings"
)

// pgnLineWidth is the export-format line limit recommended by the PGN
// standard (section 8.2.1).
const pgnLineWidth = 80

// Tag is one PGN tag pair.
type Tag struct {
	Name, Value string
}

// sevenTagRoster is the mandatory tag order of PGN export format.
var sevenTagRoster = [...]string{"Event", "Site", "Date", "Round", "White", "Black", "Result"}

// PGN is a game ready for export. Tags may hold any of the Seven Tag Roster
// (missing ones are written as "?", Result from the Result field) followed by
// supplemental tags in the given order. StartFEN is written as SetUp/FEN
// tags when it is not the standard start. Comment, if set, is written as a
// brace comment before the result, e.g. "White resigns.".
type PGN struct {
	Tags     []Tag
	StartFEN string
	SAN      []string
	Comment  string
	Result   string // "1-0", "0-1", "1/2-1/2", or "*"
}

// String renders PGN export format: tag section, blank line, movetext
// wrapped at 80 columns and ending with the result, and a final newline.
func (g PGN) String() string {
	result := g.Result
	switch result {
	case "1-0", "0-1", "1/2-1/2":
	default:
		result = "*"
	}

	var b strings.Builder
	values := make(map[string]string, len(g.Tags))
	for _, t := range g.Tags {
		values[t.Name] = t.Value
	}
	for _, name := range sevenTagRoster {
		value, ok := values[name]
		if name == "Result" {
			value, ok = result, true
		}
		if !ok || value == "" {
			value = "?"
			if name == "Round" {
				value = "-"
			}
		}
		writeTag(&b, name, value)
	}

	turn, fullmove := White, 1
	if g.StartFEN != "" && g.StartFEN != StartFEN {
		writeTag(&b, "SetUp", "1")
		writeTag(&b, "FEN", g.StartFEN)
		if start, err := ParseFEN(g.StartFEN); err == nil {
			turn, fullmove = start.turn, start.fullmove
		}
	}
	for _, t := range g.Tags {
		if isRosterTag(t.Name) || t.Name == "SetUp" || t.Name == "FEN" || !validTagName(t.Name) {
			continue
		}
		writeTag(&b, t.Name, t.Value)
	}
	b.WriteByte('\n')

	width := 0
	emit := func(token string) {
		switch {
		case width == 0:
		case width+1+len(token) > pgnLineWidth:
			b.WriteByte('\n')
			width = 0
		default:
			b.WriteByte(' ')
			width++
		}
		b.WriteString(token)
		width += len(token)
	}
	for i, san := range g.SAN {
		switch {
		case turn == White:
			emit(strconv.Itoa(fullmove) + ".")
		case i == 0:
			emit(strconv.Itoa(fullmove) + "...")
		}
		emit(san)
		if turn == Black {
			fullmove++
		}
		turn = turn.Other()
	}
	if words := strings.Fields(commentText(g.Comment)); len(words) > 0 {
		// A comment may be broken across lines at spaces, so it wraps like
		// movetext; braces cannot nest and are removed from the text.
		emit("{")
		for _, word := range words {
			emit(word)
		}
		emit("}")
	}
	emit(result)
	b.WriteByte('\n')
	return b.String()
}

// commentText drops braces and control characters, which would end or
// corrupt a brace comment.
func commentText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '{' || r == '}' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func isRosterTag(name string) bool {
	for _, r := range sevenTagRoster {
		if r == name {
			return true
		}
	}
	return false
}

// validTagName accepts PGN tag symbols: letters, digits, and underscore,
// starting with a letter.
func validTagName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '_')) {
			return false
		}
	}
	return true
}

// writeTag writes one tag pair, escaping backslash and quote and dropping
// control characters so a value cannot break out of its string token.
func writeTag(b *strings.Builder, name, value string) {
	b.WriteByte('[')
	b.WriteString(name)
	b.WriteString(` "`)
	for _, r := range value {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString("\"]\n")
}

// ResultToken maps a stored result to its PGN token.
func ResultToken(result string) string {
	switch result {
	case "white_wins":
		return "1-0"
	case "black_wins":
		return "0-1"
	case "draw", "stalemate":
		return "1/2-1/2"
	}
	return "*"
}

package chess

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on imported PGN. One game of ordinary length is a few kilobytes;
// the longest games on record are under 300 moves.
const (
	MaxPGNSize        = 64 << 10
	MaxPGNPlies       = 1000
	maxPGNTags        = 64
	maxPGNTagName     = 32
	maxPGNTagValue    = 255
	maxPGNTokenLength = 16
	maxVariationDepth = 16
)

// ErrInvalidPGN reports text that is not one well-formed PGN game.
var ErrInvalidPGN = errors.New("invalid PGN")

// ParsedPGN is one game read from PGN import format: its tag pairs in order
// and the main line's SAN moves. Comments, annotations, numeric annotation
// glyphs and variations are read and dropped.
type ParsedPGN struct {
	Tags   []Tag
	SAN    []string
	Result string // "1-0", "0-1", "1/2-1/2", or "*"
}

// Tag returns the value of the named tag, or "" when absent.
func (g *ParsedPGN) Tag(name string) string {
	for _, t := range g.Tags {
		if t.Name == name {
			return t.Value
		}
	}
	return ""
}

var (
	pgnTagName = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	// A SAN token: castling, or piece, disambiguation, capture, destination,
	// promotion, with check and annotation suffixes.
	pgnSAN = regexp.MustCompile(`^(?:O-O(?:-O)?|0-0(?:-0)?|[KQRBN]?[a-h]?[1-8]?x?[a-h][1-8](?:=?[QRBN])?)[+#]?[!?]{0,2}$`)
)

func pgnError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPGN, fmt.Sprintf(format, args...))
}

// ParsePGN reads exactly one game. It is strict about form: well-formed tag
// pairs, printable text only, balanced comments and variations, and SAN
// tokens of the standard shape; anything else is refused rather than
// guessed at. Move legality is not checked here (see Replay and ParseSAN).
func ParsePGN(text string) (*ParsedPGN, error) {
	if len(text) > MaxPGNSize {
		return nil, pgnError("larger than %d bytes", MaxPGNSize)
	}
	text = strings.TrimPrefix(text, "\uFEFF")
	if !utf8.ValidString(text) {
		return nil, pgnError("not valid UTF-8")
	}
	for i, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return nil, pgnError("control character at byte %d", i)
		}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")

	game := &ParsedPGN{}
	rest, err := parsePGNTags(text, game)
	if err != nil {
		return nil, err
	}
	if err := parsePGNMovetext(rest, game); err != nil {
		return nil, err
	}
	if len(game.SAN) == 0 {
		return nil, pgnError("no moves")
	}
	if tag := game.Tag("Result"); tag != "" {
		switch tag {
		case "1-0", "0-1", "1/2-1/2", "*":
		default:
			return nil, pgnError("Result tag %q is not a result", tag)
		}
		if game.Result == "" {
			game.Result = tag
		} else if game.Result != tag {
			return nil, pgnError("Result tag %q disagrees with the movetext result %q", tag, game.Result)
		}
	}
	if game.Result == "" {
		game.Result = "*"
	}
	return game, nil
}

// parsePGNTags reads the tag section and returns the movetext that follows.
func parsePGNTags(text string, game *ParsedPGN) (string, error) {
	seen := make(map[string]bool)
	for {
		trimmed := strings.TrimLeft(text, " \t\n")
		if !strings.HasPrefix(trimmed, "[") {
			return text, nil
		}
		end := strings.IndexByte(trimmed, '\n')
		if end < 0 {
			end = len(trimmed)
		}
		line := trimmed[:end]
		tag, err := parsePGNTag(line)
		if err != nil {
			return "", err
		}
		if seen[tag.Name] {
			return "", pgnError("tag %s given twice", tag.Name)
		}
		if len(game.Tags) >= maxPGNTags {
			return "", pgnError("more than %d tags", maxPGNTags)
		}
		seen[tag.Name] = true
		game.Tags = append(game.Tags, tag)
		text = trimmed[end:]
	}
}

// parsePGNTag reads one `[Name "Value"]` line.
func parsePGNTag(line string) (Tag, error) {
	body := strings.TrimSpace(line)
	if !strings.HasPrefix(body, "[") || !strings.HasSuffix(body, "]") {
		return Tag{}, pgnError("malformed tag line %q", line)
	}
	body = strings.TrimSpace(body[1 : len(body)-1])
	space := strings.IndexAny(body, " \t")
	if space < 0 {
		return Tag{}, pgnError("malformed tag line %q", line)
	}
	name := body[:space]
	if !pgnTagName.MatchString(name) {
		return Tag{}, pgnError("invalid tag name %q", name)
	}
	quoted := strings.TrimSpace(body[space:])
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return Tag{}, pgnError("tag %s value is not quoted", name)
	}
	var value strings.Builder
	escaped := false
	inner := quoted[1 : len(quoted)-1]
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case escaped:
			if c != '\\' && c != '"' {
				return Tag{}, pgnError("tag %s has an invalid escape", name)
			}
			value.WriteByte(c)
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			return Tag{}, pgnError("tag %s value has an unescaped quote", name)
		default:
			value.WriteByte(c)
		}
	}
	if escaped {
		return Tag{}, pgnError("tag %s value ends in a backslash", name)
	}
	if utf8.RuneCountInString(value.String()) > maxPGNTagValue {
		return Tag{}, pgnError("tag %s value longer than %d characters", name, maxPGNTagValue)
	}
	return Tag{Name: name, Value: value.String()}, nil
}

// parsePGNMovetext tokenizes the movetext: moves of the main line are kept,
// comments, variations, annotations and move numbers are read past, and
// the game ends at a result token.
func parsePGNMovetext(text string, game *ParsedPGN) error {
	depth := 0
	lineStart := true
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case c == '\n':
			lineStart = true
			i++
			continue
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '%' && lineStart:
			// Escape mechanism: the rest of the line is for other software.
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				return nil
			}
			i += end
			continue
		}
		lineStart = false
		if game.Result != "" {
			return pgnError("text after the result (one game per import)")
		}
		switch c {
		case '{':
			end := strings.IndexByte(text[i:], '}')
			if end < 0 {
				return pgnError("unterminated comment")
			}
			i += end + 1
		case '}':
			return pgnError("unexpected }")
		case ';':
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				return nil
			}
			i += end
		case '(':
			depth++
			if depth > maxVariationDepth {
				return pgnError("variations nested deeper than %d", maxVariationDepth)
			}
			i++
		case ')':
			depth--
			if depth < 0 {
				return pgnError("unexpected )")
			}
			i++
		case '$':
			j := i + 1
			for j < len(text) && j-i <= 3 && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if j == i+1 {
				return pgnError("malformed numeric annotation glyph")
			}
			i = j
		case '*':
			if depth == 0 {
				game.Result = "*"
			}
			i++
		default:
			// A token of visible, non-delimiter characters.
			j := i
			for j < len(text) && !strings.ContainsRune(" \t\n{}();$", rune(text[j])) {
				j++
			}
			token := text[i:j]
			if len(token) > maxPGNTokenLength {
				return pgnError("token %q too long", token[:maxPGNTokenLength]+"…")
			}
			i = j
			if err := pgnToken(token, depth, game); err != nil {
				return err
			}
		}
	}
	if depth != 0 {
		return pgnError("unterminated variation")
	}
	return nil
}

// pgnToken classifies one movetext token: a result, a move number, or a SAN
// move (kept when on the main line).
func pgnToken(token string, depth int, game *ParsedPGN) error {
	switch token {
	case "1-0", "0-1", "1/2-1/2":
		if depth == 0 {
			game.Result = token
		}
		return nil
	}
	if token[0] >= '0' && token[0] <= '9' && !strings.HasPrefix(token, "0-0") {
		// A move number: digits and dots ("12." or "12..."), possibly glued
		// to the move that follows ("12.Nf3").
		digits := strings.TrimLeft(token, "0123456789")
		number := token[:len(token)-len(digits)]
		if len(number) > 4 || !strings.HasPrefix(digits, ".") {
			return pgnError("unexpected token %q", token)
		}
		if rest := strings.TrimLeft(digits, "."); rest != "" {
			return pgnToken(rest, depth, game)
		}
		return nil
	}
	if !pgnSAN.MatchString(token) {
		return pgnError("unexpected token %q", token)
	}
	if depth > 0 {
		return nil
	}
	if len(game.SAN) >= MaxPGNPlies {
		return pgnError("more than %d plies", MaxPGNPlies)
	}
	game.SAN = append(game.SAN, strings.TrimRight(token, "!?"))
	return nil
}

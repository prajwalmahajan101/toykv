package main

import (
	"fmt"
	"strings"
	"unicode"
)

// tokenise splits a shell-like input line into argv. Unquoted whitespace
// separates tokens; "…" supports \\ \" \n \t \r \0 \a \b \f \v escapes;
// '…' is literal (no escapes, matching redis-cli). An unterminated
// quote returns an error.
func tokenise(line string) ([]string, error) {
	var (
		out  []string
		cur  strings.Builder
		open bool
	)
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		c := runes[i]
		switch {
		case unicode.IsSpace(c):
			if open {
				out = append(out, cur.String())
				cur.Reset()
				open = false
			}
			i++
		case c == '"':
			open = true
			i++
			for i < len(runes) && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < len(runes) {
					esc, consumed := decodeEscape(runes[i+1])
					cur.WriteRune(esc)
					i += 1 + consumed
					continue
				}
				cur.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated double-quoted string")
			}
			i++ // closing "
		case c == '\'':
			open = true
			i++
			for i < len(runes) && runes[i] != '\'' {
				cur.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated single-quoted string")
			}
			i++
		default:
			open = true
			cur.WriteRune(c)
			i++
		}
	}
	if open {
		out = append(out, cur.String())
	}
	return out, nil
}

// decodeEscape returns the rune for a backslash-escape inside double
// quotes and the number of source runes consumed past the backslash
// (always 1 for the simple forms supported here).
func decodeEscape(c rune) (rune, int) {
	switch c {
	case 'n':
		return '\n', 1
	case 't':
		return '\t', 1
	case 'r':
		return '\r', 1
	case '0':
		return 0, 1
	case 'a':
		return '\a', 1
	case 'b':
		return '\b', 1
	case 'f':
		return '\f', 1
	case 'v':
		return '\v', 1
	default:
		return c, 1
	}
}

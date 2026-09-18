package main

import (
	"fmt"
	"strings"
	"unicode"
)

// splitCommandArgs handles shell-style quoting without invoking a shell or
// expanding variables, globs, command substitutions, or environment references.
// In particular, quoted header values and empty subprocess args stay intact.
func splitCommandArgs(raw string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(raw)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\x00' {
			return nil, fmt.Errorf("command arguments must not contain NUL bytes")
		}
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '\\' {
			if i+1 == len(runes) {
				return nil, fmt.Errorf("unfinished escape in command arguments")
			}
			next := runes[i+1]
			if quote == '"' && !strings.ContainsRune("\"\\$`\n", next) {
				// Inside double quotes a backslash before an ordinary character
				// stays literal (useful for quoted Windows paths).
				word.WriteRune(c)
				continue
			}
			if next == '\x00' {
				return nil, fmt.Errorf("command arguments must not contain NUL bytes")
			}
			i++
			if next != '\n' {
				word.WriteRune(next)
				started = true
			}
			continue
		}
		if quote == '"' {
			if c == '"' {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
			started = true
		case unicode.IsSpace(c):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in command arguments")
	}
	if started {
		args = append(args, word.String())
	}
	return args, nil
}

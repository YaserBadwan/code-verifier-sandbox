// Package compare decides whether a program's output matches the expected output.
package compare

import (
	"bytes"
	"fmt"
)

type Mode int

const (
	TrailingWhitespace Mode = iota
	Exact
	Tokens
)

func (m Mode) String() string {
	switch m {
	case Exact:
		return "exact"
	case Tokens:
		return "tokens"
	default:
		return "trailing-whitespace"
	}
}

func Equal(mode Mode, expected, actual []byte) bool {
	switch mode {
	case Exact:
		return bytes.Equal(expected, actual)
	case Tokens:
		return equalTokens(expected, actual)
	default:
		return bytes.Equal(normalizeTrailing(expected), normalizeTrailing(actual))
	}
}

func equalTokens(expected, actual []byte) bool {
	a, b := bytes.Fields(expected), bytes.Fields(actual)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// strips trailing spaces, tabs and \r from each line and drops empty lines at the end
func normalizeTrailing(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	for i, line := range lines {
		lines[i] = bytes.TrimRight(line, " \t\r")
	}
	end := len(lines)
	for end > 0 && len(lines[end-1]) == 0 {
		end--
	}
	return bytes.Join(lines[:end], []byte("\n"))
}

// FirstDifference explains where the outputs stop matching, or returns "" if they match.
func FirstDifference(mode Mode, expected, actual []byte) string {
	if Equal(mode, expected, actual) {
		return ""
	}
	if len(bytes.TrimSpace(actual)) == 0 {
		return "no output"
	}

	if mode == Tokens {
		a, b := bytes.Fields(expected), bytes.Fields(actual)
		for i := 0; i < min(len(a), len(b)); i++ {
			if !bytes.Equal(a[i], b[i]) {
				return fmt.Sprintf("first difference at token %d", i+1)
			}
		}
		return fmt.Sprintf("expected %d tokens but got %d", len(a), len(b))
	}

	exp, act := normalizeTrailing(expected), normalizeTrailing(actual)
	if mode == Exact && bytes.Equal(exp, act) {
		return "output differs in whitespace or line endings"
	}
	expLines, actLines := bytes.Split(exp, []byte("\n")), bytes.Split(act, []byte("\n"))
	for i := 0; i < min(len(expLines), len(actLines)); i++ {
		if !bytes.Equal(expLines[i], actLines[i]) {
			return fmt.Sprintf("first difference on line %d", i+1)
		}
	}
	if len(actLines) < len(expLines) {
		return fmt.Sprintf("output ended early, expected %d lines but got %d", len(expLines), len(actLines))
	}
	return fmt.Sprintf("output has extra lines, expected %d lines but got %d", len(expLines), len(actLines))
}

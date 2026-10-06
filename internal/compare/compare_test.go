package compare

import "testing"

func TestEqual(t *testing.T) {
	cases := []struct {
		name     string
		mode     Mode
		expected string
		actual   string
		want     bool
	}{
		{"exact same", Exact, "1 2\n", "1 2\n", true},
		{"exact missing newline", Exact, "1 2\n", "1 2", false},
		{"exact crlf differs", Exact, "a\n", "a\r\n", false},
		{"exact both empty", Exact, "", "", true},

		{"trailing missing final newline", TrailingWhitespace, "3\n", "3", true},
		{"trailing extra blank lines", TrailingWhitespace, "3", "3\n\n\n", true},
		{"trailing spaces on lines", TrailingWhitespace, "a b\nc", "a b  \nc\t\n", true},
		{"trailing crlf", TrailingWhitespace, "a\nb\n", "a\r\nb\r\n", true},
		{"trailing leading space matters", TrailingWhitespace, "a", " a", false},
		{"trailing inner space matters", TrailingWhitespace, "a b", "a  b", false},
		{"trailing blank line in middle matters", TrailingWhitespace, "a\nb", "a\n\nb", false},
		{"trailing empty vs whitespace only", TrailingWhitespace, "", " \n \n", true},
		{"trailing wrong answer", TrailingWhitespace, "3", "4", false},

		{"tokens different layout", Tokens, "1 2 3", "1\n2\n   3\n", true},
		{"tokens different value", Tokens, "1 2 3", "1 2 4", false},
		{"tokens extra token", Tokens, "1 2", "1 2 3", false},
		{"tokens both empty", Tokens, "", "  \n", true},
		{"tokens unicode", Tokens, "héllo wörld", "héllo\twörld\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Equal(tc.mode, []byte(tc.expected), []byte(tc.actual))
			if got != tc.want {
				t.Fatalf("Equal(%v, %q, %q) = %v, want %v", tc.mode, tc.expected, tc.actual, got, tc.want)
			}
		})
	}
}

func TestUnknownModeFallsBackToTrailingWhitespace(t *testing.T) {
	if !Equal(Mode(99), []byte("x\n"), []byte("x")) {
		t.Fatal("unknown mode should behave like TrailingWhitespace")
	}
}

func TestFirstDifference(t *testing.T) {
	cases := []struct {
		name     string
		mode     Mode
		expected string
		actual   string
		want     string
	}{
		{"line differs", TrailingWhitespace, "1\n2\n3\n", "1\n2\n4\n", "first difference on line 3"},
		{"output too short", TrailingWhitespace, "1\n2\n", "1\n", "output ended early, expected 2 lines but got 1"},
		{"output too long", TrailingWhitespace, "1\n", "1\n2\n", "output has extra lines, expected 1 lines but got 2"},
		{"exact newline", Exact, "1\n", "1", "output differs in whitespace or line endings"},
		{"exact line", Exact, "a\nb", "a\nc", "first difference on line 2"},
		{"token differs", Tokens, "1 2 3", "1 2 9", "first difference at token 3"},
		{"too few tokens", Tokens, "1 2 3", "1 2", "expected 3 tokens but got 2"},
		{"no output", TrailingWhitespace, "5", "", "no output"},
		{"equal", TrailingWhitespace, "5", "5\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstDifference(tc.mode, []byte(tc.expected), []byte(tc.actual))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

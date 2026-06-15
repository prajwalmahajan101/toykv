package tui

import "testing"

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*", "", true},
		{"*", "abc", true},
		{"", "", true},
		{"", "x", false},
		{"a", "a", true},
		{"a", "b", false},
		{"a*", "abc", true},
		{"*c", "abc", true},
		{"a*c", "abxc", true},
		{"a*c", "ab", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"user:*", "user:42", true},
		{"user:*", "post:42", false},
		{"**foo", "barfoo", true},
		{"foo?", "foo", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pat, c.s, got, c.want)
		}
	}
}

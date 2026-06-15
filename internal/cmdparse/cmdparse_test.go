package cmdparse

import "testing"

func TestTokenise(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"PING", []string{"PING"}, false},
		{"SET k v", []string{"SET", "k", "v"}, false},
		{"  SET  k   v  ", []string{"SET", "k", "v"}, false},
		{`SET k "hello world"`, []string{"SET", "k", "hello world"}, false},
		{`SET k 'hello world'`, []string{"SET", "k", "hello world"}, false},
		{`SET k "a\tb\nc"`, []string{"SET", "k", "a\tb\nc"}, false},
		{`SET k "say \"hi\""`, []string{"SET", "k", `say "hi"`}, false},
		{`SET k 'no \t escapes'`, []string{"SET", "k", `no \t escapes`}, false},
		{`SET k "unterminated`, nil, true},
		{`SET k 'unterminated`, nil, true},
		{`SET "" ""`, []string{"SET", "", ""}, false},
	}
	for _, c := range cases {
		got, err := Tokenise(c.in)
		if c.err {
			if err == nil {
				t.Errorf("Tokenise(%q): want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Tokenise(%q): unexpected error: %v", c.in, err)
			continue
		}
		if !equalSlices(got, c.want) {
			t.Errorf("Tokenise(%q):\n  got  %#v\n  want %#v", c.in, got, c.want)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

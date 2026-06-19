package tui

import (
	"strings"
	"testing"
)

// containsANSI returns true if s carries any ESC escape sequence.
func containsANSI(s string) bool { return strings.Contains(s, "\x1b[") }

func TestStyles_NoColorIsIdentity(t *testing.T) {
	st := newStyles(true)
	checks := []struct {
		name string
		out  string
	}{
		{"accent", st.accent.Render("x")},
		{"muted", st.muted.Render("x")},
		{"errBanner", st.errBanner.Render("x")},
		{"warn", st.warn.Render("x")},
		{"filterHit", st.filterHit.Render("x")},
		{"respInt", st.respInt.Render("x")},
	}
	for _, c := range checks {
		if containsANSI(c.out) {
			t.Errorf("NO_COLOR style %s leaked ANSI escape: %q", c.name, c.out)
		}
	}
}

func TestRenderFooter_AlwaysIncludesHelpAndQuit(t *testing.T) {
	st := newStyles(true)
	for _, w := range []int{40, 60, 80, 120, 200} {
		out := renderFooter(st, w)
		if !strings.Contains(out, "? ") || !strings.Contains(out, "q ") {
			t.Errorf("at width %d footer dropped reserved hint: %q", w, out)
		}
	}
}

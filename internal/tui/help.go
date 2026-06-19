package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// binding is one row in the help overlay / footer hint bar. Group lets
// the overlay cluster related actions; Footer marks the bindings that
// should appear in the always-visible hint strip.
type binding struct {
	Key    string
	Desc   string
	Group  string
	Footer bool // include in always-visible footer
	Rank   int  // footer ordering — lower = shown first when space is tight
}

// bindings is the single source of truth for every documented keystroke.
// Both renderFooter and renderHelp read from here.
var bindings = []binding{
	{Key: "j/k", Desc: "down/up", Group: "Navigate"},
	{Key: "g/G", Desc: "top/bottom", Group: "Navigate"},
	{Key: "r", Desc: "refresh", Group: "Navigate"},
	{Key: "Tab", Desc: "switch pane", Group: "Navigate"},

	{Key: "n", Desc: "new key", Group: "Mutate", Footer: true, Rank: 1},
	{Key: "e", Desc: "edit value", Group: "Mutate", Footer: true, Rank: 2},
	{Key: "t", Desc: "set ttl", Group: "Mutate", Footer: true, Rank: 3},
	{Key: "d", Desc: "delete", Group: "Mutate", Footer: true, Rank: 4},
	{Key: "i", Desc: "incr", Group: "Mutate", Footer: true, Rank: 7},
	{Key: "D", Desc: "decr", Group: "Mutate"},
	{Key: "F", Desc: "flushdb", Group: "Mutate"},

	{Key: "/", Desc: "filter", Group: "View", Footer: true, Rank: 5},
	{Key: ":", Desc: "raw cmd", Group: "View", Footer: true, Rank: 6},

	{Key: "?", Desc: "help", Group: "Meta", Footer: true, Rank: 8},
	{Key: "q", Desc: "quit", Group: "Meta", Footer: true, Rank: 9},
	{Key: "Esc", Desc: "cancel", Group: "Meta"},
}

// footerHints returns the ordered subset of bindings flagged for the
// footer, ranked by Rank.
func footerHints() []binding {
	out := make([]binding, 0, len(bindings))
	for _, b := range bindings {
		if b.Footer {
			out = append(out, b)
		}
	}
	// stable insertion sort by Rank — small N, no need for sort.Slice.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Rank > out[j].Rank; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// renderFooter draws the always-visible hint strip. `?` and `q` are
// reserved (always present); other hints fill remaining width in Rank
// order.
func renderFooter(st styles, width int) string {
	if width <= 0 {
		return ""
	}
	render := func(b binding) string {
		return st.hintKey.Render(b.Key) + " " + st.hintDesc.Render(b.Desc)
	}
	// Reserve meta hints first so they're never dropped.
	reservedKeys := map[string]bool{"?": true, "q": true}
	var reserved, primary []binding
	for _, b := range footerHints() {
		if reservedKeys[b.Key] {
			reserved = append(reserved, b)
		} else {
			primary = append(primary, b)
		}
	}

	const sep = "  "
	cost := func(b binding, first bool) int {
		l := len(b.Key) + 1 + len(b.Desc)
		if !first {
			l += len(sep)
		}
		return l
	}

	// Budget = width minus the cost of all reserved.
	budget := width
	for i, r := range reserved {
		budget -= cost(r, i == 0 && len(primary) == 0)
	}
	// Reserved get a leading separator if any primary entries land before them.
	if len(reserved) > 0 {
		budget -= len(sep) * (1)
	}

	var picked []binding
	used := 0
	for _, p := range primary {
		c := cost(p, len(picked) == 0)
		if used+c > budget {
			break
		}
		used += c
		picked = append(picked, p)
	}

	all := make([]binding, 0, len(picked)+len(reserved))
	all = append(all, picked...)
	all = append(all, reserved...)
	parts := make([]string, 0, len(all))
	for _, b := range all {
		parts = append(parts, render(b))
	}
	return strings.Join(parts, sep)
}

// renderHelp draws the full bindings overlay centered over the body.
// width/height are the body dimensions to centre over.
func renderHelp(st styles, width, height int) string {
	groups := []string{"Navigate", "Mutate", "View", "Meta"}
	lines := []string{
		st.accent.Render("toykv-tui · keybindings"),
		"",
	}
	for _, g := range groups {
		lines = append(lines, st.colHeader.Render(g))
		for _, b := range bindings {
			if b.Group != g {
				continue
			}
			lines = append(lines, fmt.Sprintf("  %s  %s",
				st.hintKey.Render(padRight(b.Key, 6)),
				st.hintDesc.Render(b.Desc),
			))
		}
		lines = append(lines, "")
	}
	lines = append(lines, st.muted.Render("press ? or esc to close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(st.borderOn).
		Padding(0, 2).
		Render(strings.Join(lines, "\n"))

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

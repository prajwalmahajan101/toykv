package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// styles is the single source of truth for every named visual token in
// the TUI. One bundle is built at startup (honouring NO_COLOR) and
// threaded through View renderers.
type styles struct {
	accent     lipgloss.Style
	muted      lipgloss.Style
	header     lipgloss.Style
	statusKey  lipgloss.Style
	statusVal  lipgloss.Style
	errBanner  lipgloss.Style
	okBanner   lipgloss.Style
	warn       lipgloss.Style
	cursorRow  lipgloss.Style
	colHeader  lipgloss.Style
	hintKey    lipgloss.Style
	hintDesc   lipgloss.Style
	keyName    lipgloss.Style
	filterHit  lipgloss.Style
	respInt    lipgloss.Style
	respNil    lipgloss.Style
	respErr    lipgloss.Style
	borderOn   lipgloss.Color
	borderOff  lipgloss.Color
	paneOn     lipgloss.Style // bordered pane with focused accent
	paneOff    lipgloss.Style // bordered pane with muted accent
	tooSmall   lipgloss.Style
	promptMark lipgloss.Style
}

// newStyles returns a fresh style bundle. When noColor is true every
// style degrades to an identity Render — no ANSI escapes leak out — so
// NO_COLOR users get a strictly plain-text TUI without losing layout.
func newStyles(noColor bool) styles {
	if noColor {
		id := lipgloss.NewStyle()
		bold := lipgloss.NewStyle().Bold(true)
		reverse := lipgloss.NewStyle().Reverse(true)
		pane := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
		return styles{
			accent:     bold,
			muted:      id,
			header:     bold,
			statusKey:  id,
			statusVal:  id,
			errBanner:  bold,
			okBanner:   id,
			warn:       id,
			cursorRow:  reverse,
			colHeader:  bold,
			hintKey:    bold,
			hintDesc:   id,
			keyName:    id,
			filterHit:  bold,
			respInt:    id,
			respNil:    id,
			respErr:    bold,
			borderOn:   lipgloss.Color(""),
			borderOff:  lipgloss.Color(""),
			paneOn:     pane,
			paneOff:    pane,
			tooSmall:   bold,
			promptMark: bold,
		}
	}
	accent := lipgloss.AdaptiveColor{Light: "#0087d7", Dark: "#5fd7ff"}
	muted := lipgloss.AdaptiveColor{Light: "#6c6c6c", Dark: "#808080"}
	red := lipgloss.AdaptiveColor{Light: "#af0000", Dark: "#ff5f5f"}
	green := lipgloss.AdaptiveColor{Light: "#008700", Dark: "#5fd75f"}
	yellow := lipgloss.AdaptiveColor{Light: "#af8700", Dark: "#ffd75f"}

	return styles{
		accent:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		muted:     lipgloss.NewStyle().Foreground(muted),
		header:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		statusKey: lipgloss.NewStyle().Foreground(muted),
		statusVal: lipgloss.NewStyle(),
		errBanner: lipgloss.NewStyle().Foreground(red).Bold(true),
		okBanner:  lipgloss.NewStyle().Foreground(green),
		warn:      lipgloss.NewStyle().Foreground(yellow),
		cursorRow: lipgloss.NewStyle().Reverse(true).Bold(true),
		colHeader: lipgloss.NewStyle().Foreground(muted).Bold(true),
		hintKey:   lipgloss.NewStyle().Foreground(accent).Bold(true),
		hintDesc:  lipgloss.NewStyle().Foreground(muted),
		keyName:   lipgloss.NewStyle().Bold(true),
		filterHit: lipgloss.NewStyle().Foreground(yellow).Bold(true),
		respInt:   lipgloss.NewStyle().Foreground(accent),
		respNil:   lipgloss.NewStyle().Foreground(yellow),
		respErr:   lipgloss.NewStyle().Foreground(red).Bold(true),
		borderOn:  lipgloss.Color("#5fd7ff"),
		borderOff: lipgloss.Color("#3a3a3a"),
		paneOn: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#5fd7ff")),
		paneOff: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3a3a3a")),
		tooSmall:   lipgloss.NewStyle().Foreground(red).Bold(true),
		promptMark: lipgloss.NewStyle().Foreground(accent).Bold(true),
	}
}

// noColorEnv reports whether NO_COLOR is set to any non-empty value, per
// the no-color.org convention.
func noColorEnv() bool {
	return os.Getenv("NO_COLOR") != ""
}

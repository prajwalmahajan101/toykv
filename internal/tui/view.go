package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/prajwalmahajan101/toykv/internal/respfmt"
)

var (
	styleCursor = lipgloss.NewStyle().Reverse(true)
	styleHeader = lipgloss.NewStyle().Bold(true)
	styleStatus = lipgloss.NewStyle().Faint(true)
	styleErr    = lipgloss.NewStyle().Bold(true)
)

// View renders the two-pane layout, status bar, and any modal overlay.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "toykv-tui — waiting for terminal size…\n"
	}

	left := m.renderLeft(m.leftWidth(), m.bodyHeight())
	right := m.renderRight(m.rightWidth(), m.bodyHeight())

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	out := body + "\n" + m.renderStatus()
	if m.err != "" {
		out += "\n" + styleErr.Render("(error) "+m.err)
	}
	if m.prompt != "" {
		out += "\n" + m.prompt + m.input.View()
	}
	return out
}

func (m Model) leftWidth() int  { return m.width / 3 }
func (m Model) rightWidth() int { return m.width - m.leftWidth() }
func (m Model) bodyHeight() int {
	h := m.height - 3 // status + maybe err + prompt
	if h < 1 {
		return 1
	}
	return h
}

func (m Model) renderLeft(w, h int) string {
	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf(" keys (%d)", len(m.keys))))
	b.WriteString("\n")
	// Filter rows by client-side glob if active.
	rows := make([]KeyInfo, 0, len(m.keys))
	for _, k := range m.keys {
		if m.filter == "" || m.filter == "*" || globMatch(m.filter, k.Name) {
			rows = append(rows, k)
		}
	}
	limit := h - 1
	if limit < 0 {
		limit = 0
	}
	start := 0
	if m.cursor >= limit && len(rows) > limit {
		start = m.cursor - limit + 1
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	for i := start; i < end; i++ {
		row := fmt.Sprintf(" %-20s  ttl=%-7s  %s",
			truncate(rows[i].Name, 20),
			formatTTL(rows[i].TTL),
			formatBytes(rows[i].Size),
		)
		if i == m.cursor {
			row = styleCursor.Render(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

func (m Model) renderRight(w, h int) string {
	var b strings.Builder
	b.WriteString(styleHeader.Render(" value"))
	b.WriteString("\n")
	if m.focused == "" {
		b.WriteString(" (no key)\n")
	} else if !m.hasVal {
		b.WriteString(" (loading…)\n")
	} else {
		b.WriteString(respfmt.PrettyString(m.value))
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

func (m Model) renderStatus() string {
	parts := []string{
		m.status.Addr,
		fmt.Sprintf("dbsize=%d", m.status.DBSize),
	}
	if m.status.FsyncLabel != "" {
		parts = append(parts, "fsync="+m.status.FsyncLabel)
	}
	parts = append(parts, "lat="+formatLatency(m.status.Latency))
	if m.filter != "" && m.filter != "*" {
		parts = append(parts, "filter="+m.filter)
	}
	if m.mode == ModeConfirm {
		parts = append(parts, m.prompt)
	}
	return styleStatus.Render(strings.Join(parts, " · "))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

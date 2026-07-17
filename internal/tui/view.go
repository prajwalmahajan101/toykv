package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/prajwalmahajan101/toykv/internal/resp"
	"github.com/prajwalmahajan101/toykv/internal/respfmt"
)

// View renders the TUI. Layout depends on the breakpoint:
//
//	wide / mid / narrow → two-pane (list | value)
//	stack              → list above, value below
//	tiny               → "terminal too small" banner
//
// Headers, status, error, prompt, and footer hint bar wrap the body.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "toykv-tui — waiting for terminal size…\n"
	}
	if m.breakpoint() == LayoutTiny {
		msg := m.st.tooSmall.Render("terminal too small — need ≥ 60×16")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}

	header := m.renderHeader()
	footer := renderFooter(m.st, m.width)

	// When the help overlay is up it supersedes the err/prompt chrome rows
	// — the overlay area must match the height the renderer reserves for
	// it, otherwise lipgloss.Place centres into a smaller-than-expected
	// box and the layout drifts.
	overhead := 1 /*header*/ + 1 /*status*/ + 1 /*footer*/
	errLine := ""
	promptLine := ""
	if !m.showHelp {
		if m.err != "" {
			errLine = m.st.errBanner.Render("[!] " + m.err)
			overhead++
		}
		if m.prompt != "" {
			promptLine = m.st.promptMark.Render(m.prompt) + m.input.View()
			overhead++
		}
	}
	bodyH := m.height - overhead
	if bodyH < 3 {
		bodyH = 3
	}

	var body string
	if m.showHelp {
		body = renderHelp(m.st, m.width, bodyH)
	} else if m.breakpoint() == LayoutStack {
		body = m.renderStacked(m.width, bodyH)
	} else {
		left := m.renderLeft(m.leftWidth(), bodyH)
		right := m.renderRight(m.rightWidth(), bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}

	pieces := []string{header, body, m.renderStatus()}
	if errLine != "" {
		pieces = append(pieces, errLine)
	}
	if promptLine != "" {
		pieces = append(pieces, promptLine)
	}
	pieces = append(pieces, footer)
	return strings.Join(pieces, "\n")
}

func (m Model) leftWidth() int {
	switch m.breakpoint() {
	case LayoutNarrow:
		return m.width / 2
	default:
		return m.width * 2 / 5
	}
}

func (m Model) rightWidth() int { return m.width - m.leftWidth() }

// fsyncDisplay is the fsync label the status bar shows: the -fsync
// override when set, else the live appendfsync from INFO.
func (m Model) fsyncDisplay() string {
	if m.status.FsyncOverride != "" {
		return m.status.FsyncOverride
	}
	return m.status.FsyncLabel
}

// renderHeader draws the persistent context strip at the top. dbsize,
// fsync, uptime, and clients are driven by INFO (M14).
func (m Model) renderHeader() string {
	parts := []string{
		m.st.header.Render("toykv"),
		m.st.statusVal.Render(m.status.Addr),
	}
	if fs := m.fsyncDisplay(); fs != "" {
		parts = append(parts,
			m.st.statusKey.Render("fsync=")+m.st.statusVal.Render(fs))
	}
	parts = append(parts,
		m.st.statusKey.Render("lat=")+m.st.statusVal.Render(formatLatency(m.status.Latency)),
		m.st.statusKey.Render("dbsize=")+m.st.statusVal.Render(fmt.Sprintf("%d", m.status.DBSize)),
	)
	if m.status.Clients > 0 {
		parts = append(parts,
			m.st.statusKey.Render("clients=")+m.st.statusVal.Render(fmt.Sprintf("%d", m.status.Clients)))
	}
	if m.status.Uptime > 0 {
		parts = append(parts,
			m.st.statusKey.Render("up=")+m.st.statusVal.Render(formatUptime(m.status.Uptime)))
	}
	line := strings.Join(parts, "  ·  ")
	return lipgloss.NewStyle().Width(m.width).Render(line)
}

// renderStatus is the single-line filter / paging / mode echo just below
// the body.
func (m Model) renderStatus() string {
	var parts []string
	if m.filter != "" && m.filter != "*" {
		parts = append(parts, m.st.statusKey.Render("match=")+m.st.statusVal.Render(m.filter))
	}
	if m.mode == ModeConfirm {
		parts = append(parts, m.st.warn.Render(m.prompt))
	}
	if len(parts) == 0 {
		parts = append(parts, m.st.muted.Render(fmt.Sprintf("%d keys", len(m.keys))))
	}
	// Paging affordance: show the current page depth and whether more
	// pages follow. Page 1 with no next page stays silent to avoid noise
	// on small keyspaces.
	if page := len(m.cursorStack) + 1; page > 1 || m.nextCursor != 0 {
		ind := fmt.Sprintf("page %d", page)
		if m.nextCursor != 0 {
			ind += " →"
		}
		parts = append(parts, m.st.muted.Render(ind))
	}
	return lipgloss.NewStyle().Width(m.width).Render(strings.Join(parts, "  ·  "))
}

// pane returns a bordered, fixed-width box. `focused` selects between
// the two precomputed pane styles in the bundle so we don't rebuild the
// lipgloss.Style on every redraw.
func (m Model) pane(content string, w, h int, focused bool) string {
	base := m.st.paneOff
	if focused {
		base = m.st.paneOn
	}
	return base.Width(w - 2).Height(h - 2).Render(content)
}

// renderLeft draws the key list pane (bordered).
func (m Model) renderLeft(w, h int) string {
	nameW, sizeW, ttlW := m.colWidths(w - 2)

	var b strings.Builder

	// Column header row.
	colName := m.st.colHeader.Render(padRight("NAME", nameW))
	colTTL := m.st.colHeader.Render(padRight("TTL", ttlW))
	headerCols := []string{"  " + colName}
	if sizeW > 0 {
		headerCols = append(headerCols, m.st.colHeader.Render(padRight("SIZE", sizeW)))
	}
	headerCols = append(headerCols, colTTL)
	b.WriteString(strings.Join(headerCols, "  "))
	b.WriteString("\n")
	b.WriteString(m.st.muted.Render(strings.Repeat("─", w-3)))
	b.WriteString("\n")

	rows := m.visibleKeys()
	limit := h - 4
	if limit < 1 {
		limit = 1
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
		row := m.renderKeyRow(rows[i], nameW, sizeW, ttlW, i == m.cursor)
		b.WriteString(row)
		b.WriteString("\n")
	}

	focused := m.focus == FocusLeft
	return m.pane(b.String(), w, h, focused)
}

// renderKeyRow renders one row, applying cursor highlight if selected.
func (m Model) renderKeyRow(k KeyInfo, nameW, sizeW, ttlW int, isCursor bool) string {
	cursor := "  "
	if isCursor {
		cursor = m.st.accent.Render("▸ ")
	}

	name := truncate(k.Name, nameW)
	nameRendered := m.highlightFilter(name, nameW)

	cells := []string{cursor + nameRendered}
	if sizeW > 0 {
		cells = append(cells, padRight(formatBytes(k.Size), sizeW))
	}
	cells = append(cells, m.styleTTL(formatTTL(k.TTL), ttlW, k.TTL))

	row := strings.Join(cells, "  ")
	if isCursor {
		return m.st.cursorRow.Render(row)
	}
	return row
}

// styleTTL pads then applies the warn style when the countdown is short.
func (m Model) styleTTL(s string, w int, ttl int64) string {
	padded := padRight(s, w)
	switch {
	case ttl == -1, ttl == -2:
		return m.st.muted.Render(padded)
	case ttl > 0 && ttl < 60:
		return m.st.warn.Render(padded)
	default:
		return padded
	}
}

// highlightFilter wraps each literal segment of the glob filter that
// occurs in `name` with the filterHit style, then pads to `width` *after*
// styling so trailing spaces never end up inside an ANSI escape. Offsets
// are computed against the raw name; once a span has been claimed, later
// segments cannot re-claim or nest inside it.
func (m Model) highlightFilter(name string, width int) string {
	pad := strings.Repeat(" ", maxInt(0, width-len(name)))
	if m.filter == "" || m.filter == "*" {
		return name + pad
	}
	segs := globLiterals(m.filter)
	if len(segs) == 0 {
		return name + pad
	}

	type span struct{ start, end int }
	var claimed []span
	overlaps := func(start, end int) bool {
		for _, s := range claimed {
			if start < s.end && end > s.start {
				return true
			}
		}
		return false
	}

	for _, seg := range segs {
		if seg == "" {
			continue
		}
		// Find first non-overlapping match.
		searchFrom := 0
		for {
			idx := strings.Index(name[searchFrom:], seg)
			if idx < 0 {
				break
			}
			absStart, absEnd := searchFrom+idx, searchFrom+idx+len(seg)
			if overlaps(absStart, absEnd) {
				searchFrom = absStart + 1
				continue
			}
			claimed = append(claimed, span{absStart, absEnd})
			break
		}
	}
	if len(claimed) == 0 {
		return name + pad
	}

	// Order spans left→right and emit.
	for i := 1; i < len(claimed); i++ {
		for j := i; j > 0 && claimed[j-1].start > claimed[j].start; j-- {
			claimed[j-1], claimed[j] = claimed[j], claimed[j-1]
		}
	}
	var b strings.Builder
	cursor := 0
	for _, s := range claimed {
		if cursor < s.start {
			b.WriteString(name[cursor:s.start])
		}
		b.WriteString(m.st.filterHit.Render(name[s.start:s.end]))
		cursor = s.end
	}
	if cursor < len(name) {
		b.WriteString(name[cursor:])
	}
	b.WriteString(pad)
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// renderRight draws the value pane with metadata header + pretty value.
func (m Model) renderRight(w, h int) string {
	var b strings.Builder
	if m.focused == "" {
		b.WriteString(m.st.muted.Render("(no key)\n"))
		return m.pane(b.String(), w, h, m.focus == FocusRight)
	}

	b.WriteString(m.st.keyName.Render(m.focused))
	b.WriteString("\n")
	b.WriteString(m.st.muted.Render(strings.Repeat("─", w-4)))
	b.WriteString("\n")

	meta := []string{
		m.st.statusKey.Render("type ") + m.st.statusVal.Render(kindOrDefault(m.focusedKind())),
		m.st.statusKey.Render("ttl  ") + m.styleTTL(formatTTL(m.focusedTTL()), 8, m.focusedTTL()),
		m.st.statusKey.Render("size ") + m.st.statusVal.Render(formatBytes(m.focusedSize())),
	}
	b.WriteString(strings.Join(meta, "\n"))
	b.WriteString("\n\n")

	if !m.hasVal {
		b.WriteString(m.st.muted.Render("(loading…)"))
	} else {
		body := m.renderValueBody()
		if m.valueScroll > 0 {
			lines := strings.Split(body, "\n")
			if m.valueScroll >= len(lines) {
				lines = nil
			} else {
				lines = lines[m.valueScroll:]
			}
			body = strings.Join(lines, "\n")
		}
		b.WriteString(body)
	}

	return m.pane(b.String(), w, h, m.focus == FocusRight)
}

// renderValueBody renders the focused value according to its type. Hashes
// get a "field: value" layout (RESP2 HGETALL is a flat array, so pairs are
// read two at a time); lists and strings fall through to the shared
// redis-cli-style pretty-printer (a list is a numbered array, a string is
// a quoted scalar).
func (m Model) renderValueBody() string {
	if m.focusedKind() == KindHash && m.value.Kind == resp.KindArray && !m.value.IsNull {
		return m.renderHash(m.value)
	}
	return m.colorizePretty(respfmt.PrettyString(m.value))
}

// renderHash lays out a flat [f1,v1,f2,v2,…] array as aligned
// "field: value" rows.
func (m Model) renderHash(v resp.Value) string {
	if len(v.Array) == 0 {
		return m.st.muted.Render("(empty hash)")
	}
	var b strings.Builder
	for i := 0; i+1 < len(v.Array); i += 2 {
		field := respfmt.RawString(v.Array[i])
		val := respfmt.PrettyString(v.Array[i+1])
		b.WriteString(m.st.statusKey.Render(field))
		b.WriteString(m.st.muted.Render(": "))
		b.WriteString(val)
		if i+2 < len(v.Array) {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderStacked draws list-above-value for narrow terminals.
func (m Model) renderStacked(w, totalH int) string {
	listH := totalH * 6 / 10
	valH := totalH - listH
	left := m.renderLeft(w, listH)
	right := m.renderRight(w, valH)
	return lipgloss.JoinVertical(lipgloss.Left, left, right)
}

// colorizePretty applies semantic colour to the existing PrettyString
// output. It is line-oriented so it can't be tricked by embedded quotes.
func (m Model) colorizePretty(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "(integer)"):
			lines[i] = m.st.respInt.Render(line)
		case strings.HasPrefix(line, "(nil)"):
			lines[i] = m.st.respNil.Render(line)
		case strings.HasPrefix(line, "(error)"):
			lines[i] = m.st.respErr.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// visibleKeys returns the key slice the renderer iterates over. Filtering
// is now server-side via SCAN MATCH (M14), so this is the page as-is; the
// filter pattern is still used to highlight the matched span in each name.
func (m Model) visibleKeys() []KeyInfo {
	return m.keys
}

func (m Model) focusedKind() string {
	if m.cursor >= 0 && m.cursor < len(m.keys) {
		return m.keys[m.cursor].Kind
	}
	return ""
}

func (m Model) focusedTTL() int64 {
	if m.cursor >= 0 && m.cursor < len(m.keys) {
		return m.keys[m.cursor].TTL
	}
	return -1
}

func (m Model) focusedSize() int {
	if m.cursor >= 0 && m.cursor < len(m.keys) {
		return m.keys[m.cursor].Size
	}
	return 0
}

func kindOrDefault(k string) string {
	if k == "" {
		return "string"
	}
	return k
}

// globLiterals splits an fnmatch-style glob into its literal segments
// (the parts between *, ?, and []). Used to highlight what actually
// matched in the key list.
func globLiterals(g string) []string {
	out := []string{}
	cur := strings.Builder{}
	for i := 0; i < len(g); i++ {
		c := g[i]
		if c == '*' || c == '?' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		if c == '[' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			for i < len(g) && g[i] != ']' {
				i++
			}
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
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

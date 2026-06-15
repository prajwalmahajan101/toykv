package tui

import (
	"fmt"
	"time"
)

// formatTTL renders a TTL integer (seconds) for the left pane.
// -1 = no expiry, -2 = key missing, >=0 = countdown.
func formatTTL(ttl int64) string {
	switch ttl {
	case -1:
		return "—"
	case -2:
		return "gone"
	}
	d := time.Duration(ttl) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", ttl)
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", ttl/60, ttl%60)
	default:
		h := ttl / 3600
		m := (ttl % 3600) / 60
		return fmt.Sprintf("%dh%02dm", h, m)
	}
}

// formatBytes renders a size in bytes with a single-letter suffix.
func formatBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
}

// formatLatency renders the status-bar latency.
func formatLatency(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	if d < time.Microsecond {
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

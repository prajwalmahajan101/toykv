package tui

import (
	"testing"
	"time"
)

func TestFormatTTL(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "—"},
		{-2, "gone"},
		{0, "0s"},
		{42, "42s"},
		{61, "1m01s"},
		{3600, "1h00m"},
		{3661, "1h01m"},
	}
	for _, c := range cases {
		if got := formatTTL(c.in); got != c.want {
			t.Errorf("formatTTL(%d)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{2048, "2.0K"},
		{2 * 1024 * 1024, "2.0M"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestFormatLatency(t *testing.T) {
	if formatLatency(0) != "—" {
		t.Fatal("zero latency")
	}
	if got := formatLatency(500 * time.Microsecond); got != "500µs" {
		t.Errorf("got %q", got)
	}
	if got := formatLatency(3 * time.Millisecond); got != "3ms" {
		t.Errorf("got %q", got)
	}
}

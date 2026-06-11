package aof

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// ReplayStats summarises a replay run.
type ReplayStats struct {
	Records  int
	Bytes    int64
	Duration time.Duration
}

// Apply receives an argv exactly as a real connection would send it.
// The server injects a handler that dispatches commands without
// re-appending to the AOF.
type Apply func(argv [][]byte) error

// Replay reads the AOF in dir from the beginning and invokes apply for
// every record. A clean EOF returns nil. A header mismatch or a
// mid-stream parse error returns *ReplayError with the byte offset of
// the failing record. The file is opened read-only.
func Replay(dir string, apply Apply) (ReplayStats, error) {
	if apply == nil {
		return ReplayStats{}, errors.New("aof: apply must be non-nil")
	}
	path := filepath.Join(dir, Filename)

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReplayStats{}, nil // no file yet — nothing to replay
		}
		return ReplayStats{}, fmt.Errorf("aof: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	start := time.Now()

	if _, err := readHeader(f); err != nil {
		return ReplayStats{}, &ReplayError{Offset: 0, Err: err}
	}

	// Track byte offset via a counting wrapper so a mid-stream failure
	// reports a useful position. RESP frames are read through a
	// buffered reader, so we count bytes consumed *before* buffering.
	cr := &countingReader{r: f, n: int64(HeaderLen)}
	rr := resp.NewReader(cr)

	var stats ReplayStats
	for {
		preOffset := cr.n
		argv, err := rr.ReadCommand()
		if err != nil {
			if errors.Is(err, io.EOF) {
				stats.Bytes = preOffset
				stats.Duration = time.Since(start)
				return stats, nil
			}
			// Map io.ErrUnexpectedEOF (mid-frame truncation) to a clearer
			// sentinel for callers/operators.
			cause := err
			if errors.Is(err, io.ErrUnexpectedEOF) {
				cause = fmt.Errorf("%w: %s", ErrShortRecord, err)
			}
			return stats, &ReplayError{Offset: preOffset, Err: cause}
		}
		if err := apply(argv); err != nil {
			return stats, &ReplayError{Offset: preOffset, Err: fmt.Errorf("apply: %w", err)}
		}
		stats.Records++
	}
}

// countingReader wraps an io.Reader and tracks how many bytes have been
// read through it. Because the resp.Reader buffers internally, this
// counts bytes consumed from the underlying file rather than bytes
// returned to the resp.Reader — close enough for error-offset reporting,
// which is what we need.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

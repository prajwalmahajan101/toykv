package aof

import (
	"errors"
	"fmt"
)

// Sentinel errors. Server startup maps these to a hard-fail (no
// auto-truncate in v1; see ADR-0001).
var (
	// ErrBadHeader is returned when the file's magic does not match.
	ErrBadHeader = errors.New("aof: bad header")

	// ErrBadVersion is returned when the file's version byte is not
	// supported by this binary.
	ErrBadVersion = errors.New("aof: unsupported version")

	// ErrShortRecord is returned when a record is truncated mid-frame.
	ErrShortRecord = errors.New("aof: short record")
)

// ReplayError wraps the underlying replay failure with the byte offset
// of the failing record so the operator can point at the file location
// without guessing.
type ReplayError struct {
	Offset int64
	Err    error
}

func (e *ReplayError) Error() string {
	return fmt.Sprintf("aof: replay failed at offset %d: %s", e.Offset, e.Err.Error())
}

func (e *ReplayError) Unwrap() error { return e.Err }

package store

import "errors"

// Sentinel errors returned by Store. Handlers map these to RESP error
// replies per LLD §5.4.
var (
	// ErrNotInteger is returned by Incr/Decr when the existing value
	// does not parse as a base-10 int64.
	ErrNotInteger = errors.New("store: not an integer")

	// ErrOverflow is returned by Incr/Decr when the operation would
	// overflow or underflow int64.
	ErrOverflow = errors.New("store: integer overflow")
)

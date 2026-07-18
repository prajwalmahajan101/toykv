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

	// ErrWrongType is returned when an operation is applied to a key
	// holding a different value type (e.g. GET on a list). Handlers map
	// it to Redis's -WRONGTYPE error verbatim.
	ErrWrongType = errors.New("store: wrong type")

	// ErrNoKey is returned by Rename/RenameNX when the source key is
	// absent or expired. Handlers map it to Redis's "-ERR no such key".
	ErrNoKey = errors.New("store: no such key")

	// ErrSameObject is returned by Copy when source and destination name
	// the same key. Handlers map it to Redis's "-ERR source and
	// destination objects are the same".
	ErrSameObject = errors.New("store: source and destination objects are the same")
)

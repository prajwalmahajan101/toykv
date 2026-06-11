package resp

import "errors"

// MaxBulkSize caps the largest bulk string the codec will accept. Bulk
// strings longer than this are rejected with ErrTooLarge before any
// allocation happens.
const MaxBulkSize = 64 << 20 // 64 MiB

// Sentinel errors returned by the codec. Callers should use errors.Is to
// match.
var (
	// ErrProtocol marks any byte-level RESP grammar violation: bad
	// prefix, missing CRLF, non-numeric length, mixed-kind command
	// array, etc.
	ErrProtocol = errors.New("resp: protocol error")

	// ErrInvalidArity is returned by ReadCommand when the array has
	// zero elements (command must have at least the verb).
	ErrInvalidArity = errors.New("resp: invalid array arity")

	// ErrTooLarge is returned when a bulk-string length exceeds
	// MaxBulkSize.
	ErrTooLarge = errors.New("resp: frame exceeds size limit")
)

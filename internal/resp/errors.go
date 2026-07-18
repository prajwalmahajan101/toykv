package resp

import "errors"

// MaxBulkSize caps the largest bulk string the codec will accept. Bulk
// strings longer than this are rejected with ErrTooLarge before any
// allocation happens.
const MaxBulkSize = 64 << 20 // 64 MiB

// MaxArrayLen caps the element count of a single array frame. Without it,
// an attacker-chosen count (up to math.MaxInt64) would drive a
// make([]Value, n) allocation before a single element is read — a
// single-packet pre-auth memory-amplification DoS. Matches Redis's
// proto-max-multibulk-len default (1M). Counts over this are rejected
// with ErrTooLarge before any allocation happens.
const MaxArrayLen = 1 << 20 // 1,048,576 elements

// MaxDepth caps how deeply arrays may nest. Each array element is decoded
// recursively; without a bound, a stream of nested `*1` headers exhausts
// the goroutine stack (an un-recoverable fatal panic) — another
// single-connection pre-auth DoS. Inbound commands are flat top-level
// arrays of bulk strings, so this bound is generous. Frames nested deeper
// are rejected with ErrTooLarge.
const MaxDepth = 32

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

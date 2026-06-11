package resp

// Kind identifies the RESP2 frame type by its first-byte prefix.
type Kind byte

// RESP2 frame prefixes.
const (
	KindSimpleString Kind = '+'
	KindError        Kind = '-'
	KindInteger      Kind = ':'
	KindBulkString   Kind = '$'
	KindArray        Kind = '*'
)

// Value is a decoded RESP2 frame. Kind selects which of the other fields
// carries the payload; the unused fields are zero-valued. IsNull
// distinguishes `$-1`/`*-1` (the RESP nil) from an empty bulk-string or
// empty array, which RESP encodes distinctly.
type Value struct {
	Kind   Kind
	Str    string  // simple string or error message
	Int    int64   // integer reply
	Bytes  []byte  // bulk string payload (may be empty; IsNull marks nil)
	Array  []Value // array elements (may be empty; IsNull marks nil)
	IsNull bool    // true ⇒ nil-bulk ($-1) or nil-array (*-1)
}

// String wraps a simple-string reply (`+OK`-style).
func String(s string) Value { return Value{Kind: KindSimpleString, Str: s} }

// Error wraps an error reply (`-ERR …`). The leading `-` is added by the writer.
func Error(s string) Value { return Value{Kind: KindError, Str: s} }

// Int wraps an integer reply.
func Int(n int64) Value { return Value{Kind: KindInteger, Int: n} }

// Bulk wraps a bulk-string reply. Pass an empty slice for the empty bulk
// (`$0\r\n\r\n`); use NullBulk for the nil bulk (`$-1\r\n`).
func Bulk(b []byte) Value { return Value{Kind: KindBulkString, Bytes: b} }

// NullBulk is the RESP nil bulk-string reply (`$-1\r\n`).
func NullBulk() Value { return Value{Kind: KindBulkString, IsNull: true} }

// Array wraps an array reply.
func Array(vs ...Value) Value { return Value{Kind: KindArray, Array: vs} }

// NullArray is the RESP nil array reply (`*-1\r\n`).
func NullArray() Value { return Value{Kind: KindArray, IsNull: true} }

// OK is the canonical +OK simple-string reply.
func OK() Value { return String("OK") }

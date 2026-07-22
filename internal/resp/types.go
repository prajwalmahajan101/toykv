package resp

// Kind identifies the RESP frame type by its first-byte prefix.
type Kind byte

// RESP2 frame prefixes.
const (
	KindSimpleString Kind = '+'
	KindError        Kind = '-'
	KindInteger      Kind = ':'
	KindBulkString   Kind = '$'
	KindArray        Kind = '*'
)

// RESP3 frame prefixes (M10). These are emitted natively only to a
// connection that negotiated proto 3 via HELLO; on proto 2 the writer
// downgrades each to its RESP2 equivalent (see writer.go). Clients still
// send commands as RESP2 arrays regardless of negotiated protocol, but a
// proto-3 client reads these kinds back in replies — the Reader decodes
// them symmetrically with the Writer (see reader.go).
const (
	KindMap      Kind = '%' // Array holds flat [k1,v1,k2,v2,…]
	KindSet      Kind = '~' // Array holds elements
	KindDouble   Kind = ',' // Float carries the value
	KindBoolean  Kind = '#' // Bool carries the value
	KindNull     Kind = '_' // RESP3 null; downgrades to $-1
	KindVerbatim Kind = '=' // Bytes body, VerbatimFmt is the 3-char format
	KindPush     Kind = '>' // Array holds elements (out-of-band frame)
)

// Proto is a negotiated wire-protocol version. Default connections are
// Proto2; HELLO 3 upgrades a connection to Proto3.
type Proto int

// Supported protocol versions.
const (
	Proto2 Proto = 2
	Proto3 Proto = 3
)

// Value is a decoded RESP frame. Kind selects which of the other fields
// carries the payload; the unused fields are zero-valued. IsNull
// distinguishes `$-1`/`*-1` (the RESP2 nil) from an empty bulk-string or
// empty array, which RESP encodes distinctly.
type Value struct {
	Kind        Kind
	Str         string  // simple string or error message
	Int         int64   // integer reply
	Bytes       []byte  // bulk / verbatim payload (may be empty; IsNull marks nil)
	Array       []Value // array / set / push elements, or flat map pairs (IsNull marks nil)
	IsNull      bool    // true ⇒ nil-bulk ($-1) or nil-array (*-1)
	Float       float64 // double reply
	Bool        bool    // boolean reply
	VerbatimFmt string  // 3-char verbatim format tag, e.g. "txt"
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

// Map wraps a RESP3 map reply (`%`). pairs must be a flat, even-length
// sequence [k1, v1, k2, v2, …]. On proto 2 the writer downgrades it to a
// flat array of the same elements.
func Map(pairs ...Value) Value { return Value{Kind: KindMap, Array: pairs} }

// Set wraps a RESP3 set reply (`~`). On proto 2 it downgrades to an array.
func Set(vs ...Value) Value { return Value{Kind: KindSet, Array: vs} }

// Push wraps a RESP3 out-of-band push frame (`>`). On proto 2 it
// downgrades to an array. M10 ships the encoder scaffold; the pub/sub
// producers that use it arrive in v3.
func Push(vs ...Value) Value { return Value{Kind: KindPush, Array: vs} }

// Double wraps a RESP3 double reply (`,`). On proto 2 it downgrades to a
// bulk string carrying the same numeric text.
func Double(f float64) Value { return Value{Kind: KindDouble, Float: f} }

// Boolean wraps a RESP3 boolean reply (`#t`/`#f`). On proto 2 it
// downgrades to the integer `:1` / `:0`.
func Boolean(b bool) Value { return Value{Kind: KindBoolean, Bool: b} }

// Null wraps the RESP3 null reply (`_`). On proto 2 it downgrades to the
// nil bulk string (`$-1`), byte-identical to NullBulk — so migrating a
// handler from NullBulk to Null is invisible to RESP2 clients and only
// enriches RESP3 ones.
func Null() Value { return Value{Kind: KindNull} }

// Verbatim wraps a RESP3 verbatim-string reply (`=`). format is the
// 3-char content type (e.g. "txt", "mkd"). On proto 2 it downgrades to a
// plain bulk string of the body (format tag dropped).
func Verbatim(format string, b []byte) Value {
	return Value{Kind: KindVerbatim, VerbatimFmt: format, Bytes: b}
}

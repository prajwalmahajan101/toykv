package resp

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
)

// Writer encodes RESP frames into an io.Writer. Writes are buffered;
// callers must call Flush before the bytes are visible to the peer.
//
// Writer is not safe for concurrent use.
type Writer struct {
	bw *bufio.Writer
}

// NewWriter returns a Writer that buffers writes to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{bw: bufio.NewWriter(w)}
}

// WriteFrame encodes v as a RESP2 frame and writes it to the buffer. It
// does not flush. This is the entry point for inherently-RESP2 producers
// — AOF record encoding and the outbound command path in internal/client
// — which never negotiate RESP3. The server reply path uses
// WriteFrameProto with the connection's negotiated protocol.
func (w *Writer) WriteFrame(v Value) error {
	return w.WriteFrameProto(v, Proto2)
}

// WriteFrameProto encodes v for a connection negotiated at protocol p and
// writes it to the buffer. It does not flush.
//
// The RESP2 kinds (+ - : $ *) encode identically at both protocol
// versions. The RESP3 kinds (% ~ , # _ = >) encode natively at Proto3;
// at Proto2 each is downgraded to its RESP2 equivalent at this single
// point, so command handlers stay protocol-agnostic (ADR-0011).
func (w *Writer) WriteFrameProto(v Value, p Proto) error {
	switch v.Kind {
	case KindSimpleString:
		return w.writePrefixed('+', v.Str)
	case KindError:
		return w.writePrefixed('-', v.Str)
	case KindInteger:
		return w.writePrefixed(':', strconv.FormatInt(v.Int, 10))
	case KindBulkString:
		return w.writeBulk(v)
	case KindArray:
		return w.writeArray('*', v, p)
	case KindMap:
		return w.writeMap(v, p)
	case KindSet:
		if p == Proto2 {
			return w.writeArray('*', Value{Kind: KindArray, Array: v.Array}, p)
		}
		return w.writeAggregate('~', v, p)
	case KindPush:
		if p == Proto2 {
			return w.writeArray('*', Value{Kind: KindArray, Array: v.Array}, p)
		}
		return w.writeAggregate('>', v, p)
	case KindDouble:
		return w.writeDouble(v, p)
	case KindBoolean:
		return w.writeBoolean(v, p)
	case KindNull:
		return w.writeNull(p)
	case KindVerbatim:
		return w.writeVerbatim(v, p)
	default:
		return fmt.Errorf("resp: cannot write unknown kind %q", byte(v.Kind))
	}
}

// Flush writes any buffered bytes to the underlying writer.
func (w *Writer) Flush() error { return w.bw.Flush() }

func (w *Writer) writePrefixed(prefix byte, s string) error {
	if err := w.bw.WriteByte(prefix); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(s); err != nil {
		return err
	}
	_, err := w.bw.WriteString("\r\n")
	return err
}

func (w *Writer) writeBulk(v Value) error {
	if v.IsNull {
		_, err := w.bw.WriteString("$-1\r\n")
		return err
	}
	return w.writeBulkBytes(v.Bytes)
}

// writeBulkBytes emits a non-nil bulk string ($<len>\r\n<body>\r\n).
func (w *Writer) writeBulkBytes(b []byte) error {
	if err := w.bw.WriteByte('$'); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(strconv.Itoa(len(b))); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\r\n"); err != nil {
		return err
	}
	if _, err := w.bw.Write(b); err != nil {
		return err
	}
	_, err := w.bw.WriteString("\r\n")
	return err
}

// writeArray emits an array-shaped aggregate under the given prefix,
// honouring the RESP2 nil-array form (*-1). Used for KindArray and for
// the proto-2 downgrade of sets/pushes.
func (w *Writer) writeArray(prefix byte, v Value, p Proto) error {
	if v.IsNull {
		_, err := w.bw.WriteString("*-1\r\n")
		return err
	}
	return w.writeAggregate(prefix, v, p)
}

// writeAggregate emits <prefix><count>\r\n followed by each element,
// where count is len(Array). Elements recurse at the same protocol.
func (w *Writer) writeAggregate(prefix byte, v Value, p Proto) error {
	if err := w.writeHeader(prefix, len(v.Array)); err != nil {
		return err
	}
	for _, el := range v.Array {
		if err := w.WriteFrameProto(el, p); err != nil {
			return err
		}
	}
	return nil
}

// writeMap emits a RESP3 map (%<pairs>) at Proto3, or a flat array of the
// same elements at Proto2. Array holds [k1,v1,…]; an odd length is a
// programming error and is written as-is (the count still divides by 2).
func (w *Writer) writeMap(v Value, p Proto) error {
	if p == Proto2 {
		return w.writeAggregate('*', v, p)
	}
	if err := w.writeHeader('%', len(v.Array)/2); err != nil {
		return err
	}
	for _, el := range v.Array {
		if err := w.WriteFrameProto(el, p); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) writeDouble(v Value, p Proto) error {
	text := formatDouble(v.Float)
	if p == Proto2 {
		return w.writeBulkBytes([]byte(text))
	}
	return w.writePrefixed(',', text)
}

func (w *Writer) writeBoolean(v Value, p Proto) error {
	if p == Proto2 {
		if v.Bool {
			return w.writePrefixed(':', "1")
		}
		return w.writePrefixed(':', "0")
	}
	if v.Bool {
		return w.writePrefixed('#', "t")
	}
	return w.writePrefixed('#', "f")
}

func (w *Writer) writeNull(p Proto) error {
	if p == Proto2 {
		_, err := w.bw.WriteString("$-1\r\n")
		return err
	}
	_, err := w.bw.WriteString("_\r\n")
	return err
}

// writeVerbatim emits a RESP3 verbatim string (=<len>\r\n<fmt>:<body>\r\n)
// at Proto3, or a plain bulk string of the body at Proto2. The declared
// length covers the 3-char format tag, the ':' separator, and the body.
func (w *Writer) writeVerbatim(v Value, p Proto) error {
	if p == Proto2 {
		return w.writeBulkBytes(v.Bytes)
	}
	fmtTag := v.VerbatimFmt
	if len(fmtTag) != 3 {
		return fmt.Errorf("resp: verbatim format %q must be 3 chars", fmtTag)
	}
	total := len(fmtTag) + 1 + len(v.Bytes)
	if err := w.bw.WriteByte('='); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(strconv.Itoa(total)); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\r\n"); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(fmtTag); err != nil {
		return err
	}
	if err := w.bw.WriteByte(':'); err != nil {
		return err
	}
	if _, err := w.bw.Write(v.Bytes); err != nil {
		return err
	}
	_, err := w.bw.WriteString("\r\n")
	return err
}

// writeHeader emits <prefix><n>\r\n.
func (w *Writer) writeHeader(prefix byte, n int) error {
	if err := w.bw.WriteByte(prefix); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(strconv.Itoa(n)); err != nil {
		return err
	}
	_, err := w.bw.WriteString("\r\n")
	return err
}

// formatDouble renders a RESP3 double body. Non-finite values use Redis's
// textual forms (inf / -inf / nan); finite values use the shortest
// round-trippable decimal. The same text is reused for the Proto2 bulk
// downgrade so a value is byte-stable across a protocol switch modulo the
// frame prefix.
func formatDouble(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	default:
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
}

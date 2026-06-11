package resp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// Writer encodes RESP2 frames into an io.Writer. Writes are buffered;
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

// WriteFrame encodes v and writes it to the buffer. It does not flush.
func (w *Writer) WriteFrame(v Value) error {
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
		return w.writeArray(v)
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
	if err := w.bw.WriteByte('$'); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(strconv.Itoa(len(v.Bytes))); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\r\n"); err != nil {
		return err
	}
	if _, err := w.bw.Write(v.Bytes); err != nil {
		return err
	}
	_, err := w.bw.WriteString("\r\n")
	return err
}

func (w *Writer) writeArray(v Value) error {
	if v.IsNull {
		_, err := w.bw.WriteString("*-1\r\n")
		return err
	}
	if err := w.bw.WriteByte('*'); err != nil {
		return err
	}
	if _, err := w.bw.WriteString(strconv.Itoa(len(v.Array))); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\r\n"); err != nil {
		return err
	}
	for _, el := range v.Array {
		if err := w.WriteFrame(el); err != nil {
			return err
		}
	}
	return nil
}

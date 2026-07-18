package resp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

const readerBufSize = 16 << 10 // 16 KiB

// Reader decodes RESP2 frames from an io.Reader. It wraps the input in
// a buffered reader sized for typical command traffic; bulk strings
// larger than the buffer are streamed directly into a fresh slice.
//
// Reader is not safe for concurrent use.
type Reader struct {
	br *bufio.Reader
}

// NewReader returns a Reader that buffers reads from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, readerBufSize)}
}

// ReadFrame returns the next top-level RESP frame. On clean EOF the
// returned error is io.EOF. On grammar violations the error wraps
// ErrProtocol (or ErrTooLarge); after such an error the underlying
// stream's state is undefined and the caller should close the
// connection.
func (r *Reader) ReadFrame() (Value, error) {
	return r.readFrame(0)
}

// readFrame decodes one frame, carrying the current array-nesting depth so
// readArray can reject over-deep nesting before it recurses further.
func (r *Reader) readFrame(depth int) (Value, error) {
	prefix, err := r.br.ReadByte()
	if err != nil {
		return Value{}, err
	}
	switch Kind(prefix) {
	case KindSimpleString:
		s, err := r.readLine()
		if err != nil {
			return Value{}, err
		}
		return String(s), nil
	case KindError:
		s, err := r.readLine()
		if err != nil {
			return Value{}, err
		}
		return Error(s), nil
	case KindInteger:
		n, err := r.readInt()
		if err != nil {
			return Value{}, err
		}
		return Int(n), nil
	case KindBulkString:
		return r.readBulk()
	case KindArray:
		return r.readArray(depth)
	default:
		return Value{}, fmt.Errorf("resp: unknown prefix %q: %w", prefix, ErrProtocol)
	}
}

// ReadCommand reads a top-level array and returns its bulk-string
// elements as the command's argv. It is the convenience entry point
// used by the server for inbound commands.
//
// Errors map as follows:
//   - io.EOF                     ⇒ clean client disconnect
//   - wraps ErrProtocol          ⇒ malformed frame (close conn)
//   - wraps ErrInvalidArity      ⇒ array had zero elements
//   - wraps ErrTooLarge          ⇒ a bulk string exceeded MaxBulkSize
//
// The returned slice and its elements are not retained by the Reader.
func (r *Reader) ReadCommand() ([][]byte, error) {
	v, err := r.ReadFrame()
	if err != nil {
		return nil, err
	}
	if v.Kind != KindArray {
		return nil, fmt.Errorf("resp: expected array, got %q: %w", byte(v.Kind), ErrProtocol)
	}
	if v.IsNull {
		return nil, fmt.Errorf("resp: nil array for command: %w", ErrProtocol)
	}
	if len(v.Array) == 0 {
		return nil, ErrInvalidArity
	}
	argv := make([][]byte, len(v.Array))
	for i, el := range v.Array {
		if el.Kind != KindBulkString {
			return nil, fmt.Errorf("resp: command argv[%d] is %q, want bulk: %w", i, byte(el.Kind), ErrProtocol)
		}
		if el.IsNull {
			return nil, fmt.Errorf("resp: command argv[%d] is nil bulk: %w", i, ErrProtocol)
		}
		argv[i] = el.Bytes
	}
	return argv, nil
}

// readLine returns the next line excluding the terminating CRLF. It
// rejects lines that lack a `\r\n` terminator with ErrProtocol.
func (r *Reader) readLine() (string, error) {
	// ReadSlice up to '\n' so we can verify the preceding '\r'.
	line, err := r.br.ReadSlice('\n')
	if err != nil {
		if err == bufio.ErrBufferFull {
			return "", fmt.Errorf("resp: line exceeds buffer: %w", ErrTooLarge)
		}
		return "", err
	}
	n := len(line)
	if n < 2 || line[n-2] != '\r' {
		return "", fmt.Errorf("resp: missing CRLF: %w", ErrProtocol)
	}
	return string(line[:n-2]), nil
}

// readInt reads an integer line and returns its value.
func (r *Reader) readInt() (int64, error) {
	s, err := r.readLine()
	if err != nil {
		return 0, err
	}
	n, perr := strconv.ParseInt(s, 10, 64)
	if perr != nil {
		return 0, fmt.Errorf("resp: bad integer %q: %w", s, ErrProtocol)
	}
	return n, nil
}

// readBulk reads a bulk-string frame, assuming the `$` prefix has
// already been consumed.
func (r *Reader) readBulk() (Value, error) {
	n, err := r.readInt()
	if err != nil {
		return Value{}, err
	}
	if n == -1 {
		return NullBulk(), nil
	}
	if n < 0 {
		return Value{}, fmt.Errorf("resp: negative bulk length %d: %w", n, ErrProtocol)
	}
	if n > MaxBulkSize {
		return Value{}, fmt.Errorf("resp: bulk length %d > %d: %w", n, MaxBulkSize, ErrTooLarge)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		if err == io.ErrUnexpectedEOF {
			return Value{}, fmt.Errorf("resp: short bulk body: %w", ErrProtocol)
		}
		return Value{}, err
	}
	// Trailing CRLF.
	cr, err := r.br.ReadByte()
	if err != nil {
		return Value{}, err
	}
	lf, err := r.br.ReadByte()
	if err != nil {
		return Value{}, err
	}
	if cr != '\r' || lf != '\n' {
		return Value{}, fmt.Errorf("resp: bulk missing CRLF tail: %w", ErrProtocol)
	}
	return Bulk(buf), nil
}

// readArray reads an array frame, assuming the `*` prefix has already
// been consumed. depth is this array's nesting level (0 for a top-level
// frame); elements are decoded at depth+1.
func (r *Reader) readArray(depth int) (Value, error) {
	if depth >= MaxDepth {
		return Value{}, fmt.Errorf("resp: array nesting exceeds depth %d: %w", MaxDepth, ErrTooLarge)
	}
	n, err := r.readInt()
	if err != nil {
		return Value{}, err
	}
	if n == -1 {
		return NullArray(), nil
	}
	if n < 0 {
		return Value{}, fmt.Errorf("resp: negative array length %d: %w", n, ErrProtocol)
	}
	if n > MaxArrayLen {
		return Value{}, fmt.Errorf("resp: array length %d > %d: %w", n, MaxArrayLen, ErrTooLarge)
	}
	elems := make([]Value, n)
	for i := int64(0); i < n; i++ {
		v, err := r.readFrame(depth + 1)
		if err != nil {
			return Value{}, err
		}
		elems[i] = v
	}
	return Array(elems...), nil
}

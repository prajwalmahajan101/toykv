package aof

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// File format constants (LLD §4.1).
const (
	// HeaderLen is the total header size in bytes (7-byte magic + 1-byte
	// version).
	HeaderLen = 8

	// Version1 is the v1 record format: RESP-encoded command arrays with
	// no TTL encoding. M4 will introduce Version2.
	Version1 byte = 0x01
)

// Magic is the 7-byte file magic. Padded with NULs so the full header is
// 8 bytes including the version byte.
var Magic = [7]byte{'T', 'O', 'Y', 'K', 'V', 0x00, 0x00}

// writeHeader writes the magic and current version into w.
func writeHeader(w io.Writer) error {
	buf := make([]byte, HeaderLen)
	copy(buf, Magic[:])
	buf[HeaderLen-1] = Version1
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("aof: write header: %w", err)
	}
	return nil
}

// readHeader reads exactly HeaderLen bytes from r, validates the magic,
// and returns the version byte.
func readHeader(r io.Reader) (byte, error) {
	buf := make([]byte, HeaderLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, fmt.Errorf("aof: header truncated: %w", ErrBadHeader)
		}
		return 0, fmt.Errorf("aof: read header: %w", err)
	}
	if !bytes.Equal(buf[:len(Magic)], Magic[:]) {
		return 0, fmt.Errorf("aof: magic mismatch %x: %w", buf[:len(Magic)], ErrBadHeader)
	}
	version := buf[HeaderLen-1]
	if version != Version1 {
		return 0, fmt.Errorf("aof: version 0x%02x: %w", version, ErrBadVersion)
	}
	return version, nil
}

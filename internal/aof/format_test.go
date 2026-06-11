package aof

import (
	"bytes"
	"errors"
	"testing"
)

func TestHeader_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeHeader(&buf); err != nil {
		t.Fatalf("writeHeader: %v", err)
	}
	if buf.Len() != HeaderLen {
		t.Fatalf("header length = %d, want %d", buf.Len(), HeaderLen)
	}
	v, err := readHeader(&buf)
	if err != nil {
		t.Fatalf("readHeader: %v", err)
	}
	if v != Version1 {
		t.Fatalf("version = %x, want %x", v, Version1)
	}
}

func TestHeader_BadMagic(t *testing.T) {
	buf := bytes.NewReader([]byte{'X', 'X', 'X', 'X', 'X', 0, 0, Version1})
	_, err := readHeader(buf)
	if !errors.Is(err, ErrBadHeader) {
		t.Fatalf("got %v, want ErrBadHeader", err)
	}
}

func TestHeader_BadVersion(t *testing.T) {
	hdr := make([]byte, HeaderLen)
	copy(hdr, Magic[:])
	hdr[HeaderLen-1] = 0x99
	_, err := readHeader(bytes.NewReader(hdr))
	if !errors.Is(err, ErrBadVersion) {
		t.Fatalf("got %v, want ErrBadVersion", err)
	}
}

func TestHeader_Truncated(t *testing.T) {
	_, err := readHeader(bytes.NewReader([]byte{'T', 'O', 'Y'}))
	if !errors.Is(err, ErrBadHeader) {
		t.Fatalf("got %v, want ErrBadHeader for truncated header", err)
	}
}

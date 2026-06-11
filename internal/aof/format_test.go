package aof

import (
	"bytes"
	"errors"
	"testing"
)

func TestHeader_RoundTrip_CurrentVersion(t *testing.T) {
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
	if v != CurrentVersion {
		t.Fatalf("version = %x, want CurrentVersion %x", v, CurrentVersion)
	}
}

// readHeader must accept every byte in the supported set — v1 (written
// by pre-M4 binaries) and v2 (written by M4+). Older v1 files must
// continue to replay on M4+ binaries; this test pins that contract.
func TestHeader_AcceptsAllSupportedVersions(t *testing.T) {
	for _, want := range supportedVersions {
		hdr := make([]byte, HeaderLen)
		copy(hdr, Magic[:])
		hdr[HeaderLen-1] = want
		got, err := readHeader(bytes.NewReader(hdr))
		if err != nil {
			t.Errorf("readHeader v=0x%02x: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("readHeader v=0x%02x returned %x", want, got)
		}
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

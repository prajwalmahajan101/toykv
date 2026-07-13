package aof

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// TmpFilename is the in-progress filename used by the rewriter. It is
// renamed onto Filename atomically when the rewrite completes (LLD §4.4)
// and unlinked on Open if a previous run was killed mid-rewrite — a .tmp
// file is therefore never canonical.
const TmpFilename = "toykv.aof.tmp"

// File format constants (LLD §4.1).
const (
	// HeaderLen is the total header size in bytes (7-byte magic + 1-byte
	// version).
	HeaderLen = 8

	// Version1 is the v1 record format: RESP-encoded command arrays with
	// no TTL encoding.
	Version1 byte = 0x01

	// Version2 adds the canonical TTL-bearing forms — SET ... PXAT ms,
	// PEXPIREAT k ms, PERSIST k — to the SAME RESP-array record shape.
	// There is no binary change at the record level; the version byte's
	// job is to gate: pre-M4 binaries reject v2 files (they would not
	// understand PEXPIREAT during replay), and M4+ binaries accept both
	// v1 (every v1 record remains valid RESP and replays cleanly) and
	// v2 records. See ADR-0004.
	Version2 byte = 0x02

	// Version3 adds the typed mutating records — LPUSH / RPUSH / LPOP /
	// RPOP / HSET / HDEL — to the SAME RESP-array record shape (M11).
	// As with v2 there is no binary change at the record level; the
	// version byte gates: pre-M11 binaries reject v3 files up-front
	// (they would not understand LPUSH during replay), and M11+ binaries
	// accept v1, v2, and v3 records. Open upgrades an older file's
	// header byte in place before appending, preserving the invariant
	// that a file's header version >= the newest record format it
	// contains. See ADR-0012.
	Version3 byte = 0x03

	// CurrentVersion is the version byte emitted by writeHeader on fresh
	// AOF files, and the byte Open upgrades older headers to.
	CurrentVersion = Version3
)

// supportedVersions enumerates the version bytes this binary will
// replay. Keep the slice sorted ascending — readHeader's error message
// is more useful when the supported set is predictable.
var supportedVersions = []byte{Version1, Version2, Version3}

// Magic is the 7-byte file magic. Padded with NULs so the full header is
// 8 bytes including the version byte.
var Magic = [7]byte{'T', 'O', 'Y', 'K', 'V', 0x00, 0x00}

// writeHeader writes the magic and CurrentVersion into w.
func writeHeader(w io.Writer) error {
	buf := make([]byte, HeaderLen)
	copy(buf, Magic[:])
	buf[HeaderLen-1] = CurrentVersion
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("aof: write header: %w", err)
	}
	return nil
}

// upgradeHeader rewrites the version byte at offset HeaderLen-1 to
// CurrentVersion and fsyncs. Called by Open when an existing file's
// header is older than this binary's format — a one-byte, in-place,
// crash-safe update (a torn single-byte pwrite is impossible on any
// sane filesystem; pre- and post-states are both valid headers).
func upgradeHeader(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("aof: open for header upgrade %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt([]byte{CurrentVersion}, HeaderLen-1); err != nil {
		return fmt.Errorf("aof: upgrade header %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("aof: fsync header upgrade %s: %w", path, err)
	}
	return nil
}

// readHeader reads exactly HeaderLen bytes from r, validates the magic,
// and returns the version byte. Accepts every byte in supportedVersions.
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
	for _, v := range supportedVersions {
		if v == version {
			return version, nil
		}
	}
	return 0, fmt.Errorf("aof: version 0x%02x not in supported set %v: %w", version, supportedVersions, ErrBadVersion)
}

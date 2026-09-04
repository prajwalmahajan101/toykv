package cluster

import (
	"bytes"
	"testing"
)

func argvEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := [][][]byte{
		{[]byte("SET"), []byte("k"), []byte("v")},
		{[]byte("SET"), []byte("k"), []byte("v"), []byte("PXAT"), []byte("1609459200000")},
		{[]byte("DEL"), []byte("a"), []byte("b"), []byte("c")},
		{[]byte("INCR"), []byte("counter")},
		{[]byte("HSET"), []byte("h"), []byte("f1"), []byte("v1"), []byte("f2"), []byte("v2")},
		{[]byte("SET"), []byte("bin"), {0x00, 0x01, 0xff, '\r', '\n'}}, // binary-safe value
		{[]byte("SET"), []byte(""), []byte("")},                        // empty key + value
	}
	for _, argv := range cases {
		got, err := Decode(Encode(argv))
		if err != nil {
			t.Fatalf("Decode(Encode(%q)) error: %v", argv, err)
		}
		if !argvEqual(got, argv) {
			t.Fatalf("round-trip mismatch: got %q want %q", got, argv)
		}
	}
}

func TestEncodeDeterministic(t *testing.T) {
	argv := [][]byte{[]byte("SET"), []byte("k"), []byte("v"), []byte("PXAT"), []byte("123")}
	first := Encode(argv)
	for i := range 100 {
		if !bytes.Equal(Encode(argv), first) {
			t.Fatalf("Encode not deterministic on iteration %d", i)
		}
	}
	// Version byte is present and leading.
	if len(first) == 0 || first[0] != envelopeVersion {
		t.Fatalf("expected leading version byte 0x%02x, got %v", envelopeVersion, first)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	if _, err := Decode(nil); err == nil {
		t.Fatal("Decode(nil) should error")
	}
	if _, err := Decode([]byte{}); err == nil {
		t.Fatal("Decode(empty) should error")
	}
	// Wrong version byte.
	bad := Encode([][]byte{[]byte("PING")})
	bad[0] = 0xFF
	if _, err := Decode(bad); err == nil {
		t.Fatal("Decode with bad version byte should error")
	}
	// Truncated RESP body.
	trunc := Encode([][]byte{[]byte("SET"), []byte("k"), []byte("v")})
	if _, err := Decode(trunc[:len(trunc)-3]); err == nil {
		t.Fatal("Decode of truncated body should error")
	}
}

func FuzzEnvelopeRoundTrip(f *testing.F) {
	f.Add([]byte("SET"), []byte("k"), []byte("v"))
	f.Add([]byte(""), []byte("\r\n"), []byte{0x00})
	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		argv := [][]byte{a, b, c}
		got, err := Decode(Encode(argv))
		if err != nil {
			t.Fatalf("round-trip decode error for %q: %v", argv, err)
		}
		if !argvEqual(got, argv) {
			t.Fatalf("round-trip mismatch: got %q want %q", got, argv)
		}
	})
}

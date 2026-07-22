package resp

import (
	"bytes"
	"math"
	"testing"
)

// TestRoundTrip_RESP3 is the encode→decode gate that was missing when the
// codec was asymmetric: the Writer encoded every RESP3 kind but the Reader
// only decoded RESP2, so a HELLO 3 client choked on `%`/`~`/`,`/`#`/`_`/`=`/`>`.
// For every kind we encode at Proto3, feed the bytes back through Reader,
// and assert the decoded Value equals the original. This proves the two
// halves of the codec are inverses — the property CI never checked.
func TestRoundTrip_RESP3(t *testing.T) {
	cases := []struct {
		name string
		in   Value
	}{
		// RESP2 kinds still round-trip (regression guard for the refactor).
		{"simple", String("OK")},
		{"error", Error("ERR bad")},
		{"int", Int(42)},
		{"int-neg", Int(-7)},
		{"bulk", Bulk([]byte("hello"))},
		{"bulk-empty", Bulk([]byte{})},
		{"bulk-binary", Bulk([]byte{0x00, 0xff, 'A'})},
		{"bulk-null", NullBulk()},
		{"array", Array(Int(1), String("two"), Bulk([]byte("three")))},
		{"array-empty", Array()},
		{"array-null", NullArray()},
		// RESP3 kinds — the actual gate.
		{"map", Map(Bulk([]byte("k")), Int(1), Bulk([]byte("j")), Null())},
		{"map-empty", Map()},
		{"set", Set(Int(1), Int(2), String("three"))},
		{"set-empty", Set()},
		{"push", Push(String("message"), Bulk([]byte("ch")))},
		{"double-int", Double(3)},
		{"double-frac", Double(3.5)},
		{"double-neg", Double(-0.25)},
		{"double-inf", Double(math.Inf(1))},
		{"double-ninf", Double(math.Inf(-1))},
		{"double-nan", Double(math.NaN())},
		{"bool-true", Boolean(true)},
		{"bool-false", Boolean(false)},
		{"null", Null()},
		{"verbatim", Verbatim("txt", []byte("hi"))},
		{"verbatim-mkd", Verbatim("mkd", []byte("# heading\nbody"))},
		{"verbatim-empty-body", Verbatim("txt", []byte{})},
		// Nested aggregates carrying RESP3 leaves.
		{"map-of-set", Map(String("nums"), Set(Int(1), Int(2)),
			String("flag"), Boolean(true))},
		{"array-of-doubles", Array(Double(1.5), Double(math.Inf(-1)), Null())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := encodeProto(t, tc.in, Proto3)
			r := NewReader(bytes.NewReader([]byte(wire)))
			got, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("ReadFrame(%q): %v", wire, err)
			}
			if !valueEqual(got, tc.in) {
				t.Fatalf("round-trip mismatch for %q:\n got  %+v\n want %+v", wire, got, tc.in)
			}
		})
	}
}

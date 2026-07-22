package store

import (
	"bytes"
	"testing"
)

// hashPairs reads HKeys/HVals/HGetAll for k and returns them, failing the
// test on any error. It is the shared fixture for the correspondence
// assertions below.
func hashPairs(t *testing.T, s *Store, k string) (keys []string, vals [][]byte, all [][]byte) {
	t.Helper()
	var err error
	if keys, err = s.HKeys(k); err != nil {
		t.Fatalf("HKeys: %v", err)
	}
	if vals, err = s.HVals(k); err != nil {
		t.Fatalf("HVals: %v", err)
	}
	if all, err = s.HGetAll(k); err != nil {
		t.Fatalf("HGetAll: %v", err)
	}
	return keys, vals, all
}

// assertCorrespondence checks HKEYS[i]↔HVALS[i]↔HGETALL pairing for the
// hash at k against want (a flat [f1,v1,f2,v2,…] expectation in order).
func assertCorrespondence(t *testing.T, s *Store, k string, want []string) {
	t.Helper()
	keys, vals, all := hashPairs(t, s, k)
	if len(keys) != len(want)/2 {
		t.Fatalf("HKeys len = %d, want %d", len(keys), len(want)/2)
	}
	if len(vals) != len(keys) {
		t.Fatalf("HVals len = %d, HKeys len = %d — must match", len(vals), len(keys))
	}
	if len(all) != len(want) {
		t.Fatalf("HGetAll len = %d, want %d", len(all), len(want))
	}
	for i := range keys {
		wf, wv := want[2*i], want[2*i+1]
		if keys[i] != wf {
			t.Errorf("HKeys[%d] = %q, want %q", i, keys[i], wf)
		}
		if string(vals[i]) != wv {
			t.Errorf("HVals[%d] = %q, want %q", i, vals[i], wv)
		}
		// HGETALL flat pair i must equal (HKeys[i], HVals[i]).
		if string(all[2*i]) != keys[i] || !bytes.Equal(all[2*i+1], vals[i]) {
			t.Errorf("HGetAll pair %d = (%q,%q), want (%q,%q)",
				i, all[2*i], all[2*i+1], keys[i], vals[i])
		}
	}
}

// TestHash_FieldCorrespondence is the core gate for Fix #2: HKEYS[i] must
// correspond to HVALS[i] and to HGETALL's flat pairs, and that order must
// be insertion order — regardless of Go map iteration.
func TestHash_FieldCorrespondence(t *testing.T) {
	s := New()
	// Insert in a deliberately non-sorted order.
	if _, err := s.HSet("h",
		b("zeta"), b("1"),
		b("alpha"), b("2"),
		b("mike"), b("3"),
		b("bravo"), b("4"),
	); err != nil {
		t.Fatalf("HSet: %v", err)
	}
	assertCorrespondence(t, s, "h", []string{
		"zeta", "1", "alpha", "2", "mike", "3", "bravo", "4",
	})
}

// TestHash_OrderStableAcrossCalls proves the order does not shuffle
// between repeated reads (Go map iteration is randomized; the field-order
// slice must pin it).
func TestHash_OrderStableAcrossCalls(t *testing.T) {
	s := New()
	for i := 0; i < 32; i++ {
		f := string(rune('a'+i%26)) + string(rune('0'+i/26))
		if _, err := s.HSet("h", b(f), b(f+"v")); err != nil {
			t.Fatalf("HSet: %v", err)
		}
	}
	first, _, _ := hashPairs(t, s, "h")
	for call := 0; call < 20; call++ {
		got, _, _ := hashPairs(t, s, "h")
		if len(got) != len(first) {
			t.Fatalf("call %d: HKeys len changed", call)
		}
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("call %d: HKeys[%d] = %q, want stable %q", call, i, got[i], first[i])
			}
		}
	}
}

// TestHash_UpdateKeepsPosition verifies that overwriting an existing
// field's value does not move it (Redis semantics: only new fields append).
func TestHash_UpdateKeepsPosition(t *testing.T) {
	s := New()
	_, _ = s.HSet("h", b("a"), b("1"), b("b"), b("2"), b("c"), b("3"))
	// Overwrite b — position must not change.
	if _, err := s.HSet("h", b("b"), b("22")); err != nil {
		t.Fatalf("HSet update: %v", err)
	}
	assertCorrespondence(t, s, "h", []string{"a", "1", "b", "22", "c", "3"})
}

// TestHash_DeleteThenReaddGoesLast pins the Redis behavior that a deleted
// field, re-added, lands at the end rather than reclaiming its old slot.
func TestHash_DeleteThenReaddGoesLast(t *testing.T) {
	s := New()
	_, _ = s.HSet("h", b("a"), b("1"), b("b"), b("2"), b("c"), b("3"))
	if n, err := s.HDel("h", "b"); err != nil || n != 1 {
		t.Fatalf("HDel = %d,%v, want 1,nil", n, err)
	}
	assertCorrespondence(t, s, "h", []string{"a", "1", "c", "3"})
	// Re-add b — it must go last.
	if _, err := s.HSet("h", b("b"), b("22")); err != nil {
		t.Fatalf("HSet re-add: %v", err)
	}
	assertCorrespondence(t, s, "h", []string{"a", "1", "c", "3", "b", "22"})
}

// TestHash_DeleteMiddlePreservesRest checks the field-order slice stays
// consistent when a middle field is removed.
func TestHash_DeleteMiddlePreservesRest(t *testing.T) {
	s := New()
	_, _ = s.HSet("h", b("a"), b("1"), b("b"), b("2"), b("c"), b("3"), b("d"), b("4"))
	if _, err := s.HDel("h", "b", "d"); err != nil {
		t.Fatalf("HDel: %v", err)
	}
	assertCorrespondence(t, s, "h", []string{"a", "1", "c", "3"})
}

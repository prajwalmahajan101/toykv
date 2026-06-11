package store

import (
	"bytes"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestGet_Missing(t *testing.T) {
	s := New()
	if v, ok := s.Get("nope"); ok || v != nil {
		t.Fatalf("got (%q,%v), want (nil,false)", v, ok)
	}
}

func TestSet_GetRoundTrip(t *testing.T) {
	s := New()
	if ok := s.Set("k", []byte("v"), SetOpts{}); !ok {
		t.Fatal("Set returned false")
	}
	got, ok := s.Get("k")
	if !ok || !bytes.Equal(got, []byte("v")) {
		t.Fatalf("got (%q,%v), want (\"v\",true)", got, ok)
	}
}

func TestSet_DefensiveCopy(t *testing.T) {
	s := New()
	v := []byte("hello")
	s.Set("k", v, SetOpts{})
	v[0] = 'X' // mutate caller's buffer after Set
	got, _ := s.Get("k")
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("got %q, want %q — Set did not take a defensive copy", got, "hello")
	}
}

func TestSet_NX(t *testing.T) {
	s := New()
	if ok := s.Set("k", []byte("v1"), SetOpts{Mode: SetNX}); !ok {
		t.Fatal("first NX should succeed")
	}
	if ok := s.Set("k", []byte("v2"), SetOpts{Mode: SetNX}); ok {
		t.Fatal("second NX should fail")
	}
	got, _ := s.Get("k")
	if !bytes.Equal(got, []byte("v1")) {
		t.Fatalf("got %q, want %q", got, "v1")
	}
}

func TestSet_XX(t *testing.T) {
	s := New()
	if ok := s.Set("k", []byte("v1"), SetOpts{Mode: SetXX}); ok {
		t.Fatal("XX on missing key should fail")
	}
	s.Set("k", []byte("v1"), SetOpts{})
	if ok := s.Set("k", []byte("v2"), SetOpts{Mode: SetXX}); !ok {
		t.Fatal("XX on existing key should succeed")
	}
	got, _ := s.Get("k")
	if !bytes.Equal(got, []byte("v2")) {
		t.Fatalf("got %q, want %q", got, "v2")
	}
}

func TestExists_DupesCount(t *testing.T) {
	s := New()
	s.Set("a", []byte("1"), SetOpts{})
	if n := s.Exists("a", "a", "a", "missing"); n != 3 {
		t.Fatalf("got %d, want 3", n)
	}
}

func TestDel_CountOnlyDeleted(t *testing.T) {
	s := New()
	s.Set("a", []byte("1"), SetOpts{})
	s.Set("b", []byte("2"), SetOpts{})
	if n := s.Del("a", "a", "missing", "b"); n != 2 {
		t.Fatalf("got %d, want 2", n)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("dbsize = %d, want 0", n)
	}
}

func TestDBSize(t *testing.T) {
	s := New()
	if n := s.DBSize(); n != 0 {
		t.Fatalf("got %d, want 0", n)
	}
	s.Set("a", []byte("1"), SetOpts{})
	s.Set("b", []byte("2"), SetOpts{})
	if n := s.DBSize(); n != 2 {
		t.Fatalf("got %d, want 2", n)
	}
}

func TestFlushDB(t *testing.T) {
	s := New()
	s.Set("a", []byte("1"), SetOpts{})
	s.Set("b", []byte("2"), SetOpts{})
	s.FlushDB()
	if n := s.DBSize(); n != 0 {
		t.Fatalf("dbsize after FlushDB = %d, want 0", n)
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("key still present after FlushDB")
	}
}

func TestIncr_MissingTreatedAsZero(t *testing.T) {
	s := New()
	n, err := s.Incr("k")
	if err != nil || n != 1 {
		t.Fatalf("got (%d,%v), want (1,nil)", n, err)
	}
}

func TestIncr_Decr_RoundTrip(t *testing.T) {
	s := New()
	for i := 1; i <= 3; i++ {
		n, err := s.Incr("k")
		if err != nil || n != int64(i) {
			t.Fatalf("Incr step %d: got (%d,%v)", i, n, err)
		}
	}
	if n, err := s.Decr("k"); err != nil || n != 2 {
		t.Fatalf("Decr: got (%d,%v), want (2,nil)", n, err)
	}
}

func TestIncr_NotInteger(t *testing.T) {
	s := New()
	s.Set("k", []byte("abc"), SetOpts{})
	if _, err := s.Incr("k"); !errors.Is(err, ErrNotInteger) {
		t.Fatalf("got %v, want ErrNotInteger", err)
	}
}

func TestIncr_OverflowMax(t *testing.T) {
	s := New()
	s.Set("k", []byte(strconv.FormatInt(int64(1<<63-1), 10)), SetOpts{})
	if _, err := s.Incr("k"); !errors.Is(err, ErrOverflow) {
		t.Fatalf("got %v, want ErrOverflow", err)
	}
}

func TestDecr_OverflowMin(t *testing.T) {
	s := New()
	s.Set("k", []byte(strconv.FormatInt(int64(-1<<63), 10)), SetOpts{})
	if _, err := s.Decr("k"); !errors.Is(err, ErrOverflow) {
		t.Fatalf("got %v, want ErrOverflow", err)
	}
}

func TestKeys_Patterns(t *testing.T) {
	s := New()
	for _, k := range []string{"foo", "foobar", "baz", "qux"} {
		s.Set(k, []byte("v"), SetOpts{})
	}
	cases := []struct {
		pattern string
		want    []string
	}{
		{"*", []string{"baz", "foo", "foobar", "qux"}},
		{"foo*", []string{"foo", "foobar"}},
		{"foo", []string{"foo"}},
		{"???", []string{"baz", "foo", "qux"}},
		{"nope*", nil},
	}
	for _, c := range cases {
		got, err := s.Keys(c.pattern)
		if err != nil {
			t.Errorf("Keys(%q): unexpected err %v", c.pattern, err)
			continue
		}
		sort.Strings(got)
		if !equalStringSlices(got, c.want) {
			t.Errorf("Keys(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}

func TestKeys_BadPattern(t *testing.T) {
	s := New()
	s.Set("k", []byte("v"), SetOpts{})
	_, err := s.Keys("[unterminated")
	if !errors.Is(err, path.ErrBadPattern) {
		t.Fatalf("got %v, want path.ErrBadPattern", err)
	}
}

func TestNewWithClock_NilFallsBackToTimeNow(t *testing.T) {
	s := NewWithClock(nil)
	if s.nowFunc == nil {
		t.Fatal("nowFunc is nil; expected fallback to time.Now")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guard against accidental change in the package's exported error
// messages — the wire format depends on them.
func TestErrorMessagesStable(t *testing.T) {
	if !strings.Contains(ErrNotInteger.Error(), "not an integer") {
		t.Errorf("ErrNotInteger message changed: %q", ErrNotInteger.Error())
	}
	if !strings.Contains(ErrOverflow.Error(), "overflow") {
		t.Errorf("ErrOverflow message changed: %q", ErrOverflow.Error())
	}
}

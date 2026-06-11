package store

import (
	"bytes"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic TTL tests.
// Safe for concurrent reads via the now method; advance must not race
// with reads in a single test (the lock-upgrade race test uses real
// time, not fakeClock).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(seed time.Time) *fakeClock { return &fakeClock{t: seed} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

var fakeEpoch = time.Unix(1_700_000_000, 0)

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

// --- TTL / lazy expiry / sweeper-adjacent behaviour ----------------------

func TestGet_LazyExpire_EvictsOnRead(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("v"), SetOpts{ExpireAt: fc.now().Add(time.Second)})

	if v, ok := s.Get("k"); !ok || !bytes.Equal(v, []byte("v")) {
		t.Fatalf("pre-expiry got (%q,%v), want (v,true)", v, ok)
	}
	fc.advance(2 * time.Second)
	if v, ok := s.Get("k"); ok || v != nil {
		t.Fatalf("post-expiry got (%q,%v), want (nil,false)", v, ok)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("dbsize after Get-eviction = %d, want 0", n)
	}
}

func TestTTL_Sentinels(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)

	if d := s.TTL("missing"); d != TTLNoKey {
		t.Fatalf("missing TTL = %v, want TTLNoKey (%v)", d, TTLNoKey)
	}
	s.Set("perm", []byte("v"), SetOpts{})
	if d := s.TTL("perm"); d != TTLNoExpire {
		t.Fatalf("no-expiry TTL = %v, want TTLNoExpire (%v)", d, TTLNoExpire)
	}
	s.Set("temp", []byte("v"), SetOpts{ExpireAt: fc.now().Add(5 * time.Second)})
	if d := s.TTL("temp"); d != 5*time.Second {
		t.Fatalf("temp TTL = %v, want 5s", d)
	}
	fc.advance(3 * time.Second)
	if d := s.TTL("temp"); d != 2*time.Second {
		t.Fatalf("temp TTL after 3s = %v, want 2s", d)
	}
	fc.advance(3 * time.Second)
	if d := s.TTL("temp"); d != TTLNoKey {
		t.Fatalf("expired TTL = %v, want TTLNoKey", d)
	}
}

func TestExpire_OnExisting(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("v"), SetOpts{})
	if ok := s.Expire("k", fc.now().Add(time.Second)); !ok {
		t.Fatal("Expire on existing key returned false")
	}
	fc.advance(2 * time.Second)
	if _, ok := s.Get("k"); ok {
		t.Fatal("key should be expired after Expire + clock advance")
	}
}

func TestExpire_OnMissingOrExpired(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	if ok := s.Expire("nope", fc.now().Add(time.Second)); ok {
		t.Fatal("Expire on missing key returned true")
	}
	s.Set("k", []byte("v"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	fc.advance(2 * time.Second)
	if ok := s.Expire("k", fc.now().Add(time.Hour)); ok {
		t.Fatal("Expire on already-expired key returned true")
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("Expire on expired key should evict; dbsize = %d, want 0", n)
	}
}

func TestPersist_RemovesTTL(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("v"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	if ok := s.Persist("k"); !ok {
		t.Fatal("Persist on TTL'd key returned false")
	}
	fc.advance(10 * time.Second)
	if _, ok := s.Get("k"); !ok {
		t.Fatal("key should persist after Persist")
	}
	if d := s.TTL("k"); d != TTLNoExpire {
		t.Fatalf("TTL after Persist = %v, want TTLNoExpire", d)
	}
}

func TestPersist_NoOpCases(t *testing.T) {
	s := New()
	if ok := s.Persist("missing"); ok {
		t.Fatal("Persist on missing returned true")
	}
	s.Set("k", []byte("v"), SetOpts{})
	if ok := s.Persist("k"); ok {
		t.Fatal("Persist on key without TTL returned true")
	}
}

func TestSet_OverwriteWithoutExpireAtClearsTTL(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("v1"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	s.Set("k", []byte("v2"), SetOpts{}) // no ExpireAt
	fc.advance(10 * time.Second)
	v, ok := s.Get("k")
	if !ok || !bytes.Equal(v, []byte("v2")) {
		t.Fatalf("got (%q,%v), want (v2,true) — overwrite should clear prior TTL", v, ok)
	}
}

func TestSet_NX_ExpiredCountsAsAbsent(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("v1"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	fc.advance(2 * time.Second)
	if ok := s.Set("k", []byte("v2"), SetOpts{Mode: SetNX}); !ok {
		t.Fatal("NX should succeed when prior entry is expired")
	}
	v, _ := s.Get("k")
	if !bytes.Equal(v, []byte("v2")) {
		t.Fatalf("got %q, want v2", v)
	}
}

func TestSet_XX_ExpiredCountsAsAbsent(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("v1"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	fc.advance(2 * time.Second)
	if ok := s.Set("k", []byte("v2"), SetOpts{Mode: SetXX}); ok {
		t.Fatal("XX should fail when prior entry is expired")
	}
}

func TestExists_ExcludesExpired(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("a", []byte("1"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	s.Set("b", []byte("2"), SetOpts{})
	fc.advance(2 * time.Second)
	if n := s.Exists("a", "b"); n != 1 {
		t.Fatalf("Exists = %d, want 1 (only b is alive)", n)
	}
}

func TestKeys_ExcludesExpired(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("a", []byte("1"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	s.Set("b", []byte("2"), SetOpts{})
	fc.advance(2 * time.Second)
	got, err := s.Keys("*")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("Keys = %v, want [b]", got)
	}
}

func TestIncr_ExpiredTreatedAsZero(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("k", []byte("100"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	fc.advance(2 * time.Second)
	n, err := s.Incr("k")
	if err != nil || n != 1 {
		t.Fatalf("Incr on expired key got (%d,%v), want (1,nil)", n, err)
	}
	if d := s.TTL("k"); d != TTLNoExpire {
		t.Fatalf("post-Incr TTL = %v, want TTLNoExpire (Incr should clear stale expiry)", d)
	}
}

func TestDel_DoesNotCountExpired(t *testing.T) {
	fc := newFakeClock(fakeEpoch)
	s := NewWithClock(fc.now)
	s.Set("a", []byte("1"), SetOpts{ExpireAt: fc.now().Add(time.Second)})
	s.Set("b", []byte("2"), SetOpts{})
	fc.advance(2 * time.Second)
	// a is expired but still in the map; b is alive.
	if n := s.Del("a", "b"); n != 1 {
		t.Fatalf("Del = %d, want 1 (only b was logically present)", n)
	}
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

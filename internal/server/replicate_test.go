package server

import (
	"strconv"
	"testing"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// fixedClockServer builds a bare Server whose only configured behaviour is its
// clock — enough to exercise resolveNondeterministic without a listener/AOF.
func fixedClockServer(now time.Time) *Server {
	return &Server{nowFunc: func() time.Time { return now }}
}

func toArgv(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

func argvStrings(argv [][]byte) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = string(a)
	}
	return out
}

func equalArgv(a [][]byte, want []string) bool {
	if len(a) != len(want) {
		return false
	}
	for i := range want {
		if string(a[i]) != want[i] {
			return false
		}
	}
	return true
}

func msStr(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func TestResolveSetExpiryToAbsolute(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	s := fixedClockServer(now)
	absSec := msStr(now.Add(10 * time.Second))
	absMs := msStr(now.Add(250 * time.Millisecond))

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"EX", []string{"SET", "k", "v", "EX", "10"}, []string{"SET", "k", "v", "PXAT", absSec}},
		{"PX", []string{"SET", "k", "v", "PX", "250"}, []string{"SET", "k", "v", "PXAT", absMs}},
		{"EXAT", []string{"SET", "k", "v", "EXAT", "1700000123"}, []string{"SET", "k", "v", "PXAT", "1700000123000"}},
		{"PXAT-passthrough", []string{"SET", "k", "v", "PXAT", "1700000123000"}, []string{"SET", "k", "v", "PXAT", "1700000123000"}},
		{"no-expiry", []string{"SET", "k", "v"}, []string{"SET", "k", "v"}},
		{"NX preserved", []string{"SET", "k", "v", "NX", "EX", "10"}, []string{"SET", "k", "v", "NX", "PXAT", absSec}},
		{"XX preserved no-ttl", []string{"SET", "k", "v", "XX"}, []string{"SET", "k", "v", "XX"}},
		{"malformed passthrough", []string{"SET", "k", "v", "EX", "notint"}, []string{"SET", "k", "v", "EX", "notint"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.resolveNondeterministic(toArgv(tc.in))
			if !equalArgv(got, tc.want) {
				t.Fatalf("got %v, want %v", argvStrings(got), tc.want)
			}
		})
	}
}

func TestResolveRelativeExpire(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	s := fixedClockServer(now)
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"EXPIRE", "k", "30"}, []string{"PEXPIREAT", "k", msStr(now.Add(30 * time.Second))}},
		{[]string{"PEXPIRE", "k", "1500"}, []string{"PEXPIREAT", "k", msStr(now.Add(1500 * time.Millisecond))}},
		{[]string{"EXPIRE", "k", "-1"}, []string{"PEXPIREAT", "k", msStr(now.Add(-1 * time.Second))}},
		{[]string{"EXPIRE", "k", "notint"}, []string{"EXPIRE", "k", "notint"}}, // passthrough
		{[]string{"PEXPIREAT", "k", "123"}, []string{"PEXPIREAT", "k", "123"}}, // already absolute, untouched
	}
	for _, tc := range cases {
		got := s.resolveNondeterministic(toArgv(tc.in))
		if !equalArgv(got, tc.want) {
			t.Fatalf("resolve %v: got %v, want %v", tc.in, argvStrings(got), tc.want)
		}
	}
}

// pxatFromResolved extracts the absolute deadline a resolved SET/PEXPIREAT argv
// carries, so the test can apply it through the store as an absolute time.
func pxatFromResolved(t *testing.T, argv [][]byte) time.Time {
	t.Helper()
	// Both resolved forms end with the absolute deadline: SET …PXAT <ms> and
	// PEXPIREAT key <ms>.
	last := argv[len(argv)-1]
	ms, err := strconv.ParseInt(string(last), 10, 64)
	if err != nil {
		t.Fatalf("bad absolute deadline %q in %v: %v", last, argvStrings(argv), err)
	}
	return time.UnixMilli(ms)
}

// TestResolveCrossClockDeterminism is the M18 cross-clock check: a command
// resolved on one leader clock, then applied under a wildly different clock,
// produces identical expiry. Because resolution embeds an absolute deadline,
// the applying node's clock never re-enters the computation.
func TestResolveCrossClockDeterminism(t *testing.T) {
	leaderNow := time.UnixMilli(1_700_000_000_000)
	leader := fixedClockServer(leaderNow)

	resolvedSet := leader.resolveNondeterministic(toArgv([]string{"SET", "k", "v", "EX", "100"}))
	resolvedExp := leader.resolveNondeterministic(toArgv([]string{"EXPIRE", "k2", "100"}))
	setDeadline := pxatFromResolved(t, resolvedSet)
	expDeadline := pxatFromResolved(t, resolvedExp)

	// Apply the resolved absolute deadlines against two stores on different
	// clocks — both still before the +100s deadline, so the keys stay live and
	// the comparison is of the stored deadline, not of lazy-eviction timing
	// (that divergence is the single-clock caveat this milestone defers to M19).
	// Because the deadlines are absolute, both stores hold identical expiry.
	applyTo := func(clockOffset time.Duration) *store.Store {
		st := store.NewWithClock(func() time.Time { return leaderNow.Add(clockOffset) })
		st.Set("k", []byte("v"), store.SetOpts{ExpireAt: setDeadline})
		st.Set("k2", []byte("v2"), store.SetOpts{})
		st.Expire("k2", expDeadline)
		return st
	}
	a := applyTo(0)
	b := applyTo(50 * time.Second) // skewed, but still before the +100s deadline

	if !snapshotsEqualServer(a.Snapshot(), b.Snapshot()) {
		t.Fatalf("cross-clock divergence:\n a=%+v\n b=%+v", a.Snapshot(), b.Snapshot())
	}
	wantExpire := leaderNow.Add(100 * time.Second).UnixMilli()
	for _, se := range a.Snapshot() {
		if se.Key == "k" && se.ExpireAt.UnixMilli() != wantExpire {
			t.Fatalf("SET k expireAt = %d, want %d", se.ExpireAt.UnixMilli(), wantExpire)
		}
	}
}

// snapshotsEqualServer compares two store snapshots independent of entry order,
// comparing keys and absolute expiry deadlines.
func snapshotsEqualServer(a, b []store.SnapshotEntry) bool {
	if len(a) != len(b) {
		return false
	}
	idx := func(es []store.SnapshotEntry) map[string]int64 {
		m := make(map[string]int64, len(es))
		for i := range es {
			m[es[i].Key] = es[i].ExpireAt.UnixMilli()
		}
		return m
	}
	ma, mb := idx(a), idx(b)
	for k, va := range ma {
		if vb, ok := mb[k]; !ok || vb != va {
			return false
		}
	}
	return true
}

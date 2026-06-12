package store

import (
	"math"
	"path"
	"strconv"
	"sync"
	"time"
)

// SetMode selects the conditional semantics of Set.
type SetMode int

const (
	// SetAlways unconditionally writes the key.
	SetAlways SetMode = iota
	// SetNX writes only when the key does not already exist.
	SetNX
	// SetXX writes only when the key already exists.
	SetXX
)

// SetOpts configures a Set call.
type SetOpts struct {
	// Mode selects the conditional semantics of the write.
	Mode SetMode
	// ExpireAt sets the absolute deadline after which the entry is
	// considered expired. The zero value means no expiry — Set will
	// clear any pre-existing TTL on overwrite (matches Redis: SET
	// without EX/PX clears TTL).
	ExpireAt time.Time
}

// TTL sentinel return values. Picked to mirror LLD §3.2 — the server
// command layer translates these into Redis-compatible wire integers.
const (
	// TTLNoKey is returned when the key does not exist (or has expired
	// and not yet been swept).
	TTLNoKey time.Duration = -2 * time.Second
	// TTLNoExpire is returned when the key exists but has no expiry.
	TTLNoExpire time.Duration = -1 * time.Second
)

// Store is the in-memory key/value map. Use New or NewWithClock.
//
// All methods are safe for concurrent use. Reads (Get, Exists, Keys,
// DBSize) take an RLock; writes take a Lock.
type Store struct {
	mu      sync.RWMutex
	data    map[string]entry
	nowFunc func() time.Time
}

// New constructs an empty Store backed by time.Now.
func New() *Store {
	return NewWithClock(time.Now)
}

// NewWithClock constructs a Store with an injectable clock. The clock
// drives lazy-expiry checks and sweeper decisions; injecting it keeps
// TTL tests deterministic.
func NewWithClock(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		data:    make(map[string]entry),
		nowFunc: now,
	}
}

// Get returns the value stored under k. The returned slice is owned by
// the store; callers MUST NOT mutate it.
//
// Lazy expiry: if the entry has expired, Get evicts it and returns a
// miss. The lock-upgrade window (RLock → release → Lock → re-check) is
// the documented LLD §3.2 trade-off; the re-check protects against a
// concurrent Set that refreshes the entry before we re-acquire.
func (s *Store) Get(k string) ([]byte, bool) {
	s.mu.RLock()
	e, ok := s.data[k]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	if !e.expired(s.nowFunc()) {
		v := e.value
		s.mu.RUnlock()
		return v, true
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok = s.data[k]
	if !ok {
		return nil, false
	}
	if e.expired(s.nowFunc()) {
		delete(s.data, k)
		return nil, false
	}
	return e.value, true
}

// Exists returns the number of supplied keys that are present and not
// expired. Duplicate keys are counted multiple times (matches Redis).
// Expired entries are not evicted here — the sweeper or a future Get
// will reap them.
func (s *Store) Exists(keys ...string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowFunc()
	n := 0
	for _, k := range keys {
		if e, ok := s.data[k]; ok && !e.expired(now) {
			n++
		}
	}
	return n
}

// Keys returns all non-expired keys matching pattern (path.Match
// semantics; see package doc for the [charset] caveat). A bad pattern
// returns path.ErrBadPattern.
func (s *Store) Keys(pattern string) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowFunc()
	out := make([]string, 0, len(s.data))
	for k, e := range s.data {
		if e.expired(now) {
			continue
		}
		ok, err := path.Match(pattern, k)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, k)
		}
	}
	return out, nil
}

// DBSize returns the count of keys in the store. It includes expired
// keys that have not yet been swept — matches Redis semantics and
// keeps the read on the RLock-only fast path.
func (s *Store) DBSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// Set writes k=v subject to opts. Returns true if the write happened,
// false if it was rejected by NX/XX. An expired entry is treated as
// absent for NX/XX purposes. The supplied value is copied; the store
// owns its memory.
//
// opts.ExpireAt zero means "no expiry" and clears any pre-existing
// TTL (Redis behaviour for SET without EX/PX/KEEPTTL).
func (s *Store) Set(k string, v []byte, opts SetOpts) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()

	existing, exists := s.data[k]
	if exists && existing.expired(now) {
		delete(s.data, k)
		exists = false
	}
	switch opts.Mode {
	case SetNX:
		if exists {
			return false
		}
	case SetXX:
		if !exists {
			return false
		}
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	s.data[k] = entry{value: cp, expireAt: opts.ExpireAt}
	return true
}

// Del removes the supplied keys and returns the number actually
// deleted. Expired-but-not-yet-swept entries are not counted (they were
// already logically absent).
func (s *Store) Del(keys ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()
	n := 0
	for _, k := range keys {
		e, ok := s.data[k]
		if !ok {
			continue
		}
		delete(s.data, k)
		if !e.expired(now) {
			n++
		}
	}
	return n
}

// Expire sets the absolute expiry deadline on an existing, non-expired
// key. Returns true if the key existed; false if it was missing or
// already expired. Passing a zero expireAt clears the TTL (mirror of
// Persist; callers prefer Persist for clarity).
func (s *Store) Expire(k string, expireAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[k]
	if !ok {
		return false
	}
	if e.expired(s.nowFunc()) {
		delete(s.data, k)
		return false
	}
	e.expireAt = expireAt
	s.data[k] = e
	return true
}

// Persist clears the TTL on an existing key. Returns true only if the
// key existed AND had a TTL; false for missing, expired, or
// already-persistent keys (matches Redis PERSIST semantics).
func (s *Store) Persist(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[k]
	if !ok {
		return false
	}
	if e.expired(s.nowFunc()) {
		delete(s.data, k)
		return false
	}
	if e.expireAt.IsZero() {
		return false
	}
	e.expireAt = time.Time{}
	s.data[k] = e
	return true
}

// TTL returns the remaining duration until expiry for k. Sentinel
// values match LLD §3.2: TTLNoKey for missing/expired, TTLNoExpire for
// keys without expiry, otherwise a positive duration.
func (s *Store) TTL(k string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[k]
	if !ok {
		return TTLNoKey
	}
	now := s.nowFunc()
	if e.expired(now) {
		return TTLNoKey
	}
	if e.expireAt.IsZero() {
		return TTLNoExpire
	}
	return e.expireAt.Sub(now)
}

// Incr increments k by 1 and returns the new value. Missing or expired
// keys are treated as 0. Returns ErrNotInteger if the existing value
// does not parse as base-10 int64, or ErrOverflow on int64 overflow.
//
// Incr clears any pre-existing TTL (Redis behaviour).
func (s *Store) Incr(k string) (int64, error) { return s.incrBy(k, 1) }

// Decr decrements k by 1 with the same semantics as Incr.
func (s *Store) Decr(k string) (int64, error) { return s.incrBy(k, -1) }

func (s *Store) incrBy(k string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()

	var n int64
	if e, ok := s.data[k]; ok {
		if e.expired(now) {
			delete(s.data, k)
		} else {
			parsed, err := strconv.ParseInt(string(e.value), 10, 64)
			if err != nil {
				return 0, ErrNotInteger
			}
			n = parsed
		}
	}
	if (delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta) {
		return 0, ErrOverflow
	}
	n += delta
	s.data[k] = entry{value: []byte(strconv.FormatInt(n, 10))}
	return n, nil
}

// SnapshotEntry is one live key's payload returned by Snapshot. Value
// is a fresh copy owned by the caller. A zero ExpireAt means no expiry.
type SnapshotEntry struct {
	Key      string
	Value    []byte
	ExpireAt time.Time
}

// Snapshot returns every non-expired key in the store as a slice of
// SnapshotEntry. It takes a write lock so expired entries can be evicted
// inline — keeping the snapshot consistent with what Keys / Get would
// see at the same instant. Used by BGREWRITEAOF (LLD §4.4) to materialise
// live state for the rewriter.
func (s *Store) Snapshot() []SnapshotEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()
	out := make([]SnapshotEntry, 0, len(s.data))
	for k, e := range s.data {
		if e.expired(now) {
			delete(s.data, k)
			continue
		}
		cp := make([]byte, len(e.value))
		copy(cp, e.value)
		out = append(out, SnapshotEntry{Key: k, Value: cp, ExpireAt: e.expireAt})
	}
	return out
}

// FlushDB removes every key.
func (s *Store) FlushDB() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]entry)
}

// sweepOnce samples up to batch keys, evicting any whose expireAt is
// at or past now. Returns the number sampled and evicted. Package-
// private — driven by Sweeper. Uses Go map iteration's randomised
// order as the sampling source.
func (s *Store) sweepOnce(now time.Time, batch int) (sampled, evicted int) {
	if batch <= 0 {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.data {
		if sampled >= batch {
			break
		}
		sampled++
		if e.expired(now) {
			delete(s.data, k)
			evicted++
		}
	}
	return sampled, evicted
}

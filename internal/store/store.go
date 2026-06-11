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

// SetOpts configures a Set call. M4 will add a TTL field; the struct
// shape is locked in now so callers don't churn.
type SetOpts struct {
	Mode SetMode
}

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

// NewWithClock constructs a Store with an injectable clock. M2 does not
// use the clock yet; M4's TTL machinery does. Exposing the seam now
// keeps construction stable across the boundary.
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
func (s *Store) Get(k string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[k]
	if !ok {
		return nil, false
	}
	return e.value, true
}

// Exists returns the number of supplied keys that are present. Duplicate
// keys are counted multiple times (matches Redis semantics).
func (s *Store) Exists(keys ...string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, k := range keys {
		if _, ok := s.data[k]; ok {
			n++
		}
	}
	return n
}

// Keys returns all keys matching pattern (path.Match semantics; see
// package doc for the [charset] caveat). A bad pattern returns
// path.ErrBadPattern.
func (s *Store) Keys(pattern string) ([]string, error) {
	// Validate pattern once up front against an empty string. path.Match
	// only surfaces a bad-pattern error when it actually walks the
	// pattern, so an early probe gives the caller a fast failure.
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.data))
	for k := range s.data {
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

// DBSize returns the number of keys in the store.
func (s *Store) DBSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// Set writes k=v subject to opts.Mode. Returns true if the write
// happened, false if it was rejected by NX/XX. The supplied value is
// copied; the store owns its memory.
func (s *Store) Set(k string, v []byte, opts SetOpts) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.data[k]
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
	s.data[k] = entry{value: cp}
	return true
}

// Del removes the supplied keys and returns the number actually
// deleted.
func (s *Store) Del(keys ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range keys {
		if _, ok := s.data[k]; ok {
			delete(s.data, k)
			n++
		}
	}
	return n
}

// Incr increments k by 1 and returns the new value. Missing keys are
// treated as 0. Returns ErrNotInteger if the existing value does not
// parse as base-10 int64, or ErrOverflow on int64 overflow.
func (s *Store) Incr(k string) (int64, error) { return s.incrBy(k, 1) }

// Decr decrements k by 1 with the same semantics as Incr.
func (s *Store) Decr(k string) (int64, error) { return s.incrBy(k, -1) }

func (s *Store) incrBy(k string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int64
	if e, ok := s.data[k]; ok {
		parsed, err := strconv.ParseInt(string(e.value), 10, 64)
		if err != nil {
			return 0, ErrNotInteger
		}
		n = parsed
	}
	if (delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta) {
		return 0, ErrOverflow
	}
	n += delta
	s.data[k] = entry{value: []byte(strconv.FormatInt(n, 10))}
	return n, nil
}

// FlushDB removes every key.
func (s *Store) FlushDB() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]entry)
}

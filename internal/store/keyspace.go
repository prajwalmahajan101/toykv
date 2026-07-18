package store

// Atomic keyspace operations (M15): RENAME / RENAMENX / COPY. Each is a
// single store-mutex-guarded move or copy — the correctness win over the
// racy client-side GET+SET+DEL dance. The value payload, TTL (expireAt),
// and value type all travel with the key.
//
// Destination seq policy: the destination is a newly-appearing key, so it
// receives a fresh creation sequence (nextSeq). This keeps it consistent
// with SCAN's "keys created mid-iteration may or may not be returned"
// guarantee (store.go Scan doc, ADR-0014) — a moved key never re-appears
// behind a cursor that has already passed a stale low seq. See ADR-0016.

// clone returns a deep copy of the entry: the value payload (string bytes,
// list deque, or hash map) is duplicated so later mutation of the source
// never leaks into the copy. typ, expireAt, and seq are copied by the
// struct assignment; Copy reassigns seq afterwards.
func (e entry) clone() entry {
	c := e // copies scalars + the (about-to-be-replaced) payload pointers
	switch e.typ {
	case typeString:
		c.str = append([]byte(nil), e.str...)
	case typeList:
		c.list = e.list.clone()
	case typeHash:
		nh := make(map[string][]byte, len(e.hash))
		for f, v := range e.hash {
			nh[f] = append([]byte(nil), v...)
		}
		c.hash = nh
	}
	return c
}

// Rename moves the entry at src to dst atomically, overwriting any
// existing dst. Value, TTL, and type travel with the key; dst gets a fresh
// creation seq. Returns ErrNoKey when src is absent or expired. RENAME of
// a key onto itself is a no-op (returns nil without disturbing the key).
func (s *Store) Rename(src, dst string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(src)
	if !ok {
		return ErrNoKey
	}
	if src == dst {
		return nil
	}
	e.seq = s.nextSeq()
	delete(s.data, src)
	s.data[dst] = e
	return nil
}

// RenameNX moves src to dst only when dst does not already exist, matching
// Redis RENAMENX. Returns (true, nil) on a successful move, (false, nil)
// when dst exists (including src==dst), and (false, ErrNoKey) when src is
// absent or expired.
func (s *Store) RenameNX(src, dst string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(src)
	if !ok {
		return false, ErrNoKey
	}
	if src == dst {
		return false, nil // destination (itself) already exists
	}
	if _, exists := s.getForWrite(dst); exists {
		return false, nil
	}
	e.seq = s.nextSeq()
	delete(s.data, src)
	s.data[dst] = e
	return true, nil
}

// Copy deep-copies the entry at src to dst, matching Redis COPY. The copy
// is fully independent (list/hash payloads are cloned) and carries src's
// TTL and type, with a fresh creation seq. Returns (true, nil) on success;
// (false, nil) when src is absent or when dst exists and replace is false;
// (false, ErrSameObject) when src == dst.
func (s *Store) Copy(src, dst string, replace bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(src)
	if !ok {
		return false, nil // Redis COPY returns 0 for a missing source
	}
	if src == dst {
		return false, ErrSameObject
	}
	if _, exists := s.getForWrite(dst); exists && !replace {
		return false, nil
	}
	cp := e.clone()
	cp.seq = s.nextSeq()
	s.data[dst] = cp
	return true, nil
}

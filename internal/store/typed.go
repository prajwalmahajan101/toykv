package store

import "context"

// Typed accessors for list and hash values (M11). All follow the same
// shape: write ops take the Lock, evict an expired entry inline, check
// the type tag, and mutate; read ops take the RLock and treat expired
// entries as absent (the sweeper or a later write reaps them — same
// trade-off as Exists).
//
// Empty-collection rule (Redis parity): a list or hash never exists
// empty. The last LPop/RPop/HDel deletes the key, so TYPE/EXISTS report
// none/0 immediately afterwards.

// getForWrite fetches the entry under an already-held write lock,
// evicting it if expired. ok is false when the key is logically absent.
func (s *Store) getForWrite(k string) (entry, bool) {
	e, ok := s.data[k]
	if !ok {
		return entry{}, false
	}
	if e.expired(s.nowFunc()) {
		delete(s.data, k)
		s.metrics.KeysExpired.Add(context.Background(), 1, lazyExpiryAttr)
		return entry{}, false
	}
	return e, true
}

// getForRead fetches the entry under an already-held read lock. Expired
// entries are treated as absent but NOT evicted (RLock only).
func (s *Store) getForRead(k string) (entry, bool) {
	e, ok := s.data[k]
	if !ok || e.expired(s.nowFunc()) {
		return entry{}, false
	}
	return e, true
}

// Type returns the value type name for k ("string", "list", "hash").
// ok is false when the key is missing or expired.
func (s *Store) Type(k string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return "", false
	}
	return e.typ.String(), true
}

// --- lists ---

// LPush prepends vals to the list at k (leftmost arg pushed first, so
// the last val ends up at the head — Redis LPUSH semantics). Creates
// the list if the key is absent. Returns the new length, or
// ErrWrongType if k holds a non-list.
func (s *Store) LPush(k string, vals ...[]byte) (int, error) {
	return s.push(k, vals, true)
}

// RPush appends vals to the list at k. Creates the list if the key is
// absent. Returns the new length, or ErrWrongType if k holds a
// non-list.
func (s *Store) RPush(k string, vals ...[]byte) (int, error) {
	return s.push(k, vals, false)
}

func (s *Store) push(k string, vals [][]byte, front bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(k)
	if !ok {
		e = entry{typ: typeList, list: &deque{}, seq: s.nextSeq()}
	} else if e.typ != typeList {
		return 0, ErrWrongType
	}
	for _, v := range vals {
		cp := append([]byte(nil), v...)
		if front {
			e.list.pushFront(cp)
		} else {
			e.list.pushBack(cp)
		}
	}
	s.data[k] = e
	return e.list.len(), nil
}

// LPop removes and returns the head of the list at k. ok is false when
// the key is missing (or expired). Popping the last element deletes
// the key.
func (s *Store) LPop(k string) ([]byte, bool, error) { return s.pop(k, true) }

// RPop removes and returns the tail of the list at k, with LPop's
// semantics otherwise.
func (s *Store) RPop(k string) ([]byte, bool, error) { return s.pop(k, false) }

func (s *Store) pop(k string, front bool) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(k)
	if !ok {
		return nil, false, nil
	}
	if e.typ != typeList {
		return nil, false, ErrWrongType
	}
	var (
		v   []byte
		got bool
	)
	if front {
		v, got = e.list.popFront()
	} else {
		v, got = e.list.popBack()
	}
	if !got {
		// Unreachable in practice — empty lists are deleted eagerly —
		// but treat as a miss rather than panic if it ever happens.
		delete(s.data, k)
		return nil, false, nil
	}
	if e.list.len() == 0 {
		delete(s.data, k)
	} else {
		s.data[k] = e
	}
	return v, true, nil
}

// LLen returns the length of the list at k (0 when missing), or
// ErrWrongType if k holds a non-list.
func (s *Store) LLen(k string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return 0, nil
	}
	if e.typ != typeList {
		return 0, ErrWrongType
	}
	return e.list.len(), nil
}

// LRange returns the elements of the list at k from start to stop
// inclusive with Redis LRANGE index semantics (negatives count from the
// tail, out-of-range clamps). A missing key yields an empty slice. The
// returned slices are owned by the store; callers MUST NOT mutate them.
func (s *Store) LRange(k string, start, stop int) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return [][]byte{}, nil
	}
	if e.typ != typeList {
		return nil, ErrWrongType
	}
	return e.list.rng(start, stop), nil
}

// LIndex returns the element at index i (negative counts from the
// tail). ok is false when the key is missing or the index is out of
// range. The returned slice is owned by the store.
func (s *Store) LIndex(k string, i int) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return nil, false, nil
	}
	if e.typ != typeList {
		return nil, false, ErrWrongType
	}
	n := e.list.len()
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		return nil, false, nil
	}
	return e.list.at(i), true, nil
}

// --- hashes ---

// HSet writes field/value pairs into the hash at k, creating it if the
// key is absent. pairs must have even length (field, value, ...) —
// callers validate arity. Returns the number of NEW fields created
// (updates of existing fields don't count — Redis HSET semantics), or
// ErrWrongType if k holds a non-hash.
func (s *Store) HSet(k string, pairs ...[]byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(k)
	if !ok {
		e = entry{typ: typeHash, hash: make(map[string][]byte, len(pairs)/2), seq: s.nextSeq()}
	} else if e.typ != typeHash {
		return 0, ErrWrongType
	}
	created := 0
	for i := 0; i+1 < len(pairs); i += 2 {
		f := string(pairs[i])
		if _, exists := e.hash[f]; !exists {
			created++
			e.fieldOrder = append(e.fieldOrder, f)
		}
		e.hash[f] = append([]byte(nil), pairs[i+1]...)
	}
	s.data[k] = e
	return created, nil
}

// HGet returns the value of field f in the hash at k. ok is false when
// the key or field is missing. The returned slice is owned by the
// store.
func (s *Store) HGet(k, f string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return nil, false, nil
	}
	if e.typ != typeHash {
		return nil, false, ErrWrongType
	}
	v, ok := e.hash[f]
	return v, ok, nil
}

// HDel removes fields from the hash at k and returns the number
// actually removed. Deleting the last field deletes the key.
func (s *Store) HDel(k string, fields ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.getForWrite(k)
	if !ok {
		return 0, nil
	}
	if e.typ != typeHash {
		return 0, ErrWrongType
	}
	n := 0
	for _, f := range fields {
		if _, exists := e.hash[f]; exists {
			delete(e.hash, f)
			e.fieldOrder = removeField(e.fieldOrder, f)
			n++
		}
	}
	if len(e.hash) == 0 {
		delete(s.data, k)
	} else {
		s.data[k] = e
	}
	return n, nil
}

// HExists reports whether field f exists in the hash at k.
func (s *Store) HExists(k, f string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return false, nil
	}
	if e.typ != typeHash {
		return false, ErrWrongType
	}
	_, ok = e.hash[f]
	return ok, nil
}

// HKeys returns the field names of the hash at k in insertion order. The
// order corresponds element-for-element with HVals and HGetAll on an
// unchanged hash (Redis's HKEYS[i]↔HVALS[i] guarantee). A missing key
// yields an empty slice.
func (s *Store) HKeys(k string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return []string{}, nil
	}
	if e.typ != typeHash {
		return nil, ErrWrongType
	}
	out := make([]string, len(e.fieldOrder))
	copy(out, e.fieldOrder)
	return out, nil
}

// HVals returns the values of the hash at k in insertion order,
// corresponding element-for-element with HKeys. The returned slices are
// owned by the store.
func (s *Store) HVals(k string) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return [][]byte{}, nil
	}
	if e.typ != typeHash {
		return nil, ErrWrongType
	}
	out := make([][]byte, 0, len(e.fieldOrder))
	for _, f := range e.fieldOrder {
		out = append(out, e.hash[f])
	}
	return out, nil
}

// HLen returns the number of fields in the hash at k (0 when missing).
func (s *Store) HLen(k string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return 0, nil
	}
	if e.typ != typeHash {
		return 0, ErrWrongType
	}
	return len(e.hash), nil
}

// HGetAll returns every field/value pair of the hash at k as a flat
// [f1, v1, f2, v2, ...] slice (the shape RESP map replies want) in
// insertion order — the field sequence matches HKeys and each value
// matches HVals. A missing key yields an empty slice. The value slices
// are owned by the store.
func (s *Store) HGetAll(k string) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.getForRead(k)
	if !ok {
		return [][]byte{}, nil
	}
	if e.typ != typeHash {
		return nil, ErrWrongType
	}
	out := make([][]byte, 0, len(e.fieldOrder)*2)
	for _, f := range e.fieldOrder {
		out = append(out, []byte(f), e.hash[f])
	}
	return out, nil
}

// removeField returns order with the first occurrence of f removed,
// preserving the relative order of the remaining fields. It mutates and
// returns the same backing array when possible; callers store the result
// back on the entry.
func removeField(order []string, f string) []string {
	for i, name := range order {
		if name == f {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}

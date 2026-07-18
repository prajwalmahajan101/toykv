package store

// deque is a growable ring buffer of byte slices backing list values.
//
// It exists so LPUSH is O(1): a plain slice would pay an O(n) copy per
// left push, which turns AOF replay of a left-heavy workload quadratic.
// The ring keeps both ends O(1) while preserving O(1) random access for
// LINDEX and a contiguous-ish O(k) LRANGE.
//
// The zero value is ready to use. deque is NOT safe for concurrent use;
// the Store's mutex guards it.
type deque struct {
	buf   [][]byte
	head  int // index of the first element in buf
	count int
}

// len returns the number of elements.
func (d *deque) len() int { return d.count }

// grow doubles capacity (minimum 8) and linearises the ring so head
// starts at 0. Called only when the buffer is full.
func (d *deque) grow() {
	newCap := max(len(d.buf)*2, 8)
	nb := make([][]byte, newCap)
	for i := 0; i < d.count; i++ {
		nb[i] = d.buf[(d.head+i)%len(d.buf)]
	}
	d.buf = nb
	d.head = 0
}

// pushFront prepends v (LPUSH).
func (d *deque) pushFront(v []byte) {
	if d.count == len(d.buf) {
		d.grow()
	}
	d.head = (d.head - 1 + len(d.buf)) % len(d.buf)
	d.buf[d.head] = v
	d.count++
}

// pushBack appends v (RPUSH).
func (d *deque) pushBack(v []byte) {
	if d.count == len(d.buf) {
		d.grow()
	}
	d.buf[(d.head+d.count)%len(d.buf)] = v
	d.count++
}

// popFront removes and returns the first element (LPOP).
func (d *deque) popFront() ([]byte, bool) {
	if d.count == 0 {
		return nil, false
	}
	v := d.buf[d.head]
	d.buf[d.head] = nil // release for GC
	d.head = (d.head + 1) % len(d.buf)
	d.count--
	return v, true
}

// popBack removes and returns the last element (RPOP).
func (d *deque) popBack() ([]byte, bool) {
	if d.count == 0 {
		return nil, false
	}
	i := (d.head + d.count - 1) % len(d.buf)
	v := d.buf[i]
	d.buf[i] = nil // release for GC
	d.count--
	return v, true
}

// at returns the element at index i (0-based from the front). Callers
// must pass 0 <= i < len(); LINDEX's negative-index normalisation
// happens above this layer.
func (d *deque) at(i int) []byte {
	return d.buf[(d.head+i)%len(d.buf)]
}

// clone returns a deep copy of the deque with every element byte slice
// duplicated, so the copy is fully independent of the original. Backs
// COPY of a list value (keyspace.go).
func (d *deque) clone() *deque {
	nd := &deque{}
	for i := 0; i < d.count; i++ {
		nd.pushBack(append([]byte(nil), d.at(i)...))
	}
	return nd
}

// rng returns the elements from start to stop inclusive with Redis
// LRANGE semantics: negative indices count from the tail (-1 is the
// last element), out-of-range indices clamp, and an empty range yields
// an empty (non-nil) slice.
func (d *deque) rng(start, stop int) [][]byte {
	n := d.count
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}
	if start > stop || start >= n {
		return [][]byte{}
	}
	out := make([][]byte, 0, stop-start+1)
	for i := start; i <= stop; i++ {
		out = append(out, d.at(i))
	}
	return out
}

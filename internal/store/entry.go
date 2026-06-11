package store

// entry is a single key's stored payload. M2 carries only the value;
// M4 will add an expireAt time.Time when TTL lands. Keeping entry as a
// struct (not a bare []byte) makes that diff trivial.
type entry struct {
	value []byte
}

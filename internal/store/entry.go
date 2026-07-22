package store

import "time"

// valueType tags the variant held by an entry. M11 turns the entry into
// a tagged union (string / list / hash); the tag is checked by every
// typed accessor, and a mismatch surfaces as ErrWrongType — the store-
// level source of the wire's -WRONGTYPE reply.
type valueType byte

const (
	typeString valueType = iota + 1
	typeList
	typeHash
)

// String returns the Redis TYPE-command name for the tag.
func (t valueType) String() string {
	switch t {
	case typeString:
		return "string"
	case typeList:
		return "list"
	case typeHash:
		return "hash"
	}
	return "none"
}

// entry is a single key's stored payload — a tagged union with exactly
// one of str / list / hash populated according to typ.
//
// expireAt is the absolute deadline after which the entry is considered
// expired. The zero value means "no expiry". TTL state is uniform
// across all value types (EXPIRE / PERSIST / TTL are type-agnostic).
type entry struct {
	typ  valueType
	str  []byte            // typeString
	list *deque            // typeList
	hash map[string][]byte // typeHash
	// fieldOrder records hash field names in insertion order (typeHash
	// only). HKeys/HVals/HGetAll iterate it instead of ranging the map so
	// their results correspond pairwise and stay stable across calls —
	// matching Redis's HKEYS[i]↔HVALS[i] guarantee. A field is appended when
	// first created (HSet) and removed on HDel; re-adding a deleted field
	// appends it at the end, as Redis does.
	fieldOrder []string
	expireAt   time.Time
	// seq is the monotonic creation sequence stamped when the key is
	// first created (absent→present). It is preserved across updates so a
	// key keeps a stable position for SCAN's whole lifetime; SCAN's cursor
	// is a seq value. Never zero for a live key (the counter starts at 1).
	seq uint64
}

// expired reports whether the entry has an expiry set and now is at or
// past it. A zero expireAt is never expired.
func (e entry) expired(now time.Time) bool {
	return !e.expireAt.IsZero() && !now.Before(e.expireAt)
}

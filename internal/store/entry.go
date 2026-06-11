package store

import "time"

// entry is a single key's stored payload.
//
// expireAt is the absolute deadline after which the entry is considered
// expired. The zero value means "no expiry" — this is the v1 shape and
// is preserved across all SET-without-TTL writes.
type entry struct {
	value    []byte
	expireAt time.Time
}

// expired reports whether the entry has an expiry set and now is at or
// past it. A zero expireAt is never expired.
func (e entry) expired(now time.Time) bool {
	return !e.expireAt.IsZero() && !now.Before(e.expireAt)
}

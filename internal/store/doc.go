// Package store is the in-memory key-value map underpinning toykv.
//
// v1 holds values as strict []byte (no typed values; see PRD §5.2). The
// store is guarded by a single sync.RWMutex: reads take RLock, writes
// take Lock. Single-mutex contention is the documented v1 trade-off
// (HLD §7) — revisit only if benchmarks justify sharding.
//
// Glob matching for KEYS uses stdlib path.Match. Its [charset] handling
// is not byte-identical to Redis's; v1 ships with this limitation
// (LLD §3.4). A custom matcher will land only when a test demonstrates
// a real Redis-compat gap.
//
// Get returns the stored byte slice directly without copying. Callers
// MUST NOT mutate the returned slice. Set takes a defensive copy of
// the caller-supplied value so the store owns its memory.
//
// TTL/expiry, the lazy-expiry read path, and the background sweeper
// land in M4 (see docs/LLD.md §3.1–§3.3). M2 ships a TTL-free core.
package store

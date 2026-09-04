package cluster

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"

	"github.com/prajwalmahajan101/toykv/internal/store"
)

// This file is forward-compat scaffolding for ToyRaft v2 snapshots. None of it
// is on the M18 live path — StateMachine.Snapshot/Restore return
// ErrSnapshotUnsupported per the v1 contract — but building and testing it now
// means enabling compaction later is a one-line swap (Snapshot calls
// SerializeSnapshot; Restore replays CommandsFromSnapshot through ApplyFunc).
//
// The command rendering mirrors Server.snapshotForRewrite (bgrewriteaof.go): a
// snapshot is the set of canonical mutating commands that rebuild live state,
// which is exactly what BGREWRITEAOF already emits into a fresh AOF.

// commandsFromSnapshot renders live store state as the canonical command
// sequence that reconstructs it:
//
//	string → SET k v [PXAT ms]
//	list   → RPUSH k e1 … eN         (+ PEXPIREAT k ms if TTL)
//	hash   → HSET k f1 v1 … fN vN    (+ PEXPIREAT k ms if TTL, fields in insertion order)
//
// Entries are sorted by key so the output is deterministic across nodes
// (store.Snapshot ranges a map); within a key, list order and hash field order
// are already deterministic.
func commandsFromSnapshot(entries []store.SnapshotEntry) [][][]byte {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	out := make([][][]byte, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		switch e.Type {
		case "list":
			argv := make([][]byte, 0, 2+len(e.List))
			argv = append(argv, []byte("RPUSH"), []byte(e.Key))
			argv = append(argv, e.List...)
			out = append(out, argv)
		case "hash":
			argv := make([][]byte, 0, 2+2*len(e.HashOrder))
			argv = append(argv, []byte("HSET"), []byte(e.Key))
			for _, f := range e.HashOrder {
				argv = append(argv, []byte(f), e.Hash[f])
			}
			out = append(out, argv)
		default: // "string"
			out = append(out, renderCanonicalSet(e.Key, e.Value, e))
			continue // SET carries TTL inline via PXAT
		}
		if !e.ExpireAt.IsZero() {
			out = append(out, [][]byte{
				[]byte("PEXPIREAT"), []byte(e.Key),
				[]byte(strconv.FormatInt(e.ExpireAt.UnixMilli(), 10)),
			})
		}
	}
	return out
}

// renderCanonicalSet renders a string entry as SET k v [PXAT ms], matching the
// server's live/rewrite canonical form (commands.go:renderCanonicalSet).
func renderCanonicalSet(key string, value []byte, e *store.SnapshotEntry) [][]byte {
	argv := [][]byte{[]byte("SET"), []byte(key), value}
	if !e.ExpireAt.IsZero() {
		argv = append(argv, []byte("PXAT"), []byte(strconv.FormatInt(e.ExpireAt.UnixMilli(), 10)))
	}
	return argv
}

// SerializeSnapshot encodes live store state as a length-framed sequence of
// command envelopes: for each canonical command, a 4-byte big-endian length
// prefix followed by its Encode bytes. CommandsFromSnapshot reverses it.
func SerializeSnapshot(entries []store.SnapshotEntry) []byte {
	cmds := commandsFromSnapshot(entries)
	var out []byte
	var lenbuf [4]byte
	for _, argv := range cmds {
		env := Encode(argv)
		binary.BigEndian.PutUint32(lenbuf[:], uint32(len(env)))
		out = append(out, lenbuf[:]...)
		out = append(out, env...)
	}
	return out
}

// CommandsFromSnapshot decodes a SerializeSnapshot blob back into the command
// sequence. Replaying these through ApplyFunc against a fresh store rebuilds
// the snapshotted state.
func CommandsFromSnapshot(data []byte) ([][][]byte, error) {
	var cmds [][][]byte
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, fmt.Errorf("cluster: snapshot truncated header (%d bytes left)", len(data))
		}
		n := binary.BigEndian.Uint32(data[:4])
		data = data[4:]
		if uint32(len(data)) < n {
			return nil, fmt.Errorf("cluster: snapshot truncated body (want %d, have %d)", n, len(data))
		}
		argv, err := Decode(data[:n])
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, argv)
		data = data[n:]
	}
	return cmds, nil
}

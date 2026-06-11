// Package aof persists mutating commands to an append-only file and
// replays them on startup. See docs/LLD.md §4 and docs/adr/0001 for the
// format and fsync policy decisions.
//
// File layout: 7-byte magic "TOYKV\x00\x00" + 1-byte version + a stream
// of RESP-encoded command arrays. The same RESP codec drives both the
// wire protocol and the on-disk log — append the bytes you'd send,
// replay by feeding the file through resp.Reader.
//
// fsync policies (LLD §4.2): FsyncAlways (per-Append, strongest);
// FsyncEverysec (background ticker, ~1s window of acked-but-lost
// writes on crash); FsyncNever (kernel decides).
//
// The Replayer fails fast on a corrupted tail with the byte offset of
// the failing record; v1 does not auto-truncate.
package aof

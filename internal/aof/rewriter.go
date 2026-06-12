package aof

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// SnapshotCmd is one canonical command the rewriter will write into the
// fresh AOF — typically "SET k v" or "SET k v PXAT <unix-ms>". The
// server builds these from Store.Snapshot using the same encoder it
// uses for live SETs, so a rewritten file is byte-identical to what a
// fresh insertion sequence would produce.
type SnapshotCmd struct {
	Argv [][]byte
}

// Rewriter orchestrates BGREWRITEAOF (LLD §4.4). It:
//  1. arms side-buffer capture on the Writer,
//  2. obtains a snapshot of live state from the server-supplied callback,
//  3. writes that snapshot into a fresh .tmp file with a v2 header,
//  4. atomically renames .tmp onto the canonical path and fsyncs the dir,
//  5. tells the Writer to swap its fd to the new file and drain the
//     side buffer onto it.
//
// Failure before the rename leaves the canonical file untouched and
// removes .tmp. Failure after the rename returns an error to the caller;
// the canonical file is the new file and remains durable.
type Rewriter struct {
	w        *Writer
	snapshot func() []SnapshotCmd
}

// NewRewriter returns a Rewriter bound to w. snapshot is invoked once
// per Rewrite call to capture live state. snapshot must not retain
// references to internal store memory — the server's bridge already
// copies values via Store.Snapshot.
func NewRewriter(w *Writer, snapshot func() []SnapshotCmd) *Rewriter {
	return &Rewriter{w: w, snapshot: snapshot}
}

// Rewrite performs one BGREWRITEAOF cycle. It is safe to call only
// once at a time; the server is responsible for the single-flight gate.
//
// ctx is currently advisory — the rewrite is short for v1 store sizes
// and cancellation is checked only at coarse boundaries.
func (r *Rewriter) Rewrite(ctx context.Context) error {
	if r.w == nil {
		return errors.New("aof: nil writer")
	}
	if r.snapshot == nil {
		return errors.New("aof: nil snapshot func")
	}

	if err := r.w.BeginRewrite(); err != nil {
		return err
	}

	dir := r.w.Dir()
	tmpPath := filepath.Join(dir, TmpFilename)
	canonicalPath := filepath.Join(dir, Filename)

	if err := r.writeSnapshot(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		r.w.AbortRewrite()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tmpPath)
		r.w.AbortRewrite()
		return err
	}

	if err := os.Rename(tmpPath, canonicalPath); err != nil {
		_ = os.Remove(tmpPath)
		r.w.AbortRewrite()
		return fmt.Errorf("aof: rename %s -> %s: %w", tmpPath, canonicalPath, err)
	}

	// fsync the parent directory so the rename survives a crash.
	if err := fsyncDir(dir); err != nil {
		// Rename already happened — the new file is canonical. The
		// directory fsync failure is the operator's problem; report it
		// but do not abort. The side-buffer must still be drained onto
		// the new file or its writes are lost.
		drainErr := r.w.DrainAndSwap(canonicalPath)
		if drainErr != nil {
			return fmt.Errorf("aof: fsync dir failed (%v) and drain failed: %w", err, drainErr)
		}
		return fmt.Errorf("aof: fsync dir: %w", err)
	}

	if err := r.w.DrainAndSwap(canonicalPath); err != nil {
		return err
	}
	return nil
}

// writeSnapshot creates tmpPath, writes a v2 header, RESP-encodes every
// snapshot command into it, fsyncs, and closes. Always truncates an
// existing .tmp — startup hygiene plus the rename below guarantee that
// any pre-existing .tmp is garbage.
func (r *Rewriter) writeSnapshot(tmpPath string) error {
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("aof: open tmp %s: %w", tmpPath, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	if err := writeHeader(f); err != nil {
		return err
	}

	bw := bufio.NewWriter(f)
	rw := resp.NewWriter(bw)
	cmds := r.snapshot()
	for _, c := range cmds {
		if len(c.Argv) == 0 {
			return errors.New("aof: snapshot produced empty argv")
		}
		elems := make([]resp.Value, len(c.Argv))
		for i, a := range c.Argv {
			elems[i] = resp.Bulk(a)
		}
		if err := rw.WriteFrame(resp.Array(elems...)); err != nil {
			return fmt.Errorf("aof: encode snapshot: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("aof: flush tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("aof: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("aof: close tmp: %w", err)
	}
	closed = true
	return nil
}

// fsyncDir opens dir, fsyncs it, and closes. On POSIX this is what makes
// a rename durable.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("aof: open dir %s: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("aof: fsync dir %s: %w", dir, err)
	}
	return nil
}

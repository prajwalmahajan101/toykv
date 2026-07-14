package aof

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// FsyncPolicy controls how often the AOF is flushed to disk.
type FsyncPolicy int

const (
	// FsyncAlways fsyncs after every Append. Strongest durability; the
	// reply path waits for fsync before acknowledging the client.
	FsyncAlways FsyncPolicy = iota

	// FsyncEverysec fsyncs once per second via a background ticker. At
	// most ~1s of acked writes can be lost on a crash.
	FsyncEverysec

	// FsyncNever leaves fsync to the kernel.
	FsyncNever
)

// String returns the policy's flag-style name ("always"/"everysec"/"no").
func (p FsyncPolicy) String() string {
	switch p {
	case FsyncAlways:
		return "always"
	case FsyncEverysec:
		return "everysec"
	case FsyncNever:
		return "no"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// ParsePolicy maps the CLI flag value to a FsyncPolicy.
func ParsePolicy(s string) (FsyncPolicy, error) {
	switch s {
	case "always":
		return FsyncAlways, nil
	case "everysec":
		return FsyncEverysec, nil
	case "no":
		return FsyncNever, nil
	default:
		return 0, fmt.Errorf("aof: invalid fsync policy %q (want always|everysec|no)", s)
	}
}

// Filename is the canonical AOF file name inside the data directory.
const Filename = "toykv.aof"

// Writer appends RESP-encoded command records to the on-disk AOF.
//
// Writer.Append is safe for concurrent use; the everysec ticker shares
// the same mutex so a tick cannot race with an in-flight append.
type Writer struct {
	path   string
	dir    string
	f      *os.File
	bw     *bufio.Writer
	rw     *resp.Writer
	policy FsyncPolicy

	mu    sync.Mutex
	dirty bool // buffered/unfsynced bytes are present

	// sideBuf, when non-nil, mirrors every Append's RESP bytes for the
	// in-flight rewriter to consume after the atomic rename. Dual-write
	// is intentional: the canonical file remains durable and consistent
	// at every instant until the swap. See ADR-0005.
	sideBuf *bytes.Buffer

	stopCh chan struct{} // closed to stop the everysec goroutine
	doneCh chan struct{} // closed when the goroutine has exited
}

// Open opens (or creates) the AOF in dir under the given fsync policy.
// The directory is created if it does not exist; the file is created
// with the header if it is fresh, and validated if it already exists.
func Open(dir string, policy FsyncPolicy) (*Writer, error) {
	if dir == "" {
		return nil, errors.New("aof: dir must be set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("aof: mkdir %s: %w", dir, err)
	}
	// A leftover .tmp means a previous run was killed mid-rewrite. The
	// canonical file is always the source of truth; the .tmp is garbage.
	// Remove ENOENT-tolerantly so this is a no-op on the common path.
	tmpPath := filepath.Join(dir, TmpFilename)
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("aof: remove stale %s: %w", tmpPath, err)
	}
	path := filepath.Join(dir, Filename)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("aof: open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("aof: stat %s: %w", path, err)
	}

	if info.Size() == 0 {
		if err := writeHeader(f); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("aof: fsync header: %w", err)
		}
	} else {
		// Validate the existing header before we trust the file.
		rf, err := os.Open(path)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("aof: open for header check %s: %w", path, err)
		}
		ver, herr := readHeader(rf)
		_ = rf.Close()
		if herr != nil {
			_ = f.Close()
			return nil, herr
		}
		if ver < CurrentVersion {
			// In-place header upgrade (single byte at offset 7) BEFORE
			// any append, so the invariant "header version >= newest
			// record format in the file" holds at every instant: once
			// v3 records can land in this file, an older binary must
			// refuse it up-front with ErrBadVersion rather than dying
			// mid-replay on an unknown command. f is O_APPEND (WriteAt
			// would error), so use a dedicated handle for the pwrite.
			if err := upgradeHeader(path); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
	}

	bw := bufio.NewWriter(f)
	w := &Writer{
		path:   path,
		dir:    dir,
		f:      f,
		bw:     bw,
		rw:     resp.NewWriter(bw),
		policy: policy,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}

	if policy == FsyncEverysec {
		go w.runEverysec()
	} else {
		close(w.doneCh)
	}
	return w, nil
}

// Path returns the absolute path of the AOF file.
func (w *Writer) Path() string { return w.path }

// Append RESP-encodes argv as a command array, writes it, flushes the
// buffer, and fsyncs per the configured policy. The reply path must
// call Append before sending +OK so the durability contract holds.
func (w *Writer) Append(argv [][]byte) error {
	if len(argv) == 0 {
		return errors.New("aof: empty argv")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	elems := make([]resp.Value, len(argv))
	for i, a := range argv {
		elems[i] = resp.Bulk(a)
	}
	if w.sideBuf != nil {
		// Encode into a scratch buffer so the exact RESP bytes can be
		// mirrored into both the live file and the rewrite side buffer.
		// resp.Writer buffers internally — Flush surfaces the bytes.
		var scratch bytes.Buffer
		mirror := resp.NewWriter(&scratch)
		if err := mirror.WriteFrame(resp.Array(elems...)); err != nil {
			return fmt.Errorf("aof: encode record: %w", err)
		}
		if err := mirror.Flush(); err != nil {
			return fmt.Errorf("aof: flush mirror: %w", err)
		}
		if _, err := w.bw.Write(scratch.Bytes()); err != nil {
			return fmt.Errorf("aof: write: %w", err)
		}
		w.sideBuf.Write(scratch.Bytes())
	} else {
		if err := w.rw.WriteFrame(resp.Array(elems...)); err != nil {
			return fmt.Errorf("aof: encode record: %w", err)
		}
	}
	if err := w.bw.Flush(); err != nil {
		return fmt.Errorf("aof: flush: %w", err)
	}
	w.dirty = true

	switch w.policy {
	case FsyncAlways:
		if err := w.f.Sync(); err != nil {
			return fmt.Errorf("aof: fsync: %w", err)
		}
		w.dirty = false
	case FsyncEverysec, FsyncNever:
		// ticker / kernel handle fsync
	}
	return nil
}

// Close stops the everysec ticker (if running), flushes the buffer,
// fsyncs, and closes the file.
func (w *Writer) Close() error {
	close(w.stopCh)
	<-w.doneCh

	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	if err := w.bw.Flush(); err != nil {
		firstErr = fmt.Errorf("aof: flush on close: %w", err)
	}
	if err := w.f.Sync(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("aof: fsync on close: %w", err)
	}
	if err := w.f.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("aof: close: %w", err)
	}
	return firstErr
}

// Dir returns the data directory the writer was opened against.
func (w *Writer) Dir() string { return w.dir }

// BeginRewrite arms side-buffer mode: every subsequent Append continues
// to write to the canonical file *and* mirrors the same RESP bytes into
// an in-memory side buffer. FinalizeRewrite folds that buffer into the
// fresh file before it becomes canonical. Returns an error if a rewrite
// is already in flight.
//
// The dual-write design (ADR-0005) keeps the canonical file durable and
// consistent at every instant until the rename swaps it.
func (w *Writer) BeginRewrite() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sideBuf != nil {
		return errors.New("aof: rewrite already in progress")
	}
	w.sideBuf = &bytes.Buffer{}
	return nil
}

// AbortRewrite clears side-buffer mode without swapping the file. Used
// on rewriter error paths. Safe to call when no rewrite is active.
func (w *Writer) AbortRewrite() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sideBuf = nil
}

// FinalizeRewrite completes a rewrite atomically with respect to Append.
// The rewriter calls it after writing the snapshot to tmpPath (fsynced,
// closed). It holds the writer lock across the ENTIRE swap so no acked
// write can be stranded in the old (about-to-be-unlinked) inode:
//
//  1. Append the side buffer onto tmpPath and fsync it — so the fresh
//     file holds snapshot + every acked append BEFORE it becomes
//     canonical (this is the ordering that closes the crash window: a
//     crash before the rename keeps the complete old file; a crash after
//     it keeps the complete new file).
//  2. Atomically rename tmpPath onto canonicalPath and fsync the dir.
//  3. Swap the writer's fd from the old inode to the new canonical file.
//  4. Exit side-buffer mode.
//
// Because the lock is held throughout, no Append can interleave between
// the side-buffer capture and the fd swap. On return (success or error)
// the writer has exited side-buffer mode. canonicalPath must equal
// filepath.Join(w.dir, Filename); callers pass it explicitly so the
// rewriter can choose its own filename layout in tests.
func (w *Writer) FinalizeRewrite(tmpPath, canonicalPath string) (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sideBuf == nil {
		return errors.New("aof: FinalizeRewrite without BeginRewrite")
	}
	// Always leave side-buffer mode, even on error — a stuck sideBuf would
	// dual-write forever and block the next rewrite.
	defer func() { w.sideBuf = nil }()

	// Flush any tail still buffered in the old file. Under FsyncAlways
	// each Append already flushed+fsynced, so this is a no-op there; it
	// matters for everysec/no, where the old file must be complete in
	// case we crash before the rename.
	if err := w.bw.Flush(); err != nil {
		return fmt.Errorf("aof: flush before finalize: %w", err)
	}

	// 1. Fold the side buffer into the fresh file and fsync it, so the
	//    new file is complete before the rename makes it canonical.
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("aof: reopen tmp %s: %w", tmpPath, err)
	}
	if w.sideBuf.Len() > 0 {
		if _, err := tmp.Write(w.sideBuf.Bytes()); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("aof: append side-buffer to tmp: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("aof: fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("aof: close tmp: %w", err)
	}

	// 2. Atomic rename + dir fsync makes the complete file canonical.
	if err := os.Rename(tmpPath, canonicalPath); err != nil {
		return fmt.Errorf("aof: rename %s -> %s: %w", tmpPath, canonicalPath, err)
	}
	if afterRenameHook != nil {
		afterRenameHook()
	}
	if err := fsyncDir(w.dir); err != nil {
		// Rename already happened — the new file is canonical and complete.
		// Swap the fd so later appends target it, then report the fsync.
		if swapErr := w.swapTo(canonicalPath); swapErr != nil {
			return fmt.Errorf("aof: fsync dir failed (%v) and fd swap failed: %w", err, swapErr)
		}
		return fmt.Errorf("aof: fsync dir: %w", err)
	}

	// 3. Swap the writer's fd off the old (now-unlinked) inode.
	return w.swapTo(canonicalPath)
}

// swapTo closes the old fd and reopens canonicalPath in append mode so
// subsequent appends target the new file. The caller must hold w.mu.
func (w *Writer) swapTo(canonicalPath string) error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("aof: close pre-swap fd: %w", err)
	}
	f, err := os.OpenFile(canonicalPath, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("aof: open post-swap %s: %w", canonicalPath, err)
	}
	w.f = f
	w.path = canonicalPath
	w.bw = bufio.NewWriter(f)
	w.rw = resp.NewWriter(w.bw)
	w.dirty = false
	return nil
}

func (w *Writer) runEverysec() {
	defer close(w.doneCh)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.mu.Lock()
			if w.dirty {
				_ = w.f.Sync()
				w.dirty = false
			}
			w.mu.Unlock()
		}
	}
}

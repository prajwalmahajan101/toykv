# toykv — Low-Level Design

> Types, signatures, byte layouts, error taxonomy, and concurrency mechanics.
> Implementation reference. If [HLD](./HLD.md) is "what fits where", this is "what compiles".

| Field | Value |
|---|---|
| Status | Draft — pre-implementation |
| Module | `github.com/prajwalmahajan101/toykv` |
| Go version | 1.26 |
| Last updated | 2026-06-11 |

---

## 1. Package layout

```
toykv/
├── cmd/
│   ├── toykv/           main.go            (server entrypoint)
│   ├── toykv-cli/       main.go            (CLI entrypoint)
│   └── toykv-tui/       main.go            (TUI entrypoint)
├── internal/
│   ├── resp/            reader.go writer.go types.go errors.go
│   ├── store/           store.go entry.go ttl.go sweeper.go
│   ├── aof/             writer.go replayer.go rewriter.go format.go
│   ├── server/          server.go conn.go dispatch.go commands.go
│   ├── client/          client.go            (RESP client over net.Conn — shared)
│   ├── cli/             oneshot.go repl.go piped.go format.go
│   └── tui/             model.go update.go view.go msgs.go
├── docs/
├── Makefile
├── go.mod
├── LICENSE
└── README.md
```

**Internal-only.** Everything is under `internal/` — no exported library surface. Three binaries, no third-party consumers.

## 2. RESP codec (`internal/resp`)

### 2.1 Grammar (RESP2 subset)

```
frame      = simple-string / error / integer / bulk-string / array
simple-string = "+" content CRLF
error      = "-" content CRLF
integer    = ":" digits CRLF
bulk-string = "$" length CRLF bytes CRLF      ; length=-1 → nil
array      = "*" length CRLF *frame           ; length=-1 → nil array
CRLF       = "\r\n"
```

Commands arrive as arrays of bulk strings: `*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n`.

### 2.2 Types

```go
package resp

type Kind byte
const (
    KindSimpleString Kind = '+'
    KindError        Kind = '-'
    KindInteger      Kind = ':'
    KindBulkString   Kind = '$'
    KindArray        Kind = '*'
)

type Value struct {
    Kind   Kind
    Str    string   // for simple/error/bulk
    Int    int64    // for integer
    Bytes  []byte   // for bulk (nil = nil-bulk)
    Array  []Value  // for array (nil = nil-array)
    IsNull bool     // distinguishes nil-bulk/array from empty
}

// Convenience constructors
func String(s string) Value          // simple-string +s
func Error(s string) Value           // error       -s
func Int(n int64) Value              // integer     :n
func Bulk(b []byte) Value            // $len\r\nb
func NullBulk() Value                // $-1
func Array(vs ...Value) Value        // *len\r\n…
func NullArray() Value               // *-1
func OK() Value                      // +OK   (shortcut)
```

### 2.3 Reader

```go
type Reader struct{ br *bufio.Reader }

func NewReader(r io.Reader) *Reader

// ReadFrame returns the next top-level frame.
// Returns io.EOF on clean close, ErrProtocol on malformed input.
func (r *Reader) ReadFrame() (Value, error)

// ReadCommand convenience: expects an array of bulk strings, returns
// argv as [][]byte. Caller owns lifetime of returned slice — Reader
// does not retain it.
func (r *Reader) ReadCommand() ([][]byte, error)
```

Buffer policy: `bufio.NewReaderSize(conn, 16 * 1024)`. Bulk strings larger than the buffer are read in chunks into a fresh slice.

### 2.4 Writer

```go
type Writer struct{ bw *bufio.Writer }

func NewWriter(w io.Writer) *Writer
func (w *Writer) WriteFrame(v Value) error
func (w *Writer) Flush() error
```

Writes are buffered; the conn handler flushes after each reply (latency over throughput for a learning KV).

### 2.5 Errors

```go
var (
    ErrProtocol     = errors.New("resp: protocol error")
    ErrInvalidArity = errors.New("resp: invalid array arity")
    ErrTooLarge     = errors.New("resp: frame exceeds limit")
)
```

Max bulk-string length: **64 MiB**. Larger → `ErrTooLarge`, conn dropped.

## 3. Store (`internal/store`)

### 3.1 Types

```go
package store

import (
    "sync"
    "time"
)

type valueType byte // typeString | typeList | typeHash

type entry struct {
    typ      valueType
    str      []byte            // typeString
    list     *deque            // typeList — growable ring buffer, O(1) both ends
    hash     map[string][]byte // typeHash
    expireAt time.Time         // zero ⇒ no expiry; uniform across types
}

type Store struct {
    mu      sync.RWMutex
    data    map[string]entry
    nowFunc func() time.Time // injectable for tests; defaults to time.Now
}

func New() *Store
func NewWithClock(now func() time.Time) *Store
```

v1 shipped a strict `[]byte` value (`entry{value, expireAt}`); M11 turned the entry into the tagged union above. Rules (Redis parity): `SET` overwrites any type; `GET`/`INCR`/`DECR` on a non-string return `ErrWrongType` (wire: `-WRONGTYPE …`); `DEL`/`EXISTS`/`EXPIRE`/`TTL`/`PERSIST`/`KEYS` are type-agnostic; a list/hash emptied by its last `LPop`/`RPop`/`HDel` deletes the key — empty collections never exist. Lists are backed by a growable ring-buffer deque so `LPUSH` is O(1) (a slice would make left-heavy AOF replay quadratic).

### 3.2 Operations

```go
// Reads
func (s *Store) Get(k string) (val []byte, ok bool)
func (s *Store) Exists(keys ...string) int
func (s *Store) TTL(k string) time.Duration // -2 nokey, -1 noexp, ≥0 remaining
func (s *Store) Keys(pattern string) []string  // glob via path.Match-like matcher
func (s *Store) DBSize() int

// Writes
type SetMode int
const (
    SetAlways SetMode = iota
    SetNX                  // only if not exists
    SetXX                  // only if exists
)

type SetOpts struct {
    Mode SetMode
    TTL  time.Duration // 0 means no expiry
}

func (s *Store) Set(k string, v []byte, opts SetOpts) (ok bool)
func (s *Store) Del(keys ...string) int
func (s *Store) Expire(k string, ttl time.Duration) bool
func (s *Store) Incr(k string) (int64, error)
func (s *Store) Decr(k string) (int64, error)
func (s *Store) FlushDB()

// Maintenance
func (s *Store) sweepOnce(now time.Time) int // returns # evicted, package-private
```

**Lock policy:**
- Reads (`Get`, `Exists`, `TTL`, `Keys`, `DBSize`) take `RLock`.
- Writes take `Lock`.
- Lazy expiry check happens inside the lock: a `Get` that finds an expired entry **upgrades** by releasing `RLock`, taking `Lock`, deleting, returning miss. (Trade-off documented in HLD §7; revisit if benchmarks justify.)

### 3.3 TTL sweeper

```go
type Sweeper struct {
    s        *Store
    interval time.Duration // 1s
    batch    int           // sample 20 keys per tick
    stop     chan struct{}
}

func NewSweeper(s *Store) *Sweeper
func (sw *Sweeper) Run(ctx context.Context)
```

Algorithm matches Redis's "expire random sample" — sample 20 keys per tick, evict expired, repeat if >25% were expired. Caps lock-hold time to a small bounded window per tick.

**Lifecycle.** The sweeper is dormant during AOF replay — `server.New` does replay before constructing/launching the sweeper, and `server.Run` is what starts the goroutine. As a consequence, immediately after replay the store may contain entries whose `expireAt` is already past; lazy expiry catches them on first read, and the first sweeper tick (≤ 1s after Run starts) reaps the rest. `DBSIZE` during that brief window can include not-yet-swept expired keys — matches Redis semantics and is documented behaviour, not a bug.

### 3.4 Glob matcher

`path.Match` is *close* but doesn't support `[charset]` ranges identically. v1 uses `path.Match` and documents the limitation; if a test demonstrates a Redis-compat gap, the matcher gets its own file (`store/glob.go`) and a unit test suite.

## 4. AOF (`internal/aof`)

### 4.1 File layout

```
┌──────────────────────────────────────────────┐
│  header (8 bytes):                           │
│    magic  = "TOYKVAOF"                       │
│    version = 0x02            (1 byte, inside)│
│                                              │
│  Actually: 7-byte magic + 1-byte version     │
│    "TOYKV"  + 0x00 + 0x00 + 0x03             │
│                                              │
│  Padded to 8 bytes for alignment.            │
│                                              │
│  Supported versions on read: {0x01, 0x02,    │
│  0x03}. v1 files (pre-M4) replay cleanly.    │
│  v2 introduces SET ... PXAT, PEXPIREAT,      │
│  PERSIST records — see ADR-0004.             │
│  v3 (M11) adds typed records — LPUSH/RPUSH/  │
│  LPOP/RPOP/HSET/HDEL. Open() upgrades an     │
│  older file's version byte in place (pwrite  │
│  at offset 7 + fsync) before appending, so   │
│  header version >= newest record format in   │
│  the file at every instant.                  │
└──────────────────────────────────────────────┘
┌──────────────────────────────────────────────┐
│  records (repeated):                         │
│    RESP-encoded command array                │
│    e.g. *3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n  │
└──────────────────────────────────────────────┘
```

**Why store records as RESP arrays?** The append path becomes: write the same bytes you'd send over the wire. Replay becomes: feed bytes through `resp.Reader` and dispatch each command. One codec, two consumers.

### 4.2 Writer

```go
type FsyncPolicy int
const (
    FsyncAlways  FsyncPolicy = iota // every write
    FsyncEvery                        // every 1s via ticker
    FsyncNever                        // kernel decides
)

type Writer struct {
    f      *os.File
    bw     *bufio.Writer
    policy FsyncPolicy
    mu     sync.Mutex // serialises Append + fsync
}

func Open(dir string, policy FsyncPolicy) (*Writer, error)
func (w *Writer) Append(cmd [][]byte) error  // RESP-encodes argv, writes, fsyncs per policy
func (w *Writer) Close() error
```

Open also creates the header if the file is fresh, and validates magic+version if not.

### 4.3 Replayer

```go
type Replayer struct {
    r       *resp.Reader
    apply   func(cmd [][]byte) error // dispatcher injects this
}

func Replay(path string, apply func(cmd [][]byte) error) (ReplayStats, error)

type ReplayStats struct {
    Records  int
    Bytes    int64
    Duration time.Duration
}
```

Replay errors include the byte offset of the failing record. Partial replay (corrupted tail from a crash) is **not** silently truncated in v1 — the operator must opt in via `-aof-truncate` (out of scope for v1; documented as future work).

### 4.4 Rewriter

```go
type Rewriter struct {
    w         *Writer
    snapshot  func() []Command
    dir       string
}

type Command struct {
    Argv [][]byte
}

func (r *Rewriter) Rewrite(ctx context.Context) error
```

Steps:
1. `snapshot()` returns a list of RESP `SET k v` (with `PEXPIREAT` where applicable) reconstructing live state under a brief write lock.
2. Open `dir/toykv.aof.tmp`, write header, write all snapshot commands, `fsync`.
3. While step 2 ran, live appends went to a **side buffer** the writer maintains. Append the side buffer to `.tmp`, then `fsync`.
4. `rename(tmp, canonical)` (atomic on POSIX same-fs).
5. `fsync` the directory.
6. The live `Writer.f` is swapped to the new file; the side buffer is cleared.

Failure at any point leaves `.aof` intact and `.tmp` is removed on next startup.

## 5. Server (`internal/server`)

### 5.1 Lifecycle

```go
type Config struct {
    Addr        string         // :6390
    Dir         string         // /var/lib/toykv
    FsyncPolicy aof.FsyncPolicy
    LogLevel    slog.Level
    RequirePass string         // M12: "" ⇒ no authentication
    TLS         *tls.Config    // M12: nil ⇒ plaintext listener
}

type Server struct {
    cfg      Config
    store    *store.Store
    aof      *aof.Writer
    sweeper  *store.Sweeper
    rewriter *aof.Rewriter
    log      *slog.Logger
}

func New(cfg Config) (*Server, error)
func (s *Server) Run(ctx context.Context) error
```

`New` opens the AOF, replays it, constructs the store; it does **not** open the listener. `Run` opens the listener, starts goroutines, blocks until `ctx` cancels.

### 5.2 Connection handler

```go
func (s *Server) handleConn(ctx context.Context, c net.Conn) {
    defer c.Close()
    r := resp.NewReader(c)
    w := resp.NewWriter(c)

    for {
        argv, err := r.ReadCommand()
        if err != nil { ... }

        reply := s.dispatch(argv)
        if err := w.WriteFrame(reply); err != nil { return }
        if err := w.Flush(); err != nil { return }
    }
}
```

Idle conns are not closed in v1. (No `CLIENT TIMEOUT`.) Documented as a known limit.

### 5.3 Command dispatch

```go
type handler func(s *Server, argv [][]byte) resp.Value

var commands = map[string]handler{
    "PING":         cmdPing,
    "ECHO":         cmdEcho,
    "GET":          cmdGet,
    "SET":          cmdSet,           // SET k v [NX|XX] [EX s | PX ms | EXAT s | PXAT ms]
    "DEL":          cmdDel,
    "EXISTS":       cmdExists,
    "EXPIRE":       cmdExpire,
    "PEXPIRE":      cmdPExpire,
    "PEXPIREAT":    cmdPExpireAt,     // canonical TTL form for AOF v2 replay; exposed on wire too
    "TTL":          cmdTTL,
    "PTTL":         cmdPTTL,
    "PERSIST":      cmdPersist,
    "INCR":         cmdIncr,
    "DECR":         cmdDecr,
    "KEYS":         cmdKeys,
    "FLUSHDB":      cmdFlushDB,
    "DBSIZE":       cmdDBSize,
    "BGREWRITEAOF": cmdBGRewriteAOF,
}

func (s *Server) dispatch(argv [][]byte) resp.Value {
    if len(argv) == 0 {
        return resp.Error("ERR empty command")
    }
    name := strings.ToUpper(string(argv[0]))
    h, ok := commands[name]
    if !ok {
        return resp.Error(fmt.Sprintf("ERR unknown command '%s'", argv[0]))
    }
    return h(s, argv)
}
```

Each handler:
1. Validates arity → returns `-ERR wrong number of arguments` if off.
2. Mutates store.
3. Appends to AOF (if mutating).
4. Returns reply.

The "mutate then AOF" order is the durability contract: the operator gets `+OK` only after `fsync` returns under `FsyncAlways`. Implementation-wise: handler returns, conn loop calls `WriteFrame` only after `aof.Append` returns nil.

### 5.4 Errors → RESP mapping table

| Source | Returned | Reply |
|---|---|---|
| Unknown command | dispatcher | `-ERR unknown command 'XYZ'` |
| Wrong arity | handler | `-ERR wrong number of arguments for 'CMD'` |
| `INCR` non-int | store | `-ERR value is not an integer or out of range` |
| `INCR` overflow | store | `-ERR increment or decrement would overflow` |
| `EXPIRE` missing key | store | `:0` |
| `SET NX` already-exists | store | `$-1` (nil) |
| AOF write error | conn loop | conn dropped + log + process exit |
| Gated command, unauthenticated (M12) | dispatcher | `-NOAUTH Authentication required.` |
| Wrong password / non-`default` user (M12) | `authenticate` | `-WRONGPASS invalid username-password pair or user is disabled.` |
| `AUTH` with no `requirepass` (M12) | `authenticate` | `-ERR Client sent AUTH, but no password is set. Did you mean AUTH <username> <password>?` |

### 5.5 AUTH + TLS (M12)

Per-connection auth state lives in `connState` (the M10 protocol-state
struct), touched only by its connection's goroutine — no locking:

```go
type connState struct {
    proto         resp.Proto // RESP2 until HELLO 3
    id            uint64     // monotonic, echoed by HELLO
    authenticated bool       // M12: pre-set true when RequirePass == ""
}
```

A server with no `requirepass` marks every connection authenticated at
accept, so dispatch gating stays a single condition, placed *before* the
command-table lookup (an unauthenticated client learns nothing about the
command table; unknown commands also get `-NOAUTH`):

```go
if !cs.authenticated && name != "AUTH" && name != "HELLO" && name != "PING" {
    return resp.Error("NOAUTH Authentication required.")
}
```

Unauthenticated `PING` is a deliberate deviation from Redis (which gates
it), mandated by ROADMAP §M12 — the e2e harness readiness probe and
load-balancer-style health checks depend on it (ADR-0013).

`AUTH [username] password` and `HELLO … AUTH u p` share one
`authenticate` helper: `crypto/subtle.ConstantTimeCompare` on the
password, username must be `default`, and both failure modes return the
same `-WRONGPASS` so a client cannot probe which part failed. In
`cmdHello`, authentication commits *before* the protocol switch; a failed
AUTH leaves both auth state and proto untouched (validate-before-mutate,
ADR-0011). AOF replay uses a pre-authenticated throwaway `connState` so
gating never rejects a replayed record.

TLS wraps the raw listener in `Run` — everything downstream (accept
loop, EMFILE backoff, ctx-cancel drain) is listener-shape agnostic:

```go
l, err := net.Listen("tcp", s.cfg.Addr)
if s.cfg.TLS != nil { l = tls.NewListener(l, s.cfg.TLS) }
```

`main` builds the `*tls.Config` (`MinVersion: TLS12`) from the
`-tls-cert`/`-tls-key` pair and exits 2 if only one is given. Config
carries the `*tls.Config` rather than file paths so tests inject
in-memory self-signed certs.

### 5.6 Protected mode + atomic keyspace ops (M15)

**Protected mode** is the safe-by-default startup refusal that earns the
`2.0.0` major. `checkProtectedMode` runs inside `server.New` — before AOF
replay and before the listener opens — so an unsafe bind never touches disk:

```go
if err := checkProtectedMode(cfg.Addr, cfg.RequirePass, cfg.TLS != nil, cfg.ProtectedMode); err != nil {
    return nil, err // main logs + exits non-zero
}
```

The refusal fires only when protected mode is enabled **and** the bind is
non-loopback **and** neither `requirepass` nor TLS is set. `bindIsLoopback`
is fail-safe: an empty/unspecified host (`:6390`, `0.0.0.0`, `::`) is
non-loopback (the all-interfaces case), `localhost` and any loopback IP are
loopback, and a hostname is loopback only when *every* resolved address is
(an unresolvable host is treated as non-loopback). The `-protected-mode`
flag (`yes|no`) is validated in `main` (bad value → exit 2); the unsafe-bind
refusal is a distinct error class from `New` (→ exit 1). Config defaults the
empty value to enabled so an embedder is protected by default. This
deliberately deviates from Redis, which accepts the connection and refuses
non-loopback *commands* — toykv surfaces the unsafe posture at boot (ADR-0016).

**Atomic `RENAME` / `RENAMENX` / `COPY`** are single store-mutex-guarded
moves in `internal/store/keyspace.go`, replacing the racy client-side
`GET`+`SET`+`DEL`. Value payload, TTL (`expireAt`), and value type travel
with the key inside the moved `entry`; the destination gets a **fresh
creation seq** (`nextSeq`) because it is a newly-appearing key for SCAN — a
moved key never hides behind a cursor that already passed a stale low seq
(consistent with §SCAN's guarantee, ADR-0014). `RENAME` moves the pointer
(source is deleted); `COPY` deep-copies the deque/map so later source
mutation cannot leak into the copy. The handlers record the verbatim command
via `appendIfLive` on a real mutation only (no-op self-rename and `:0`
RENAMENX/COPY skip the append) — **no AOF format bump**: these ride the v3
record shape and replay deterministically. `COPY` accepts `DB 0` (single-DB;
every real client, incl. go-redis, sends it) and rejects other indices as
out of range (ADR-0016).

### 5.7 Observability (M16)

`internal/telemetry` owns the OpenTelemetry surface: `telemetry.Init` builds
`TracerProvider`/`MeterProvider`/`LoggerProvider` from the SDK and wires the
OTLP exporter (gRPC | HTTP) when `-otel-endpoint` is set; with no endpoint it
installs the SDK **no-op** providers as the globals, so instrument handles and
span-openers are created **unconditionally** — there is no `if enabled` guard
on the hot path (ADR-0017). `observeCommand` is the RED chokepoint wrapping
each `dispatch` with a command span, the call/error counters, the latency
histogram, and the in-flight gauge; `connState.ctx` carries the connection
span so store/AOF spans nest under the command.

Because building an `attribute.Set` or a `metric` option allocates even
against no-op providers, the per-command instrument attributes are **memoized
once at construction** in a `cmdInstr` cache keyed by the bounded command label
(dispatch verbs + `UNKNOWN`): `attribute.Set`-backed `MeasurementOption`s plus
a spread-at-`Start` `[]SpanStartOption` for the static span attributes. This
was added at M17 after the release-gate A/B measured a ~20% disabled-path
regression; it restores parity without a guard, and
`TestObserveCommand_Disabled_AllocBudget` (≤16 allocs) pins it. The telemetry
handle threads into `store` (context-first methods) and the AOF writer for
`store.<op>` and `aof.append`/`aof.fsync` spans; export failures are logged
and dropped — telemetry never fails a command (ADR-0017).

### 5.8 RESP frame bounds (M17 hardening)

The codec caps not just bulk-string size (`MaxBulkSize`, 64 MiB) but also
array element count (`MaxArrayLen` = 1 048 576) and array nesting depth
(`MaxDepth` = 32). `readArray` rejects an over-count with `ErrTooLarge`
**before** the `make([]Value, n)` allocation, and `readFrame` threads a depth
counter so nesting past `MaxDepth` is rejected before it recurses further —
closing a pre-auth memory-amplification OOM and a stack-exhaustion panic
reachable before the dispatch gate (SECURITY-REVIEW-v2).

## 6. CLI (`internal/cli` + `cmd/toykv-cli`)

### 6.1 Mode dispatch

```go
package main // cmd/toykv-cli

func main() {
    addr := flag.String("addr", "127.0.0.1:6390", "server address")
    raw  := flag.Bool("raw", false, "raw output (no pretty-print)")
    flag.Parse()

    c, err := client.Dial(*addr)
    if err != nil { exit(2, err) }
    defer c.Close()

    fmt := cli.NewFormatter(os.Stdout, os.Stderr, *raw)

    switch {
    case flag.NArg() > 0:
        os.Exit(cli.OneShot(c, fmt, flag.Args()))
    case isatty.IsTerminal(os.Stdin.Fd()):
        os.Exit(cli.REPL(c, fmt, os.Stdin, *addr))
    default:
        os.Exit(cli.Piped(c, fmt, os.Stdin))
    }
}
```

Note: `isatty` check uses stdlib only — `(os.Stdin.Stat().Mode() & os.ModeCharDevice) != 0`. No third-party isatty lib.

### 6.2 Engine types

```go
package cli

type Doer interface {
    Do(argv ...string) (resp.Value, error)
}

type Formatter struct {
    out io.Writer
    err io.Writer
    raw bool
}

func NewFormatter(out, err io.Writer, raw bool) *Formatter
func (f *Formatter) Print(v resp.Value)
func (f *Formatter) PrintError(err error) // exit-status hint

// Returns process exit code.
func OneShot(c Doer, f *Formatter, argv []string) int
func Piped(c Doer, f *Formatter, in io.Reader) int
func REPL(c Doer, f *Formatter, in io.Reader, prompt string) int
```

`Doer` interface lets tests inject a fake `client` without dialling.

### 6.3 Pretty-printer

```go
func (f *Formatter) Print(v resp.Value) {
    switch v.Kind {
    case resp.KindSimpleString:
        fmt.Fprintln(f.out, v.Str)              // both modes
    case resp.KindError:
        if f.raw { fmt.Fprintln(f.err, v.Str) }
        else      { fmt.Fprintf(f.err, "(error) %s\n", v.Str) }
    case resp.KindInteger:
        if f.raw { fmt.Fprintln(f.out, v.Int) }
        else      { fmt.Fprintf(f.out, "(integer) %d\n", v.Int) }
    case resp.KindBulkString:
        if v.IsNull {
            if !f.raw { fmt.Fprintln(f.out, "(nil)") }
            return
        }
        if f.raw { f.out.Write(append(v.Bytes, '\n')) }
        else      { fmt.Fprintf(f.out, "%q\n", v.Bytes) }
    case resp.KindArray:
        for i, item := range v.Array {
            if !f.raw { fmt.Fprintf(f.out, "%d) ", i+1) }
            f.Print(item)
        }
    }
}
```

### 6.4 Argv tokenisation (REPL/piped)

Lines are tokenised by:
- Whitespace split.
- Quoted strings (`"..."` or `'...'`) preserve spaces.
- Backslash escapes inside double quotes only: `\n`, `\t`, `\\`, `\"`, `\xNN`.

Implementation: handwritten `tokenise(line string) ([]string, error)` in `internal/cli/lex.go`. ~50 lines, table-tested. (No `csv`/`shellwords` dep.)

### 6.5 Exit-status mapping

```go
func exitCode(v resp.Value, err error) int {
    switch {
    case err != nil:                return 2
    case v.Kind == resp.KindError:  return 1
    default:                        return 0
    }
}
```

In `OneShot`: single command → exit with the resulting code.
In `Piped`: returns the **last** command's code (matches `redis-cli` behaviour).
In `REPL`: always returns 0 on clean exit (Ctrl-D, `quit`).

### 6.6 REPL specifics

- Prompt: `toykv:<addr>> ` (host:port from `-addr`).
- Built-ins: `quit`, `exit`, `help` (handled in-process, never sent over wire).
- History: in-memory ring buffer (last 100 commands), navigable with **up/down arrows** via raw terminal mode.
- Stdlib only: use `golang.org/x/term` (allowed dep — published by the Go team, semver-stable, no transitive deps). If even that is out of bounds, fall back to line-only input without arrow keys; documented limitation.

> **Open call:** allow `golang.org/x/term` for arrow-key history, or stay strictly stdlib and accept no-history REPL? Defaulting to **allow** since it's a Go-team module. Revisit if reviewer disagrees.

### 6.7 Shared client (`internal/client`)

```go
package client

type Client struct {
    addr string
    conn net.Conn
    r    *resp.Reader
    w    *resp.Writer
    mu   sync.Mutex
}

func Dial(addr string) (*Client, error)
func (c *Client) Do(argv ...string) (resp.Value, error)
func (c *Client) DoBytes(argv [][]byte) (resp.Value, error) // for binary-safe values
func (c *Client) Close() error
```

Single connection, single in-flight call (mutex-serialised). Pipelining is out of scope for v1.

Used identically by `internal/cli` and `internal/tui`.

## 7. TUI (`internal/tui` + `cmd/toykv-tui`)

### 7.1 Bubble Tea Model

```go
type Model struct {
    client   *client.Client
    addr     string
    refresh  time.Duration

    keys     []KeyInfo       // populated by refresh
    cursor   int             // selected index
    filter   string          // current glob filter; "" means *
    focused  *KeyDetail      // value + ttl of cursor key

    mode     Mode            // Normal, Filter, Edit, NewKey, NewTTL, RawCmd
    input    textinput.Model
    confirm  *ConfirmPrompt  // populated when awaiting Y/N (e.g., FLUSHDB)

    status   StatusLine
    err      error           // last command error
}

type KeyInfo struct {
    Name   string
    Type   string        // always "string" in v1
    TTL    time.Duration // -1 noexp, -2 missing
    Size   int           // bytes
}

type KeyDetail struct {
    Name string
    Val  []byte
    TTL  time.Duration
}

type Mode int
const (
    ModeNormal Mode = iota
    ModeFilter
    ModeEdit
    ModeNewKey
    ModeNewTTL
    ModeRawCmd
    ModeConfirm
)
```

### 7.2 Messages

```go
type (
    tickMsg     time.Time
    refreshMsg  struct{ keys []KeyInfo; focused *KeyDetail }
    replyMsg    struct{ value resp.Value; latency time.Duration }
    errMsg      struct{ err error }
    submitMsg   struct{ kind Mode; text string }
)
```

### 7.3 Update loop

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tickMsg:
        return m, m.fetchRefresh()
    case refreshMsg:
        m.keys = msg.keys
        m.focused = msg.focused
        return m, tea.Tick(m.refresh, toTickMsg)
    case replyMsg:
        m.status.LastLatency = msg.latency
        return m, m.fetchRefresh()
    case errMsg:
        m.err = msg.err
        return m, nil
    case tea.KeyMsg:
        return m.handleKey(msg)
    }
    return m, nil
}
```

`handleKey` is mode-aware. In `ModeNormal`:

| Key | Action |
|---|---|
| `j`/`↓` | `cursor++` (clamp) |
| `k`/`↑` | `cursor--` (clamp) |
| `enter` | request `GET` for cursor key, populate `focused` |
| `/` | enter `ModeFilter`, focus input |
| `e` | enter `ModeEdit` seeded with current value |
| `d` | run `DEL <k>`, refresh |
| `t` | enter `ModeNewTTL` seeded with current TTL |
| `n` | enter `ModeNewKey` |
| `i` | run `INCR <k>` |
| `D` | run `DECR <k>` |
| `F` | enter `ModeConfirm` for `FLUSHDB` |
| `r` | force refresh now |
| `:` | enter `ModeRawCmd` |
| `q` | quit |

### 7.4 View

Two-pane Lipgloss layout:

```
┌─────────────────────────────────────────────────────────────┐
│ KEYS (filter: *)            │ user:42                      │
│ ──────────────────────────  │ ──────────────────────────── │
│ ▸ user:42      str 12B  -1 │ {"name":"alice",             │
│   session:abc  str  4B 32s │  "score":1337}               │
│   counter      str  3B  -1 │                              │
│   …                        │                              │
│                             │                              │
│ ──────────────────────────  │ ──────────────────────────── │
│ DBSIZE: 3                                                   │
│ fsync: always   latency: 0.4ms   addr: 127.0.0.1:6390      │
└─────────────────────────────────────────────────────────────┘
[normal] j/k nav  /filter  enter inspect  e edit  d del  t ttl  n new  : raw  q quit
```

### 7.5 Client wiring

The TUI uses the shared `internal/client.Client` (defined in §6.7). The Bubble Tea tick fires a goroutine that calls `client.Do(...)` and emits messages back to the program via `tea.Cmd`. No TUI-private client type.

### 7.6 M14 — TUI v2 (paging, types, auth, INFO status)

The v1 sketch above (§7.1–7.4) predates the v2 surface. M14 lands the following deltas on the same Model/Update/View split and the same shared `internal/client` — no new client type, still RESP2.

- **SCAN paging (replaces `KEYS *`).** The keys pane shows one `SCAN cursor MATCH pattern COUNT n` page at a time. `Model` carries `pageCursor` (cursor that produced the page), `nextCursor` (0 ⇒ last page), a `cursorStack []uint64` for back-nav, and `pageCount` (COUNT hint, default 50). `]`/`[` (or PgDn/PgUp) step forward/back; forward pushes `pageCursor` onto the stack and moves to `nextCursor`, back pops. A match-pattern change or `FLUSHDB` calls `resetPaging()` (cursor 0, empty stack) — prior cursors are meaningless after either. Page-back via the stack is a UX convenience, **not** a consistency guarantee; SCAN's own full-scan guarantee (ADR-0014) is what makes a start-to-finish walk complete.
- **Server-side filter.** `/` now sets the `SCAN MATCH` pattern (`Model.filter`) instead of filtering a local slice; `visibleKeys()` returns the page as-is and the pattern is reused only to highlight the matched span in each name (`globLiterals`, retained). The old client-side `globMatch` (`glob.go`) is removed.
- **Typed values.** Per-key `TYPE` populates `KeyInfo.Kind` (`string|list|hash|none`). The focused value is fetched by kind: `GET` (string), `LRANGE k 0 -1` (list), `HGETALL` (hash). The value pane renders a string as a quoted scalar, a list as numbered rows (shared `respfmt`), and a hash as aligned `field: value` rows (RESP2 `HGETALL` is a flat array, read two-at-a-time). `KeyInfo.Size` is bytes for a string, element/field count for a list/hash. Cost note: one `TYPE`+`TTL` per key per page — bounded by `COUNT`, not the whole keyspace; the single-conn client does not pipeline.
- **INFO-driven status bar.** Each sweep issues `INFO`; `parseInfo` pulls `appendfsync`, `uptime_in_seconds`, `connected_clients`, and `db0:keys=` (dbsize) out of the verbatim body. `-fsync` becomes an override (`StatusLine.FsyncOverride`) that wins over the live value, rather than the sole source.
- **AUTH.** `ModeAuth` is a masked (`EchoPassword`) prompt entered on any `-NOAUTH` reply (refresh or mutation). Submitting sends `AUTH <pass>`; the refresh tick pauses while the prompt is up so it cannot clobber a half-typed password. `-a` on the CLI authenticates non-interactively at launch. TLS dialing is **deferred** (v2.x backlog) — M14 connects over plain TCP + AUTH only.

Owned risk test: `teatest` smokes for each type view, a multi-page paging scenario, and the `-NOAUTH` → prompt → authenticated flow (`cmd/toykv-tui/smoke_test.go`).

## 8. Concurrency invariants

1. **One writer to AOF.** All `Writer.Append` calls go through `Writer.mu`.
2. **Store lock never crosses I/O.** Handlers release the store lock *before* calling `aof.Append`. This prevents an `fsync` from blocking other reads. (Trade-off: a crash between `store.Apply` and `aof.Append` means in-memory state is ahead of disk. Since the durability contract is "ack after fsync", the client never received `+OK`, so no consistency promise is broken.)
3. **TTL sweeper holds the write lock only during eviction**, not during sampling.
4. **Rewrite never blocks online writes** except during the final swap (a sub-millisecond window).
5. **TUI client mu** serialises commands; the Bubble Tea program owns the tick goroutine.

## 9. Test harness layout

```
internal/resp/    *_test.go    unit (table-driven)
internal/store/   *_test.go    unit + race
internal/aof/     *_test.go    unit + crash-injection
internal/server/  *_test.go    integration (go-redis driving real server)
internal/tui/     *_test.go    teatest snapshots
test/e2e/         *_test.go    end-to-end (subprocess server + go-redis)
```

Crash injection: `aof_crash_test.go` uses `os.Process.Kill` mid-write, then re-opens and replays.

## 10. Build & tooling

- `Makefile` targets:
  - `build` → builds `bin/toykv`, `bin/toykv-cli`, `bin/toykv-tui` from `./cmd/...`
  - `test` → `go test -race -count=1 ./...`
  - `bench` → `redis-benchmark -p 6390 -t set,get -n 100000`
  - `lint` → `golangci-lint run`
  - `run` → `./bin/toykv -addr :6390 -dir ./data`
  - `cli` → `./bin/toykv-cli -addr :6390`
  - `tui` → `./bin/toykv-tui -addr :6390`
- `.golangci.yml`: `errcheck`, `govet`, `staticcheck`, `revive`, `gofmt`, `goimports`.
- CI: GitHub Actions runs `make lint test` on push.

## 11. Error taxonomy (Go-side, distinct from RESP wire errors)

```go
package store
var (
    ErrKeyNotFound  = errors.New("store: key not found")
    ErrWrongType    = errors.New("store: wrong type")
    ErrNotInteger   = errors.New("store: not an integer")
    ErrOverflow     = errors.New("store: integer overflow")
)

package aof
var (
    ErrBadHeader   = errors.New("aof: bad header")
    ErrBadVersion  = errors.New("aof: unsupported version")
    ErrShortRecord = errors.New("aof: short record")
)
```

These are wrapped with `fmt.Errorf("%w: ...")` at boundaries; conn handlers map them to RESP errors per §5.4.

## 12. Out-of-scope details (post-v1)

- Multi-DB (`SELECT`).
- ~~`SCAN`/`HSCAN` cursors~~ — `SCAN` shipped in M13; the TUI pages on it (M14, §7.6). `HSCAN` remains out of scope.
- `CLIENT` family commands.
- ~~`INFO` reply~~ — shipped in M13; drives the TUI status bar (M14, §7.6).
- Streaming replies (`XADD`/`XREAD`).
- ~~TLS transport~~ — server-side TLS shipped in M12; TUI-client TLS dialing is deferred (v2.x backlog).
- ~~ACL/auth~~ — `requirepass`/`AUTH` shipped in M12; TUI prompts/authenticates (M14, §7.6). ACLs remain out of scope.

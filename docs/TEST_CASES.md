# toykv — Local test cases & usage walkthrough

A hands-on guide to exercising **everything toykv does** locally: the server, the
`toykv-cli`, and the `toykv-tui`, plus the automated suites and the feature-by-feature
manual scenarios (types, TTL, AUTH/TLS, protected mode, AOF durability, RESP3,
observability). Every command below is real — arities and replies trace to the dispatch
table (`internal/server/dispatch.go`) and the command reference in [README](../README.md).

> **Convention.** Examples use the built binaries under `bin/` and bind the **loopback**
> `127.0.0.1:6390`. v2 protected mode refuses a non-loopback bind (e.g. the bare `:6390`
> default) without auth or TLS — that refusal is itself a test case (§9.3).

---

## 0. Prerequisites & build

```bash
# Go 1.26 (1.25 also builds). From the repo root:
make build                 # → bin/toykv, bin/toykv-cli, bin/toykv-tui
./bin/toykv --help         # server flags
./bin/toykv-cli --help     # CLI flags
./bin/toykv-tui --help     # TUI flags
```

Optional external tooling (only for the compat and bench scenarios):
- `redis-cli` / `valkey-cli` — byte-compat sweep (§5). Not required: `make compat` runs the
  sweep from Docker with no local install (§5).
- Docker — the redis-cli compat sweep (§5, `make compat`), the LGTM observability stack (§10),
  and `valkey-benchmark` (§11).

---

## 1. Running the server

### 1.1 Flags (all of them)

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `:6390` | Listen address. Use `127.0.0.1:6390` locally (protected mode — §9.3). |
| `-dir` | `./data` | AOF data directory. `""` disables persistence (in-memory only). |
| `-appendfsync` | `always` | fsync policy: `always` \| `everysec` \| `no`. |
| `-log-level` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `-requirepass` | `""` | Password clients must `AUTH` with; `""` disables auth. |
| `-tls-cert` | `""` | TLS certificate (PEM); requires `-tls-key`. |
| `-tls-key` | `""` | TLS private key (PEM); requires `-tls-cert`. |
| `-protected-mode` | `yes` | Refuse a non-loopback bind without auth/TLS: `yes` \| `no`. |
| `-otel-endpoint` | `""` | OTLP collector `host:port`; `""` disables telemetry. |
| `-otel-protocol` | `grpc` | OTLP transport: `grpc` \| `http`. |
| `-otel-service-name` | `toykv` | `service.name` reported to telemetry. |
| `-otel-sampling` | `0.05` | Trace sampling ratio `[0,1]`; errors always sampled. |
| `-otel-capture-keys` | `false` | Record a salted-hash of the key on store spans. |

### 1.2 Common launch recipes

```bash
# In-memory only (no AOF), loopback:
./bin/toykv -addr 127.0.0.1:6390 -dir ""

# Persistent, durable (fsync every write) — the default policy:
./bin/toykv -addr 127.0.0.1:6390 -dir ./data -appendfsync always

# Persistent, throughput-biased:
./bin/toykv -addr 127.0.0.1:6390 -dir ./data -appendfsync everysec

# With authentication:
./bin/toykv -addr 127.0.0.1:6390 -requirepass s3cret

# Via the Makefile (builds first; binds :6390 — pass PROTECTED off or use loopback):
make run ADDR=127.0.0.1:6390 DIR=./data
```

**Expected:** the server logs a structured startup line and blocks on `Accept` only after AOF
replay completes. `Ctrl-C` (SIGINT/SIGTERM) drains in-flight connections, flushes+fsyncs the
AOF, and exits `0`.

---

## 2. Automated test suites

Run these first — they prove the whole system without any manual steps.

```bash
make ci            # fmt-check + vet + golangci-lint + go test -race ./...  (the gate)
make test          # go test -race -timeout 5m ./...
go test -race ./... # same, directly

# Targeted:
go test -race ./internal/store        # store + concurrency
go test -race ./internal/resp         # RESP2/RESP3 codec (incl. DoS bounds)
go test -race ./internal/aof          # AOF format + v1→v2→v3 upgrade + replay
go test -race ./internal/server       # dispatch, auth, protected mode, spans
go test -race ./test/e2e              # subprocess: go-redis + toykv-cli + redis-cli sweep
make chaos                            # full crash-injection soak (test/chaos, 10m)
make chaos-smoke                      # short crash soak (-short, 2m)
```

**Crash / durability specifics** (subprocess tests; run without `-short`):

```bash
go test -race -run TestAOF_CrashInjection_DuringRewrite ./internal/server   # SIGKILL mid-rewrite
go test -race -run TestOpen_Upgrade ./internal/aof                          # v1/v2 files replay under v3
go test -run x -bench BenchmarkObserveCommand_Disabled -benchmem ./internal/server  # OTel-off no-op cost
```

**Expected:** every package `ok`; lint `0 issues`; crash tests reconstruct every acked write.

---

## 3. `toykv-cli` — three modes

**Flags:** `-addr` (default `127.0.0.1:6390`), `-raw` (no pretty-print), `-timeout` (default `5s`).

```bash
# One-shot — a single command, one connection, then exit:
./bin/toykv-cli SET greeting "hello world"
./bin/toykv-cli GET greeting
./bin/toykv-cli -raw GET greeting          # raw output for scripts

# REPL — interactive, when stdin is a TTY:
./bin/toykv-cli
# toykv 127.0.0.1:6390> SET k 1
# toykv 127.0.0.1:6390> INCR k
# toykv 127.0.0.1:6390> quit        (or exit, or Ctrl-D)

# Piped — one command per line from stdin:
printf 'SET a 1\nSET b 2\nKEYS *\n' | ./bin/toykv-cli
echo 'GET greeting' | ./bin/toykv-cli
```

**Exit status** (one-shot): `0` OK · `1` `-ERR` reply · `2` connection/parse failure.

> **AUTH note.** The CLI has **no `-a` flag** — one-shot opens a fresh connection per
> invocation, so `AUTH` must share the connection with the commands it protects. Against a
> `-requirepass` server, use **piped** or **REPL**:
> ```bash
> printf 'AUTH s3cret\nSET k 1\nGET k\n' | ./bin/toykv-cli
> ```

### 3.1 Full command matrix (verify each round-trips)

Paste into a piped session against a fresh server (`printf '…' | ./bin/toykv-cli`), or run
line-by-line in the REPL. Expected replies noted inline as `toykv-cli` prints them — the
pretty-printer renders the RESP wire form (`+OK`, `:N`, `$-1`, `-ERR msg`) as `OK`,
`(integer) N`, `(nil)`, `(error) ERR msg`.

**Connection / meta**
```
PING                      # PONG
PING hello                # "hello"
ECHO hi                   # "hi"
HELLO                     # handshake map (RESP2: flat array)
HELLO 3                   # upgrade this connection to RESP3
INFO                      # # Section\nkey:value text
INFO server               # single-section filter
```

**Strings & counters**
```
SET k v                   # OK
SET k v NX                # (nil) if k exists, OK if not
SET k v XX                # OK only if k exists
GET k                     # "v"
GET missing               # (nil)
SET n 10                  # OK
INCR n                    # (integer) 11
DECR n                    # (integer) 10
EXISTS k n missing        # (integer) 2
DEL k n                   # (integer) 2
SET big 99999999999999999999999   # OK  (stored as string)
INCR big                  # (error) ERR value is not an integer or out of range
```

**Keyspace**
```
SET a 1
SET b 2
KEYS *                    # a, b  (glob patterns: KEYS 'a*')
DBSIZE                    # (integer) 2
SCAN 0                    # cursor + keys (see §6)
SCAN 0 MATCH 'a*' COUNT 100
RENAME a a2               # OK  (overwrites dest; TTL+type travel)
RENAMENX a2 b             # (integer) 0  (dest exists)  → :1 if it didn't
COPY a2 c                 # (integer) 1  (:0 if dest exists without REPLACE)
COPY a2 c REPLACE         # (integer) 1
TYPE a2                   # string
FLUSHDB                   # OK  (empties the keyspace)
```

**TTL**
```
SET s v
EXPIRE s 100              # (integer) 1
TTL s                     # (integer) ~100
PTTL s                    # (integer) ~100000
PERSIST s                 # (integer) 1  (removes the TTL)
SET t v
PEXPIRE t 500             # (integer) 1  (ms)
PEXPIREAT t 9999999999999 # (integer) 1  (absolute ms epoch)
SET quick v EX 1
# wait 2s:
GET quick                 # (nil)  — expired
```

**Lists**
```
RPUSH mylist a b c        # (integer) 3
LPUSH mylist z            # (integer) 4   → z a b c
LRANGE mylist 0 -1        # z, a, b, c
LINDEX mylist 0           # "z"
LINDEX mylist -1          # "c"
LLEN mylist               # (integer) 4
LPOP mylist               # "z"
RPOP mylist               # "c"
```

**Hashes**
```
HSET h f1 v1 f2 v2        # (integer) 2   (fields newly added)
HGET h f1                 # "v1"
HGET h missing            # (nil)
HEXISTS h f1              # (integer) 1
HKEYS h                   # f1, f2        (insertion order)
HVALS h                   # v1, v2        (HVALS[i] pairs with HKEYS[i])
HLEN h                    # (integer) 2
HGETALL h                 # RESP3: %map ; RESP2: flat array f1 v1 f2 v2 (insertion order)
HDEL h f1                 # (integer) 1
TYPE h                    # hash
```

**Type safety (WRONGTYPE)**
```
SET str hello
LPUSH str x               # (error) WRONGTYPE Operation against a key holding the wrong kind of value
HSET str f v              # (error) WRONGTYPE …
DEL str
RPUSH lst a
GET lst                   # (error) WRONGTYPE …
```

**Persistence**
```
BGREWRITEAOF              # OK (async compaction; see §8)
```

---

## 4. RESP2 vs RESP3

```bash
# RESP2 (default) — HGETALL is a flat array, nulls are $-1:
printf 'HSET h a 1 b 2\nHGETALL h\n' | ./bin/toykv-cli

# RESP3 — upgrade the connection first; HGETALL is a native map, misses are _.
# toykv-cli's own reader decodes the RESP3 frames, so this Just Works:
printf 'HELLO 3\nHSET h a 1 b 2\nHGETALL h\nGET missing\n' | ./bin/toykv-cli

# With redis-cli, force RESP3 for a whole session (an alternative client):
redis-cli -3 -p 6390 HGETALL h
```

**Expected:** a RESP2 client's replies are byte-identical to v1; a `HELLO 3` client gets the
richer frames (`%` map, `_` null, `=` verbatim `INFO`) and `toykv-cli`/`toykv-tui` decode
them without error. RESP2 and RESP3 clients coexist.

> **Regression note:** through v2.0.0 the `internal/resp` codec was asymmetric — the writer
> encoded every RESP3 kind but the reader (used by `internal/client`) decoded only RESP2, so
> `HELLO 3` under `toykv-cli` failed with `resp: unknown prefix '%'`. Fixed in
> `fix/resp3-reader-hash-order` by making the reader decode `% ~ , # _ = >`, locked by an
> encode→decode round-trip test. If you see `unknown prefix`, you're on an old build.

### 4.1 RESP3 kinds, decoded by the shipped client

```bash
# Each line exercises a distinct RESP3 frame the client must now decode:
printf 'HELLO 3\n'                                | ./bin/toykv-cli   # % map (handshake)
printf 'HELLO 3\nHSET h a 1 b 2 c 3\nHGETALL h\n' | ./bin/toykv-cli   # % map (fields+values)
printf 'HELLO 3\nGET missing\n'                   | ./bin/toykv-cli   # _ null
printf 'HELLO 3\nSET k v NX\nSET k v NX\n'        | ./bin/toykv-cli   # _ null on the 2nd (NX fails)
printf 'HELLO 3\nINFO server\n'                   | ./bin/toykv-cli   # = verbatim string
```

**Expected:** no `resp: unknown prefix …` error on any RESP3 reply; the CLI renders the map,
the null, and the verbatim `INFO` block. Cross-check with `redis-cli -3 -p 6390 <cmd>` — the
two clients must agree.

### 4.2 Hash field ordering (`HKEYS[i]` ↔ `HVALS[i]` ↔ `HGETALL`)

Redis guarantees that within an unchanged hash the *i*-th `HKEYS` field pairs with the *i*-th
`HVALS` value, and `HGETALL` lists the same field/value pairs in the same order. toykv now
matches this by tracking field **insertion order** (not Go map-iteration order).

```bash
# Insert in a deliberately non-alphabetical order:
printf 'DEL h\nHSET h zeta 1 alpha 2 mike 3 bravo 4\nHKEYS h\nHVALS h\nHGETALL h\n' \
  | ./bin/toykv-cli
# HKEYS  → zeta alpha mike bravo
# HVALS  → 1 2 3 4                (position-for-position with HKEYS)
# HGETALL→ zeta 1 alpha 2 mike 3 bravo 4

# Updating an existing field does NOT move it; a deleted+re-added field goes last:
printf 'HSET h alpha 22\nHKEYS h\n'          | ./bin/toykv-cli   # order unchanged
printf 'HDEL h mike\nHSET h mike 3\nHKEYS h\n'| ./bin/toykv-cli   # ...zeta alpha bravo mike
```

**Expected:** `HKEYS`/`HVALS`/`HGETALL` agree on order across repeated calls; overwriting a
field keeps its slot; deleting then re-adding appends the field at the end.

#### 4.2.1 Ordering survives persistence

```bash
# Insertion order must be rebuilt identically after an AOF-backed restart and a rewrite.
# Persistence is on whenever -dir is set (non-empty).
./bin/toykv -addr 127.0.0.1:6390 -dir /tmp/toykv-order &
printf 'HSET h zeta 1 alpha 2 mike 3 bravo 4\n' | ./bin/toykv-cli
./bin/toykv-cli BGREWRITEAOF
# ...stop the server, restart it on the same -dir, then:
./bin/toykv-cli HKEYS h            # still: zeta alpha mike bravo
```

**Expected:** after both a plain AOF replay and a `BGREWRITEAOF` snapshot reload, `HKEYS`
returns the original insertion order. (Automated by `internal/server` `TestHashFieldOrder_*`
and `test/e2e` `TestRESP3_HashMapAndOrder`.)

---

## 5. Redis-client compatibility

The byte-compat sweep needs a real `redis-cli`. If you don't have one installed, run it
straight from Docker — no local install, same Valkey image the benchmarks use (§11):

```bash
# One command: run the whole redis-cli byte-compat sweep (TestRedisCLI_ByteCompat)
# against a fresh subprocess server, sourcing redis-cli from Docker.
make compat            # verifies Docker, pulls valkey/valkey:8-alpine, runs the sweep
make compat-prep       # just verify Docker + pre-pull the image

# The Docker-backed shim behaves like a normal redis-cli — put scripts/ on PATH,
# or call it directly against a running server:
./scripts/redis-cli -p 6390 PING
./scripts/redis-cli -p 6390 SET k v
./scripts/redis-cli -3 -p 6390 HGETALL h          # force RESP3 for the session
TOYKV_COMPAT_IMAGE=redis:7-alpine ./scripts/redis-cli -p 6390 PING   # override image

# Or the raw one-liner (Linux host networking reaches the loopback server):
docker run --rm --network host valkey/valkey:8-alpine redis-cli -p 6390 PING
```

If you *do* have `redis-cli`/`valkey-cli` natively, the same commands work without Docker:

```bash
redis-cli -p 6390 PING
redis-cli -p 6390 -a s3cret GET k                 # against a -requirepass server
redis-cli --tls --cacert cert.pem -p 6390 PING    # against a TLS server (§9.2)

# go-redis/v9 is exercised end-to-end by the subprocess suite regardless:
go test -race ./test/e2e
```

**Expected:** every command in the matrix (§3.1) round-trips byte-compatibly; the e2e suite
drives the shipped binary via `go-redis/v9`, `toykv-cli`, and a `redis-cli` sweep. The sweep
auto-skips only when no `redis-cli` is on `PATH` — `make compat` supplies one via Docker, so it
runs everywhere Docker does.

---

## 6. SCAN paging

```bash
# Seed many keys, then walk the cursor:
for i in $(seq 1 50); do ./bin/toykv-cli SET "key:$i" "$i"; done
printf 'SCAN 0 COUNT 10\n' | ./bin/toykv-cli      # returns next-cursor + up to ~10 keys
# Feed the returned cursor back in until it returns 0:
printf 'SCAN <cursor> COUNT 10\n' | ./bin/toykv-cli
printf 'SCAN 0 MATCH "key:1*" COUNT 100\n' | ./bin/toykv-cli
```

**Expected:** a full loop (cursor `0` → … → `0`) enumerates every key present for the whole
scan; concurrent writes never crash a stale cursor. `SCAN` replaces `KEYS *` for large
keyspaces.

---

## 7. INFO introspection

```bash
./bin/toykv-cli INFO | less
./bin/toykv-cli INFO server
```

**Expected:** Redis-faithful `# Section\nkey:value` text (verbatim string on RESP3, bulk on
RESP2) reporting uptime, `dbsize`, `appendfsync` policy, AOF byte size, connected clients, and
replay stats. The TUI status bar and the e2e suite both read state via `INFO`.

---

## 8. AOF persistence, durability & compaction

### 8.1 Restart round-trip
```bash
./bin/toykv -addr 127.0.0.1:6390 -dir ./data -appendfsync always &
./bin/toykv-cli SET durable yes
./bin/toykv-cli RPUSH log a b c
./bin/toykv-cli HSET cfg mode fast
kill %1                      # stop the server
./bin/toykv -addr 127.0.0.1:6390 -dir ./data -appendfsync always &   # restart
./bin/toykv-cli GET durable    # "yes"
./bin/toykv-cli LRANGE log 0 -1  # a b c
./bin/toykv-cli HGETALL cfg      # mode fast
```
**Expected:** every acknowledged write (string/list/hash + TTLs) survives the restart via AOF
replay. Under `-appendfsync always` this holds even across a hard `kill -9` (proven by
`test/chaos` and the M3/M11 crash suites).

### 8.2 Compaction
```bash
for i in $(seq 1 1000); do ./bin/toykv-cli SET churn "$i"; done   # lots of overwrites
ls -l data/*.aof            # note the size
./bin/toykv-cli BGREWRITEAOF
ls -l data/*.aof            # smaller — one canonical record per live key
```
**Expected:** the AOF shrinks after churn; no data loss across rewrite + restart, and no
half-written file under the canonical name at any crash point.

---

## 9. Security: AUTH, TLS, protected mode

### 9.1 AUTH
```bash
./bin/toykv -addr 127.0.0.1:6390 -requirepass s3cret &
./bin/toykv-cli PING                       # PONG  (PING allowed pre-auth)
printf 'GET k\n' | ./bin/toykv-cli         # (error) NOAUTH Authentication required.
printf 'AUTH wrong\n' | ./bin/toykv-cli    # (error) WRONGPASS invalid username-password pair or user is disabled.
printf 'AUTH s3cret\nSET k 1\nGET k\n' | ./bin/toykv-cli   # OK / +OK / "1"
```
**Expected:** an unauthenticated connection may run only `AUTH`, `HELLO`, `PING`; everything
else returns `-NOAUTH`. Password compare is constant-time; wrong password → `-WRONGPASS`.

### 9.2 TLS
```bash
# Generate a throwaway self-signed cert/key:
openssl req -x509 -newkey rsa:2048 -nodes -keyout key.pem -out cert.pem \
  -days 1 -subj "/CN=localhost" -addext "subjectAltName=IP:127.0.0.1"
./bin/toykv -addr 127.0.0.1:6390 -tls-cert cert.pem -tls-key key.pem &
redis-cli --tls --cacert cert.pem -p 6390 PING     # PONG over TLS
./bin/toykv -addr 127.0.0.1:6390 -tls-cert cert.pem &   # only one of the pair → exits non-zero
```
**Expected:** min TLS 1.2; cert/key must be given as a pair or the server exits with a clear
error. Composes with `-requirepass`.

### 9.3 Protected mode (the earned-2.0.0 break)
```bash
# Non-loopback bind with no auth/TLS → refuses to START (exit non-zero):
./bin/toykv -addr 0.0.0.0:6390 ; echo "exit=$?"     # exit=1 + a message naming the fix

# Each of these starts cleanly:
./bin/toykv -addr 127.0.0.1:6390                        # loopback
./bin/toykv -addr 0.0.0.0:6390 -requirepass s3cret      # auth present
./bin/toykv -addr 0.0.0.0:6390 -tls-cert cert.pem -tls-key key.pem   # TLS present
./bin/toykv -addr 0.0.0.0:6390 -protected-mode no       # explicit override (logged)

# Bad flag value → exit 2:
./bin/toykv -addr 127.0.0.1:6390 -protected-mode maybe ; echo "exit=$?"   # exit=2
```
**Expected:** unsafe non-loopback bind refuses at boot (before the listener or AOF touch disk).
Override with `-protected-mode no`. Unsafe-bind refusal exits `1`; a bad flag value exits `2`.

### 9.4 Pre-auth codec bounds (M17 hardening)
The RESP decoder rejects an oversized array (`> 1,048,576` elements) or over-deep nesting
(`> 32`) with an error before any allocation — closing single-packet pre-auth DoS vectors.
Covered by `go test ./internal/resp -run 'Oversized|OverDeep'`; not hand-triggerable safely.

---

## 10. Observability (OpenTelemetry → LGTM)

```bash
# Bring up the local Grafana LGTM stack (Loki/Tempo/Mimir/Grafana):
docker compose -f deploy/otel-lgtm/compose.yaml up -d      # see deploy/otel-lgtm/README.md

# Point the server at the collector:
./bin/toykv -addr 127.0.0.1:6390 -dir ./data \
  -otel-endpoint 127.0.0.1:4317 -otel-protocol grpc -otel-service-name toykv &

# Generate traffic:
for i in $(seq 1 200); do ./bin/toykv-cli SET "k$i" "$i" >/dev/null; ./bin/toykv-cli GET "k$i" >/dev/null; done
```
**Expected:** in Grafana — RED metrics per command (Mimir), a `connection → command →
{store, aof}` trace tree (Tempo), and trace-correlated logs (Loki). With `-otel-endpoint`
**unset**, telemetry is a true no-op (no hot-path cost; behaviour matches the pre-M16 binary).
A dead collector never fails a command — export failures are logged and dropped.

---

## 11. Benchmarks

```bash
# Requires redis-benchmark (or valkey-benchmark). Server on loopback:
./bin/toykv -addr 127.0.0.1:6390 -dir ./data -appendfsync everysec &
make bench BENCH_HOST=127.0.0.1 BENCH_PORT=6390         # set,get,lpush,rpush,hset

# valkey-benchmark via Docker (no local install; matches the recorded methodology):
docker run --rm --network host valkey/valkey:8-alpine \
  valkey-benchmark -h 127.0.0.1 -p 6390 -t set,get,lpush,rpush,hset -n 100000 --csv
```
**Expected:** numbers in the band recorded in [BENCHMARKS.md](./BENCHMARKS.md). Remember the
loopback bind — a bare `:6390` is refused by protected mode.

---

## 12. `toykv-tui` — terminal UI

**Flags:** `-addr` (default `127.0.0.1:6390`), `-a <password>` (non-interactive AUTH),
`-refresh` (poll interval, default `2s`), `-timeout` (connect, default `5s`),
`-fsync` (status-bar label override), `-log <file>` (structured logs off by default).

```bash
# Seed some data first so the panes aren't empty:
printf 'SET user:1 alice\nSET user:2 bob\nRPUSH queue a b c\nHSET cfg mode fast tries 3\n' | ./bin/toykv-cli

# Launch:
./bin/toykv-tui -addr 127.0.0.1:6390
./bin/toykv-tui -addr 127.0.0.1:6390 -a s3cret      # against a -requirepass server
make tui ADDR=127.0.0.1:6390
```

### 12.1 Keybindings (every one)

| Key | Action |
|---|---|
| `?` | Toggle help overlay (`?` or `esc` closes it) |
| `q` / `Ctrl-C` | Quit |
| `j` / `↓` | Next key (in stacked layout with the value pane focused: scroll value down) |
| `k` / `↑` | Previous key (or scroll value up) |
| `g` | Jump to first key |
| `G` | Jump to last key |
| `tab` | Toggle focus left/right (stacked layout only) |
| `r` | Refresh (re-fetch keys + focused value) |
| `]` / `PgDn` | Next `SCAN` page |
| `[` / `PgUp` | Previous `SCAN` page |
| `/` | Filter keys by glob (`SCAN … MATCH`); pre-fills the current filter — `Ctrl-U` clears it |
| `n` | New key: `SET key value` prompt |
| `e` | Edit the focused key's value |
| `t` | Set a TTL on the focused key (`EXPIRE key seconds`) |
| `i` | `INCR` the focused key |
| `D` | `DECR` the focused key |
| `d` | `DEL` the focused key (confirm `y`/`N`) |
| `F` | `FLUSHDB` (confirm `y`/`N`) |
| `:` | Raw command prompt (run any command, e.g. `HSET h f v`) |
| `enter` | Submit a prompt · `esc` cancels a prompt/confirm |

### 12.2 TUI workflows to exercise

1. **Browse & inspect** — `j`/`k` to move the cursor; the value pane shows the focused key
   rendered by `TYPE`: a string, a list (indexed lines), or a hash (field/value pairs).
2. **Multi-type rendering** — focus `user:1` (string), `queue` (list), `cfg` (hash) and confirm
   each renders in its own view.
3. **Create** — `n`, type `color red`, `enter` → the key appears; the pane refreshes.
4. **Edit** — focus a key, `e`, type a new value, `enter`.
5. **Counters** — focus a numeric key, `i` (INCR) / `D` (DECR); watch the value update.
6. **TTL** — focus a key, `t`, type `30`, `enter`; the status/value reflects the expiry.
7. **Delete** — focus a key, `d`, confirm `y`; it disappears. `n`/`esc` cancels.
8. **Filter** — `/`, type `user:*`, `enter` → only matching keys listed. Clear with `/` + empty.
   Note: `/` **pre-fills the current filter** (so you can tweak it); press `Ctrl-U` to wipe the
   line and type a fresh glob from scratch.
9. **Paging** — seed 100+ keys (§6), then `]`/`[` to walk `SCAN` pages; the status bar shows
   the page position.
10. **Raw command** — `:`, type `HSET cfg retries 5`, `enter`; then focus `cfg` to see it.
11. **FLUSHDB** — `F`, confirm `y` → keyspace empties, paging resets to page 0.
12. **AUTH prompt** — launch against a `-requirepass` server without `-a`; the TUI prompts for
    the password on connect. Or pass `-a s3cret` to skip the prompt.
13. **Status bar** — confirm it shows fsync policy, `dbsize`, and uptime (read from `INFO`).
14. **Resize** — shrink the terminal; the layout collapses to a stacked single-column view and
    `tab` becomes meaningful (focus the value pane, then `j`/`k` scrolls it).

**Expected:** every mutating command from the reference is reachable from the TUI; all v1
keybindings still work; the TUI runs on the same `internal/client` package as the CLI.

---

## 13. Quick smoke checklist

A five-minute confidence pass:

```bash
make build && make ci                                   # builds + full suite green
./bin/toykv -addr 127.0.0.1:6390 -dir ./data &          # start
./bin/toykv-cli PING                                    # PONG
printf 'SET k v\nGET k\nRPUSH l a b\nLRANGE l 0 -1\nHSET h f 1\nHGETALL h\nTYPE l\nINFO server\n' \
  | ./bin/toykv-cli                                      # mixed round-trip
./bin/toykv-cli BGREWRITEAOF                             # compaction
kill %1 && ./bin/toykv -addr 127.0.0.1:6390 -dir ./data &  # restart
./bin/toykv-cli GET k                                   # "v" — survived
./bin/toykv-tui -addr 127.0.0.1:6390                    # eyeball the UI, then q
```

If all of the above behave as noted, the system is working end-to-end across the server, CLI,
TUI, persistence, types, and introspection.

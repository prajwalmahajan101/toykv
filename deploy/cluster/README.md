# Local 3-node toykv cluster

A one-command replicated toykv cluster (v3) for local testing — redirect, failover,
and `INFO replication` against real nodes over the ToyRaft HTTP transport.

## Bring it up

```sh
docker compose -f deploy/cluster/compose.yaml up --build
```

Three nodes (`n1`, `n2`, `n3`) come up on a compose bridge network, each with its client
port published to the host:

| node | host client port | raft addr (internal) |
|---|---|---|
| n1 | 6390 | n1:7001 |
| n2 | 6391 | n2:7001 |
| n3 | 6392 | n3:7001 |

The nodes elect a leader within a few seconds. See [ADR-0019](../../docs/adr/0019-cluster-mode-transport-and-raft-log-storage-layout.md).

## Smoke test

Find the leader and write to it:

```sh
for p in 6390 6391 6392; do echo -n "$p "; redis-cli -p $p INFO replication | grep role; done
redis-cli -p <leader-port> set k v      # -> +OK
redis-cli -p <leader-port> get k        # -> "v"
```

Writing to a **follower** returns a redirect:

```sh
redis-cli -p <follower-port> set k v    # -> (error) NOTLEADER n<leader>:6390
```

> The `NOTLEADER` hint is the leader's **docker-internal** address (e.g. `n1:6390`), which a
> host `redis-cli` cannot resolve. To exercise auto-redirect, run `toykv-cli` from inside the
> network (`docker compose exec n2 /toykv-cli ...` if you add the CLI to the image) or just
> target the current leader port directly. Redirects resolve correctly container-to-container.

## Failover

```sh
docker compose -f deploy/cluster/compose.yaml stop n1   # kill the leader
# re-run the role loop above: a surviving node reports role:master within a few seconds,
# and its INFO replication shows the remaining connected replica.
```

## Security note

Both `-protected-mode no` (client bind) and `-raft-insecure` (plaintext peer transport) are set
here **because the compose bridge is a trusted local network**. Do not copy these flags to a real
deployment without reading [SECURITY § Cluster / replication](../../docs/SECURITY.md#cluster--replication-v3).

## Benchmarks

`make bench-cluster BENCH_PORT=<leader-port>` measures replication cost vs standalone — see
[`docs/BENCHMARKS.md`](../../docs/BENCHMARKS.md#cluster-mode-v3-m23).

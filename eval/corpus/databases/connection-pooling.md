---
tags: [postgres, pgbouncer, performance]
---

# Connection pooling

Every Postgres backend is a forked process with its own memory and its own
snapshot. Ten thousand connections is not a database, it is a fork bomb with
WAL. The fix is a pooler: clients connect cheaply, the pooler hands them a
real backend for as long as they need one, and the backends stay in the tens.

## Sizing with Little's law

Concurrency = throughput x latency. At 2000 queries a second averaging 5ms of
hold time, the steady-state number of busy backends is 10. Everything above
that is queueing you are choosing to do inside the database. This is the
arithmetic behind the default advice of "2-4 x cores" for OLTP: it is a
ceiling on useful parallel work, and past it you are paying context switches
and lock contention for negative return. The plateau is visible in
`pg_stat_statements` exec time and in throughput graphs: add connections,
watch the line go flat, stop well before.

Each application Pod carrying its own 20-connection pool times fifty replicas
is the anti-pattern; an HPA event turns it into a thousand backends and the
database falls over from load that is nominally read traffic. This is why
[[postgres-on-kubernetes#The pieces that fight the platform]] puts a pooler in
front of everything.

## PgBouncer modes

- **session** — a client holds a backend for the life of its connection.
  Fixes the process count, changes nothing about semantics. A gateway
  for applications you cannot change.
- **transaction** — a backend is assigned per transaction. This is the mode
  that collapses a thousand client connections into twenty real ones, and the
  default answer. The price: nothing session-scoped survives between
  transactions. `SET`/`RESET` vanish at COMMIT, advisory locks held across
  statements break, prepared statements need the protocol-level support
  (PgBouncer 1.21+ tracks them per client; before that, PgJDBC silently
  fell back to full parses or errored on ` DEALLOCATE`).
- **statement** — one statement, one backend. Multi-statement transactions are
  refused. For stateless micro-queries only.

The knobs that matter in `pgbouncer.ini`:

```ini
pool_mode = transaction
max_client_conn = 2000
default_pool_size = 25
reserve_pool_size = 5
reserve_pool_timeout = 3
```

`max_client_conn` is cheap file descriptors on the pooler — set it high.
`default_pool_size` is per (database, user) pair, which is the detail people
miss: five databases through one pooler is five pools of 25, not one of 125.
`reserve_pool_size` is the burst valve, borrowed connections that trigger
`reserve_pool_timeout` seconds of waiting.

## What pooling breaks

- `LISTEN/NOTIFY` — no session, no notification stream. Route it around the
  pooler entirely.
- Advisory locks — session-level ones are meaningless in transaction mode.
- Temp tables and `SET` — gone at transaction end. Applications that "lose"
  their settings through PgBouncer are usually setting them per session.
- PgBouncer's own admin console: `psql -p 6432 pgbouncer` then `SHOW POOLS`,
  `SHOW CLIENTS`, `SHOW SERVERS` — the `cl_waiting` column is the queue
  building, which is the number to alert on.

## Client-side pools

Go applications should still pool locally — `pgxpool` with `MaxConns` small
(single digits per Pod), `MinConns` > 0 to avoid cold-start latency,
`MaxConnLifetime` around 30m to survive DNS changes and planned failovers,
`MaxConnIdleTime` to reap dead weight. The local pool's job is to avoid
connection setup per query; multiplexing the fleet's total is the server-side
pooler's job. Two layers with different purposes, both sized deliberately.

## Failure modes seen in production

- Pool exhaustion after a deploy: new code holds a transaction across an HTTP
  call. The `cl_waiting` queue grows, request latency becomes queue latency,
  and the database itself is idle. Fix the transaction scope, not the pool
  size.
- `server_lifetime` shorter than a slow analytic query, killing it mid-flight.
- One pooler Deployment for all services: a chatty batch job starves the API.
  Pool per service, or at least per database — isolation is the point.

Sizing the database under the pool is its own topic: memory per connection
math in [[postgres-tuning#Memory settings]], and the pooler is not a
substitute for finding the query that is actually slow.

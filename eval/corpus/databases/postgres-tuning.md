---
tags: [postgres, performance, vacuum]
---

# Postgres tuning and vacuum

## MVCC, and why dead rows exist

An UPDATE does not overwrite a row. It writes a new version and marks the old
one dead at the current transaction id. Readers with an older snapshot still see
the old version; that is the whole concurrency model, and the price is garbage.

VACUUM reclaims dead tuples into free space inside the same pages. It does not
return space to the operating system — only `VACUUM FULL` does, and it takes an
ACCESS EXCLUSIVE lock and rewrites the whole table, so it is a maintenance
window, not a routine. `pg_repack` does the same job online.

## Autovacuum

Triggers when dead tuples exceed
`autovacuum_vacuum_threshold + autovacuum_vacuum_scale_factor * reltuples`. The
default scale factor of 0.2 means a 100M row table waits for 20M dead rows,
which is far too long. Set it per table:

```sql
ALTER TABLE events SET (autovacuum_vacuum_scale_factor = 0.01,
                        autovacuum_vacuum_cost_delay = 2);
```

Signs it is losing: table size growing while row count is flat, index bloat,
`pg_stat_user_tables.n_dead_tup` climbing, and query plans drifting because
`last_autoanalyze` is old.

The three things that block cleanup, all of which hold back the xmin horizon:

1. A long-running transaction. `idle in transaction` for hours is the classic —
   set `idle_in_transaction_session_timeout`.
2. An abandoned replication slot. Nothing can be cleaned past what the slot has
   not confirmed; see [[postgres-replication]].
3. `hot_standby_feedback = on` with a slow reporting query on a standby.

Transaction id wraparound is the emergency version. Past
`autovacuum_freeze_max_age` an anti-wraparound vacuum starts and will not be
cancelled; ignore the warnings long enough and the server refuses writes. Watch
`age(datfrozenxid)`.

## Memory settings

- `shared_buffers` — 25% of RAM, more on a dedicated box. Postgres also relies on
  the OS page cache, so this is not the whole cache.
- `work_mem` — per sort or hash **per node per connection**. A parallel query
  with three sorts can use several multiples of it. 64MB with 200 connections is
  a way to run out of memory unexpectedly; set it low globally and raise it in
  the session for known-heavy jobs.
- `maintenance_work_mem` — index builds and vacuum. Being generous here (1-2GB)
  makes vacuum dramatically faster.
- `effective_cache_size` — not an allocation, a hint. Set it to roughly the
  memory the machine will actually use for cache so the planner prefers index
  scans.

## Finding the slow thing

`pg_stat_statements` is the first extension to install. Sort by `total_exec_time`
rather than mean — the query that runs a million times is usually the problem,
not the one that takes ten seconds nightly.

`EXPLAIN (ANALYZE, BUFFERS)` on the offender. Read it inside out. The
discrepancies worth chasing: estimated rows far from actual (statistics or a
correlated predicate the planner cannot model — `CREATE STATISTICS` for the
latter), `Rows Removed by Filter` in the thousands (a missing or wrong index,
see [[btree-indexes]]), and a nested loop chosen on a bad estimate that becomes
quadratic.

`random_page_cost = 1.1` on SSDs. The default of 4.0 encodes a spinning disk and
biases the planner away from index scans on hardware nobody runs any more.

## Connections

Each backend is a process with its own memory. Past a few hundred, context
switching and lock contention dominate and throughput goes *down*. PgBouncer in
transaction pooling mode fixes this, at the cost of session-level features:
prepared statements need care, `SET` does not persist, and advisory locks held
across statements break. In the cluster it goes in front of every service, see
[[postgres-on-kubernetes]].

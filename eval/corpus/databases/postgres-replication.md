---
tags: [postgres, replication, ha]
---

# Postgres streaming replication

## Write-ahead log first

Every change is written to the WAL before it touches a data page. WAL is a
sequence of 16MB segments under `pg_wal/`, addressed by LSN — a byte offset into
the log, printed as `3F/A80001C8`. Replication is nothing more than shipping
those bytes to another server and having it replay them. Crash recovery is the
same mechanism with the reader and writer on one machine.

A standby therefore does not run your queries again; it applies physical page
changes. That is why a physical replica is byte-identical, cannot differ in
schema, and must run the same major version.

## Setting it up

On the primary:

```
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
wal_keep_size = 2GB
```

A replication slot is the durable bookmark: the primary refuses to recycle WAL
the slot has not confirmed. That guarantees a standby can always catch up, and
it will happily fill `pg_wal` until the disk is full when a standby is gone for
a week. `max_slot_wal_keep_size` caps that and invalidates the slot instead —
losing the standby rather than the primary is the right trade, so set it.

The standby is built with `pg_basebackup -h primary -D /var/lib/postgresql/data
-U replicator -R --slot=standby1 --wal-method=stream`. The `-R` writes
`postgresql.auto.conf` with `primary_conninfo` and creates `standby.signal`,
which is what makes the server start in recovery instead of as a primary.

## Synchronous or not

`synchronous_commit` is per-transaction, which people forget:

- `off` — commit returns before the WAL is even flushed locally. Fast, loses
  recent transactions on a crash. Never for money.
- `local` — flushed to local disk, no wait on standbys.
- `remote_write` — standby received it into the OS.
- `on` — standby flushed it to disk.
- `remote_apply` — standby has applied it and will serve it to readers. The only
  setting that makes read-after-write on a replica correct, and the slowest.

`synchronous_standby_names = 'ANY 1 (s1, s2)'` waits for any one of two, which
keeps you writable when a single standby dies. With a single named synchronous
standby, losing it stalls every commit on the primary — an availability trap
dressed as a durability feature.

## Monitoring lag

On the primary, `pg_stat_replication` gives `write_lag`, `flush_lag`,
`replay_lag` as intervals, plus the LSNs. On the standby,
`pg_last_wal_replay_lsn()` and `pg_last_xact_replay_timestamp()`. Byte lag:
`pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)`.

Replay lag on an idle primary reads as huge because the timestamp is old, not
because anything is behind. Alert on bytes, not seconds, unless you also
heartbeat.

Long-running read queries on a hot standby conflict with replay of vacuum
records. `hot_standby_feedback = on` tells the primary to hold back cleanup,
trading bloat on the primary for queries that do not get cancelled; the
alternative is `max_standby_streaming_delay` and accepting cancellations.

## Failover

Promotion is `pg_ctl promote` or `SELECT pg_promote()`. The standby stops
recovery, increments its timeline, and starts accepting writes. Everything hard
is around it:

- The old primary must be fenced. Two writable primaries is the one unrecoverable
  outcome, and no amount of later repair fixes a split brain cleanly.
- Other standbys must be repointed at the new timeline. `restore_command` plus
  archived WAL, or `pg_rewind` for the old primary, which needs
  `wal_log_hints = on` or data checksums to work at all.
- Clients must learn the new address. That is a routing problem, not a database
  one — a VIP, a proxy like PgBouncer or HAProxy, or DNS with a short TTL.

Patroni is the usual answer: it holds a leader lock in etcd or Consul, runs the
health checks, and does the promotion. In the cluster it drives the numbered
workload described in [[postgres-on-kubernetes]].

## Logical replication is a different thing

`wal_level = logical`, a PUBLICATION on the source, a SUBSCRIPTION on the
target. It decodes WAL into row changes, so it can replicate a subset of tables,
cross major versions, and write on both ends. It does not carry DDL, sequences
need a manual bump at cutover, and a subscriber missing a replica identity for
UPDATE/DELETE fails at runtime. It is the tool for a zero-downtime major upgrade,
not for high availability.

Neither replaces backups: replication faithfully replicates a `DROP TABLE`. See
[[backups]].

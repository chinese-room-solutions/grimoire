---
tags: [sqlite, embedded, storage]
---

# SQLite notes

An embedded library, not a server. The database is one file; there is no
process to connect to, no port, no user accounts. Most SQLite advice on the
internet is written by people who did not enable WAL mode.

## The pragmas that matter

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA temp_store = MEMORY;
```

- **WAL** lets readers proceed while a writer is active. It creates `-wal` and
  `-shm` sidecar files, and it does not work over a network filesystem, because
  it depends on shared memory and real POSIX locks. NFS will corrupt it.
- **synchronous = NORMAL** under WAL fsyncs at checkpoints rather than every
  commit. Safe against process crash, exposed to a power loss between
  checkpoints. FULL is the paranoid setting and costs an fsync per transaction.
- **busy_timeout** turns instant `SQLITE_BUSY` errors into a retry loop inside
  the library. Without it, concurrent writers surface as random failures.
- **foreign_keys** is *off* by default, per connection, forever, for backward
  compatibility. Set it on every connection or your constraints are decoration.

Checkpointing moves WAL pages back into the main file. It happens automatically
around 1000 pages; a long-lived read transaction blocks it, and the WAL grows
without bound. A connection that opened a transaction and went to sleep is the
usual cause of a 4GB `-wal` file.

## Concurrency model

One writer at a time, database-wide. Readers are unlimited under WAL and see a
consistent snapshot. That single-writer rule is not a bug to work around; it is
the design, and it is why a write-heavy multi-tenant service is the wrong fit
while a read-heavy local application is close to ideal.

Practical shape in Go: two connection pools over the same file, one with
`SetMaxOpenConns(1)` for writes and one unbounded for reads. `BEGIN IMMEDIATE`
for any transaction that will write, so it takes the write lock up front instead
of failing to upgrade halfway through and losing its snapshot.

## Typing

Dynamic. A column declared `INTEGER` will accept `'banana'` unless the table is
declared `STRICT` (3.37+). Type affinity rules convert where they can and shrug
otherwise. `STRICT` tables plus explicit `CHECK` constraints are how you get the
behaviour you assumed you had.

There is no separate boolean, no date type. Store timestamps as Unix integers
and be done with it.

## Query planning

Same B-tree machinery as anywhere else, see [[btree-indexes]]. `ANALYZE`
populates `sqlite_stat1`; without it the planner guesses from structure alone.
`EXPLAIN QUERY PLAN` is short enough to read entirely — look for `SCAN` where you
expected `SEARCH`, and for `USE TEMP B-TREE FOR ORDER BY`, which means the index
did not satisfy the sort.

Covering indexes matter more than usual because the table is a B-tree keyed by
rowid: a non-covering index lookup is a second descent per row.

## Full-text search

FTS5 is a virtual table with its own inverted index, `MATCH` queries, and
`bm25()` ranking. `porter` or `unicode61` tokenizers, `content=` for an external
content table so the text is not stored twice, and triggers to keep them in
sync. Ranking is negative — smaller is better — which trips up every first
attempt at an ORDER BY. Combining it with a vector index is the hybrid approach
in [[vector-search]].

## Backups

`VACUUM INTO 'snapshot.db'` gives a consistent, defragmented copy without
stopping writers, and it is the simplest correct answer. `sqlite3 .backup` uses
the online backup API and restarts if pages change underneath it.

Copying the file with `cp` while anything is connected is not a backup — you
will get a torn file plus a `-wal` you did not copy. Litestream streams WAL
frames to object storage for continuous replication, which is genuinely good and
what I use at home; see [[backups]].

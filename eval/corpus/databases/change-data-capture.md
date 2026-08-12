---
tags: [databases, streaming, integration]
---

# Change data capture

Turning a table's modifications into a stream other systems can consume, without
asking the application to publish them. The whole appeal is that the database
already writes down every change it makes; capture reads that record instead of
inventing a second one that can disagree with it.

## Read the log, not the table

Polling `WHERE updated_at > ?` is the version everyone writes first, and it is
wrong in three specific ways: it misses deletes entirely, it misses intermediate
states between polls, and it races with transaction commit order, so a row
committed with an older timestamp can appear after the watermark has passed it.

Logical decoding solves all three because it is the commit stream:

```sql
SELECT * FROM pg_create_logical_replication_slot('search_sync', 'pgoutput');
ALTER TABLE orders REPLICA IDENTITY FULL;
```

`REPLICA IDENTITY` is the one setting that decides how useful the stream is.
The default emits only the primary key for updates and deletes, so a consumer
that wants the previous values — to unindex an old term, to compute a delta —
needs `FULL`, and pays for it in log volume.

## The slot is a loaded gun

A replication slot holds the write-ahead log until its consumer acknowledges it.
A consumer that stops — crashed, paused for a migration, deployed with a bad
config over a weekend — makes the primary retain segments until the disk fills
and the database stops accepting writes. Monitor the lag in bytes, set
`max_slot_wal_keep_size`, and drop slots that belong to systems that no longer
exist. The same log that makes replication work is the one being pinned, see
[[postgres-replication]].

## The outbox, for when you need both

Capture from the log gives you rows. What a downstream consumer usually wants is
events with meaning — "order shipped", not "column status changed from 3 to 4".
The outbox pattern writes the event into a table inside the same transaction as
the state change, and the capture pipeline follows that table instead. One
transaction, so the event cannot exist without the state change or the reverse,
which is the entire problem with publishing to a broker from application code.

The write model owning the events and the read side owning its own shape is the
same split as [[command-query-separation]], arrived at from the operations end.

## Delivery is at-least-once, always

There is no configuration that makes it exactly-once. The consumer will see
duplicates after any restart, because the acknowledgement and the side effect
cannot commit together across two systems. Make every consumer idempotent on the
row's identity and version, and stop worrying about it.

Ordering holds per key if — and only if — the partition key is the row's
identity. Fan out by table and you will process an insert and its update out of
order and spend an afternoon on a bug that is really a partitioning choice.

## Where I actually use it

Keeping a search index in step with the database, which is a chunk-and-embed
pipeline hanging off the stream rather than a nightly rebuild; see
[[vector-search]] and [[embeddings]]. Also feeding an analytics warehouse, where
the alternative was a nightly dump that was always a day stale and took the
primary down with it once a quarter.

Snapshot first, then stream from the log position the snapshot was taken at.
Getting that handover wrong — snapshot, then start streaming from *now* — silently
drops every change made during the snapshot, and nothing detects it until
someone notices the counts differ months later.

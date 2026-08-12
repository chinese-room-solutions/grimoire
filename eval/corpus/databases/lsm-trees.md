---
tags: [databases, storage-engines]
---

# LSM trees

The other shape a storage engine can have. Writes go to memory and to a log,
memory is flushed to immutable sorted files, and background work merges those
files so reads do not have to look in too many of them. Everything interesting
about an LSM engine follows from "the files are immutable".

## The write path

1. Append to the log for durability.
2. Insert into the memtable, an in-memory sorted structure — a skip list,
   usually, because it is concurrent without much fuss.
3. When the memtable is full, freeze it, start a new one, and flush the frozen
   one to disk as an SSTable.

The flush is sequential. Nothing is updated in place, ever: a delete writes a
tombstone, an update writes a new version, and the truth is whichever entry is
found in the newest file. That is why the write path is fast and predictable
compared to a page-oriented engine that has to find and dirty a specific page,
see [[btree-indexes]].

## The read path and why it is not slow

A read has to check the memtable, then every SSTable, newest first, until it
finds the key. Three things keep that from being ruinous:

- Every SSTable carries a bloom filter, so a file that does not contain the key
  is usually skipped without touching the disk. A point lookup that misses
  everywhere costs roughly one false-positive read.
- Files carry a sparse index of block offsets, so a hit is one seek and one
  block read.
- Compaction keeps the number of files bounded.

Range scans get none of the bloom filter's help — you must merge iterators
across every file that overlaps the range. Scan-heavy workloads are the case
where this design loses.

## Compaction is the whole tuning problem

Two families, and choosing between them is choosing which amplification to pay:

- **Levelled.** Each level holds non-overlapping files and is ten times the size
  of the one above. Reads touch at most one file per level; writes are rewritten
  once per level, so write amplification is high — ten or more.
- **Tiered.** Files accumulate at a level and are merged when enough have piled
  up. Writes are rewritten far less; reads may touch many overlapping files, and
  space amplification is worse because old versions stay around longer.

Read, write, space: you get to pick two. Levelled for read-heavy stores with
room to spare on write bandwidth, tiered for ingest-heavy ones.

The pathology to watch for is a compaction backlog. Ingest outruns the merge
threads, the file count at the top level climbs, every read starts checking
more files, and latency degrades gradually rather than failing — so the first
symptom is a support ticket, not an alert. Graph the file count per level and
put an objective on read latency, see [[service-level-objectives]].

## What it means operationally

- Deletes make things bigger until compaction runs. A mass delete followed by a
  disk-full alert is normal and infuriating.
- Space needs headroom: a compaction writes the merged output before dropping
  the inputs, so a full disk can leave the engine unable to compact its way out.
- Snapshots are cheap, because immutable files plus a manifest is exactly what a
  snapshot wants — hard-link the files, copy the manifest, and the backup is
  consistent without stopping writes; see [[backups]].
- Bulk load wants the ingest path that writes finished files directly and links
  them in, skipping the memtable entirely.

SQLite is not one of these — it is a B-tree engine and stays one, which is the
right call for a single-file embedded database, see [[sqlite]].

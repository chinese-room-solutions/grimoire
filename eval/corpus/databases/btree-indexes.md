---
tags: [databases, indexes, performance]
---

# B-tree indexes

The default index type everywhere, and the reason `WHERE id = ?` is fast.

## Structure

A B+ tree: internal nodes hold separator keys and child pointers, all values
live in leaves, and leaves are linked so a range scan walks sideways without
returning to the root. Fanout is high because a node is a page — a 8KB page with
16-byte keys holds hundreds of children, so a billion-row table is three or four
levels deep. Every lookup is that many page reads, and the upper levels are
always in cache.

Height is `log_fanout(N)`. That is the whole performance story: the tree is
shallow, so point lookups are effectively constant, and the interesting costs
are elsewhere.

## Ordering and composite keys

Entries are sorted, which is why a B-tree can serve:

- equality (`=`), ranges (`<`, `BETWEEN`), and prefix `LIKE 'abc%'`
- `ORDER BY` without a sort step
- `MIN`/`MAX` as a single descent to one end

An index on `(a, b, c)` is sorted by `a`, then `b` within equal `a`. So it
serves `WHERE a = 1 AND b = 2`, and it serves `WHERE a = 1 ORDER BY b`. It does
**not** serve `WHERE b = 2` — no leftmost column, no useful ordering. Column
order in a composite index is a design decision, not a formality: equality
columns first, then the range column, then anything needed only for output.

One range predicate is where usable ordering stops. In
`WHERE a = 1 AND b > 5 AND c = 9`, the index can seek on `a` and `b`, but `c` is
only a filter applied to the rows it walks.

## Covering and index-only scans

If every column a query touches is in the index, the heap is never visited.
Postgres calls it an index-only scan and it still needs the visibility map to be
current, which means recently updated rows send it back to the heap anyway —
another reason autovacuum matters, see [[postgres-tuning]]. `INCLUDE (col)`
adds payload columns to the leaves without making them part of the key or the
uniqueness constraint.

## Selectivity

An index on a boolean column is usually pointless: the planner will pick a
sequential scan over fetching half the table one random page at a time, and it
is right to. Rough threshold is a few percent of the table, lower when rows are
wide or the correlation between index order and physical order is bad.

Partial indexes fix the common version of this:

```sql
CREATE INDEX ON jobs (created_at) WHERE state = 'pending';
```

A small hot index over a large mostly-cold table. Expression indexes
(`CREATE INDEX ON users (lower(email))`) work the same way, and the query must
use the identical expression to match.

## Write cost

Every index is a second structure to maintain per insert, and a random write
into an interior page. Page splits fragment the tree; a monotonically increasing
key (a timestamp, a sequence, a ULID) always appends to the rightmost leaf and
splits cleanly, while a random UUIDv4 key scatters writes across the whole tree
and destroys cache locality. That is the concrete reason to prefer sortable
identifiers.

Unused indexes are pure cost. In Postgres, `pg_stat_user_indexes.idx_scan = 0`
after a full business cycle means drop it. Duplicate and redundant indexes hide
here too: `(a)` is redundant if `(a, b)` exists.

## When it is the wrong index

- Full-text — inverted index, GIN in Postgres, FTS5 in [[sqlite]].
- Containment on jsonb or arrays — GIN.
- Geometry, ranges, nearest-neighbour — GiST or SP-GiST.
- High-dimensional vector similarity — none of the above; a B-tree is useless
  past a handful of dimensions because there is no total order to exploit. That
  is the whole reason approximate structures like HNSW and IVF exist, see
  [[vector-search]].
- Append-only analytics on one column — BRIN, tiny and only useful when physical
  order correlates with the value.

## Rebuilding

`REINDEX CONCURRENTLY` in Postgres, or `CREATE INDEX CONCURRENTLY` plus a swap,
avoids the exclusive lock. Both are slower and can leave an invalid index behind
if they fail, which then has to be dropped by hand.

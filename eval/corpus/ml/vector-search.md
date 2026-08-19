---
tags: [ml, retrieval, indexes, search]
---

# Vector search

## Similarity

Cosine similarity is the angle between two vectors, ignoring magnitude:
`dot(a, b) / (|a| * |b|)`. Normalize at write time and it collapses to a plain
dot product, which is one fused multiply-add per dimension and vectorizes
perfectly.

Euclidean distance ranks identically to cosine *for normalized vectors*, so the
choice only matters if you skipped normalization. Inner product on
un-normalized vectors is a different ranking and favours long vectors; know
which one your index is configured for, because the mismatch is silent.

Exact search is a full scan: N dot products, then a top-k heap. At 100k vectors
of 768 dimensions that is around 300MB touched per query and a few tens of
milliseconds — entirely fine. Approximate indexes exist for the case where it is
not, and they are not free.

## HNSW

Hierarchical Navigable Small World graphs. A layered proximity graph: the top
layer is sparse and long-range, each layer down is denser, and search greedily
descends from an entry point, moving to whichever neighbour is closer to the
query and dropping a layer when it can no longer improve.

Parameters:

- `M` — neighbours per node, typically 16-64. Memory is roughly
  `M * 2 * 4 bytes` per vector on top of the vectors themselves. Higher `M`
  helps high-dimensional and clustered data.
- `ef_construction` — candidate list size while building, 100-500. Higher builds
  slower and gives a better graph. Cannot be improved after the fact.
- `ef_search` — candidate list at query time. This is the recall/latency dial,
  tunable per query, and the one to actually measure against a ground truth set.

Properties worth knowing: build time is `O(N log N)` and memory-resident by
design — HNSW on disk performs badly because the graph walk is random access.
Deletes are tombstones, so a high-churn corpus needs periodic rebuilds. There is
no incremental way to reclaim the graph structure a deleted node occupied.

IVF-Flat is the alternative: cluster into `nlist` cells with k-means, search
`nprobe` of them. Much cheaper to build, worse recall at the same latency, and
it needs training data up front. IVF-PQ adds product quantization to compress
the vectors themselves, which is how billion-scale indexes fit in memory, at a
real cost in precision.

For anything under a million vectors, brute force with SIMD is simpler, exact,
and fast enough. Reach for a graph index when the numbers say to, not by
default.

## Why lexical search still matters

Vectors are bad at exactly the queries that are easy for an inverted index: a
rare identifier, an error code, a function name, a quoted phrase. A symbol like
that either appears in the note or it does not, and the embedding of a rare
token is mostly noise because the model saw it rarely in training.

Conversely BM25 cannot match a paraphrase. A query using none of the note's
vocabulary scores zero, no matter how obviously it is about the same thing.

The failure modes are complementary, which is the entire argument for hybrid
search.

## Fusion

Two ways to combine the runs:

**Reciprocal rank fusion.** Score each document `sum(1 / (k + rank_i))` over the
runs it appears in, with `k` around 60. It uses only ranks, so it needs no score
calibration and cannot be broken by one engine's scores being on a different
scale. It is also insensitive to *how much* better the top hit was.

**Score normalization** — min-max or z-score per run, then a weighted sum. Keeps
magnitude information and is worth more when the two engines are well
calibrated, which they usually are not. Per-query normalization is unstable when
a run returns few results.

RRF first. It is hard to make it much worse than either input, which is not true
of a weighted sum.

## Reranking

A cross-encoder reads the query and the document *together* and scores the pair.
Far more accurate than any bi-encoder because it can attend across the two, and
far too slow to run over the corpus — so it runs over the top 50 from the hybrid
stage. This is usually the single largest quality gain available, and it costs a
model call per candidate.

## Measuring it

Fixed query set, graded judgments, and these numbers:

- **recall@k** — did the right document make the candidate set at all. This is
  what the retrieval stage owns; if it is not in the top 50, no reranker saves
  it.
- **nDCG@10** — graded, position-discounted. The headline number.
- **MRR** — where the first relevant result landed. Good proxy for the "I want
  one answer" case.

Report all three, always against the same set, and change one thing at a time.
Chunking strategy, embedding model, `ef_search`, fusion constant, reranker —
each of them moves the numbers, and evaluating a bundle of changes tells you
nothing about which one helped. Model choice details in
[[embeddings#Versioning]], and the SQLite side of the stack in [[sqlite]].

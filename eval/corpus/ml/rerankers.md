---
tags: [ml, retrieval, rerankers]
---

# Rerankers

A reranker scores (query, document) pairs jointly instead of comparing two
independent vectors. The retrieval stack becomes: cheap first stage gets 50
candidates, expensive second stage sorts them. It is usually the single
largest quality jump available in a search pipeline, for one model call per
candidate.

## Cross-encoders

The query and the document go through the transformer *together* — one
attention pass in which every query token can attend to every document
token. That interaction is exactly what a bi-encoder cannot have: two texts
embedded separately and compared by cosine are matching topics, not
answering questions, the limitation spelled out in
[[embeddings#What the geometry means]]. A cross-encoder sees that the number,
the function name, or the negation lines up, because it is reading them side
by side.

Models: `BAAI/bge-reranker-v2-m3` for multilingual, the ms-marco MiniLM
cross-encoders for English, or ONNX-exported local variants. Scores are not
comparable across models or even runs — only the *ordering* within one query
is meaningful. Thresholding a cross-encoder score ("only keep documents above
0.5") is calibrated per model and per corpus, and re-calibrated after any
model change, or it silently eats recall.

## Where it sits

```
candidates := hybridSearch(query, 50)   // BM25 + vectors, RRF-fused
scored    := reranker.Score(query, candidates)
top       := scored[:8]
```

The contract with the first stage is recall: if the answer is not in the 50,
the reranker cannot rescue it, which is why `recall@50` on the candidate set
is the number to watch before tuning anything about the reranker,
[[vector-search#Measuring it]]. Rerank depth is a real dial — 50 is the
common default, 100 buys a little recall at double the cost, and beyond that
latency has stopped being competitive with just returning more results.

The latency budget is the constraint that decides whether to have one at
all: 50 pairs through a small cross-encoder is tens of milliseconds on CPU
with ONNX and a few on GPU, but it is 50x the cost of the vector search it
sits on top of. For a personal corpus of a few thousand notes where the first
stage already returns the right answer in the top five, measuring before
adding the stage is not optional — an nDCG@10 delta of less than a point
does not buy back the complexity, the same discipline as any other
[[rag#Pipeline]] stage.

## Late interaction: ColBERT

The middle ground. Embed every token separately, store all of them, and score
a query against a document with MaxSim — each query token takes its best
match among the document's token vectors, and the sum is the score. It keeps
per-token granularity (so "pg_basebackup" matches itself exactly) without
running the pair jointly, making it far cheaper per pair than a
cross-encoder at better quality than a bi-encoder.

The price is storage: one vector per token means 10-100x the footprint of a
single-vector index, which is why token-level indexes compress aggressively
and why the honest comparison is against a cross-encoder over a shortlist,
not against brute-force bi-encoders. For my scale the cross-encoder over a
fused shortlist wins on simplicity; the crossover point where ColBERT-style
indexes pay off is somewhere past the corpus sizes I run.

## Measuring it

Reranking moves nDCG@10 and MRR; it does not move recall@k of the candidate
stage. Evaluate it as a separate stage against the same graded judgments —
compare (first stage only) against (first stage + reranker) — or a quality
change from chunking or fusion will be misattributed to it, the staged
evaluation argument from [[rag#Evaluating it]]. Two things worth checking
specifically:

- **Rank inversions on rare identifiers.** The cases where the reranker
  should beat lexical ordering are exactly the hard ones — when the query is
  a rare token the cross-encoder has seen rarely, it can score it below a
  topically smoother candidate. A keyword-exact query set catches this.
- **Score collapse near the top.** If positions 1-8 score within a hundredth
  of each other, the model is not discriminating and the ordering is noise;
  spilling eight near-ties into the prompt wastes context, cf.
  [[rag#Context assembly]].

## Notes from running one

ONNX runtime with dynamic quantization (INT8) costs under a point of nDCG
and cuts CPU latency by more than half — a good trade for interactive use.
Batch the pairs, sort by length descending to minimise padding, and cache
nothing: document scores are query-specific, so unlike embeddings there is
no index-time work to amortise.

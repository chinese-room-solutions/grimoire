---
tags: [ml, embeddings, retrieval]
---

# Embeddings

A model that maps text to a fixed-length vector such that related text lands
nearby. That is the whole idea; everything else is detail about *which* notion
of "related" the training procedure produced.

## What the geometry means

Distance in the space reflects whatever the training objective rewarded, which
for contrastive-trained retrieval models is roughly topical and functional
similarity. It is not entailment, not factual agreement, and not sentiment. Two
sentences that contradict each other sit very close together, because they are
about the same thing — which is why "the deploy succeeded" and "the deploy
failed" are near neighbours and why a naive similarity search cannot be used as
a fact check.

Antonyms are the clearest demonstration: hot and cold are near-synonymous by
this measure.

## Symmetric and asymmetric

- **Symmetric** — both sides are the same kind of text. Sentence similarity,
  deduplication, clustering.
- **Asymmetric** — a short query against a long passage. This is retrieval, and
  it is a different task. Models trained for it (E5, BGE, GTE, Nomic) expect
  prefixes like `query: ` and `passage: ` at inference time, and omitting them
  costs several points of recall for no visible error.

Using a symmetric model for retrieval half-works, which is worse than failing,
because you never learn why the results are mediocre.

## Practical properties

- **Dimension** is a cost, not a quality. 768 is the common middle; 1536 and
  above buy little for most corpora and cost memory linearly.
- **Matryoshka** training makes prefixes of the vector usable on their own, so
  you can truncate 1024 to 256 for a first pass and rescore with the full vector.
  Genuinely useful.
- **Normalization.** Retrieval models are trained with cosine similarity, so
  normalize to unit length and then the dot product *is* the cosine. Do it once
  at write time.
- **Context window.** Most are 512 tokens, some 8k. Text beyond the limit is
  silently truncated, not errored — the tail of a long note simply is not in the
  index.
- **Pooling.** Mean over token embeddings for most, the CLS token for some. Match
  what the model was trained with; getting it wrong produces vectors that look
  fine and rank badly.

## Chunking

The unit you embed is the unit you can retrieve. One vector for a 3000-word note
averages away everything specific in it, and the note will lose to a short note
that is entirely about the query term.

What works: split on headings, then pack to a target size with a small overlap,
and prepend the document title and heading path to each chunk so an isolated
chunk still carries its context. Late chunking — embedding the full document
with a long-context model and pooling per span afterwards — keeps cross-chunk
context and is worth trying when the model supports it.

Store the chunk offsets, not just the text, so a hit can be highlighted in the
original.

## Versioning

The model's identity is part of the index. Different model, different space, and
mixing vectors from two models produces silently wrong neighbours rather than an
error — there is no runtime signal at all. Key the index by the model name and
its dimension, and treat a model change as a full rebuild. A vector index is a
derived cache; the notes are the source of truth.

Also worth pinning: the exact preprocessing. A change to how titles are prepended
changes every vector, and a half-reindexed corpus ranks strangely in a way that
takes a long day to attribute.

## Evaluation

Do not evaluate by looking at ten results and feeling good about them. Build a
small set of queries with graded judgments over your own corpus, and measure
nDCG@10 and recall@50. Fifty labelled queries beats any amount of eyeballing,
and it is the only way to know whether a model swap helped. Scoring details in
[[vector-search]]; the end-to-end version in [[rag]].

---
tags: [ml, rag, llm, retrieval]
---

# Retrieval-augmented generation

Retrieve relevant text, put it in the prompt, generate an answer grounded in it.
The idea is a paragraph; the quality is entirely in the retrieval, and almost
every disappointing system is a retrieval problem being blamed on the model.

## Pipeline

1. **Ingest** — parse, clean, split into chunks, embed, index. Chunking notes in
   [[embeddings#Chunking]].
2. **Retrieve** — hybrid lexical plus vector, fused. [[vector-search#Fusion]].
3. **Rerank** — cross-encoder over the top 50, keep the top 5-8.
4. **Assemble** — the prompt with the passages, each labelled with its source.
5. **Generate** — with an instruction to answer only from the passages and to say
   when they are insufficient.
6. **Cite** — map claims back to the passages that supported them.

## Query rewriting

The user's question is often not a good retrieval query, and in a conversation
it is frequently not even self-contained — "what about the other one?" retrieves
nothing useful. Rewrite it against the conversation history first.

Two techniques that reliably help:

- **Multi-query.** Generate three paraphrases, retrieve for each, fuse the runs.
  Covers vocabulary the original phrasing missed.
- **HyDE.** Generate a hypothetical answer and embed *that* instead of the
  question. A fake answer sits in the same region of the space as real answers,
  which fixes the asymmetry between a short question and a long passage. It costs
  a generation call before every search, and it fails interestingly when the
  model hallucinates a confident answer in the wrong domain.

Both add latency. Measure whether they earn it on your own query set rather than
adopting them because a paper reported a gain on a different corpus.

## Context assembly

More context is not better. Recall is a real effect: models attend well to the
beginning and the end of a long context and less well to the middle, so ten
mediocre passages can score worse than four good ones. Put the strongest
passage last, or first and last.

Deduplicate near-identical chunks — three copies of the same paragraph from three
documents crowd out the one that had the actual answer, and give a false
impression of corroboration.

Include the source path and heading with each passage. It makes citation
possible, and it measurably improves the answer because the model uses the
structure.

## Failure modes

- **The answer is not in the corpus.** The model answers anyway from its own
  weights. The instruction to abstain helps and does not solve it.
- **The chunk boundary split the answer.** The definition is in one chunk and the
  caveat is in the next, so the answer is confidently half-right. Overlap and
  heading-aware splitting reduce it.
- **A stale document outranks the current one.** Nothing in the embedding knows
  about time. Filter or boost by recency explicitly.
- **The question needs aggregation.** "How many notes mention X" is a query, not
  a retrieval. Route it to a different tool instead of pretending.
- **Contradictory sources.** Retrieval returns both and the model picks one,
  usually the longer one. Surface the conflict rather than hiding it.

## Evaluating it

Two layers, and they must be separated:

**Retrieval** — recall@k and nDCG@10 against graded judgments. This is
deterministic, cheap, and it is where almost all the improvement comes from. Fix
this first.

**Generation** — faithfulness (is every claim supported by a retrieved passage)
and answer relevance. Usually scored by a model, which means the judge needs its
own validation against human labels before its numbers mean anything.

The trap is evaluating end to end only: a change to chunking moves the final
score through six intermediate steps, and you cannot tell what happened. Measure
each stage, change one thing at a time.

## When not to

If the corpus is small enough to fit in the context window, put it all in the
context window. If the questions are always the same twenty, cache the answers.
If the data is structured, write a query. RAG is for a large unstructured corpus
and open-ended questions, and it is a lot of machinery to maintain for anything
else. My own notes on building this for the vault start in
[[2026-06-02#Search prototype]].

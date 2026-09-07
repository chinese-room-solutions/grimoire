---
tags: [ml, quantization, inference]
---

# Quantization

Trading precision for memory. A model's weights are stored as 16-bit floats
by default; quantization stores them as 8, 4, or fewer bits with a scaling
factor, so a model that needed 14GB of VRAM runs in 4GB on the same card.
For local inference this is the difference between a usable model and a
screenshot of one.

## The arithmetic

Parameters times bytes-per-parameter is the floor of the memory bill: a 7B
model is ~14GB at FP16, ~7GB at 8-bit, ~3.8GB at 4-bit. The KV cache adds
context-dependent memory on top (roughly `2 x layers x ctx x dim x bytes`
per token), which is why long contexts hurt more than parameter counts
suggest and why KV cache quantization exists as its own knob. The same
arithmetic appears in [[fine-tuning#Serving]].

Weight quantization is mostly per-group integer scaling: a block of 32-128
weights shares a scale, the value is stored as an int. The reason groups are
small is outlier channels — a handful of weight channels with magnitudes 10x
the rest, which dominate the error if one scale has to cover everyone.
Format families differ in how they handle this: GGUF (llama.cpp) has the
K-quant ladder (`Q4_K_M`, `Q5_K_S`, `Q6_K`, `Q8_0`) with different block
sizes for different tensors; AWQ quantizes while protecting the salient
channels identified by a calibration pass; GPTQ solves the rounding error
per layer, also with calibration data. All three land within a similar
quality band at the same bit width; the practical choice follows the runtime
you are targeting (llama.cpp vs vLLM/exllama) more than the algorithm.

## What it costs

At 8-bit, essentially nothing — `Q8_0` is within noise of FP16 on most
evals, and the default answer when memory allows. At 4-bit, small but real:
perplexity drifts up, and the drift concentrates in exactly the tasks with
thin margins — long-chain reasoning, precise instruction following,
low-resource languages. Between them, `Q6_K` is the sweet spot nobody
regrets. Below 4-bit, quality falls off a cliff and I have not found a use
for 2-3 bit outside of "is this even possible" experiments.

The honest procedure: pick your eval first (a handful of real tasks, not a
vibe), measure the FP16 baseline, then measure the quantized model on the
same tasks. A win rate computed against nothing, per
[[fine-tuning#Evaluation]], is how 4-bit ships for a workload it quietly
ruins. Compounding matters too: a QLoRA adapter trained on a 4-bit base,
served quantized again, stacks two approximations — measure the end state,
not the pieces.

## Quantization during training is a different tool

QLoRA ([[fine-tuning#LoRA]]) quantizes the frozen base to 4-bit so fine-tuning
fits on one GPU, then learns adapters at full precision. That is a training
convenience with its own quality profile, not a way to produce a quantized
model. Serving-side quantization (AWQ, GPTQ, GGUF) is applied to a finished
model and is what this note is about.

## Not to be confused with product quantization

PQ in the retrieval literature compresses *vectors in an index* — splitting
an embedding into subvectors and coding each against a codebook, the "PQ" in
IVF-PQ, [[vector-search#HNSW]]. Same word, different layer of the stack:
one shrinks model weights for inference, the other shrinks embeddings for
billion-scale search. A search for "quantization" that means the second thing
finds this note irrelevant, and vice versa — worth saying out loud because
the collision has cost me an afternoon of reading the wrong papers.

## Practical defaults

- Local chat/assistant on a single GPU: `Q6_K` GGUF, or 4-bit AWQ if the
  context window needs the headroom.
- Batch/serving fleet with VRAM to spare: FP16 or 8-bit; the quality cliff
  is small at 8-bit but the latency win from bigger batches is real.
- Embedding and reranker models: usually leave them alone — they are small
  ([[embeddings#Practical properties]], [[rerankers]]) and their error
  compounds into retrieval quality, which is harder to notice than a bad
  sentence.
- Whatever the choice, record it with the eval numbers. "Q4_K_M seemed fine"
  in a README is a note to no one, including future me.

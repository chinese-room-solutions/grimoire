---
tags: [ml, training, llm]
---

# Fine-tuning

## Decide whether you need it

In order of cost, try: a better prompt, few-shot examples, retrieval
([[rag]]), then fine-tuning. Fine-tuning teaches *form* — a style, a schema, a
domain's phrasing, a consistent refusal behaviour. It is a poor way to teach
*facts*, because facts change and retrieval already handles them without a
retraining cycle.

The clearest signal that fine-tuning is right: the model can do the task when
you give it eight examples, and you want to stop paying for those eight examples
on every call.

## LoRA

Freeze the base weights, learn a low-rank update `W + BA` where `B` is `d x r`
and `A` is `r x d`, with `r` typically 8 to 64. Trains under 1% of the
parameters, produces an adapter of a few tens of megabytes, and several adapters
can be swapped over one loaded base model at serve time.

- **Rank** — 8 for style, 32-64 for a genuinely new capability. Higher ranks
  overfit small datasets fast.
- **Alpha** — the scaling factor, conventionally 2x the rank.
- **Target modules** — attention projections at minimum; including the MLP layers
  helps for harder tasks and roughly doubles the adapter.
- **QLoRA** quantizes the frozen base to 4-bit so a 70B fits on one large GPU.
  Slower per step, and the quality cost is small in practice.

Merging the adapter back into the base gives a standalone model with no
inference overhead, at the cost of losing the ability to swap.

## Data

This is the whole job. A thousand carefully checked examples beat a hundred
thousand scraped ones, consistently and by a lot.

- Every example must be the output you actually want, in the exact format you
  want. The model learns the format including its mistakes.
- Diversity across the input distribution matters more than volume within one
  slice.
- Hold out a real validation set, split by some natural grouping (document,
  user, date) rather than at random, or near-duplicates leak across the split
  and the validation loss lies to you.
- Mask the loss on the prompt tokens; train on completions only. Forgetting this
  is the most common silent mistake and it makes the model good at generating
  questions.

## Hyperparameters

Learning rate 1e-4 to 2e-4 for LoRA (much higher than full fine-tuning's 1e-5),
cosine schedule with a short warmup, 2-3 epochs. More epochs memorize.

Watch validation loss, not training loss, and stop when it turns up. Also
evaluate the base model's original capabilities — catastrophic forgetting is
real, and a model that learned your format and lost its reasoning is a
regression you will not notice from the loss curve.

## Evaluation

Build the eval set before training anything. Task-specific and automatic where
possible: exact match on structured output, a schema validator, a unit test for
generated code. Model-graded pairwise comparison against the base for open-ended
output, with the position of the two answers randomized because judges have a
strong position bias.

The number that matters is win rate against what you were already doing —
against the prompted base model, not against nothing.

## Serving

vLLM or TGI with the adapter loaded separately; both support multiple LoRAs over
one base. Watch memory: the KV cache dominates and scales with batch size times
context length, and it is what actually decides your throughput.

Quantization for serving (AWQ, GPTQ, GGUF) is a separate decision from QLoRA for
training, and the quality effects compound. Measure the end state, not the
pieces.

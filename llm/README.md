# Large language models — training, inference, optimisation and system design

A large language model is a program that predicts the next token given
the ones before it, trained on so much text that the prediction task
forces something like knowledge of the world into the weights. This
section is the systems view: what goes into pretraining, what a run
costs, how weights become a service, and where each optimisation buys
latency or memory back. It assumes you can read Python and know what a
matrix multiply is; it does not assume a machine-learning background.

Every entry below carries the same five labels, in the same order, every
time: the need that justifies the technique (**Why.**), a precise
definition of it (**What.**), the mechanics with tables, diagrams and
Python blocks that make it concrete (**How.**), what you gain
(**Advantages.**) and what it costs in practice (**Disadvantages.**).
The set is deliberately the contract the [rl](../rl/README.md) section
uses, so a reader moving between the two never relearns the map.

Read them top to bottom the first time — entry 1 defines tokens,
scaling and the vocabulary the rest reuse — and after that they work as
reference. There is no test suite behind this section: the Python
snippets are the specification, and where a snippet and a paragraph
disagree, the snippet is the claim you can run.

Preference optimisation and its mechanics — reward models, PPO, DPO —
live in [rl](../rl/README.md) and are linked to rather than repeated
here. Hardware, memory hierarchy and what a GPU costs sit in
[gpu](../gpu/README.md); the schedulers that pack training jobs onto a
cluster in [distributed](../distributed/README.md); the interconnects
that cap sharded training in [networking](../networking/README.md). What
a model *is* once trained — artefact formats, storage and distribution —
is entries 19 and 20; the compiler and framework stacks that run it are
entries 21 and 22; the practical single-card guide is entry 25, and
task-specific tool/agent/coding/3D/PCB fine-tuning is entry 26. The
operating-system and storage analogies — paging, caches, fragmentation
— come from [linux](../linux/README.md) and
[database](../database/README.md), and engineering failure modes are
catalogued in [mistakes](../mistakes/README.md). For a practical serving
engine walkthrough — vLLM's internals, commands, metrics, CPU/GPU modes,
optimization and deployment — see the [vLLM guide](vllm.md).

---

## 1. What an LLM is: next-token prediction and scaling laws

**Why.** Every term below — token, context window, pretraining,
perplexity — hangs off one short equation, and a team that skips it
argues about the wrong variable for a quarter. The framing also states
the resource bill in advance: text in, FLOPs in, tokens out. Without
it, "the model knows Python" is mysticism, not engineering.

**What.** A large language model emits a distribution over its
vocabulary for the next token, given the tokens before it, and a
document's probability is the product of those conditionals — so
training on text is minimising next-token cross-entropy. "Large" means
billions of parameters: one objective over enough data yields general
behaviour instead of a lookup table.

**How.** Tokens arrive as integer ids, and the model sees neither
characters nor words — only indices into a vocabulary of a few tens of
thousands (entry 3). Training writes the chain rule as a loss over
positions, one term per predicted token:

```text
P(t_1 ... t_n) = Π_i P(t_i | t_1 ... t_{i-1})
loss = − (1/n) Σ_i log P(t_i | t_1 ... t_{i-1})
```

Exponentiating the mean loss gives perplexity — the geometric mean
number of plausible next tokens — so a loss of 3.0 is perplexity ≈ 20.
Predicting web text well needs grammar, factual recall and a model of
whoever wrote the prompt, so the cheapest way to lower the loss is to
internalise the regularities that generated it: pretraining is
compression, and capability is its side effect. A prompt carrying
examples of a task is more context, and predicting it best means
inferring the task from it — in-context learning, trained not added.

Scaling turns that into a budget: held fixed the distribution and the
architecture, loss falls as a power law in parameters N, tokens D and
compute C — straight lines on a log-log plot across a decade of scale:

| Run | Parameters | Training tokens | Tokens/param | What it showed |
|---|---|---|---|---|
| GPT-3 (2020) | 175B | 0.3T | ~1.7 | scale alone buys in-context learning |
| Gopher (2021) | 280B | 0.3T | ~1.1 | parameters bought ahead of data |
| Chinchilla (2022) | 70B | 1.4T | 20 | same compute as Gopher, better at it |
| Llama-3 8B (2024) | 8B | 15T | ~1900 | past optimal, on purpose, for serving |

Chinchilla's finding was the corrective: at fixed compute, ~20 tokens
per parameter beats a fatter model. Production runs then crossed back —
15T tokens on 8B parameters — because the real objective is serving
cost, and a smaller model reads fewer bytes per token (entries 9, 12).

"Emergence" deserves the same scepticism: an exact-match benchmark
shows a cliff while per-step success improves smoothly — ten steps at
90% score 0.9¹⁰ ≈ 35%, ten at 97% score 74%, so the metric, not the
model, jumped. Swap in a soft score or a chain-of-thought prompt and
the "emergent" point shifts or vanishes. State metric and prompt.

**Advantages.** One self-supervised objective, so the label budget is
the internet rather than an annotation team. Loss, cost and capability
scale with measurable inputs rather than benchmark luck. The same
weights answer questions, write code and translate, and a held-out
slice of the corpus scores progress every step for free.

**Disadvantages.** The objective teaches continuation, not truth:
confidently wrong text is rewarded exactly like checked text, and the
loss cannot separate them. Corpora cost money and licence trouble, so
runs are capped by data rather than by ideas. Cross-entropy maps only
loosely onto task scores, so a lower loss can ship nothing. And the
laws predict loss — not safety, not reasoning, not contamination.

---

## 2. The transformer, block by block

**Why.** Recurrent networks read one step at a time and use a GPU
badly; a convolution forgets the start of a document by its end. The
transformer drops recurrence and position-dependent weights, and pays
with an operation that maps every token to every other token in one
matrix multiply — exactly the shape of work hardware likes.

**What.** A transformer language model is L identical blocks over a
residual stream of width d, each holding a multi-head attention that
mixes information across positions and a position-wise MLP that mixes
it within a position, both normalised before the sub-layer and added
back into the stream. Attention computes softmax(QKᵀ/√d_k)V from three
projections of that stream; position enters as a rotation of Q and K.

**How.** The residual stream is the one data structure: a vector of
width d (4096 for a 7B, 8192 for a 70B) every block reads and adds
back to, like a bus. Blocks write small updates rather than
transforming it, so depth composes and dropped layers survive:

```
input:    token ids → embedding table → residual stream x
block:    x → layer norm → Q, K, V projections → attention → + x
mlp:      x → layer norm → up project → GELU or SiLU → down → + x
stack:    [block → mlp] × L layers (32 for a 7B, 80 for a 70B)
output:   x → final norm → lm head → logits → softmax → next token
```

```text
Attention(Q, K, V) = softmax(Q Kᵀ / √d_k + mask) V
output = head_1 ‖ ... ‖ head_h W^O
```

Why three projections? Q is "what am I looking for", K is "what do I
contain", V is "what I hand over" — lookup scored independently of
payload, so keys and values cache while queries change each step
(entry 10). Multi-head splits d into h heads of d/h, each learning its
own relation; one wide head is less expressive at the same cost.

Pre-norm versus post-norm decides whether training survives depth:
post-norm needs warmup and drifts past a few dozen layers, pre-norm
leaves an identity path from embedding to logits, so modern models
normalise before each sub-layer and once before the lm head. The MLP
holds the remaining parameters, d → 4d → d; SwiGLU gates the up-
projection through a second matrix for a fraction of perplexity.
Position: RoPE rotates Q and K so scores depend on relative distance
(entry 15) — RoPE plus a scaling scheme is the default.

The word *layer* can mean one sub-layer (attention, MLP), one complete
transformer block, or one stage in a serving system. Keep those meanings
separate. A decoder block is usually pre-normalization → self-attention →
residual add → pre-normalization → MLP/MoE → residual add; the embedding,
final normalization and output head sit outside the repeated blocks:

| Component | What it does | Per-token decode behavior |
|---|---|---|
| Token embedding | Maps token ID to the residual-stream vector | Looked up for the new token |
| RMSNorm/LayerNorm | Rescales the stream before a sub-layer | Recomputed for the current token |
| Q/K/V projections | Build query, key and value vectors for attention | New Q/K/V computed at every attention block |
| Self-attention | Reads prior positions and mixes their value information | New query attends to the saved K/V history |
| Output projection + residual | Projects attention result and adds it to the stream | Current-token update only |
| MLP/FFN (often SwiGLU) | Applies nonlinear feature mixing at each position | Recomputed for the new token; no standard KV entry |
| MoE router + experts (optional) | Chooses a subset of expert MLPs for each token | Router/experts execute for the new token |
| Final norm + language-model head | Turns the last stream vector into vocabulary logits | Produces logits for the next-token sampler |

An *attention layer* is one set of Q/K/V and attention operations in a
transformer block. A *transformer layer/block* usually means attention
plus its MLP/MoE and residual/norm operations. A MoE block changes the
feed-forward sub-layer, not the basic reason attention K/V can be cached.
Some modern models interleave attention and state-space/recurrent blocks;
those have a mixture of KV cache groups and recurrent state rather than
the same K/V shape in every block.

```python
import torch.nn.functional as F

def attention(q, k, v, mask=None, fused=False):
    if fused:                                # tiled kernel, same maths
        return F.scaled_dot_product_attention(q, k, v, attn_mask=mask)
    scores = q @ k.transpose(-2, -1) / q.shape[-1] ** 0.5
    if mask is not None:                     # causal, lower triangle
        scores = scores.masked_fill(mask == 0, float("-inf"))
    return F.softmax(scores, dim=-1) @ v
```

**Advantages.** Everything is a dense matmul, so a decade of kernel and
silicon investment applies unchanged. Depth and width scale by adding
blocks under the loss curve of entry 1, the identity path keeps
80-layer training stable, and one set of weights serves both teacher-
forced scoring and generation, switched by a causal mask.

**Disadvantages.** Attention reads every earlier token: cost grows with
the square of context length and the KV cache linearly with it (entries
10, 15). d_model is one global width, so capacity cannot follow demand.
Pre-norm buys stability at a small loss penalty against a tuned
post-norm. RoPE and SwiGLU are empirical wins without theory, so each
new scale re-derives them. And the diagram hides the engineering —
kernels, cache layout and precision decide whether the maths runs at
line rate.

---

## 3. Tokenization: BPE and the economics of text

**Why.** The model cannot take characters — a sentence becomes 16k
steps — and it cannot take words, because a word list explodes past any
embedding table and dies on morphology, names and code. Something in
between is required, and the choice sets sequence length, vocabulary
size, multilingual fairness and the price of every request ever served.

**What.** Byte-pair encoding starts from a vocabulary of bytes and
repeatedly merges the most frequent adjacent pair into a new token,
until the vocabulary reaches a target size (32k to 256k in production).
Encoding applies the learned merges greedily; unseen text degrades to
bytes instead of failing. Special tokens — BOS, EOS, pad and the control
markers a chat template emits — are added, never merged away.

**How.** Two rounds of merges on a word-frequency corpus:

```text
corpus:   low ×5   lower ×2   newest ×6   widest ×3
start:    vocabulary = characters: l o w e r n s t i d ...
round 1:  pair (e, s) occurs 9 times → new token "es"
          newest → n e w es t        widest → w i d es t
round 2:  pair (es, t) occurs 9 times → new token "est"
          newest → n e w est         widest → w i d est
round 3:  pair (l, o) occurs 7 times → new token "lo"
          low → lo w                 lower → lo wer
```

Common words collapse to one or two tokens; rare ones keep their tails
("lower" = lo + wer), which is right: the frequency budget follows the
traffic. A production trainer runs this over a sampled corpus, floors
rare pairs, and special-cases digits so numbers split in groups of up
to three instead of byte by byte.

Vocabulary size trades sequence length against embedding cost:

| Vocab size | Sequence effect | Embedding cost at d = 4096 | Seen in |
|---|---|---|---|
| 4k-16k | about 1.4× longer sequences | 16k × 4096 = 66M params | older multilingual models |
| 32k-64k | the usual baseline | 64k × 4096 = 262M, often weight-tied | Llama-class default |
| 128k-256k | shortest sequences | 256k × 4096 = 1.05B untied | code-heavy and GPT-4 class |
| byte fallback | never fails, worst length | nothing extra | any well-built tokenizer |

The unfair part is language. A tokenizer trained mostly on English
splits Thai, Burmese or emoji near the byte level, so the same message
costs two to five times the tokens: identical information, longer
sequence, higher bill, less window left. Code is cheap because
punctuation repeats; any language thinly represented in the merge table
pays that tax on every request. When an API prices output per million
tokens, the tax is literal — you pay for the tokenizer's corpus.

Latency follows the same arithmetic. Decode produces one token per step
(entry 9), so a 600-token answer takes twice as long as a 250-token one
on identical hardware. That is why teams count tokens, not words, when
they cut prompts.

**Advantages.** Roughly 3-4× compression against raw characters shortens
every sequence, and any byte still round-trips, so no input is
unencodable. Frequency-driven merging spends vocabulary where traffic
is dense: names, punctuation and code idioms become single ids. It is
cheap to train and deterministic to apply, so token counts reproduce
exactly across runs.

**Disadvantages.** Boundaries are arbitrary, so the model must learn
around artefacts such as numbers split mid-value and names cut at odd
points. Multilingual users get longer sequences, higher cost and less
context with no way to opt out. Growing the vocabulary trades hundreds
of millions of embedding parameters against sequence length, with no
universally correct answer. Control tokens are a security surface: a
string in scraped text that collides with a turn marker can imitate a
boundary. And the tokenizer is frozen for the model's life.

---

## 4. Pretraining data: what goes in

**Why.** Two runs with identical architecture and compute routinely
differ by several benchmark points because one trained on better text.
Data is the one lever that costs no FLOPs, and the one most often spent
badly: duplicated pages, benchmark answers and scraped boilerplate all
train cleanly, then surface as memorisation, contamination or a flat
eval.

**What.** Pretraining data is a mixture of corpora — web text, code,
books, mathematics, multilingual text, curated encyclopedic material —
pushed through a pipeline that filters, deduplicates and mixes to a
token budget. The product is a shuffled stream of fixed-length token
sequences plus a written recipe: sources, weights, epoch cap, filters.
Without it the run is not reproducible.

**How.** The pipeline, in the order every real run applies it:

```
fetch:     crawl → warc → text extraction → boilerplate and nav strip
filter:    language ID → quality classifier → toxic and PII rules
dedupe:    exact hash → MinHash with LSH bands → near-duplicate clusters
mix:       domain weights → epoch cap → shuffle → fixed-length shards
```

Language identification comes first, because a page in the wrong bucket
corrupts both the mix weights and the quality classifier. Quality
filtering is a trained classifier — fastText-style, or an LLM judge —
whose threshold you tune against held-out perplexity: push it too far
and you keep only wire-service prose, losing the messy text the model
will meet in production. PII stripping then regexes emails, phone
numbers and addresses out of the survivors — the obvious cases only.

Deduplication is the step people skip and regret. Exact dedup is a hash
of normalised text — cheap, and it catches mirrors and syndicated
articles. Fuzzy dedup is MinHash signatures over shingles bucketed
through LSH bands, clustering pages that differ only by a footer or a
byline: a 1,000-word story on 400 news sites is one example seen 400
times, which overfits the phrasing and inflates any eval drawn from the
same crawl. Deduplication is also the contamination defence — if a
benchmark's test set survived in the corpus, later scores on it measure
recall. Detection is the same tooling: n-gram overlap between eval sets
and training shards, checked before the run rather than after the paper.

Mixing is a dial, not a default. Code lifts code ability out of
proportion to its share and leaks into prose as fences nobody asked
for; mathematics lifts arithmetic and multi-step reasoning; too much
web and the model learns listicles and SEO. Recipes get published as
weights — 60% web, 20% code, 10% books, 10% else — and labs tune them
with a small proxy model instead of guessing at the 70B. Synthetic data
fills gaps and carries a specific failure: training on a model's own
output narrows the distribution each generation and trims the tail —
model collapse — unless real data stays in the mix.

The budget question is epochs. Chinchilla's ~20 tokens per parameter
(entry 1) means about 1.4T unique tokens for a 70B — one pass over a
well-chosen corpus. Modern runs go past that deliberately: repeated
passes over a fixed, high-quality corpus beat fresh low-quality tokens,
and deduplication plus an explicit epoch cap takes repeats on purpose.

**Advantages.** Quality moves benchmark scores without adding a FLOP,
so it is the cheapest capability available. A written recipe makes runs
comparable and ablatable rather than anecdotal. Deduplication shrinks
the corpus — often 10-30% — which is free compute efficiency.
Pre-running contamination checks buy back the right to quote a
benchmark number.

**Disadvantages.** Every filter is a trade: quality thresholds remove
dialects and informal registers beside the spam, and the model then
writes like a press release. Licensing and consent are unresolved —
robots.txt is a convention, not a contract, and news outlets litigate
over trained-on copies. PII removal is probabilistic, so leakage stays
unknown until it surfaces. Fuzzy dedup is its own distributed job (see
[distributed](../distributed/README.md)), and mixing weights tuned on
a proxy model rarely transfer to the large one.

---

## 5. The pretraining run

**Why.** The objective is one line of maths; the run is three months of
several hundred GPUs, and almost everything separating the two — token
batches, warmup, precision, spike handling — is folklore that costs a
restart when it is wrong.

**What.** A pretraining run is next-token cross-entropy over a shuffled
token stream, minimised by AdamW under a warmup-then-decay schedule,
in bf16 with fp32 master weights, batches counted in tokens (4-16M per
optimiser step), and checkpoints that restore optimizer state too.

**How.** The objective never changes from entry 1; the bookkeeping does.
Batch size is counted in tokens, not sequences — 512 × 8192 = 4.2M is
the floor, large runs sit at 8-16M — so a batch means the same thing
when context length changes mid-run and when two runs are compared.

The learning rate is the most consequential number. It rises from near
zero to a peak over 500-2,000 warmup steps, then decays — cosine to
~10% of peak, or warmup-stable-decay, which trains at peak for most of
the run so it can be extended without re-planning. Peak is ~1e-4 for
large models and 3e-4 for smaller ones, and gradient norm is clipped
at 1.0 so one bad batch cannot rewrite the weights.

Precision is a hardware decision with a numerical tail. bf16 keeps
fp32's 8 exponent bits, so the range matches and nothing overflows —
fp16 caps at 65,504 and needs a dynamic loss scaler. Master weights
stay fp32 while gradients accumulate across micro-batches:

```python
def train_step(model, batch, opt, cfg, step):
    opt.zero_grad(set_to_none=True)
    set_lr(opt, cfg.peak_lr * min(step / cfg.warmup, 1.0))   # warmup
    for micro in range(cfg.accum_steps):        # token batch = sum
        ids, tgt = slice_micro(batch, micro, cfg)
        with torch.autocast("cuda", dtype=torch.bfloat16):
            loss = model(ids).loss(tgt) / cfg.accum_steps
        loss.backward()
    norm = torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
    opt.step()
    return float(loss), float(norm)
```

Context length is taught as a curriculum: most of the run at 8k, then
32k, then a short 128k stage with the RoPE base rescaled (entry 15).
Extending late costs a slice of the budget and leaves learned
short-range behaviour alone.

Loss spikes are the operational core. A spike — loss jumps 1.5-3× and
stays — is a poisoned batch, a bad optimizer state, or an LR too high
for the data. Skip a few batches first; if the loss stays up, roll
back to the last checkpoint and resume at half peak LR; if it repeats
on a schedule, quarantine the shard. Every later step trains corrupted.

Checkpoints are large because AdamW keeps a first and second moment per
parameter: fp32 master plus m and v is 12 bytes each, so a 70B holds
~840 GB of optimizer state beside 140 GB of weights. Runs write
sharded files every 1,000-2,000 steps, and a resume restores weights,
moments, LR position, RNG and data offset — weights alone start a new
run.

```bash
torchrun --standalone --nproc_per_node=8 train.py \
    --config configs/pretrain-70b.yaml \
    --data_mix refined-web,code,books,math --tokens_per_step 16M \
    --seq_len 8192 --lr 3e-4 --warmup_steps 2000 --lr_decay wsd \
    --bf16 --grad_clip 1.0 --ckpt_dir /mnt/pretrain/70b
```

**Advantages.** Token-denominated batches make runs comparable across
context lengths and labs. bf16 removes loss scaling from the failure
list, and warmup with clipping turns most divergences into recoverable
events. A written spike playbook turns a 3 a.m. decision into a
checklist, and sharded checkpoints lose no work to an interrupted
fleet.

**Disadvantages.** Every choice is made once: an LR picked too low
cannot be raised after warmup, and the data mix is baked in from step
zero, diagnosed later from one scalar on a dashboard. Optimizer state
multiplies storage sixfold, checkpoint IO stalls hundreds of GPUs, and
reproducing a run means reproducing data order, RNG and libraries.

---

## 6. Distributed training: sharding a model across machines

**Why.** A 70B model needs 140 GB of bf16 weights before activations,
optimizer state or gradients — more than one card holds, and one GPU
could not supply the arithmetic anyway. Sharding is how the model stops
being a single machine's problem, and each scheme splits a different
thing.

**What.** Data parallelism replicates the model, splits the batch and
all-reduces gradients each step. Tensor parallelism splits individual
matmuls across GPUs and all-reduces within the layer. Pipeline
parallelism splits the stack into stages that stream micro-batches.
ZeRO and FSDP shard optimizer state (1), gradients (2), parameters (3)
— composed as TP inside a node, PP across nodes, DP everywhere.

**How.** Data parallelism is the default because it needs no
cooperation during the forward pass: each rank runs its own
micro-batches, then a ring all-reduce combines gradients. That move
costs ~280 GB for a 70B in bf16 — 2(N−1)/N of a model copy — and at
50 GB/s of 400 Gb/s InfiniBand it eats 5.6 seconds of every step, so
gradients are accumulated, compressed or sharded rather than sent whole.

Tensor parallelism splits the two big matmuls per block — output
projection and MLP down row-wise, QKV and up/gate column-wise — each
half computing a slice that an all-reduce rejoins, twice per layer.
Volume is small, latency is not: TP groups stay on NVLink inside a node.

Pipeline parallelism trades bandwidth for idle time: stage p holds a
slice of layers and the first sits idle while the pipeline fills. The
bubble is (p−1)/(m+p−1) for p stages and m micro-batches, so 1F1B with
m ≥ 4p keeps it under 20%. Which scheme shards what:

| Scheme | Shards across ranks | Communication per step | Typical placement |
|---|---|---|---|
| DDP baseline | nothing | all-reduce of full gradients | wherever the NIC keeps up |
| ZeRO-1 | optimizer states (m, v, fp32 copy) | reduce-scatter + all-gather | data-parallel group |
| ZeRO-2 | plus the gradient buffer | reduce-scatter only | data-parallel group |
| ZeRO-3 / FSDP | plus bf16 parameters | all-gather per layer, both passes | fast interconnect required |
| Tensor parallel | weights inside one matmul | two all-reduces per layer | NVLink, inside a node |
| Pipeline parallel | layers, by stage | point-to-point activations | across nodes |

Activation checkpointing is the trade that makes the rest affordable:
store only block inputs, recompute the forward pass in backward — 30%
more FLOPs for nearly all the activation memory, the difference between
a 4k and a 64k context fitting at all:

```
gpu 0-7:   tensor parallel degree 8 → all-reduce over NVLink
stages:    stage 0 → stage 1 → stage 2 → stage 3 → logits
data:      4 stages × 8 GPUs = 32-way data parallel, one global batch
comm:      TP all-reduce inside node → PP hops across → DP last
```

The interconnect decides the plan, not merely its speed: Ethernet at
25-100 Gb/s makes all-reduce dominate, so runs shrink TP and accept
more PP, while InfiniBand at 400 Gb/s (see
[networking](../networking/README.md)) buys TP's low-latency sync, and
[gpu](../gpu/README.md) sets how large a stage can be. Failures are the
other half: a 4,096-GPU fleet restarts every hour or two, so runs
checkpoint cheaply and route around dead nodes. A straggler — one rank
slow on a PCIe read — stalls every rank at the next collective, which
is what "communication timeouts" really are.

**Advantages.** Each scheme attacks one resource: DP scales batch, TP
the largest matmuls, PP depth, ZeRO memory, and they compose — a 400B
trains on hardware that cannot hold it. FSDP needs no model surgery,
and activation checkpointing converts spare FLOPs into context length.

**Disadvantages.** Communication volume grows with every axis, and the
slowest link sets the fleet's step time. TP caps at the node boundary,
so a layer larger than one node cannot be trained without
repartitioning. The pipeline bubble is pure waste that only large
micro-batch counts hide, at the cost of memory. And distributed runs
fail statistically: a desynchronised rank corrupts gradients silently,
and debugging it takes a job queue.

---

## 7. Fine-tuning: SFT, LoRA and QLoRA

**Why.** Pretraining supplies capability but not behaviour: the base
model completes text, it does not follow instructions, hold a format or
decline a request. Full fine-tuning of a 70B also means 70B optimizer
states for a few thousand examples — adapters buy the behaviour change
without paying for the whole update.

**What.** Supervised fine-tuning (SFT) trains the same next-token loss
on curated prompt–response pairs, loss masked to the response tokens.
LoRA freezes the base weights W and learns a low-rank delta,
W' = W + (α/r)·B A with r = 4-64 — under 1% of the model trains, and
the delta merges back into W at inference. QLoRA holds the frozen base
in 4-bit NF4 with adapters in bf16, so a 65B fine-tunes on 48 GB.

**How.** SFT is pretraining with the loss switched to the assistant
turn: the chat template wraps each example in role markers, prompt
positions are masked, and only the response is scored up to EOS. Data
quality dominates quantity — LIMA trained on about 1,000 chosen
examples and beat models fine-tuned on 52,000 mediocre ones — because
the signal is style and format. Three epochs over a small set teaches
the set, not the task: memorised phrasing that looks like success.

LoRA rests on the observation that the update you need is low-rank:
most adaptation moves a small subspace of each matrix. Rank 8 on every
linear layer of a 70B trains roughly 100M parameters — about 0.2% — so
AdamW holds two moments for 100M instead of 70B, and one read-only
copy of the base serves every adapter:

```python
import torch
import torch.nn as nn

class LoRALinear(nn.Module):
    def __init__(self, base, rank=8, alpha=16):
        super().__init__()
        self.base, self.scale = base, alpha / rank
        self.base.weight.requires_grad = False    # freeze the model
        self.A = nn.Parameter(torch.randn(rank, base.in_features) * 0.02)
        self.B = nn.Parameter(torch.zeros(base.out_features, rank))
    def forward(self, x):
        return self.base(x) + self.scale * (x @ self.A.T @ self.B.T)
def merge_into_base(lora):                        # at serving time
    W = lora.base.weight
    W.data += lora.scale * (lora.B @ lora.A)      # (out, r) @ (r, in)
    return lora.base
```

Initialising B at zero makes the delta zero at step 0, so the run
starts as the base model. Merging costs one add and removes the extra
kernel from serving. QLoRA then removes the last barrier: the base is
quantised to 4-bit NF4 with per-group scales and double quantisation,
adapters and computation run in bf16, and dequantisation happens on
the fly inside the matmul — a quarter of the memory for quality close
to full bf16, at the price of a slower step and a base to dequantise.

Forgetting is the failure to plan for. Fine-tuning on 1,000 chat
examples shifts the whole distribution: rare languages, long-form
recall and code idioms drift. The defence is mixing a slice of
pretraining data (5-30%) into the batches, which holds the base
distribution while the new format is learned. Preferences —
sycophancy, verbosity, refusal — belong to entry 8, not to more SFT.

**Advantages.** A LoRA run needs the base model in memory once, a few
optimiser states and a small dataset, so one workstation can adapt a
70B. Adapters are interchangeable — one base, dozens of behaviours,
switchable per request — and merging leaves the serving path unchanged,
with no extra latency and no new runtime.

**Disadvantages.** Data quality is unmeasured: a thousand bad examples
fine-tune as happily as a thousand good ones, and the loss will not
tell you which you had. Small sets overfit within an epoch or two, and
memorised phrasing looks like success on a curve. Low rank under-fits
new knowledge, so factual injection through LoRA alone often fails.
QLoRA's 4-bit base slows training, and a chat template mismatch between
training and serving degrades every response silently.

For task-specific recipes — tool calling, agent trajectories, coding,
3D/CAD workflows and PCB design — see entry 26. That entry also explains
when to use fine-tuning versus tools, structured outputs or retrieval.

---

## 8. Alignment: preference optimisation, honestly

**Why.** A model that is merely competent is still sycophantic, verbose
and willing to answer anything, because those are the completions that
dominate its training text. SFT fixes format but not judgement — there
is no demonstration for "refuse this", and preference data is the only
scalable way to express it.

**What.** The pipeline is SFT → reward model → preference optimisation.
SFT fits demonstrations with the usual next-token loss; a reward model
is a scalar head trained on pairwise preferences; preference
optimisation then raises that reward while staying near the SFT policy,
either by PPO with a KL penalty or by the cheaper DPO and GRPO, which
drop the reward model or the rollout loop entirely.

**How.** The mechanics of PPO, value heads and reward models are covered
properly in [rl](../rl/README.md) — entry 6 for actor–critic and PPO,
entry 17 for learning from preferences — so this entry stays with what
is specific to language models. The division of labour:

```
pretrain:   web text → base model → capability, no format
sft:        demonstrations → chat format, same next-token loss
reward:     paired completions → reward model → scalar per token
optimise:   reward − KL → PPO   or   pairs → DPO   or   groups → GRPO
```

PPO puts four models in flight — policy, reference copy, reward model
and value head — plus a rollout loop, so a step costs several forward
passes and a serving pool. DPO collapses the whole thing to one
classification loss over the policy's own log-probabilities on
preferred and rejected completions, which is why it runs on one node
where PPO needs a fleet. GRPO sits between: sample G completions per
prompt, normalise reward within the group for an advantage, skip the
value network — the default for reasoning models with verifiable scores.

Reward hacking is the central failure, and in chat it looks nothing
like the reward-hacking of [rl](../rl/README.md). Sycophancy scores well
with human raters; verbosity wins votes because long answers look
thorough; bold headers and a confident tone beat correct hedging; and a
model learns all three from a reward model trained on exactly those
judgements. Constitutional and self-critique approaches have the model
generate, criticise itself against written principles and revise —
useful when labels are scarce, but it cannot exceed the model's own
judgement of itself.

The honest summary: pretraining sets the ceiling on capability, SFT
teaches the interface, preference optimisation chooses among the
completions capability can produce — and pushing the last stage too far
trades capability for style.

**Advantages.** Preferences express judgements no demonstration can —
tone, refusal, helpfulness under ambiguity — from pairs rather than
absolute scores, which humans are bad at giving. DPO makes the stage
affordable outside a large lab: one forward and backward per pair, no
sampler, no reward model, and group-relative methods need no human
labels when the answer is checkable.

**Disadvantages.** Preference data inherits the annotators: their
culture, their patience and their taste for confident long answers all
become the reward. Over-alignment taxes capability — models pushed far
along the pipeline give up points on knowledge and code for
politeness. The KL term holds a real tension: too strong and nothing
changes, too weak and the policy drifts into the reward model's blind
spots. And the stage is hardest to evaluate, because its purpose is to
move numbers benchmarks do not measure.

---

## 9. Inference I: prefill and decode ★

**Why.** Serving looks like ordinary request/response until you time a
token. The two phases of generation have opposite bottlenecks — one
wants arithmetic, the other wants bandwidth — and a server tuned for
either one alone wastes most of the card. The split is the basis of
every number in entries 10 to 12.

**What.** Prefill processes the whole prompt in a single parallel
forward pass: n tokens in, n positions of logits and n rows of KV cache
out, arithmetic-bound, time roughly linear in prompt length. Decode
then generates one token per step, each step rereading every weight:
memory-bandwidth-bound, latency fixed per token whatever the
arithmetic headroom. Different phases, different batch sizes, metrics.

**How.** Prefill is the easy half. A prompt of 4,096 tokens runs as one
matmul-heavy pass costing about 2 × parameters × tokens FLOPs — for a
70B, 4096 × 70B × 2 ≈ 573 TFLOP, near 1.8 seconds at 312 TFLOPS dense
and roughly twice that once you count utilisation and memory traffic.
Nothing about that is quadratic in the naive sense; attention's share
grows with context length (entry 15), but the dominant term is the
linear-in-n matmuls. Prefill also fills the KV cache, which is why the
first token of every stream waits for it.

Decode is the half that decides your hardware bill. Each step produces
one token and must read the whole weight tensor once:

```text
weights:  70B × 2 bytes (bf16) = 140 GB read per decode step
floor:    140 GB ÷ 2 TB/s (A100 HBM) ≈ 70 ms per token at batch 1
roofline: 312 TFLOPS ÷ 2 TB/s = 156 FLOP/byte to be compute-bound
decode:   ≈ 1 FLOP per byte read → 100× below the ridge point
prefill:  4096 × 70B × 2 FLOP = 573 TFLOP → compute-bound, ~2 K tok/s
```

The number behind that floor is arithmetic intensity. One decode step
does about 2 FLOP per parameter — one multiply-add — while reading 2
bytes of bf16 per parameter, so intensity sits near 1 FLOP/byte against
a ridge point of 156: the card waits on memory for essentially the
whole step. Prefill inverts it, because the same 140 GB of weights are
reused across 4,096 token positions, lifting intensity into the
thousands. Batching is what lets decode borrow that advantage: B
streams share one read of the weights, so intensity rises towards B
FLOP/byte with no new arithmetic.

The consequence is the whole design of inference servers. At batch 1
the card does one token's worth of arithmetic per 70 ms and is idle for
almost all of it; at batch 32 the same 140 GB of weights is read once
and serves 32 tokens, so throughput climbs nearly linearly until the
arithmetic finally catches up — usually around batch 32-128 depending
on context length, because KV reads join the weight reads (entry 10).
Batching is not an optimisation on top of decode; batching is what
makes decode viable at all.

The two phases pull scheduling in opposite directions, and the request
path shows it:

```
prompt:    user text → tokens → queue → prefill pass (parallel)
prefill:   n tokens → logits for token 1 → KV cache written
decode:    step → step → step → ... → EOS, one token each, cached KV
stream:    TTFT at the end of prefill → one token per ITL → done
```

Long prompts are therefore a prefill problem — they queue, they compete
for compute that decode does not need, and they set time-to-first-token
— while long generations are a bandwidth problem, where the only levers
are batch size, weight bytes and the number of streams sharing them.
Entry 11 schedules the two phases against each other; entry 12 shrinks
the 140 GB.

**Advantages.** The model is a read-only binary at serving time, so
scaling out is mechanical: replicas are interchangeable and warm cache
is the only per-replica asset. Prefill parallelises across prompt
tokens for free, giving thousands of tokens per second on one card.
Decode's floor is predictable from two published numbers, so capacity
can be modelled before buying anything.

**Disadvantages.** Batch 1 latency is set by physics: a 70B in bf16
cannot beat roughly 70 ms per token on a 2 TB/s card, whatever the
code does. Throughput and per-stream latency trade directly, so every
batch-size decision is a latency-SLO decision. A long prompt inflates
time-to-first-token and stalls decode for everyone on the queue.
Weights that no longer fit force quantisation (entry 12) or sharding
(entry 6). And prefill-heavy traffic starves decode unless the
scheduler separates the two.

---

## 10. The KV cache ★

**Why.** Recomputing attention over every previous token at every step
would make generation quadratic in sequence length, and no amount of
hardware fixes that. Caching keys and values turns decode linear — and
in exchange hands the system a mutable, per-request object that grows
until it is the reason the request fails.

**What.** The KV cache stores the key and value tensors for every layer,
every KV head and every token produced so far. Each new token computes
its own K and V once, appends them, and every later query reads that
append-only history instead of recomputing it. Memory scales as
layers × KV heads × head dim × 2 × bytes × tokens × sequences — and that
product, not the weights, often sets how many requests a device can hold.
For a conventional decoder-only model, the raw size is approximately
`2 × L × H_KV × D_head × bytes_per_value × cached_tokens`; the factor 2
is K plus V. It is per cache replica, before block rounding, quantization
scales, layout padding and implementation overhead.

**How.** Attention at step t needs Q from the new token and K, V from
positions 1..t. Without a cache, producing those K and V for all t−1
past positions means rerunning their projections at every step: total
work quadratic in the sequence, with a constant the size of the model.
With a cache, step t costs one token's projections plus one attention
read over t stored rows — linear overall, and the read is a streaming
operation hardware is good at.

The size arithmetic is simple enough to do by hand, and worth doing:

```python
def cache_bytes(layers=80, kv_heads=8, head_dim=128, dtype_bytes=2):
    per_token = layers * kv_heads * head_dim * 2 * dtype_bytes
    return per_token            # 80 * 8 * 128 * 2 * 2 = 327,680 B

per = cache_bytes()             # 320 KB per token, per sequence
ctx_8k = per * 8192             # 2.6 GB for one 8k sequence
ctx_32k = per * 32768           # 10.7 GB for one 32k sequence
batch_64 = ctx_8k * 64          # 168 GB — over an 80 GB card alone
```

So a Llama-3-70B class model holds 320 KB of cache per token: an 8k
chat turn costs 2.6 GB, and a batch of 64 such turns costs 168 GB
beside 140 GB of weights. That table is the real batching limit —
entry 9's "batch 32-128" is bounded by this product, and every
framework OOM you have seen at high context was this multiplication.
It is also why 128k context is a deployment decision rather than a
config flag: at 128k, a single sequence is 42 GB.

The memory is not wasted, but a naive allocator wastes a lot of it.
Each sequence reserved for its maximum length arrives at EOS with
half the allocation unused, and continuous batching (entry 11) makes
that fragmentation worse because lengths change every step. The fix is
borrowed wholesale from operating systems: PagedAttention stores the
cache in fixed blocks — typically 16 tokens — allocated on demand from
a free list, exactly the way a paged file system allocates blocks (see
[database](../database/README.md) for the same framing). Utilisation
goes from roughly 60-70% of reserved memory to within a percent or two,
and blocks become shareable: two requests with the same system prompt
point at the same prefix blocks instead of holding two copies.

### Which layers and tensors are cached?

The name *KV cache* is literal: it caches attention keys and values, not
all intermediate activations of every transformer layer. During decode,
the current token still runs through normalization, Q projection,
attention-output projection, residual additions, MLP/MoE and the final
head. The model does not retain all those intermediate outputs from the
previous token positions in the ordinary autoregressive cache.

| State/data | Cached across decode steps? | Scope and reason |
|---|---|---|
| Model weights | Yes, kept resident when memory permits | Shared by requests; static parameters, not per-request KV |
| Self-attention K and V | Yes, for each attention block and prior token | Reused when every later query attends to prior positions |
| Query vectors | No | Only the current token's query is needed for the current attention step |
| MLP/MoE intermediate activations | No in standard decode | Used to produce the current token's next-layer state, then discarded |
| Residual stream / hidden states | No full history in standard KV decoding | Recomputing prior token layers is avoided through K/V, not by saving every hidden state |
| Encoder/cross-attention state | Often reusable in encoder-decoder models | Source-side representations and projected cross-attention K/V are fixed for that request |
| SSM/recurrent state | A compact recurrent state may be cached | Architecture-specific state cache; it is not conventional per-token K/V |
| Shared prompt prefix | May share K/V blocks across requests | Requires matching tokenized prefix and compatible model/cache settings |

Thus, in a regular decoder transformer, K and V from every self-attention
layer are the tensors to budget. In hybrid models only their attention
layers contribute ordinary KV; recurrent/Mamba-style layers maintain
their own state. Encoder-decoder models may also retain source-side
encoder and cross-attention state. Image/audio/video preprocessing may
have separate multimodal caches. Logits, tokenizer output and application
response caches are other cache layers with different lifetimes and
invalidation rules — they are not the vLLM KV cache.

Prefix caching is a reuse policy over these KV blocks, not a different
tensor. A new request with the same exact cacheable prefix can reference
already-computed blocks rather than prefill them again. It does not skip
the new suffix, guarantee reuse across independent server replicas, or
save model weights a second time. Changes to tokenization, model revision,
adapter identity or cache-affecting inputs can make two textually similar
prompts non-shareable.

Bytes per token can also be cut at the source. Grouped-query and
multi-query attention store fewer KV heads than query heads, and
multi-head latent attention stores one compressed vector instead:

| Scheme | KV stored per layer | Bytes/token, 80 layers | Against MHA | Seen in |
|---|---|---|---|---|
| MHA | 64 heads × d 128 | 2.5 MB | 1× | small and pre-2022 models |
| GQA-8 | 8 heads × d 128 | 320 KB | 1/8 | Llama-3-70B class |
| MQA | 1 head × d 128 | 40 KB | 1/64 | speed-first 7B models |
| MLA | latent 512 + rope | ~190 KB | ~1/13 | DeepSeek-V2 and later |
| GQA-8 + int8 cache | 8 heads, 1 byte each | 160 KB | 1/16 | entry 12 |

GQA at ratio 8 costs a small, measurable amount of quality for an 8×
cache saving, which is why it is the default above 30B parameters; MQA
is cheaper and visibly worse; MLA recovers MHA-level quality at a
fraction of the bytes but needs its own kernel path. Cache
quantisation — fp8 or int8 per head — is the orthogonal lever: it
halves the numbers above with little visible drift over short contexts,
and its failure mode (drift compounding across a long generation) is
entry 12's subject.

**Advantages.** Linear decode cost is what makes streaming possible at
all: one attention read per step, not t of them. The logical history is
append-only; a paged engine allocates physical blocks from a shared pool
rather than requiring a contiguous per-request buffer. Blocks can be
shared across requests, copied on write, offloaded to host memory where
supported, or pre-warmed from a saved prefix. Counted explicitly, cache
becomes the honest capacity number — batch sizes stop being guesses.

**Disadvantages.** It grows with batch × context until memory, not
compute, caps the service: the model fits and the traffic does not.
Per-request state means a request cannot be moved, scaled or retried
cheaply — restarting a replica drops every in-flight cache. Long
contexts multiply the per-step read, so decode slows as a conversation
grows. GQA and MQA trade quality for bytes in a way benchmarks
under-measure. And cache bugs — a RoPE offset applied twice, a block
reused before it is cleared — produce confident garbage, no exception.

---

## 11. Inference II: batching, scheduling and decoding tricks ★

**Why.** Static batching fills the GPU and then wastes it: the batch
waits for its slowest sequence while finished ones idle, and a long
prompt in the queue stalls the decode already streaming. Throughput
comes from scheduling decisions made every step, not from the forward
pass.

**What.** Continuous batching runs the scheduler at iteration
granularity — each decode step, finished sequences leave and waiting
requests enter. Chunked prefill splits long prompts into pieces
interleaved with decode; disaggregation splits prefill from decode
pools; prefix caching shares KV; speculation drafts k tokens at once.

**How.** Static batching pads every sequence to the batch maximum and
runs until the last one emits EOS: a batch of 32 where one request
generates 900 tokens and the rest finish by 120 pays 780 steps for
empty slots. Treat each decode step as the scheduling unit instead:

```
step 1:    slot A: seq 101 → token    slot B: seq 102 → token
step 2:    slot A: seq 101 → token    slot B: seq 102 → EOS, freed
refill:    slot B: ← seq 103 enters, prefill runs as a chunk
steady:    every step → finished out → waiting in → batch stays full
```

A 40k-token prompt arriving whole would stop every other slot for the
duration of its prefill; chunked prefill cuts it into 2,048-token
pieces scheduled where a full batch would run, so decode keeps flowing
and the new request pays a slightly later first token. Disaggregation
goes further, splitting prefill onto arithmetic-heavy pools and decode
onto bandwidth-heavy ones, while prefix caching shares system prompts.

Speculative decoding attacks the per-token floor. A small draft model
proposes k tokens; one forward of the big model verifies all k in
parallel, keeping the longest accepted — and since verification reuses
the big model's distribution the output is unchanged, exactly. At
acceptance α = 0.9 with k = 5 the expected yield is 4.1 tokens per
forward against 1: (1 − αᵏ)/(1 − α). Medusa and EAGLE skip the draft
model with extra heads on the same hidden state.

```text
T = 1.0 leaves logits alone; T < 1 sharpens; T > 1 flattens them
p_i = exp(z_i / T) / Σ_j exp(z_j / T)          temperature applied
top-k: keep the k largest probabilities           k = 40 is typical
top-p: keep the smallest set summing above p     p = 0.9 to 0.95
greedy: argmax, temperature 0 → deterministic, no diversity
```

```python
def sample(logits, temperature=0.8, top_k=0, top_p=1.0):
    if temperature <= 0.0: return logits.argmax(-1)   # greedy path
    logits = logits / temperature
    if top_k > 0:
        cut = logits.topk(top_k, -1).values[..., -1, None]
        logits = logits.masked_fill(logits < cut, float("-inf"))
    probs = logits.softmax(-1)
    if top_p < 1.0:                              # nucleus sampling
        s, idx = probs.sort(-1, descending=True)
        s = s.masked_fill(s.cumsum(-1) - s > top_p, 0.0)
        probs = probs.scatter_add(-1, idx, s)
    return probs.multinomial(1, replacement=False)
```

| Metric | Definition | Interactive target | Driven by |
|---|---|---|---|
| TTFT | request start → first token | under 500 ms, short prompt | queue + prefill |
| TPS | decode tokens/s, per stream | 30 to 60 | bandwidth ÷ batch |
| ITL | gap between streamed tokens | under 100 ms at p95 | scheduler stalls |
| E2E | request → last token | 60 tokens in 1-2 s | sum of the above |
| Goodput | acceptable tokens/s per GPU | the number to optimise | retries, quality |

Queueing is what teams discover in production: wait grows like
ρ/(1 − ρ), so at 80% utilisation a p99 request waits four times its
service time and at 95% twenty — hence queue-depth autoscaling
(entry 16).

**Advantages.** Continuous batching lifts utilisation to near
saturation with no model change and identical outputs. Chunked prefill
ends the head-of-line blocking that made mixed traffic unusable.
Speculative decoding adds throughput without touching quality, because
the distribution is preserved exactly. And iteration-level scheduling
gives one place to enforce priorities, quotas and fairness.

**Disadvantages.** Scheduling sits in the hot path: a per-step decision
that takes microseconds becomes the latency floor, and a bug corrupts
every stream rather than one. Performance now depends on the traffic
mix, so a benchmark under one mix does not predict production.
Speculation needs a draft that agrees with the target — below ~30%
acceptance it makes you slower, workload-dependent. Disaggregation
doubles the pools and adds a KV transfer hop. And every metric can be
gamed: batching harder raises TTFT past the point users abandon.

---

## 12. Quantisation: fitting more model in less memory ★

**Why.** Decode reads the weights every step (entry 9), so bytes per
weight are seconds per answer and gigabytes per replica. Halving the
bytes halves the bandwidth bill and can halve the card count — the
cheapest performance available, bought with accuracy instead of
hardware.

**What.** Weight-only quantisation stores parameters in 8 or 4 bits and
dequantises them into bf16 inside the matmul: GPTQ rounds weights with
a Hessian-based error term so remaining weights compensate, AWQ scales
salient channels from activation magnitudes before rounding.
Group-wise quantisation gives blocks of 32 or 128 weights one shared
scale; FP8 is a native float, E4M3 for weights, E5M2 for gradients.

**How.** The arithmetic is the argument. A 70B at 4 bits is 280 Gbit =
35 GB, plus roughly 3% for per-group scales, so about 36 GB beside
140 GB in bf16. Reading 36 GB instead of 140 GB at 2 TB/s moves the
per-token floor from 70 ms to 18 ms: a 2-3× speedup, and the
difference between two cards and one.

```python
def quantise_group(w, bits=4, group=128):
    """Returns packed byte codes plus per-group scales."""
    qmax = 2 ** bits - 1
    rows = w.reshape(-1, group)                 # one scale per group
    scale = rows.abs().amax(-1, keepdim=True) / qmax
    code = (rows / scale.clamp_min(1e-8)).round().clamp(0, qmax)
    return code.byte(), scale

def dequantise(code, scale, group=128):
    rows = code.reshape(-1, group).float() * scale
    return rows.reshape(code.shape)             # back to bf16 inputs
```

Rounding naively costs quality because weights are not independent: one
bad rounding changes the layer's output and the next rounding was
chosen without knowing it. GPTQ processes a row at a time, quantising
each weight and immediately updating the ones still to come, using the
layer's Hessian — second-order information about what matters. It
needs a couple hundred calibration prompts and a GPU pass per layer.
AWQ skips that solve: scale the channels with the largest activations
toward the middle, round, scale back. Both are post-training: hours.

Activations are harder, because a handful of channels run an order of
magnitude above the rest across every layer and per-tensor activation
quantisation fails on them. SmoothQuant migrates the difficulty —
divide those weights by a per-channel scale, multiply the matching
activations by it — so FP8 needs none of it in training.

| Format | Bytes per weight | 70B resident | Typical quality cost | Notes |
|---|---|---|---|---|
| bf16 reference | 2.00 | 140 GB | none | the number others are judged against |
| FP8 E4M3 | 1.00 | 70 GB | 0 to 1 point | native on H100-class hardware |
| INT8 GPTQ or AWQ | ~1.03 | 73 GB | under 1-2% | near-lossless, mature kernels |
| INT4 GPTQ | ~0.53 | 37 GB | 1-4% | needs calibration, group 128 |
| INT4 NF4 | ~0.53 | 37 GB | similar | built for QLoRA training |
| INT4 + int8 KV | ~0.53 | 37 GB, cache halved | long-context drift | entries 10, 11 |

Post-training quantisation is the default because it is cheap;
quantisation-aware training — fake-quantising weights during a
fine-tune — recovers most of the gap when PTQ is not enough, at the
cost of a training run. What breaks is consistent: first and last
layers stay in bf16; small models degrade faster, because a fixed error
is a larger fraction of a smaller capacity; and loss concentrates where
you do not benchmark — rare tokens, long generations where KV error
compounds. A calibration set unlike production is the usual cause of a
"lossless" quantisation that regresses on your task.

```bash
python quantise.py --model ckpt/llama-70b-bf16 --format awq-int4 \
    --group_size 128 --calib_set pile-2k --calib_samples 128 \
    --device cuda:0 --save out/llama-70b-awq-int4
python serve.py --model out/llama-70b-awq-int4 --dtype int4 \
    --kv_cache fp8 --max_batch 32 --tensor_parallel 1
```

**Advantages.** Bytes down means latency down, capacity up and cost
down at once, from a pass that takes hours. INT8 is close enough to
lossless to be the default, and INT4 fits a 70B on one card — a
deployment change, not a benchmark point. Group-wise scales make 4-bit
viable on models per-tensor quantisation ruins, and formats are
interchangeable at deploy time, KV cache included.

**Disadvantages.** Quality loss is uneven and least visible where you
measure it: MMLU within a point while a long multi-step task loses
five, and small models lose proportionally more than large ones. GPTQ
needs a calibration set and a GPU pass, and a mismatched one regresses
silently. Dequantisation kernels can eat the win if they materialise
full bf16 tensors instead of fusing. FP8 needs hardware that has it,
KV quantisation compounds error, and two variants of one model mean
every bug report must be reproduced on both.

---

## 13. Distillation, pruning and sparsity

**Why.** The expensive thing about a large model is running it, and most
of its parameters are not needed for most inputs. Distillation moves
the behaviour rather than the parameters; pruning removes parameters
and hopes the behaviour survives. Each fails in a way you can name
before you start.

**What.** Knowledge distillation trains a smaller student against the
teacher's output distribution — the full probability vector, softened
by temperature — instead of only the correct token, with a KL loss
scaled by T². Pruning zeroes weights (unstructured) or removes whole
structures (heads, layers, width) and retrains; layer dropping trains
with random block skips so a thinner stack stays in distribution.

**How.** A soft target carries the teacher's second and third choices —
which word was nearly as plausible, which was ruled out — and a one-hot
label has none of that. Temperature T flattens the distribution so more
of the tail survives:

```text
soft:  L = T² · KL( softmax(z_teacher / T) , softmax(z_student / T) )
hard:  L = − Σ_i log P_student(t_i | context)          one-hot labels
T:     higher T exposes more of the tail; T = 2 to 8 is typical
mix:   L = α · L_soft + (1 − α) · L_hard        α ≈ 0.5 in practice
```

The T² factor compensates for the softmax's shrinking gradients at high
T, keeping the student's learning rate comparable across temperatures.
Teacher-forced distillation suits plain text; chat and reasoning
models compound sequence errors over contexts the teacher never scored,
so on-policy distillation inverts the loop — the student generates,
the teacher scores those exact tokens — matching deployment, which is
how small models inherit long chain-of-thought behaviour.

Distillation beats training small from scratch when the budget is
tokens: a soft label per position is a far denser signal than a
one-hot, so a student reaches a given loss with materially less
compute. It stops at the teacher's ceiling, and it copies the teacher's
blind spots, confident refusals and all.

Pruning comes in two shapes with different physics. Unstructured
pruning removes the smallest weights and stores the rest sparsely:
memory falls, time does not, because a GPU skipping zeros at 50%
sparsity runs as fast as at 0% — only hardware 2:4 structured sparsity
converts sparsity into a real 2×. Structured pruning removes things
with shape: heads (most layers use a minority of them), whole layers —
dropping every fourth typically costs a small perplexity bump — or
width, and training with random layer drops beats cutting afterwards.

The honest comparison:

| Technique | Cost to apply | Tooling | Where accuracy is lost | Buys |
|---|---|---|---|---|
| Quantisation (entry 12) | hours, one GPU | mature | rounding, worst on outliers | memory and bandwidth |
| Distillation | training run plus teacher | mature, needs a teacher | ceiling set by the teacher | capability at small size |
| Unstructured pruning | search, retrain, kernels | poor for LLMs | spread thin across weights | memory only, no speed |
| Structured pruning | retrain or layer drop | moderate | the heads and layers removed | latency and FLOPs |
| MoE (entry 14) | a full pretraining run | active research | routing imbalance, drops | params at fixed FLOPs |

**Advantages.** Distillation is the only one of the three that improves
capability per byte: the student learns what the teacher knows rather
than fitting the data again, and needs no architecture change. On-
policy distillation extends that to behaviour, layer dropping gives a
speedup from a model trained to survive it, and all three stack — a
distilled model quantises as well as any other.

**Disadvantages.** Distillation needs a teacher to run, often the most
expensive part of the exercise, plus licence to use it, and a student
inherits the teacher's errors it cannot be audited separately from.
Pruning without sparse hardware buys no latency at all, which is how
the technique most often disappoints. Structured pruning concentrates
damage in specific heads and layers, showing up only at long context
or on rare tasks. And evaluating any of them needs the task-specific
held-out sets of entry 17, because aggregate perplexity hides them.

---

## 14. Mixture of Experts

**Why.** Training cost scales with parameters and inference cost scales
with parameters read per token. Sparse MoE breaks the coupling: hold
many more parameters, run a few of them per token, and buy capability
per FLOP instead of per parameter.

**What.** An MoE layer replaces one MLP with N expert MLPs and a
router. The router scores each expert for the token, the top-k
(typically top-2 of 8, or top-2 of 64 for fine-grained designs) are
selected, and the output is their score-weighted sum. Total parameters
multiply by N while per-token FLOPs stay near k experts' worth: a
Mixtral-class model holds ~47B parameters and activates ~13B per token.

**How.** Routing is a linear layer over expert scores plus a softmax
and top-k selection — trivial arithmetic that decides everything
downstream. Without pressure the router sends most tokens to whichever
expert won early, so an auxiliary loss enforces load balance:

```text
load:    tokens T = 8192, experts N = 8, top-k = 2
         balanced load = T × k / N = 2048 tokens per expert
cap:     capacity C = CF × T × k / N, with CF = 1.0 to 1.25 typical
overflow: tokens past C are dropped (training) or rerouted (serving)
aux:     L_aux = N × Σ_i f_i · P_i   f_i = share routed, P_i = score
```

Dropped tokens are the tax: training drops a few percent of bypasses
as harmless noise, but at inference a drop silently loses content, so
serving raises the capacity factor or routes overflow losslessly.
Fine-grained designs (64 small experts rather than 8 large, plus one
shared expert every token visits) give a finer combination and are
the current default.

```
route:    token → router logits → softmax over N experts → top-k scores
combine:  expert 3 output × 0.6 + expert 7 output × 0.4 → stream
deploy:   aux loss → balanced load → capacity C → sharded experts
```

Training and inference then disagree about the arithmetic. A batch
spreads across all N experts, so the saving against dense is smaller
than the per-token figure suggests, and expert parallelism adds two
all-to-all exchanges per layer. Serving has the worse problem: all
~47B parameters must be resident because any token may route anywhere
— 94 GB in bf16, of which one token's compute uses 13B. Sparse
compute with dense memory caps the requests per card.

The headline number does not scale either: experts are partially
redundant and quality per active parameter sits well below an equal-
parameter dense model — a 47B total, 13B active MoE does not beat a
dense 47B, only the dense model at the same FLOPs:

| Property | Dense 8B | Dense 70B | MoE ~47B total, top-2 |
|---|---|---|---|
| Active parameters per token | 8B | 70B | ~13B |
| FLOPs per token | 1× | 9× | ~1.6× |
| Weights in bf16 | 16 GB | 140 GB | ~94 GB |
| Serving constraint | memory | two cards | all experts resident |

**Advantages.** Capability per FLOP is genuinely better, so a fixed
inference budget buys more quality — the reason the largest models are
now sparse. Routing gives conditional computation: easy tokens take the
cheap path, specialised tokens find their expert. Scaling experts is
architecturally cheap, and pretraining recipes largely transfer from
dense at the same token budget.

**Disadvantages.** Every expert must live in memory, so serving an MoE
costs the full parameter count in bytes whether or not the token uses
it. Expert parallelism adds two all-to-all steps per layer, paid
exactly where imbalance hurts most, and load balancing can collapse: a
run where one expert captures the batch wastes it. Dropped tokens lose
content at inference if the capacity factor is tight. And there is no
single "the network" to profile, only a fleet of small ones behind a
router.

---

## 15. Context, attention and architecture frontiers

**Why.** Three constraints bite as soon as a product needs a long
prompt: attention's quadratic arithmetic, the KV cache growing beside
it (entry 10), and a positional scheme that has never observed the
positions you are asking about. Each frontier below answers one.

**What.** FlashAttention is exact attention computed IO-aware: tile Q,
K and V into SRAM, maintain the softmax incrementally, never write the
n × n score matrix to HBM — same results, linear memory. Sliding-window
attention restricts each token to the last w positions; RoPE is
extended by scaling positions plus a short fine-tune; state-space
models replace attention with a selective linear recurrence.

**How.** FlashAttention changes no mathematics and every performance
profile. The naive kernel materialises n × n scores — 32k context is a
billion entries per head — while arithmetic intensity sits far below
the roofline in [gpu](../gpu/README.md). Tiling computes a block of
queries against a block of keys and keeps a running max and sum so the
softmax can be corrected as tiles arrive (online softmax), writing
only the output: memory falls from O(n²) to O(n), and attention gets
2-4× faster at 8k-32k with identical results.

Local attention attacks the same quadratic from the model side: each
token attends to at most the previous w positions (w = 4096 is
common), turning per-layer cost into O(n · w). With L layers,
information propagates w tokens per layer, so a 32-layer model with a
4k window still reaches 128k back — as a relay, not a lookup.

Position extension is the subtler failure. RoPE rotates queries and
keys by angles that grow with position; train at 4k and the angles
beyond it never appeared, so scores there interpolate nonsense and
quality falls off a cliff. Position interpolation scales the index so
32k maps onto 4k of trained range; NTK-aware scaling raises the
rotation base; yaRN blends schemes per band. All cost a short fine-tune
and degrade retrieval before fluency.

The cost at long context is asymmetric: prefill spends attention
quadratically — a 128k prompt's attention is 256× an 8k prompt's —
while decode spends it linearly per step and the KV cache grows
linearly for ever. Long context is cheap to offer and expensive to run.

```
flash:      Q, K, V tiles → SRAM → online softmax → HBM written once
window:     token → attends to last w tokens → O(n · w) per layer
position:   index → rescaled or re-based → short fine-tune → 128k read
multimodal: image → patches → projector → same stream as text tokens
```

Beyond attention, state-space models such as Mamba keep a fixed-size
recurrent state, h_t = A h_(t−1) + B x_t with A, B and C selected by
the input, so sequence cost is linear — the trade is retrieval, since
a recurrent state only retains what it compressed, hence the
compromise of a few attention layers inside an SSM body. Multimodal
follows the same reduction: a vision encoder turns patches into
embeddings and a linear projector maps them into the residual stream,
so one forward pass covers interleaved text and images, paid for in
context. Reasoning models spend the same context on chain-of-thought
that RL shaped (entry 8; mechanics in [rl](../rl/README.md)).

**Advantages.** FlashAttention is exact, so it is a drop-in that buys
speed on every workload. Windowing plus RoPE extension give 100k-plus
contexts on hardware that could not compute them naively. The unified-
token view makes multimodal models architecturally simple: no fusion
module, just more tokens. And the techniques compose — tiling, windows,
cache quantisation and position scaling apply at once.

**Disadvantages.** Long context is sold as a feature and delivered as a
cost: prefill attention grows quadratically while the cache grows for
ever, and both are billed to someone. Position extension papers over a
model that never trained there, and retrieval drops first, silently.
Windowed attention cannot recall beyond the window without a relay.
SSMs have younger tooling and weaker retrieval. Multimodal tokens
quietly shorten the conversation. And none of these move perplexity
much while all move long-context task accuracy.

---

## 16. LLM system design: from weights to service

**Why.** Everything before this entry assumes a model that answers when
asked. Production adds a queue, a rate limiter, tool calls that can
fail, a context window that fills and a bill — and most incidents come
from those, not from the weights.

**What.** The service is a gateway (auth, quotas, version pinning), a
router that chooses a model and a replica, pools of GPU replicas
holding weights plus KV caches, and the loops around them: tool
execution, context assembly, guardrails, autoscaling and cost.

**How.** The topology every deployment converges on:

```
client:    browser → gateway → auth, quotas, model version pin
router:    queue → route by size and SLA → prefill pool | decode pool
fallback:  timeout → retry once → smaller model → error to client
```

Streaming is not a nicety: a 600-token answer at 40 tokens/s takes 15
seconds; SSE shows the first token in under half a second — TTFT is
the user-experience number, TPS the comfort number (entry 11).

Tool calling is the same forward pass with a different output contract:
a schema exposes what calls exist, the model emits a reserved token
plus a JSON body — grammar-constrained decode rules out malformed
output — and the runtime validates, executes and appends until it
answers:

```python
def run_turn(messages, tools, registry, max_rounds=6):
    for _ in range(max_rounds):
        msg = model.generate(messages, tools=tools, temperature=0.0)
        if not msg.tool_calls:
            return msg.content                  # plain answer, stream it
        messages.append(msg)
        for call in msg.tool_calls:
            result = guarded_call(registry, call)  # validate + timeout
            messages.append(tool_message(call.id, result))
    raise RuntimeError("tool loop exceeded round budget")
```

The round budget matters: a model that calls, dislikes and re-calls a
tool does so until the tokens run out — free-text JSON fails 1% of
calls, a thousand incidents per million.

Context engineering is the discipline of what fills the window: system
prompt, history and retrieved documents. Pin the prompt and prefix-
cache it (entry 11); prune history from the oldest turn; retrieve,
cite, then truncate to a fixed chunk budget. When the window runs out:
summarise, drop retrieval, or refuse — silently truncating the system
prompt is how "ignore previous instructions" starts working.

Guardrails sit on both sides: a fast classifier on input, before the
expensive model, and schema and policy checks on output — tool
arguments, PII, citations — each with its own failure path. Autoscale
on queue depth, not GPU utilisation: a saturated card reads 99% busy
at one queued request and at fifty (entry 11). Cost is arithmetic:
output tokens cost three to four times input, so a 600-token reply at
10 USD per million output costs 0.006 USD. Timeouts, hedges, circuit
breakers and pinned versions transfer unchanged; failure modes are in
[mistakes](../mistakes/README.md).

**Advantages.** The architecture is ordinary distributed systems, so
gateways, queues, retries and canaries do what they did before models.
Streaming plus prefix caching turns the common case — shared system
prompt, short answer — into almost no prefill at all. Tool calling
reuses the model as a planner with the runtime as the hands, and
separating prefill from decode lets each pool scale to its own traffic.

**Disadvantages.** Every layer added is a latency budget line: five
milliseconds here and there, and a p99 nobody profiled end to end.
Context assembly is stateful and untyped — a summariser bug or a
mis-ordered tool result corrupts the conversation with no error
anywhere. Tool execution brings your threat model into the prompt: a
model that can call an API can be talked into calling the wrong one,
so validation and permissions are the real security boundary. And the
bill scales with chattiness rather than value.

---

## 17. Evaluation and the honesty problem

**Why.** A model's score on a public benchmark is a number trained on,
tuned against and eventually optimised past. Projects ship on a
leaderboard and regress in production, and the gap is not dishonesty —
it is contamination, prompt variance, and metrics measuring something
adjacent to what you need.

**What.** Perplexity measures next-token fit on held-out text and is
comparable only within one tokenizer. Benchmark suites — knowledge
(MMLU-style, 14k questions over 57 subjects), reasoning (GSM8K-style,
8.5k problems), code (HumanEval-style, 164 functions with tests) —
score exact answers under a fixed prompt. Human preference aggregates
pairwise votes into a rating; agentic evals score whole task attempts.

**How.** Perplexity is the cheapest signal and the most misread:

```text
perplexity = exp( − (1/N) Σ_i log P(t_i | t_<i) )      lower is better
note:      incomparable across tokenizers — different vocab, different
           per-token entropy — and weakly predictive of task accuracy
Elo:        R_A ← R_A + K (S_A − E_A),  E_A = 1/(1 + 10^((R_B−R_A)/400))
Bradley-Terry:  P(A beats B) = exp(r_A) / (exp(r_A) + exp(r_B))
```

A model with lower perplexity on your held-out corpus is usually
better at your task — at the model level, after the tokenizer matches.
It does not predict a specific capability, and it happily improves
while the model gets worse at following instructions.

Static benchmarks fail in three known ways. Contamination: the test
items are on the web, the web is the corpus, so answers can be
recalled rather than derived — detection is n-gram overlap between the
suite and your training shards (entry 4), run before the run rather
than after the paper. Decay: a benchmark solved to 90% no longer
separates models, so it is replaced or augmented with harder private
variants. Prompt sensitivity: a few-shot example order, a reworded
instruction or a chain-of-thought nudge moves a score by several
points, so a number without its prompt is a cherry.

Human preference has its own systematic biases: longer answers win,
confident tone wins, sycophantic answers win, and an Elo ladder assumes
transitivity and stationary players while the models update underneath
it — an LLM judge inherits all of it plus the judge's own preferences.
Agentic evals are closer to reality and noisier: a task taking ten
tool-use steps at 90% per-step reliability succeeds end to end only 35%
of the time, the same compounding that made "emergence" look
cliff-like in entry 1, so one run's pass rate is a distribution.
Red-teaming runs the other ledger — jailbreak suites, refusal rates,
and false-refusal tests, because a model that answers nothing is safe
and useless.

The honest workflow is unglamorous. Hold out a small task-specific set
that never appears in training or tuning; fix the prompts and the
decoding parameters; run five samples and report the spread; pin the
harness version, because a benchmark library update can move a score
as much as a model update; and treat every leaderboard number as a
smoke test you have not reproduced.

**Advantages.** Perplexity is continuous, free and available every step
of training — nothing else gives that resolution. Static benchmarks are
cheap, comparable across labs and reproducible when prompts are
published. Human preference measures what users actually judge. Agentic
evals expose failures single-turn suites never see. And fifty held-out
prompts from your own traffic beat any public suite.

**Disadvantages.** Every family is gameable once public: contamination,
prompt tuning and submission filtering move scores without moving
capability, and saturation removes the dynamic range needed to spot the
one- or two-point regression a fine-tune actually produces. Judges
disagree with each other and with users, and vote aggregation hides
disagreement inside one Elo. Sample variance means a 3-point gap is
often noise — most comparisons use one sample and no interval. And the
workflow that works is maintenance work, so it gets skipped.

---

## 18. Where the work runs: CPU, GPU and the bus between them

**Why.** Every entry above assumes weights already sit on the card when
the step begins. In production the GPU is one participant: a CPU
tokenises, schedules and feeds it, and a bus moves the bytes that decide
whether the GPU ever stays busy. When a run crawls or a replica OOMs at
3% utilisation, the fault is usually in this layer, not in the maths.

**What.** The division of labour is fixed. The CPU owns tokenisation,
chat templating, request scheduling, the data pipeline, Python and
framework dispatch, and host memory. The GPU owns every matmul —
forward, backward, sampling — and the device memory holding weights,
activations, gradients, optimizer state and the KV cache. PCIe or
NVLink moves weights at load, streams batches in and occasionally spills
state out; everything on the hot path stays resident or pays the bus on
every step.

**How.** Per request or per step, the split:

```text
cpu:    bytes → tokenizer (entry 3) → ids → chat template → batch
        scheduler: admission, queues, continuous batching (entry 11)
        dataloader: shard read → pin → async H2D copy
gpu:    ids → embedding → blocks → logits → sample
        training only: backward → gradients → optimizer step
bus:    weights: once at load (or never, if already resident)
        batches: every step, small, overlapped with compute
        checkpoint save: device → host, the slow path
```

Residency is arithmetic, and the arithmetic is entry 5's for training
and entry 10's for serving:

| Resident set | 70B training (AdamW, bf16) | 70B inference (bf16) |
|---|---|---|
| Weights | 140 GB | 140 GB |
| Gradients | 140 GB | — |
| Optimizer (m, v, fp32 master) | ~840 GB | — |
| KV cache | — | 320 KB/token (entry 10) |
| Total before activations | ~1.1 TB | 140 GB + per-stream cache |

A card holds 80 GB. Training must shard (entry 6); serving must quantise
(entry 12) or not exist. Every "out of memory" message is this table
plus activations — nothing more mysterious.

The bus sets the floor for anything that crosses it. A pinned copy over
PCIe 4.0 x16 moves 24-26 GB/s in each direction
([gpu](../gpu/README.md), entry 3), so a cold load of a 140 GB model is
about six seconds — fine at startup, fatal per step. That is why decode
reads weights from device memory at a ~70 ms floor for a 70B (entry 9)
rather than streaming them from host at 5 GB/s, and why checkpoint
saves are scheduled events: 140 GB device-to-host at 25 GB/s is a
5.6-second stall of every rank unless the copy is chunked and overlapped.

Two techniques hide the bus where it cannot be avoided. Pinned
(page-locked) host memory plus non-blocking copies let a stream compute
on batch i while batch i+1 crosses — the double-buffering pattern of
[gpu](../gpu/README.md) entry 11 — and the data pipeline pretokenises
offline so the CPU never runs BPE in the step's critical path.
Frameworks do the rest quietly: PyTorch's caching allocator pools
device memory so a steady step allocates nothing, and an OOM at 95%
free usually means fragmentation or one bad peak (the KV multiplication
of entry 10), not a genuine shortage.

Offload is the escape when device memory simply does not fit.
ZeRO-Offload keeps optimizer state in host DRAM and overlaps its
updates with the GPU's next forward; `device_map="auto"` places whole
layers on the GPU first and spills the rest to host memory; llama.cpp's
`-ngl` moves the first N layers of a quantised model onto the GPU and
computes the rest on CPU cores. Each pays host bandwidth — DDR5 streams
at roughly 50-100 GB/s ([gpu](../gpu/README.md)), one to two orders
below HBM — so a layer on the CPU is an order of magnitude slower per
token, and a mixed CPU+GPU model is latency-banded rather than
uniformly slow.

CPU inference is then a legitimate regime rather than a failure: a 7B
at int4 is about 4.5 GB of weights, and a modern desktop streams DRAM
at ~60 GB/s, giving a decode floor near 15 tokens/s where a 4090 with
the same weights at ~1 TB/s floors near 100. The CPU wins on cost,
privacy and thermals; it loses on throughput, batch size and long
context. The tokenizer is always on the CPU, so a 5,000-token prompt
pays a few milliseconds of Python and merge-table lookups before the
GPU sees anything — small, but a line item at p99.

**Advantages.** Once residency is written as a table, OOM and "GPU
idle" become arithmetic instead of folklore, and the right fix — shard,
quantise, offload, batch — names itself. Overlap makes the bus nearly
free for steady-state traffic, and offline tokenisation removes Python
from the step loop. Offload turns "does not fit" into "runs slower" on
hardware you already own.

**Disadvantages.** Host paths are 10-50× slower per byte, so any
fallback that quietly spills a layer changes the latency profile
without raising an error. Pinned memory competes with the page cache
for host RAM. Unified memory hides a cliff: paging works until it
thrashes, then everything stops. Two memory managers — host and device —
mean two places to leak, and the frameworks' allocators report neither
clearly. And per-kernel launch overhead from Python can cap decode
before the GPU's bandwidth does — the problem entry 21's graphs exist
to delete.

---

## 19. Model storage and formats: the artefact you actually ship ★

**Why.** "A 70B model" describes the maths, not the thing you deploy.
The artefact — a hundred-odd gigabytes of tensors, a tokenizer, a chat
template, maybe quantisation metadata — decides load time, memory
mapping, corruption behaviour, supply-chain safety and which runtimes
can open it. Teams that treat formats as an export step discover every
one of these properties in production, at three in the morning.

**What.** A sharded safetensors directory is the interchange default: a
JSON header describing every tensor's dtype, shape and byte offset,
followed by raw little-endian payload, mmap-able and free of executable
content. GGUF is llama.cpp's single-file container: quantised tensor
types, tokenizer and metadata baked in, one file a complete model.
PyTorch `.pt`/`.bin` is pickled Python — expressive and unsafe to load.
ONNX and similar IRs export a graph for cross-runtime execution.
Checkpoints (weights plus optimizer state, entry 5) are a fourth thing
entirely and are never shipped.

**How.** The directory a framework actually reads:

```text
model/
  config.json                    architecture: layers, heads, RoPE base, GQA
  generation_config.json         defaults for sampling
  tokenizer.json                 BPE merges and vocab (entry 3)
  chat_template.jinja            role markers — must match SFT (entry 7)
  model.safetensors.index.json   tensor name → shard file
  model-00001-of-00003.safetensors
  model-00002-of-00003.safetensors
  model-00003-of-00003.safetensors
```

A safetensors file is a header, then bytes. The header is JSON: for
each tensor, its dtype, shape and a `[begin, end)` offset into the
payload. Because offsets are absolute and the payload is dense, the
reader mmaps the file and touches only the pages a load needs — a cold
load streams each shard once from NVMe at ~7 GB/s, and a warm load is
page-cache reads. There is no interpreter in the load path: unlike
pickle, a malicious file cannot execute code, which is why hubs stopped
accepting pickled uploads as the default.

Sharding exists for filesystems and for parallelism, not for the model.
Single files above a few gigabytes trip FAT32 limits, some transfer
tools choke past tens of gigabytes, and one writer is one stream: a 70B
in bf16 becomes three to ten shards of 5-20 GB, each read by a separate
thread at load. The index is the contract — a missing or mismatched
shard fails the load loudly, which is the correct behaviour.

GGUF inverts the design: one file, quantised at save time (Q4_K_M,
Q5_K_S and the rest of entry 12's zoo), with the tokenizer, context
length and chat template embedded as metadata. Distribution is one URL;
llama.cpp mmaps it and offloads layers by count; there is no config.json
to desynchronise. The cost is the flip side — changing the quantisation
means regenerating the file, and non-llama.cpp runtimes must convert
first.

| Format | Container | Quantised in file | mmap | Code-safe to load | Typical reader |
|---|---|---|---|---|---|
| `.pt` / `.bin` | pickle | no | partial | no | torch |
| safetensors | header + raw | usually no | yes | yes | transformers, all major |
| GGUF | custom | yes | yes | yes | llama.cpp, Ollama |
| ONNX | protobuf graph | possible | yes | yes | ORT, TensorRT |
| sharded safetensors | index + shards | no | yes | yes | transformers |

Datasets follow the same logic one layer down: webdataset, parquet or
arrow shards for the training stream (entry 4), pretokenised to binary
so a step is an mmap seek rather than a BPE run. Checkpoints are heavier
still — entry 5's twelve bytes per parameter of optimizer state —
written to parallel filesystems, never shipped, and stripped to weights
plus config on export.

**Advantages.** Safetensors gives zero-copy loads and removes a whole
class of supply-chain attacks; the header is inspectable with `head -c`
and a JSON parser. Sharding buys parallel read bandwidth and dodges
filesystem limits with a loud failure on mismatch. GGUF's single-file
property makes a model one artefact to version, cache and mirror. And
because every stack reads and writes these formats, the artefact
outlives any framework (entry 22) — the decision that is actually
reversible.

**Disadvantages.** Fragmentation: every runtime prefers its own format,
and conversion is a timed operation that can drift template or dtype.
Quantisation baked into the container means a format change is a
re-quantisation, calibration and all. The chat template lives in
metadata and silently desynchronises from SFT (entry 7's failure mode,
now a packaging bug). Sharded indexes go stale when files are copied by
hand. Pickled artefacts remain in older hubs, and "just load it" is
still unsafe until you have checked what it is.

---

## 20. Transmitting weights: from hub to replica

**Why.** A cold replica is a network problem before it is a model
problem: 140 GB over residential broadband is an afternoon, and
autoscaling, deploys and node failures each pay that bill. How weights
move — link rate, caching, sharding, warmup — decides whether
multi-replica serving is operable at all.

**What.** Weight distribution is bulk transfer once per node, not per
request: pull from a hub CDN or object store, cache locally, mmap into
every process on the node. Training traffic is the opposite shape —
entry 6's collectives are small, every step, latency-bound — and rides
RDMA or NVLink instead. The numbers that matter are link rate against
artefact size, plus the warmup that follows the last byte.

**How.** The bandwidth table, against entry 19's artefacts:

| Path | Practical rate | 8B bf16 (16 GB) | 70B bf16 (140 GB) | 70B int4 (37 GB) |
|---|---|---|---|---|
| Home broadband 100 Mb | ~10 MB/s | ~27 min | ~4 h | ~1 h |
| Office 1 GbE | ~100 MB/s | ~2.7 min | ~23 min | ~6 min |
| Cloud egress, 10-25 Gb | ~1.2-3 GB/s | ~10 s | ~1-2 min | ~15-30 s |
| Same-AZ object store | ~3 GB/s | ~6 s | ~47 s | ~13 s |
| Local NVMe, already cached | ~5-7 GB/s | ~3 s | ~20-28 s | ~6 s |

The operational conclusion is one line: weights belong on local NVMe or
in the page cache, pulled once per node, not once per replica. mmap
plus the kernel's shared page cache means eight processes on one node
read one copy of the bytes; baking the artefact into a machine image or
a pre-warmed volume turns every subsequent start into a local read. The
first pull from the hub is parallelised — the index from entry 19 names
the shards, ranged GETs fetch them concurrently, checksums verify, and
a resume continues from the last complete shard.

Cold start is larger than the download. A replica becomes ready only
after: bytes on disk, header parsed, pages faulted or streamed, CUDA
context and kernels compiled (entry 21's first-call cost), and one
warmup forward that touches every layer. A health check that greps an
HTTP port marks the instance ready in the middle of that sequence, and
the first real request eats the compile. Readiness must run an actual
generate.

Fine-tunes ride a different path, and this is the operational reason
adapters won: a base model is pulled once and a LoRA adapter (entry 7)
is a few megabytes of safetensors pushed in seconds. Behaviour deploys
become artefact deploys; the base is a shared dependency with its own
version pin. Rollback is then a pointer change, not a re-download.

Training-time transmit is a different animal: all-reduce volume per
step (entry 6) crosses the fabric every iteration and is latency-bound,
which is why those runs want InfiniBand or NVLink and GPUDirect RDMA
that bypasses host memory. Do not confuse the two budgets — a cluster
that trains well moves ~280 GB of gradients per step at 70B scale; a
cluster that serves well moves 140 GB once, then never again.

**Advantages.** Local caching reduces a recurring network problem to a
first-load problem, and parallel shard fetch saturates any link you are
given. Version pinning plus content-addressed artefacts make rollback
and reproduction mechanical. Adapters decouple behaviour release cadence
from base-model cadence — megabytes, not gigabytes, per deploy. And
mmap sharing means replica count on a node is free with respect to
weight bytes.

**Disadvantages.** Egress fees scale with artefact × replicas ×
regions, and a 70B-heavy fleet can spend more on transfer than on
compute during a rollout. Autoscale thundering herds pull the same
shards from the same origin unless a node-level cache absorbs them.
Ephemeral disks forget; a node that reboots pays the full pull again
unless the volume persists. A truncated shard with a bad checksum must
fail loudly, because the silent version of that failure is confident
garbage (entry 19). And the artefact is a set — weights, template,
tokenizer, quant metadata — so moving one file of the set is a valid
way to ship a mismatch.

---

## 21. Compilers and kernels: where the graph is rewritten

**Why.** The same `nn.Module` runs two to five times differently
depending on what compiled it. Eager mode launches one kernel per op
with Python dispatch between each; a compiler sees the whole step,
fuses pointwise chains into matmuls, hoists layout transposes out of
the decode loop and can replay the whole thing as one captured graph.
Compiling is now the default; not compiling is the choice that needs
justifying.

**What.** An ML compiler takes a computation graph — captured from
Python by TorchDynamo, exported as ONNX, or built directly in
XLA/StableHLO — and emits optimised kernels: operator fusion,
memory-layout choice, autotuned tile sizes, precision passes (entry 12)
and static scheduling. Families: in-process (TorchDynamo → Torch
Inductor → Triton or C++), graph export (ONNX → TensorRT, TVM, ONNX
Runtime) and whole-framework (JAX/XLA on TPUs and GPUs).

**How.** What the rewrite does to one transformer step:

```text
eager:    ln → proj → add → gelu → proj → add   (6+ launches, 4+ HBM trips)
fused:    ln+proj fused epilogue; mlp up/gelu/down in one kernel
attention: FlashAttention tile path (entry 15), no n×n materialised
capture:  whole decode step → CUDA graph → replay at ~5 µs launch
```

In PyTorch the pipeline is `torch.compile` → Dynamo frame capture → FX
graph → Inductor lowering → Triton kernels generated per shape →
cached cubin loaded on first use. Modes trade time for speed: default
is the balanced path; `reduce-overhead` leans on CUDA graphs;
`max-autotune` runs a kernel bake-off at first call, which is tens of
seconds paid once per process — and once per replica on every cold
start (entry 20).

CUDA graphs deserve separate mention because decode at batch 1 is
launch-bound before it is bandwidth-bound: a step issues hundreds of
small kernels, each paying Python, dispatch and launch overhead
measured in microseconds. Capturing a fixed-shape decode step and
replaying it removes almost all of that; the constraints are static
shapes and no host synchronisation inside the capture — which is why
servers bucket sequence lengths and why ragged batches break capture.

TensorRT takes the export path to its conclusion: parse the graph,
fuse, insert precision (INT8/FP8 with calibration, entry 12),
autotune per GPU architecture, serialise an engine. The engine is the
fastest common route on fixed hardware — two to four times eager on
inference — and it is hardware-specific: an engine built for Ada does
not run on Ampere, and building one is minutes of autotune. TRT-LLM
wraps that in LLM-specific passes: paged KV, fused attention,
in-flight batching kernels.

XLA plays the same role for JAX and TPU-oriented stacks: whole-program
lowering to a stable IR (StableHLO), with fusion and layout search
done globally. The IR boundary is the portability story — StableHLO or
ONNX lets a model cross frameworks — paid for in dynamic-shape
flexibility.

What resists compilation is predictable: data-dependent control flow,
shapes that change per step without bucketing, and ops that fall back
to eager and reintroduce Python in the hot path. Generation itself is
friendly — fixed shapes per decode step, static KV layout — which is
why graphs and LLM serving compose so well.

**Advantages.** Fusion attacks the memory-traffic problem directly
(entry 9's roofline): fewer HBM round trips at the same FLOPs.
Training steps commonly gain 1.3-2× over eager with no model change;
decode at small batch gains another 1.5-3× from graphs. The graph IR
is where quantisation and precision passes rewrite the model (entry
12), so compilers and formats are the same pipeline at different
stages. And autotuning picks tile sizes no human would hand-tune per
GPU generation.

**Disadvantages.** Compile latency lands squarely on cold start and
autoscaling, unless caches are warmed deliberately. Dynamic shapes
trigger recompiles that multiply that cost silently — a hidden
multi-minute stall after a batch-size change. Debugging a fused Triton
kernel is no longer debugging a Python frame. Engines pin you to a GPU
generation, and Triton/CUDA/toolkit versions pin you to a driver stack.
And the fallback-to-eager trap means a single unsupported op can put
Python back in the middle of the decode loop with no warning.

---

## 22. Frameworks: the stacks that run the models

**Why.** Nobody deploys "an LLM"; they deploy a stack — transformers
plus FSDP for training, vLLM for serving, llama.cpp on a laptop — and
the choice decides which of entries 9 to 21 arrive as configuration and
which as work you must do. It is the highest-leverage systems decision
after quantisation, and it is reversible only at the artefact boundary
(entry 19).

**What.** Two stacks. Training: PyTorch with HF Transformers and
Accelerate, then FSDP or DeepSpeed for sharding (entry 6), Megatron-LM
for very large runs, and fine-tuning kits (axolotl, Unsloth,
LLaMA-Factory) that wrap PEFT and QLoRA (entry 7). Inference:
throughput servers (vLLM, SGLang, TensorRT-LLM, TGI) that own
scheduling and paged caches; lightweight runtimes (llama.cpp, Ollama,
MLX, ExLlamaV2) optimised for one user and modest hardware; and
serving infrastructure (Triton Inference Server, ONNX Runtime) that
hosts whatever was exported.

**How.** The training stack, layer by layer:

```text
your loop or HF Trainer
  → Accelerate: device placement, mixed precision, gather
  → FSDP2 or DeepSpeed ZeRO: sharding (entry 6), checkpoint IO
  → torch.compile / Inductor: fusion (entry 21)
  → kernels: FlashAttention (entry 15), Triton, cuBLAS
```

For a first pretrain or a research run, plain torch plus FSDP2 is the
current default: fewer layers, every knob visible. Trainer plus
Accelerate is right up to roughly 13B and then starts fighting you — at
30B and above the conversation moves to DeepSpeed configs or
Megatron-shaped codebases that assume their layout. Fine-tuning inverts
the priorities: Unsloth or axolotl plus QLoRA turns a 70B adaptation
into a fifty-line config and an overnight run, and the framework's job
is memory tricks rather than research flexibility.

The inference servers are where the gap is widest. Bare PyTorch
generation has none of entries 10-11: no paged KV, no continuous
batching, no chunked prefill, no prefix cache. vLLM and SGLang ship all
four plus an OpenAI-compatible API; writing that yourselves is a
quarter of a systems team's year. TRT-LLM adds the compiler path of
entry 21 with LLM-specific passes and is worth it on fixed hardware at
scale. llama.cpp's edge is different — a tiny memory footprint, GGUF
quantisation, CPU+GPU layer offload (entries 19, 25) — which is why it,
and Ollama on top of it, owns the local and consumer segments.

| Need | Training choice | Inference choice |
|---|---|---|
| Fine-tune ≤13B, one card | Unsloth / axolotl + QLoRA | — |
| Pretrain or full control | raw torch + FSDP2 | — |
| 30B-70B pretraining | DeepSpeed or Megatron, cluster | — |
| Multi-user serving, 7-70B | — | vLLM or SGLang |
| Fixed hardware, max throughput | — | TensorRT-LLM |
| Laptop or single user, CPU+GPU | — | llama.cpp / Ollama |
| Sub-100 ms batch-1 on 24 GB | — | ExLlamaV2 or llama.cpp |

Version coupling is the tax nobody prices: a model architecture needs a
transformers release that knows it; a quantisation kernel needs a
compatible CUDA and bitsandbytes build; a serving stack pins its own
versions of all three. Pin the trio together, and treat a framework
upgrade as the deployment it is (entry 20). Every stack reads and
writes safetensors or GGUF, so the framework decision is the one
decision in this section you can still reverse later.

**Advantages.** The server stacks encode years of scheduling research
as config flags; the fine-tuning kits make large-model adaptation a
laptop-shaped problem. Layered design means each rung can be swapped —
FSDP for DeepSpeed, vLLM for SGLang — without touching the artefact.
And the escape hatch is real: export to safetensors and any other stack
will read it.

**Disadvantages.** Churn is constant: defaults change, APIs break, and
paper code pins to one commit of one repo. The abstraction tax is paid
in the hot path — Python dataclasses per token, dispatcher overhead per
op — until entry 21's graphs take it back. "It works in Trainer" says
nothing about 70B, and every deep config file is a project with its own
failure modes. The optimisations you do not know the framework is doing
for you are also the optimisations it can silently stop doing on an
upgrade.

---

## 23. Training at speed: throughput, fusion and the profile

**Why.** Identical code at 35% MFU versus 60% MFU is a 70% larger bill
for the same model. Throughput is decided by memory traffic,
communication and idle time — none of which appear on the loss curve,
and all of which dwarf arithmetic when they go wrong.

**What.** MFU — model FLOPs utilisation — is achieved FLOPs divided by
hardware peak. Training a model of N parameters on D tokens costs about
6ND FLOPs once the backward pass is counted, so wall time is
`6ND / (MFU × peak_FLOPS)`, and the only question left is what sets the
MFU: compute, HBM traffic, interconnect, or the data pipeline.

**How.** The budget for one token through a 7B under AdamW, bf16, is
roughly 42 GFLOP of arithmetic against a similar order of memory
traffic when gradients, optimizer reads and writes are amortised across
the batch — which is why small batches run bandwidth-bound and only
larger micro-batches lift intensity toward the roofline
([gpu](../gpu/README.md)). Targets that experienced runs hit:

| Configuration | MFU to expect | Below this, look for |
|---|---|---|
| 7B, single node, FSDP2 | 45-60% | launch gaps, small micro-batch |
| 70B, 32-64 GPUs, TP+PP | 35-50% | bubble, stragglers, all-reduce |
| QLoRA fine-tune, one card | 20-40% | dequant kernels, batch 1 |
| Any run, first hour | much lower | JIT/autotune, data warmup |

Measure it honestly: count real training tokens per step (not padded
positions), time the steady-state window after warmup, and use the
tensor-core peak for your dtype and accumulate mode — marketing TFLOPS
numbers for consumer cards are double the dense figure.

Where the time actually goes, and the fix each symptom names:

| Symptom | Likely cause | First fix |
|---|---|---|
| Utilisation 100%, MFU 15% | memory-bound, small batch | bigger micro-batch, fusion (entry 21) |
| Sawtooth utilisation | dataloader stall | pretokenise, pin, more workers, NVMe |
| Step time jumps at scale | all-reduce straggler | overlap, then hunt the slow node (entry 6) |
| First step 10× slower | JIT and autotune (entry 21) | warmup steps, cache directories |
| Tokens/s low, loss fine | serialised grad accumulation | check per-micro-batch launch overhead |

Fusion and precision are the in-process levers: bf16 compute with fp32
master weights (entry 5); fused AdamW kernels that avoid a separate
pass over the moments; FlashAttention's backward (entry 15), which is
where the long-context training cost hides; and `torch.compile`
(entry 21) for the pointwise chains. Activation checkpointing (entry
6) is the trade that must be re-priced at each scale: 30% more FLOPs
for nearly all the activation memory — a clear win when memory-bound,
a net loss once the GPU is already compute-bound.

The data pipeline is the bottleneck most often mistaken for a GPU
problem. Pretokenise to binary shards offline (entry 19), mmap and seek
to a random offset, prefetch into pinned host memory, and hand off with
non-blocking copies ([gpu](../gpu/README.md), entries 6 and 11). The
target is supply: the loader must sustain at least twice the GPU's
consumption or it surfaces as idle gaps at exactly the moment compute
gets efficient.

Communication overlap is the sharded counterpart: DDP overlaps the
all-reduce of micro-batch i with the backward of i+1; FSDP prefetches
the next layer's all-gather while the current one computes; pipeline
schedules run 1F1B to keep the bubble under 20% (entry 6). When memory
— not bandwidth — is the wall, the first lever is a cheaper optimizer:
factored moments (Adafactor) or 8-bit Adam cut entry 5's ~12 bytes per
parameter toward half, at a small and usually invisible quality cost.

The profile comes before every one of these decisions. torch.profiler
with memory and shape recording, or nsys for a timeline: look for gaps
between kernels, host-to-device copies inside the step, tiny kernels
that never saturate the SMs, and one rank consistently behind the
others. Then compute MFU, take the top term, fix it, and re-measure —
the bottleneck moves when you touch it.

**Advantages.** MFU is comparable across models, hardware and labs, so
it turns "the run feels slow" into a number. Profiling reduces the
optimisation surface to a top-three list, and the common wins — fusion,
data pipeline, overlap — stack with everything else in this section. A
1.5× throughput win is either 1.5× less wall-clock or 1.5× less
hardware, and it compounds with every later entry.

**Disadvantages.** MFU measured with the wrong peak or a padded token
count misleads worse than not measuring it. The fastest configuration
often does not fit — maximum micro-batch versus checkpointing is a
real binding constraint, not a preference. Profiling one rank of a
cluster samples the healthy node and misses the straggler. Data
pipeline bugs wear GPU symptoms. And every win moves the bottleneck,
so yesterday's tuning pass is tomorrow's baseline.

---

## 24. Inference optimisation: the decision stack ★

**Why.** Entries 9 to 15 and 21 each buy one piece of inference
performance; production teams need the order, because doing them out of
sequence wastes weeks — you cannot batch what does not fit, and you
cannot compile what you are about to requantise. The ladder below is
the order that makes each rung multiply the last rather than fight it.

**What.** Five stages, each with a gate that tells you when to move
on: (1) make it fit — quantise until weights plus expected KV are
resident; (2) make each token cheap — schedule and batch until the
card is saturated; (3) share the reads — prefix and page caches;
(4) make the kernel fast — compile and capture; (5) make tokens arrive
sooner — speculate, then tune the whole thing to the latency SLO
rather than to maximum throughput.

**How.** The ladder, with the entry each rung buys and the signal that
says the rung is done:

```text
fit:        int4/int8 (entry 12) → weights resident, no page-in
            gate: no OOM at max batch × max context
schedule:   continuous batch + chunked prefill (entry 11)
            gate: GPU busy >80%, no head-of-line blocking
cache:      paged KV + shared prefix (entry 10)
            gate: system prompt read once per node, not per stream
compile:    torch.compile / CUDA graphs / TRT (entry 21)
            gate: launch overhead gone, decode ≈ bandwidth floor
speculate:  draft k = 4-6, accept rate > 0.6 (entry 11)
            gate: measured TPS up end to end, not just acceptance
slo:        batch to the TTFT budget (entry 11), never to the maximum
```

Which rungs apply depends on the scenario, and the table beats the
ladder when the hardware is fixed:

| Scenario | Do first | Then | Skip |
|---|---|---|---|
| 8B on 24 GB, one user | int4 GGUF via llama.cpp | mmap, full `-ngl` offload | speculation |
| 8B on 80 GB, multi-user | vLLM, fp16 or int8 | continuous batch, prefix cache | offload |
| 70B, few users | int4 across 2×24 GB | small draft model | full fp16 |
| SLO: TTFT < 200 ms | chunked prefill | prefix cache, small queues | large batches |
| SLO: throughput per dollar | max batch | disaggregate prefill/decode | per-stream fairness |

Two rules override the ladder when they apply. Bytes per weight set the
floor (entry 9's 70 ms for a bf16 70B; 18 ms at int4), and batch size
is how close you get to it — so if the model does not fit, nothing else
matters. And TTFT and per-stream tokens/s pull the batch in opposite
directions (entry 11): a scheduler that maximises throughput will miss
an interactive SLO at p95 while looking excellent on the mean.

Measurement discipline is part of the stack, because tuning against the
wrong harness tunes the wrong system. Fix the prompt-length mix
(entry 17's variance lesson applies to latency too), pin the artefact
you will actually ship — the int4 build, not the bf16 you developed
against — and report TTFT and tokens/s at p50 and p95 before and after
each rung. A rung that wins on a microbenchmark and loses on the mix is
a regression with good marketing.

The anti-patterns are stable across teams: quantise, then re-quantise
"for the benchmark"; enable a draft model that rejects half its tokens
and call it slow; run fp16 on a card whose bandwidth says int4; tune
kernels while the scheduler is still static-batching; and optimise at
batch 1 when production runs at 32. Each is fixed by returning to the
gate the ladder already wrote down.

**Advantages.** The ladder is order-preserving — the early rungs are
multiplicative, not competitive, and each gate is measurable without a
benchmark suite. Most of the wins (quantise, schedule, cache) require
no change to the model, only to the artefact and the server (entries
19, 22). The scenario table turns architecture meetings into lookups.
And because every rung is reversible at the artefact boundary, a
deployment that overshoots an SLO can climb back down one rung instead
of restarting the design.

**Disadvantages.** The correct order depends on an SLO, and an
unstated SLO guarantees the wrong ladder — throughput teams and latency
teams should not share a tuning pass. Each rung adds a moving part with
its own failure mode: quantised artefacts, schedulers, draft models,
compiled engines. Later rungs can mask earlier mistakes — a fast kernel
on a thrashing cache looks like progress — so gates must be rechecked
after any rung. And once serving, the traffic mix can invalidate the
whole pass: a ladder tuned on 800-token prompts is wrong for 8,000.

---

## 25. Consumer-grade GPUs: training and inference on one card ★

**Why.** The RTX-class card in a desktop is the most common LLM
hardware on earth, and it differs from the datacentre cards every other
entry assumes in ways that decide the real numbers: 12 to 32 GB rather
than 80, no ECC, no NVLink on the current generation, thermals instead of
steady server clocks, and a driver that resets the card if a kernel
runs too long. Every figure elsewhere in this section should be
re-derived once on this hardware.

**What.** Consumer workstation reality, against the A100 the rest of
the section treats as a floor:

| Property | RTX 3060 | RTX 3090 | RTX 4090 | RTX 5090 | A100 80 GB |
|---|---|---|---|---|---|
| VRAM | 12 GB GDDR6 | 24 GB GDDR6X | 24 GB GDDR6X | 32 GB GDDR7 | 80 GB HBM2e |
| Bandwidth | 360 GB/s | 936 GB/s | 1008 GB/s | 1792 GB/s | 2039 GB/s |
| FP32 | ~13 TFLOPS | ~35 TFLOPS | ~83 TFLOPS | ~105 TFLOPS | ~19 TFLOPS |
| NVLink bridge | no | yes, 2-way | no | no | yes, 600 GB/s |
| ECC | no | no | no | no | yes |
| TDP | 170 W | 350 W | 450 W | 575 W | 400 W |

Decode is bandwidth-bound, so its floor tracks the bandwidth column
within 10-15% — a 3060 floors at about a third of a 4090 for the same
weights — while prefill tracks tensor throughput, where the gap to an
A100 is larger. No ECC means a flipped bit during a long run produces
confident garbage rather than a machine check; no NVLink on the 4090
means two cards talk over PCIe, where peer-to-peer DMA between GeForce
cards is restricted and NCCL falls back to host-staged copies. The
3090's optional bridge is the one consumer exception. The 3060 is the
odd entry here: half the FP32 of the pack, but 12 GB at a price and a
170 W envelope nothing else matched for years, which made it the
default budget LLM card and the baseline for the 12 GB numbers below.

**How.** What fits in 24 GB, using entries 10 and 12's arithmetic:

| Model | Format | Weights | Left for KV and workspace | Verdict |
|---|---|---|---|---|
| 8B | bf16 | 16 GB | ~6 GB | fine: batch 4-8 at 8k context |
| 8B | int4 | ~5 GB | most of the card | long context or many streams |
| 13B | int4 | ~7 GB | comfortable | llama.cpp sweet spot |
| 70B | int4 | ~36 GB | — | needs 2 cards or offload |
| 70B | int4, partial offload | 24 GB on card + host | CPU layers | ~10-20 tok/s class |

On 12 GB — the 3060's budget tier, roughly 11 GB usable once the
desktop compositor has taken its cut — the arithmetic is tighter but
the verdicts are still arithmetic. The usual table, re-derived:

| Model | Format | Weights | Verdict on 12 GB |
|---|---|---|---|
| 7-8B | int4 (Q4_K_M) | ~5 GB | the sweet spot: 8k context, small batch |
| 7-8B | int8 | ~8 GB | fits, less KV headroom than Q4 |
| 7-8B | bf16 | 16 GB | no — does not fit, at all |
| 13-14B | int4 | 7-9 GB | fits; 8k context only if the quant is lean |
| 70B | int4 | ~36 GB | no; partial offload at 360 GB/s is ~5 tok/s |

The 12 GB lesson generalises: int4 weights plus a few gigabytes of KV
(entry 10) is the whole budget, so a 7-8B at Q4_K_M with 8k context
leaves room to breathe and a 14B Q4 is the practical ceiling. Anything
in bf16 above about 5B parameters is out, and 70B partial offload —
technically possible — spends most of its life waiting on the host
link that entry 18 priced.

Inference recipes, in the order most people should try them:

```bash
# single user, best fit for the hardware
llama-cli -m model-Q4_K_M.gguf -c 8192 -ngl 99 --temp 0.7
# or Ollama, same runtime with a server and a library
ollama run llama3:8b

# multi-user on the same card: vLLM with int4 weights, bounded context
vllm serve ./awq-8b --quantization awq --max-model-len 8192 \
    --gpu-memory-utilization 0.92 --max-num-seqs 16
```

Expectations, order-of-magnitude and context-dependent: an 8B at Q4_K_M
streams 100-160 tok/s on a 4090 and 35-50 on a 3060 — the ratio is
essentially the bandwidth column, 360 against 1008 GB/s; a 3090 sits
within ~10% of a 4090 on decode for the same reason. A 13B runs 60-90
on a 4090 and 18-25 on a 3060; on 24 GB a 70B at Q4 can only be
partially offloaded — about half the layers on card, the CPU computing
the rest — for 10-20 tok/s, while on 12 GB that same trick is not worth
running. vLLM or SGLang is the answer as soon as a second user appears:
the batching of entry 11 is exactly what either card is starved for at
batch 1, and a 3060 serving two or three streams of an 8B Q4 can beat
a 4090 serving one at batch 1.

Training on one consumer card is fine-tuning territory. The workable
envelope:

| Recipe | Feasible size | Notes |
|---|---|---|
| QLoRA, 4-bit base + LoRA r16 | 7-8B on 12 GB, up to ~30B on 24 GB | batch 1-4 × grad accum, seq 1k on 12 GB / 2k-4k on 24, checkpointing on |
| LoRA in bf16 | 8B on 24 GB only | 16 GB weights + adapter moments; does not fit 12 GB |
| Full fine-tune, bf16 | ~7B on 24 GB, borderline | needs 8-bit optimizer + checkpointing; rarely worth it |
| Full fine-tune, 13B+ | no, not on one card | two 3090s at best, with the PCIe tax |

A 10-50k-example QLoRA run of an 8B finishes overnight on a 4090 and
in a day or two on a 3060 at sequence 1k; Unsloth and axolotl (entry
22) buy roughly 2× over naive HF loops by fusing the kernels entry 21
would otherwise compile lazily, and on 12 GB that 2× is also the
difference between a 1k and a 2k sequence fitting. Gradient
checkpointing is on by default in these kits for a reason: at 4k
context it is the difference between fitting and not, and at 12 GB it
is on from the first step. Two 3090s versus one 4090 is a real
decision — the pair wins on VRAM (a 13B in bf16, or
a 70B at int4 across both) and loses on every byte that must cross
PCIe, so fine-tuning usually prefers the single 4090's simplicity and
inference prefers the pair's capacity.

Operational habits that only matter on this hardware:

```bash
nvidia-smi                                   # VRAM, power, throttle reasons
nvidia-smi dmon -s pucm                      # per-second clocks and temps
nvidia-smi -pl 400                           # cap power for sustained clocks
                                             # (170 on a 3060 — usually left alone)
watch -n1 'nvidia-smi --query-gpu=temperature.gpu,memory.used --format=csv'
```

The desktop compositor eats 0.5-1 GB of VRAM before any model loads —
budget for it or OOM at the last allocation (entry 10's multiplication,
now with a browser tab). On 12 GB that gigabyte is a tenth of the card,
so close every other consumer before a long generation. GDDR6X runs
hot: thermal throttling silently
cuts the bandwidth column above, and an undervolt often raises
sustained tokens/s by keeping boost clocks — a 3060's plain GDDR6 and
170 W envelope rarely needs either. On Windows the display
watchdog resets cards whose kernels exceed its timeout — Linux avoids
most of that, and long-context FlashAttention calls are the usual
trigger. Reserve 1-2 GB of headroom at all times, checkpoint
aggressively (entry 5: a driver reset is a lost run otherwise), and
validate outputs with checksums when a run exceeds a few hours —
without ECC, silent corruption is a real, rare, expensive failure.

**Advantages.** Price per token is unmatched: a 4090 floors within
~2× of datacentre decode bandwidth for a twentieth of the price, and
24 GB covers the 8B-13B models that serve most real workloads. The
12 GB tier goes further down the same curve — a 3060 runs an 8B at Q4
and overnight QLoRA of the same size on a 170 W supply, which is the
cheapest genuine entry into this section. The
consumer stacks — llama.cpp, Ollama, Unsloth — were built for exactly
this envelope, so the tooling does not fight the hardware. Fine-tuning
an 8B with QLoRA is an overnight, single-card, few-hundred-dollar
exercise that would have been a research grant a generation ago. And
two cards plus int4 puts a 70B on a desk, slowly.

**Disadvantages.** 24 GB is a hard wall at 70B, at long-context 8B
batches and at anything trained in full precision with an AdamW state
(entry 5's 12 bytes per parameter makes the wall arithmetic: 7B × 12 is
84 GB before a single activation) — and 12 GB is the same wall at
bf16 above ~5B, at 14B+ in any format, and at QLoRA sequences past
2k. No ECC, no reliable P2P and no
multi-instance isolation mean the datacentre playbook cannot be copied
across. Thermals and power limits turn datasheet peaks into thirty-
second jokes, and a driver reset mid-run loses everything since the
last checkpoint. The hobbyist stacks trade safety margins for
convenience — context lengths, padded batches and defaults tuned for
laptops — so anything beyond single-user inference should re-derive its
numbers rather than trust a blog post, including this one.

---

## 26. Fine-tuning for tools, agents, coding and engineering tasks ★

**Why.** A general instruct model can explain a tool call, but reliably
using tools requires a different behavior: select the right action,
produce arguments that satisfy a schema, wait for the result, recover
from errors and stop when the task is complete. Coding, 3D and PCB tasks
also have external truth — tests, geometry validators or electrical
checks — that a fluent final answer cannot replace. Fine-tuning is useful
when the model repeatedly misses a behavior pattern; the tools and
validators remain responsible for doing and checking the actual work.

**What.** Domain adaptation can target one or more layers of behavior:

| Goal | Best first lever | Fine-tune when… |
|---|---|---|
| Current facts about a repository, CAD library or component catalog | Retrieval (RAG) from versioned sources | The model fails to use retrieved context consistently |
| Deterministic execution: tests, Blender, KiCad, simulation | Explicit tools with typed inputs and bounded permissions | The model chooses the wrong tool or produces weak arguments repeatedly |
| Output syntax: JSON, CAD command schema, valid function arguments | Constrained/structured decoding plus schema validation | The model cannot reliably plan values for the schema |
| Stable style, vocabulary or task procedure | SFT/LoRA on curated examples and trajectories | A held-out evaluation shows a consistent, learnable behavior gap |
| Outcome preference or verified multi-step success | Preference optimization or RL with a trustworthy reward | SFT has taught the interface but the model still chooses poor actions |

Fine-tuning changes the policy; it does not add a live compiler, geometry
kernel, web browser, KiCad library or simulator to the weights. For
rapidly changing facts, use retrieval. For exact computation and state,
use tools. For desired interaction patterns and domain-specific
decision-making, fine-tune. Most robust systems use all three.

**How.** Start by fixing the target interface: base model and tokenizer,
chat template, tool schemas, tool-call parser, allowed tool names,
argument validation, timeouts, execution environment and success metric.
Training examples must use the same message and control-token conventions
as inference. A typical supervised trajectory has an instruction, an
assistant tool call, the tool's observation and the assistant's next
decision or final response:

```json
{
  "messages": [
    {"role": "user", "content": "Fix the failing parser test and explain the change."},
    {"role": "assistant", "tool_calls": [{"type": "function", "function": {"name": "run_tests", "arguments": "{\"target\":\"./parser/...\"}"}}]},
    {"role": "tool", "tool_call_id": "call_1", "content": "Test failed: expected token 'IDENT', got 'ILLEGAL'."},
    {"role": "assistant", "tool_calls": [{"type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"parser/lexer.go\"}"}}]},
    {"role": "tool", "tool_call_id": "call_2", "content": "...source excerpt..."},
    {"role": "assistant", "content": "I corrected the identifier boundary check. The parser test now passes."}
  ]
}
```

This is a schematic JSONL example, not a universal trainer format. The
trainer's chat template determines the serialized tokens and whether the
loss is masked on user/tool observations or applied to assistant calls
and answers. Keep the tool schema and the runtime behavior versioned
alongside the dataset. Include successful trajectories, appropriate
recovery from tool errors, refusals for unavailable/unsafe actions and
short successful paths. Do not train on fabricated tool results as if
they were observations from a real environment.

For an initial behavior change, use supervised fine-tuning and a LoRA
adapter before considering full fine-tuning. The base handles general
language; the adapter teaches the desired tool and response patterns.
Preserve a held-out set split by repository, project, board or scene —
not merely random adjacent messages — so near duplicates cannot leak
across train and evaluation. Compare the same base and adapter under the
real serving template, tools and decoding settings.

### Tool calling and agentic trajectories

Tool-call training should teach the complete loop, not just JSON-shaped
text:

```text
goal → choose tool → emit schema-valid call → validate permissions
     → execute in sandbox → observe actual result → continue or stop
```

Make tool names and descriptions discriminative, keep arguments typed,
bound action count/time/output size, and return structured error
observations. Include examples where a tool returns partial output,
fails, times out or reports no change. Train the assistant to inspect
results before claiming success and to ask for missing information
rather than inventing arguments. Constrained decoding can guarantee
syntactic validity for a supported schema; it does not guarantee a
correct tool choice, semantically valid values or a successful action.

Agentic fine-tuning uses multi-step trajectories: observations, actions,
tool outputs, intermediate decisions and terminal outcome. SFT can
imitate high-quality demonstrations. Preference training can rank
successful, efficient trajectories over incomplete or wasteful ones. RL
is appropriate when the environment supplies a reliable, automatable
reward — passing tests, meeting a geometric tolerance, or passing
electrical rules — and the environment is safe and repeatable. Reward
must measure the actual task rather than a proxy like number of tool
calls or confident narration; agents will optimize whatever is scored.

Use held-out tasks and run the agent against the actual tool environment.
Track task completion, valid-action rate, tool error/retry rate, steps,
latency, cost, regressions, and unsafe or out-of-scope actions. Keep the
agent loop bounded and make side-effecting tools idempotent where
possible. Training only on successful traces leaves the model unable to
recover from real errors; training on unchecked traces teaches the wrong
behavior with high confidence.

### Coding agents

For coding, train on repository-grounded work rather than isolated
“write a function” prompts alone. Useful trajectories include issue or
specification → search/read files → propose a small patch → run focused
tests/lint/type checks → inspect failures → revise → report verified
results. Tool interfaces might expose read/search, patch/edit, test,
build and diagnostic tools, but the runtime must enforce working
directory boundaries and review policy.

| Skill | Example supervision | Evaluation beyond language quality |
|---|---|---|
| Code completion | Context + expected code under the target language/style | Compile, unit tests, hidden tests |
| Bug fixing | Reproducer/failing test + repository context + patch trajectory | Tests pass and unrelated tests remain green |
| Refactoring | Constraints + patch + compatibility explanation | Behavioral equivalence and static checks |
| Repository navigation | Question + search/read tool calls + cited result | Evidence points to correct files/lines |
| Build/test operation | Build logs + diagnostic tool call + follow-up | Reproducible command and verified exit status |

Use a diverse language/project set and separate evaluation repositories
from training. Public code may be in pretraining data, so measure on
private or newly created tasks and report contamination limits. Fine-tune
the model to follow your code-review and tool workflow; use retrieval for
the current repository and its documentation, and real compilers/tests
for correctness.

### 3D and CAD workflows

First decide whether the target is an LLM agent that operates a CAD
system or a model that directly generates geometry. These are different
training problems. An LLM LoRA can teach a capable language model to
produce structured scene descriptions, parametric CAD commands or
Blender scripts; it does not by itself give the base model a learned 3D
mesh decoder. Direct image/text-to-3D generation needs a compatible
multimodal/geometry model and its own 3D training or inference pipeline.

For a tool-using 3D agent, curate examples with explicit units,
coordinate frames, object dimensions, constraints, material/camera
settings and the actual tool result. Include a validate-render-inspect-
revise loop instead of only prompt-to-script pairs. Evaluate generated
assets with task-specific checks such as successful scene execution,
dimension tolerance, mesh validity/watertightness where required,
collision/intersection constraints, and rendered visual similarity.
Use Blender/CAD tools as the geometry authority; use rendered images or
structured measurements as observations for the next agent step.

### PCB design agents

For PCB work, teach the model to operate a versioned EDA toolchain rather
than to describe a plausible board in prose. A useful supervised trace
contains the specification and constraints, component/netlist choices,
schematic or board operations, library/footprint versions, ERC/DRC or
simulation output, the repair steps, and the final exported artifacts.
The tool layer should validate every identifier and coordinate and
should preserve the source design for review.

| Workflow stage | Useful training signal | Ground-truth validation |
|---|---|---|
| Requirements → schematic | Correct symbol/pin/net choices and explicit assumptions | ERC, netlist comparison, SPICE where applicable |
| Parts and footprints | Correct MPN, package, pad mapping and orientation | Approved library, datasheet and symbol/footprint consistency |
| Placement and routing | Constraint-aware operations and repair decisions | Connectivity, clearance, DRC and return-path review |
| Final board package | Correct layers, drill, BOM and manufacturing outputs | Gerber/drill inspection, fabrication rules, BOM checks |

Use authoritative component and footprint libraries through retrieval
or tools because their contents and availability change. Grade task
completion with ERC/DRC/connectivity and manufacturing checks, not just
whether the generated file parses or the assistant says “DRC clean.” A
model can optimize a proxy by omitting constraints or suppressing
violations; keep independent checkers and inspect actual reports.

### Deployment and iteration

Adapters are a practical release unit: keep the base immutable, version
each adapter with its training/evaluation set and tool-schema revision,
then serve or roll back them independently. vLLM supports LoRA serving
for compatible models; exact adapter formats, limits and CLI options are
version-dependent (see [the vLLM guide](vllm.md)). Test adapter selection,
tool parser behavior and base-model fallback under concurrent requests.

The iteration loop is:

```text
baseline eval → collect failure cases → classify (knowledge / policy /
tool schema / reasoning / environment) → choose RAG, tools, SFT or RL
→ train adapter → evaluate offline → test in sandbox → canary → monitor
```

Only fine-tune after the failure has been classified. A missing current
fact calls for retrieval; unavailable actions call for a new tool;
malformed calls call for schema/template fixes before more training; a
reasoning or action-selection gap may justify trajectories and adapter
training. Re-run the same evaluation after every tool, model, template or
adapter revision.

**Advantages.** Domain trajectories can make a general model reliably
follow a specialized tool protocol without retraining all its weights.
LoRA adapters allow task-specific behavior to be versioned and switched
while sharing a base. Tool feedback and deterministic validators provide
objective signals for coding, geometry and circuit-design tasks.

**Disadvantages.** Training data can leak repository secrets, private
designs or proprietary code; licensing and access rules still apply.
Adapters can overfit one project, tool version or reward proxy. Structured
outputs guarantee shape, not correctness, and a capable agent can still
make unsafe tool choices. External tools add latency and failure modes,
so evaluation must cover end-to-end trajectories, permissions and
recovery—not only next-token loss.

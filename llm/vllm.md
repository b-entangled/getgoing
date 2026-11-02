# vLLM: serving from request to token

This guide extends the inference and system-design material in
[`README.md`](README.md), especially entries 9–16 and 18–25. It covers why
vLLM exists, how its serving engine works, how to run it and inspect its
metrics, how to optimize CPU/GPU deployments, and how to scale beyond one
replica. It assumes the reader understands the prefill/decode split and
KV-cache arithmetic introduced in the main guide.

The five-part contract applies throughout: the need (**Why.**), precise
definition (**What.**), mechanics and runnable examples (**How.**), gains
(**Advantages.**) and practical costs (**Disadvantages.**). vLLM changes
quickly; exact feature support, CLI names and metric labels are release-
dependent. Pin a release and check the documentation for that release
before using a production configuration.

---

## 1. Why vLLM: the serving problem

**Why.** `model.generate()` proves that weights can produce tokens; it
does not make a useful multi-user service. A production server must keep
the GPU fed while prompts arrive, streams finish at different times,
KV-cache memory fragments, clients cancel, and latency targets compete
with throughput. vLLM packages those serving mechanisms into one engine
and an HTTP API, so teams can focus on the model and workload rather
than rebuilding a scheduler, cache manager and streaming protocol.

**What.** vLLM is an inference and serving runtime for language models.
It loads supported model architectures and weight formats, schedules
requests, executes model steps on supported hardware, manages KV-cache
memory, samples tokens and exposes results through APIs. It is not a
training framework and is not itself a model. Its central design idea
is to treat generation as dynamically scheduled token work, with KV
blocks allocated as needed, instead of reserving one large, fixed cache
buffer per request.

**How.** The inference path covered in the main guide has four major
pieces: model weights, a scheduler, per-request sequence state, and the
KV cache. vLLM coordinates them in one serving runtime. Its API server
accepts requests; an engine schedules prefill and decode work; model
workers execute forward passes; and generated token IDs are returned to
the client, often as a stream. The design is useful because real traffic
contains a mixture of prompt lengths, output lengths and arrival times,
not one static batch.

The feature map below lists major capability families, not a promise
that every model, backend and feature combination is supported:

| Capability | What it provides | Typical use |
|---|---|---|
| Online serving | OpenAI-compatible completions, chat, responses, embeddings and supported audio APIs; streaming; Anthropic Messages and gRPC integrations in supported releases | Existing clients connect with a base URL and model name |
| Offline inference | Python API for batched prompts and generation without an HTTP server | Evaluation, batch jobs, synthetic data |
| Scheduling and cache | Continuous batching, paged KV cache, chunked prefill, automatic prefix caching | Multi-user throughput and long prompts |
| Model tasks | Text generation and, for supported models, pooling tasks such as embeddings, scoring or classification | Generation, retrieval and reranking pipelines |
| Multimodal | Supported vision, audio and other modality models, with modality-specific preprocessing and cache paths | Image QA, speech and multimodal assistants |
| Decoding | Sampling, constrained/structured output, tool calling, reasoning parsers, and selected beam/best-of modes | Applications that need valid JSON or tool use |
| Adapters | LoRA and multi-LoRA serving for supported architectures | Serve many domain adapters over one base |
| Performance | Attention backends such as FlashAttention, FlashInfer, FlashMLA, TRT-LLM-GEN and Triton (hardware/model-dependent); tuned GEMM/MoE kernels; piecewise/full CUDA or HIP graphs; `torch.compile` | Improve throughput, latency or cost |
| Model memory | FP8, MXFP8/MXFP4, NVFP4, INT8/INT4, GPTQ, AWQ, GGUF, compressed-tensors, ModelOpt, TorchAO and KV-cache dtype options, subject to backend support | Fit weights and concurrent context in device memory |
| Speculative decoding | Draft-model and other supported approaches including n-gram/suffix matching and model-specific EAGLE/DFlash methods | Reduce target-model decode steps when acceptance is high |
| Parallelism | Tensor, pipeline, data, expert and context parallel approaches, depending on model and release | Fit large models or increase aggregate serving capacity |
| Deployment | Prometheus metrics, health/API endpoints, configurable CLI/YAML, container images and distributed execution options | Operate as a service and integrate with platform tooling |

**Advantages.** A serving engine absorbs the hardest common inference
systems work: dynamic scheduling, paged cache management, streaming,
metrics, model execution and multi-GPU support. The OpenAI-compatible
interface reduces application integration effort. Continuous batching
and shared prefixes can turn otherwise idle memory bandwidth into
throughput, while supported quantization and parallelism let one engine
cover a range of model sizes and hardware.

**Disadvantages.** Feature breadth creates a compatibility matrix:
model architecture × hardware × dtype × quantization × decoding feature
× parallelism. An option may exist but have no fast kernel for a given
combination. Batching increases throughput by sharing work and can worsen
individual latency; replicas duplicate weights and cache state; model
parallelism adds synchronization; and cache size still limits long
contexts.

---

## 2. Internals: processes, scheduler and the request path

**Why.** When a request is slow, the cause may be HTTP processing,
tokenization, queueing, prefill, decode, cache pressure or network
transfer. Knowing which process owns each step turns a vague “GPU is
slow” incident into a measurable diagnosis.

**What.** In vLLM V1, the major process roles are the API server, engine
core and model workers, with a data-parallel coordinator in some
configurations. The API layer handles HTTP, request validation,
tokenization and media preparation. The engine core owns scheduling and
KV-cache state. Worker processes load model weights and execute model
forward passes on their assigned devices. For model-parallel deployments,
the worker count scales with the configured ranks; data parallelism adds
engine cores. Process topology and APIs are implementation details that
may change between releases.

**How.** One request travels through these steps:

```text
client
  │ HTTP / streaming
  ▼
API server ── tokenize, validate, chat template, multimodal inputs
  │ engine client / IPC
  ▼
engine core ── admission → scheduler → sequence/KV block tables
  │ model execution request
  ▼
workers ── model layers → logits → sampling
  │ token IDs / finished request
  └──────────────────────────────► API server ── stream tokens to client
```

For chat messages, the server applies the model's chat template and
tokenizes the result. It prepares supported media inputs, admits the
request subject to limits, and schedules prefill. Prefill computes prompt
representations and fills cache blocks. Decode advances active sequences
one or more steps; at each scheduling opportunity, finished or cancelled
work leaves and waiting work may enter. Sampling applies the request's
decoding settings, and the API streams token text until a finish reason.
The HTTP connection, engine request ID, sequence state and cache blocks
are related but distinct objects. Cancellation must reach the engine to
stop work and release cache state.

The scheduler is an iteration-level admission controller. **Continuous
batching** lets a finished sequence leave without making unrelated
sequences wait for the longest generation. **Chunked prefill** divides a
large prompt into schedulable pieces, limiting how long prompt compute
can block decode. **Prefix caching** reuses KV for repeated prompt
prefixes; it is most valuable when requests share long system prompts.
These mechanisms control different costs: batching amortizes weight
reads, chunking controls interference, and prefix reuse avoids repeated
prefill.

### Paged KV blocks

Paged KV allocation is the key memory abstraction. A sequence maps
logical token positions to fixed-size cache blocks through a block table.
Blocks come from a pool and are allocated as context grows; a request
does not need one contiguous maximum-length allocation. Completed
requests release blocks for reuse, and matching prompt prefixes can
share cached blocks where prefix caching is enabled and the prefix is
identical under the model's tokenization and cache rules. Attention
kernels read through the mapping.

Paged allocation reduces reservation waste and makes dynamic batching
practical; it does not make KV bytes disappear. Long contexts and high
concurrency can still exhaust cache. The original PagedAttention paper
explains the idea, but current kernel implementations are release- and
backend-dependent rather than a literal copy of the paper's kernel.

### Estimate and inspect KV-cache memory

For a standard decoder-only attention cache, estimate the unquantized
cache size before planning concurrency:

```text
KV bytes ≈ 2 × layers × KV heads × head dimension
             × bytes per cache value × cached tokens
```

The `2` counts keys and values. For a 32-layer GQA model with 8 KV heads,
head dimension 128 and a 2-byte cache value (bf16/fp16):

```text
bytes per token per sequence = 2 × 32 × 8 × 128 × 2 = 131,072 B = 128 KiB
8,192-token sequence          = 1 GiB
16 full 8,192-token sequences = 16 GiB of raw KV
```

This is only the KV requirement: weights, CUDA/ROCm context, graph
capture, activation/workspace buffers and allocator headroom need their
own budget. A model's parameter count alone is not enough to calculate
KV size; use its layer count, KV-head count, head dimension and cache
dtype. An MHA model with 32 KV heads at otherwise identical dimensions
would use four times this cache. FP8 can roughly halve bytes versus a
2-byte cache, subject to scales/layout and supported kernels; it should
be quality-tested on long contexts.

For tensor parallelism, calculate per rank with the **local** attention
layers and local KV heads only when the implementation actually shards
them. KV heads can be replicated when their count is smaller than the TP
group; pipeline parallelism changes the number of layers held on each
rank. Hybrid attention/state-space models can have multiple cache groups
with different block sizes. Do not divide a single-GPU estimate by the
number of GPUs blindly; vLLM's startup calculation is the authoritative
capacity for that version, model and layout.

Paged allocation rounds each request to whole blocks. With block size
`B` and active length `T`, the simple one-group capacity is
`ceil(T / B) × B` tokens, so the last block can have up to `B - 1`
unused token slots for that sequence. Capacity planning should use the
distribution of active cached tokens and peak contexts, not just
`average request count × maximum context`.

On GPU, inspect startup logs for messages similar to:

```text
GPU KV cache size:  ... tokens
Maximum concurrency for ... tokens per request: ...x
```

The first reports the token capacity of the GPU KV cache; the second is
an estimate at the displayed per-request length (often the configured
maximum model length). It is not a promise that production traffic will
achieve that concurrency: prefills, varying lengths, adapters, cache
groups and other workload details matter. `nvidia-smi` shows total device
memory in use, not a clean split of weights versus KV. Correlate it with
vLLM's startup capacity and `/metrics`, particularly
`vllm:kv_cache_usage_perc` and `vllm:num_preemptions`.

The memory controls have different meanings. `--gpu-memory-utilization`
sets the fraction of device memory available to the vLLM executor for
that instance; it is not a global reservation shared between vLLM
processes. `--kv-cache-memory-bytes` explicitly sets KV bytes **per GPU**
and, in current versions, overrides/ignores `--gpu-memory-utilization`.
Check `vllm serve --help` for your installed version before using either.
For example:

```bash
# Let vLLM profile memory and choose a KV budget within this executor limit.
vllm serve Qwen/Qwen3-0.6B --max-model-len 8192 --gpu-memory-utilization 0.90

# Or explicitly budget cache bytes per GPU; do not assume this is additive
# to the utilization flag. Validate startup logs and OOM headroom.
vllm serve Qwen/Qwen3-0.6B --max-model-len 8192 --kv-cache-memory-bytes 4G
```

On hardware/model combinations that support it, a lower-precision KV
cache can increase token capacity. Treat this as a separate experiment
from weight quantization:

```bash
vllm serve Qwen/Qwen3-0.6B --max-model-len 8192 --kv-cache-dtype fp8
```

Compare startup token capacity, cache usage, preemptions, long-context
quality and end-to-end latency against the `auto` cache dtype. An FP8
cache reduces KV storage; it does not reduce model-weight memory.

If the cache fills, requests can wait or be preempted and recomputed. A
rising preemption counter means concurrency is exceeding the available
cache budget or cache reservation is too small; reduce context or
concurrency, improve the weight/cache footprint, or add memory. More TP
may free per-device memory by sharding weights, but communication cost
can outweigh that gain. If `kv_cache_usage_perc` is low while latency is
poor, look at queueing, prefill, CPU tokenization, device utilization and
decode bandwidth instead of blindly increasing cache.

### CPU cache, GPU cache and offloading are distinct

The CPU backend stores model state and KV in host memory and uses CPU
compute; `VLLM_CPU_KVCACHE_SPACE` controls the CPU backend's KV-cache
space in GiB. Choose it within actual RAM headroom, leave memory for the
OS and API process, and bind inference threads and memory to local NUMA
nodes. If the machine starts swapping, throughput and latency collapse.

GPU serving normally stores active KV blocks in accelerator memory.
Some releases/backends also support offloading reusable KV blocks to
larger host-memory tiers, then copying hits back to the accelerator.
That is a separate cache/offload connector configuration, not the same
thing as running vLLM's CPU backend or setting
`VLLM_CPU_KVCACHE_SPACE`. CPU offload can help repeated prefixes when
GPU cache eviction would otherwise lose reusable work; transfer latency
and host-memory bandwidth can make it slower for one-off traffic. Check
the connector/backend compatibility for the pinned vLLM release and
measure hit rate, transfer time, GPU cache usage and end-to-end goodput.

**Advantages.** Separating API handling from execution lets request
processing scale without placing HTTP work in the model's hot loop.
Dynamic scheduling improves utilization under variable sequence
lengths; block allocation gives the engine a compact, reusable way to
manage variable-size cache state.

**Disadvantages.** More processes mean CPU, shared-memory and IPC needs.
Cache locality is per engine/replica unless an explicit KV-transfer
feature is configured. Prefix reuse depends on exact prefix identity,
and cache pressure can cause queueing or preemption even when weights
fit in device memory.

---

## 3. Run a model: install, serve and call the API

**Why.** The shortest path to a useful vLLM experiment is a known model,
a pinned environment, a running endpoint and one reproducible request.
This exposes architecture support, tokenizer/chat-template behavior,
memory fit and startup time before performance tuning begins.

**What.** vLLM provides an online server and a Python API for offline
batched inference. The server commonly exposes OpenAI-compatible
endpoints. A model's weight repository is not enough by itself: its
architecture, tokenizer, configuration, chat template and license/access
requirements must also work for the chosen vLLM release.

**How.** The current upstream NVIDIA quickstart recommends an isolated
Python environment and a matching PyTorch backend. Other hardware has
platform-specific installation instructions; do not assume a CUDA wheel
is suitable for CPU, ROCm, Intel GPU, TPU or another accelerator.

```bash
uv venv --python 3.12 --seed
source .venv/bin/activate
uv pip install vllm --torch-backend=auto

# Optional: pre-fetch compatible kernels to reduce cold-start compilation.
vllm download-kernels

# Start a small instruct model. Substitute a model whose license/access
# you accept and whose architecture is supported by this vLLM release.
vllm serve Qwen/Qwen3-0.6B --host 127.0.0.1 --port 8000
```

Call the OpenAI-compatible endpoint. The model field is normally the
served identifier returned by `/v1/models`; use a chat-capable model
with a chat template for Chat Completions.

```bash
curl http://127.0.0.1:8000/v1/models
curl http://127.0.0.1:8000/health

curl http://127.0.0.1:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"Explain KV caching in one sentence."}],"stream":true,"max_tokens":96}'
```

For offline batched generation, the Python interface avoids the HTTP
server and is useful for evaluation or batch jobs:

```python
from vllm import LLM, SamplingParams

llm = LLM(model="Qwen/Qwen3-0.6B")
outputs = llm.generate(
    ["Explain KV caching in one sentence.", "What is continuous batching?"],
    SamplingParams(temperature=0.0, max_tokens=96),
)
for result in outputs:
    print(result.outputs[0].text)
```

For a GPU container, start from the official image and pin it to a
release tag or digest in production. Mount the model cache to avoid
re-downloading weights; provide a token only for gated models. Shared
memory is important for multi-process execution. Replace the illustrative
`vX.Y.Z` below with the exact tested release tag or image digest.

```bash
docker run --rm --runtime nvidia --gpus all \
  -v "$HOME/.cache/huggingface:/root/.cache/huggingface" \
  -e HF_TOKEN="$HF_TOKEN" -p 8000:8000 --ipc=host \
  vllm/vllm-openai:vX.Y.Z \
  --model Qwen/Qwen3-0.6B --host 0.0.0.0
```

For a first experiment, record the model revision, vLLM version, runtime
and driver versions, dtype/quantization, CLI arguments, GPU type, prompt
and output token counts, and whether the run includes startup or warmup.
That record makes later throughput comparisons meaningful.

**Advantages.** The command-line server is a fast route from a model
artifact to a standards-based API. The Python API is convenient for
offline batches, evaluation and data generation. A single interface
supports a large model and backend ecosystem.

**Disadvantages.** Installation is coupled to Python, PyTorch, driver,
accelerator and kernel compatibility. Model downloads and kernel
compilation can dominate cold starts. A model that loads successfully
may still need the right chat template or generation defaults to behave
as expected.

---

## 4. Metrics: measure service quality, not just GPU busy time

**Why.** A GPU utilization percentage cannot tell whether requests are
queued, whether first tokens are late, whether cache pressure is forcing
preemption, or whether the output rate meets the product SLO. Metrics
need to expose both user experience and engine capacity.

**What.** vLLM exposes a Prometheus-compatible `/metrics` endpoint.
Server-level gauges and counters describe engine state; histograms
describe request sizes and latency distributions. Names and labels can
change across releases, so inspect the actual endpoint and bind dashboards
to the deployed release.

**How.** Scrape the live endpoint and start with the following metric
families (the exact spelling/labels can differ by version):

| Signal | What it tells you | Read it with |
|---|---|---|
| `vllm:time_to_first_token_seconds` | Queueing plus prefill delay before first output | Prompt lengths, queue time and prefix-cache hits |
| `vllm:inter_token_latency_seconds` | Gaps between streamed output tokens | Decode rate and scheduler interference |
| `vllm:request_time_per_output_token_seconds` | Request-level output-token latency | Compare same workload and output lengths |
| `vllm:e2e_request_latency_seconds` | Full request duration | Token counts and SLO percentiles |
| `vllm:num_requests_running` / `vllm:num_requests_waiting` | Active and queued work | Queue growth, admission limits and capacity |
| `vllm:kv_cache_usage_perc` | Fraction of KV cache in use | Context lengths, concurrency and preemption |
| `vllm:num_preemptions` | Work displaced under cache/scheduler pressure | Cache headroom and request mix |
| `vllm:prefix_cache_hits` / `vllm:prefix_cache_queries` | Reuse of cached prompt tokens | Prompt identity and routing locality |
| `vllm:prompt_tokens`, `vllm:generation_tokens` | Input/output work served | Throughput, cost and workload drift |
| GPU utilization, HBM, power, clocks, ECC | Device bottleneck or health | `nvidia-smi`, DCGM exporter or platform telemetry |

Counters are monotonic and should be queried as rates; gauges are
snapshots; histograms support quantile calculation through bucket
aggregation. Track p50, p95 and p99 TTFT, inter-token latency and
end-to-end time, plus goodput (requests or tokens meeting the SLO),
errors, cancellations, queue depth and per-GPU cost. Correlate vLLM with
gateway, host CPU, memory, network and GPU telemetry. GPU utilization
alone is not capacity: it may be high while the service misses TTFT, or
low while requests wait on CPU tokenization, networking or
synchronization.

**Advantages.** Request latency and engine counters distinguish the
prefill, decode, queue and cache problems that aggregate GPU statistics
hide. Prometheus/Grafana integrates with standard service monitoring.

**Disadvantages.** Histograms and high-cardinality request labels can
cost storage and query performance. Averages hide tail latency, metric
names evolve, and a metric without the corresponding workload context
can lead to tuning in the wrong direction.

---

## 5. Optimization: a measured layer-by-layer plan

**Why.** Quantizing, increasing batch size or changing parallelism can
each help one bottleneck and hurt another. A measured order makes each
change explainable and prevents a fast kernel from masking an overloaded
queue or a model that barely fits.

**What.** Optimize from the workload and model outward: validate output
correctness; establish a representative latency/throughput baseline;
make the intended model, context and concurrency fit; tune scheduling;
then consider kernel, decode and topology changes. Every test should
report both user-facing SLOs and goodput.

**How.** Use this layer map to select the next experiment:

| Layer | Inspect first | Levers to test | Common cost or trap |
|---|---|---|---|
| Workload | Prompt/output token distributions, concurrency, arrival rate | Bound `max_tokens`, group traffic by SLO/model | Closed-loop tests can hide queue collapse |
| Model/task | Architecture, tokenizer/chat template, quality baseline | Serving-sized model, correct template, LoRA adapter | Template errors appear as model-quality loss |
| Weight memory | Weight bytes plus runtime/workspace headroom | BF16/FP16, supported FP8/INT8/INT4/GPTQ/AWQ formats | Kernel/hardware support varies; validate quality and speed |
| KV cache | Usage, preemptions, contexts and concurrency | Shorter context, fewer sequences, supported cache dtype | Cache quantization can affect long-context quality |
| Scheduler | Queue time, TTFT, ITL, batch token counts | Tune `--max-num-seqs`, `--max-num-batched-tokens`; chunk prefill | Bigger batches improve throughput but can worsen latency |
| Repeated prompts | Prefix hit tokens and prompt mix | Prefix caching; route similar prefixes consistently | Reuse requires identical cacheable prefixes and locality |
| Kernels/graphs | GPU timeline, kernel time, compile/startup logs | Supported attention backend, CUDA/HIP graphs, `torch.compile` | Shape changes and compilation hurt cold starts |
| Sampling | Output length and sampling overhead | Avoid needless best-of/beam; use supported constrained decode | Product behavior and quality change |
| Speculation | Draft acceptance and end-to-end output rate | Supported draft model or speculative method | Verification overhead loses when acceptance is low |
| CPU/input path | Tokenization/media preprocessing, event-loop saturation | Reserve cores, parallelize preprocessing, tune worker threads | Too many workers oversubscribe the host |
| Network/distribution | Collectives and KV-transfer time | Keep TP on fast fabric; choose DP/PP/EP for the workload | More ranks and network hops can lower throughput |

### Optimize the transformer and serving stack by layer

vLLM can choose kernels, precision, scheduling and cache policy, but it
does not rewrite a checkpoint's fundamental architecture at serving
time. Separate runtime optimizations from model changes:

| Model/runtime layer | What usually costs time or memory | Serving-time optimization | Requires a new/fine-tuned model? |
|---|---|---|---|
| Tokenizer and embedding | Long tokenized prompts and vocabulary/embedding storage | Correct tokenizer/template; shorten redundant prompt; prefix reuse | Changing vocabulary/tokenizer generally requires a compatible checkpoint |
| Attention Q/K/V and output projections | Large matrix multiplies and weight reads | Supported weight quantization and fast GEMM kernels; suitable TP layout | Changing head count/width requires a new checkpoint |
| Attention over context | KV reads, context length, cache capacity | Efficient attention backend; supported KV dtype; prefix cache; bound context | GQA/MQA/MLA and sliding-window design are architectural choices |
| MLP/FFN | Often most dense-model parameters and weight bandwidth | Quantization, fused activation/GEMM kernels, dtype supported by hardware | Pruning width/layers or replacing MLP architecture needs model adaptation |
| MoE router and experts | Expert GEMMs, routing imbalance and all-to-all traffic | Supported fused MoE kernels; EP layout; inspect expert load | Expert count/router/structure changes require model training |
| Norms and residual operations | Many small launches and memory passes | Graph capture/compilation and kernel fusion where supported | Usually no model change |
| Sampler and decode loop | One-step launch/dispatch overhead and sequential generation | CUDA/HIP graphs, supported multi-step/speculative decoding, bounded output | Draft models or new heads require compatible trained artifacts |
| Scheduler/cache manager | Queue delay, preemption and mixed prompt/decode work | Continuous batching, chunked prefill, token/sequence limits, cache reuse | No weight change; tune to workload and SLO |

Use the model's architecture config to inspect `num_hidden_layers`,
`num_attention_heads`, `num_key_value_heads` and head dimension; the
serving startup logs show the resolved model and cache layout. Only
serving-time controls can be changed without retraining. For example,
lowering `--max-model-len` changes the admitted request envelope, not the
model's trained context ability; quantizing changes numeric precision,
not the number of layers; and increasing TP splits weights but adds
collectives. A request to “remove layers” or “turn MHA into GQA” is a
checkpoint transformation/fine-tuning project, not a vLLM command.

`--max-model-len` bounds accepted context, `--max-num-seqs` limits
concurrent sequences, and `--max-num-batched-tokens` constrains scheduled
token work; exact scheduling semantics and defaults are version-specific.
Memory utilization flags are planning inputs, not extra physical memory.
Weight memory, KV cache, graph capture, temporary activations and
fragmentation all consume device RAM. Test the peak context and concurrency
envelope, not only the median request.

For example, after measuring a model that fits comfortably, a bounded
concurrency/context experiment might look like this; it is a starting
point, not a universal optimum:

```bash
vllm serve Qwen/Qwen3-0.6B \
  --max-model-len 8192 \
  --max-num-seqs 16 \
  --max-num-batched-tokens 4096 \
  --gpu-memory-utilization 0.90
```

Benchmark real prompt and output length distributions, concurrency,
streaming behavior, sampling settings and target SLO. Use open-loop
arrival control when testing overload; a closed-loop client that waits
for each response can conceal queue collapse. Change one variable at a
time, warm weights and kernels, repeat runs, and compare p50/p95/p99 plus
goodput. Keep cold start and model load time separate from steady-state
token latency.

**Advantages.** A bottleneck-first sequence gives each optimization a
measurable purpose. Quantization, scheduling and caching often improve
serving without changing model architecture, while profiling shows when
kernel or topology work is justified.

**Disadvantages.** The best settings depend on traffic and SLO. Batch
size trades per-request latency for aggregate throughput; quantization
trades numerical fidelity for memory/bandwidth; speculative decoding
depends on draft acceptance; and results from one GPU/model/version do
not automatically transfer to another.

---

## 6. Scaling: replicas, parallelism and disaggregated serving

**Why.** One GPU may be full because a model does not fit, or because
traffic is queuing. Those are different problems. Model-parallel
strategies fit or execute one replica across devices; data-parallel
replicas increase capacity for independent requests.

**What.** **Tensor parallelism (TP)** splits layer matrices across
devices and uses collectives. **Pipeline parallelism (PP)** assigns
different layers to stages. **Expert parallelism (EP)** distributes MoE
experts. **Context parallelism (CP)** partitions long-context work where
supported. **Data parallelism (DP)** runs independent model replicas or
engine ranks to process more requests. Strategies may compose, but each
adds communication or coordination.

**How.** First choose the bottleneck. If a model cannot fit on one GPU,
use a supported model-parallel layout and leave memory for KV cache. If
it fits but queue time is high, additional replicas or DP are often a
simpler capacity lever than TP. Each replica owns its cache; route
repeated prompts consistently if prefix-cache locality matters. For a
simple single-node TP experiment:

```bash
vllm serve Qwen/Qwen3-0.6B --tensor-parallel-size 2
```

For multi-node deployments, configure node rank/address, reachable
communication ports and distributed executor as described by the
release's deployment guide; set the correct network interface/IP on each
node. Keep TP collectives on NVLink/NVSwitch when possible. Where model
and topology allow it, PP or DP can cross slower links more efficiently.
Benchmark collectives/RDMA separately, verify all ranks use the same
model revision, and provision CPU, shared memory and model-file cache in
addition to GPUs. In vLLM's online DP implementation, the API and engine
process topology can include coordinators and engine cores; use the
release's documented DP deployment mode or an external load balancer.

Disaggregated prefill/decode uses distinct pools for prefill and decode
and transfers KV state through a connector. It can tune TTFT and
inter-token latency separately, but introduces transfer cost and
operational complexity and is not automatically a throughput gain.
Multiple independent containers behind a load balancer do not share KV
cache unless an explicit KV-transfer/cache feature is configured.

**Advantages.** Parallelism lets models exceed one device's memory or
compute; data parallelism scales independent request capacity; and
disaggregation can isolate phase-specific latency needs. Keeping the
strategies distinct makes capacity decisions more precise.

**Disadvantages.** TP collectives can dominate across slow links, PP
adds pipeline bubbles, DP duplicates weights and KV state, and expert
traffic can stress network bandwidth. More ranks add process, port,
failure and coordination complexity. Distributed scaling must be
measured end-to-end, not inferred from GPU count.

---

## 7. GPU and CPU: different operating envelopes

**Why.** The GPU and CPU backends have different kernels, memory
hierarchies, thread schedulers and supported features. A GPU configuration
cannot be copied onto CPU and expected to work or perform similarly.

**What.** GPU inference generally uses accelerator-specific optimized
attention and matrix kernels and device HBM. CPU inference uses the
backend's supported operators and CPU memory bandwidth, typically with
OpenMP parallelism. vLLM supports CPU on selected architectures, but
performance and supported dtype/model combinations vary by build and
architecture.

**How.** On GPU, choose dtype/quantization based on hardware and installed
kernels. Make room for cache, graphs and workspaces rather than loading
weights to the last byte. Check topology and clocks, and consider
pre-downloading compatible kernels to avoid startup compilation.

For CPU, follow the CPU-specific installation instructions. Do not
install a CUDA-only build or use GPU flags as a CPU setup. NUMA placement
and thread oversubscription are critical. After installing the CPU
backend appropriate to the host, documented runtime controls include:

```bash
export VLLM_CPU_KVCACHE_SPACE=40       # KV cache in GiB; fit to host RAM
export VLLM_CPU_OMP_THREADS_BIND=0-30  # bind inference threads to local cores
vllm serve facebook/opt-125m --dtype bfloat16
```

The example reserves substantial host memory and core IDs `0-30` are
illustrative; adjust both for the actual machine. On NUMA hosts, begin
with automatic binding or assign rank threads to cores local to their
memory node. Reserve CPU capacity for the API process, tokenization,
media loading and network work. Tune `VLLM_CPU_KVCACHE_SPACE`, thread
binding and OpenMP threads together. For CPU+GPU layer offload and
compact GGUF workflows, llama.cpp is often a better fit; vLLM targets
server-side scheduling and throughput.

**Advantages.** GPU execution offers high throughput for supported
models, while CPU execution can provide a service or fallback on
hardware without accelerators. CPU-only inference is useful for small
models and low-rate traffic where deployment simplicity can matter more
than token latency.

**Disadvantages.** Dense large-model CPU decode is often constrained by
memory bandwidth and can be far slower than a GPU. CPU operator/dtype
support varies by architecture; NUMA mistakes and thread oversubscription
can erase performance. GPU deployments have their own VRAM, driver,
kernel and power constraints.

---

## 8. Deployment, security and troubleshooting

**Why.** A working inference endpoint is not yet a production service.
Version drift, cold starts, request overload, rollout failures and
unprotected routes can negate engine-level performance.

**What.** A production vLLM deployment includes the engine plus a
gateway/load balancer, authentication and authorization, admission and
token limits, health checks, telemetry, autoscaling, canary rollout and
model/version management. The gateway is responsible for product and
security policy; the engine is responsible for model execution.

**How.** Pin vLLM, PyTorch, CUDA/ROCm and model revisions as a tested
set. Load-test before rollout; expose health and metrics; set request,
queue and token limits; and drain replicas before replacement. Scale on
queue delay, waiting requests and SLO goodput rather than GPU utilization
alone. Canary new weights or quantization, compare quality and
performance, and keep a rollback artifact.

Place the server behind an authenticated gateway or reverse proxy.
vLLM's API-key option does not authenticate every route in all releases,
so it is not a complete security boundary. Do not expose the raw port
as the only protection. Treat model hubs, tokenizer files, chat
templates and remote model code as deployment inputs to review and pin.

| Symptom | Likely cause | First checks |
|---|---|---|
| Startup fails loading model | Unsupported architecture, missing access token, incompatible dtype/dependency | Full startup log, model config, release support/install matrix |
| CUDA OOM during startup | Weights, graph capture or workspace exceed VRAM | Free VRAM, model dtype/quantization, per-GPU split and headroom |
| OOM under load, preemptions rise | KV cache/context/concurrency pressure | Cache usage, max context, active sequences and cache dtype |
| High TTFT, acceptable inter-token latency | Queue or prefill bottleneck | Waiting requests, queue time, prompt length, prefix cache, chunking |
| Good TTFT, poor inter-token latency | Decode bandwidth/compute, contention or long context | Per-GPU memory, decode timing, batch size and co-tenants |
| Low GPU utilization and slow requests | CPU preprocessing, small batches, synchronization or input starvation | Host CPU, tokenizer/media time and system timeline |
| High utilization but poor goodput | SLO exceeded or oversized requests | p95/p99, queue limits, request mix and admission policy |
| First request unusually slow | Download, graph/kernel compilation, cold caches | Separate startup/warmup from steady state; cache weights/kernels |
| Wrong chat behavior | Chat template/generation defaults/model mismatch | Served model, tokenizer template, generation config and payload |

**Advantages.** Standard service practices—version pinning, canaries,
draining, metrics and load balancing—compose naturally around vLLM.
Separating the gateway from inference keeps authentication and product
policy independent of accelerator workers.

**Disadvantages.** vLLM is a serving engine, not a complete production
platform: auth, routing, autoscaling, rollout and quality evaluation
still belong to the system around it. Startup compilation and downloads
make cold-start behavior different from steady state. A deployment may
be functionally healthy while silently missing its latency or quality
SLO.

---

## Further reading

- [vLLM Quickstart](https://docs.vllm.ai/en/latest/getting_started/quickstart/)
- [Architecture overview](https://docs.vllm.ai/en/latest/design/arch_overview/)
- [Feature and hardware compatibility](https://docs.vllm.ai/en/latest/features/)
- [Production metrics](https://docs.vllm.ai/en/latest/usage/metrics/)
- [CPU backend](https://docs.vllm.ai/en/latest/getting_started/installation/cpu/)
- [Parallelism and scaling](https://docs.vllm.ai/en/latest/serving/parallelism_scaling/)
- [OpenAI-compatible server](https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/)

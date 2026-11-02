# Learning path: fundamentals to advanced systems

The repository is a set of related guides, not one textbook. This path
orders every guide by the background it assumes. Follow it top to bottom
for a broad systems foundation; if you already know Go, start at the
systems tier. Each row links to one document, including the composed
design walkthroughs.

## 0. Orientation

1. [Repository overview](README.md) — the map and how to run checks.
2. [This learning path](LEARNING_PATH.md) — the complete progression.

Run `make site` to generate the interactive browse-and-search page.

## 1. Programming language and design fundamentals

These explain the language constructs and small design tools used by
later systems examples.

1. [Go basics](concepts/BASICS.md) — syntax, values, functions, data
   structures, packages, errors and concurrency.
2. [Rust](rust/README.md) — ownership, borrowing, traits, async,
   concurrency, unsafe and compiler/runtime tradeoffs.
3. [Focused Go concepts](concepts/README.md) — interfaces, errors,
   generics, synchronization and tests.
4. [Go construction idioms](idioms/README.md) — functional options and
   configuration design.
5. [Go pitfalls](mistakes/README.md) — common mistakes and their fixes.
6. [Creational patterns](creational/README.md) — object construction.
7. [Structural patterns](structural/README.md) — composition and
   boundaries.
8. [Behavioral patterns](behavioral/README.md) — collaboration and
   control flow.

## 2. Runtime and execution

Understand what a program does while it runs before studying a whole
machine or a cluster.

1. [Concurrency patterns](concurrency/README.md) — goroutines, channels,
   cancellation and worker pools.
2. [Go runtime internals](internals/README.md) — measured allocation,
   memory layout, interfaces, maps, GC and goroutines.
3. [Go runtime deep dive](internals/RUNTIME.md) — scheduler, allocator,
   reflection, Green Tea GC and arenas.
4. [Compilers](compilers/README.md) — how source becomes executable code.
5. [Build a DSL, compiler and assembler in Go](compilers/BUILDING_A_LANGUAGE.md)
   — a runnable source-to-bytecode pipeline with a toy language and VM.
6. [Program execution](execution/README.md) — compiler output, ELF loading,
   process startup and execution hardening.
7. [Linux](linux/README.md) — processes, memory, filesystems and the
   kernel boundary.

## 3. Hardware and single-machine systems

These guides establish the CPU, storage, network and accelerator costs
that larger architectures have to work within.

1. [Networking](networking/README.md) — packets, transports and network
   performance.
2. [Web services](webservices/README.md) — HTTP, TLS, gRPC, identity and
   service security.
3. [Database internals](database/README.md) — storage engines, buffers,
   indexes and durability.
4. [GPU internals](gpu/README.md) — execution, memory hierarchy,
   interconnects and kernels.
5. [Single-node system-design building blocks](sdesign/README.md) — queues,
   caches, rate limits, metrics and pools.
6. [Design patterns decision record](adr/0001-golang-design-patterns.md)
   — why this repository adopts or changes the classic patterns.

## 4. Composed and distributed systems

Read the relevant mechanism first, then the end-to-end design that uses
it.

1. [System-design compositions](sdesign/DESIGNS.md) — build complete
   services from single-node components.
2. [Networking designs](networking/DESIGNS.md) — follow data across the
   network stack.
3. [Linux designs](linux/DESIGNS.md) — trace requests through kernel
   subsystems.
4. [Database designs](database/DESIGNS.md) — compose storage and memory
   mechanisms.
5. [Distributed systems](distributed/README.md) — consistency, clocks,
   elections, replication and coordination.
6. [Distributed-system designs](distributed/DESIGNS.md) — compose those
   mechanisms into services.

## 5. Machine learning and model-serving systems

These guides assume comfort with systems costs and then move from model
fundamentals to serving and agent tooling.

1. [Reinforcement learning basics](rl/BASICS.md) — agent/environment
   interaction, reward and return, policies, values and exploration.
2. [Deep reinforcement learning](rl/README.md) — algorithm families,
   optimization, PyTorch implementations and preference learning.
3. [MuJoCo robot modeling for RL](rl/MUJOCO.md) — model a robot, match its
   controller and sensors, train in simulation, and evaluate sim-to-real.
4. [Deep RL for physical robots](rl/ROBOTICS.md) — task formulation,
   algorithm selection, simulation-to-real transfer and deployment.
5. [Large language models](llm/README.md) — transformers, training,
   fine-tuning, inference, memory and deployment.
6. [vLLM serving guide](llm/vllm.md) — engine internals, KV-cache sizing,
   metrics, optimization and deployment.
7. [Agentic tools](agentictools/README.md) — tools for working with,
   documenting and investigating software and models.

## Companion: Android reader

[Android app build and install guide](android/README.md) — package this
progression as an offline-friendly reader on a device.

## Suggested tracks

- **Go application developer:** tiers 1–2, then the system-design guide
  that matches your work.
- **Rust systems developer:** Rust → program execution → Linux and
  networking → the hardware or service guides relevant to the project.
- **Systems engineer:** tiers 2–4, then the GPU or LLM guides as needed.
- **LLM inference engineer:** GPU internals → networking → database
  internals → LLM guide entries 9–16 and 18–25 → the vLLM guide.
- **LLM fine-tuning / agent engineer:** LLM guide entries 1–8 and 17,
  then its fine-tuning-for-tools and agentic tasks chapter, followed by
  vLLM serving.

The order is a default progression, not a prerequisite gate. Hardware,
machine-learning and Go-specific branches can be read independently
once their terms are familiar.

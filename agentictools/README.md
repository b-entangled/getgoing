# Agentic tools — graphify, Obsidian, Soup, REA

There are no Go packages in this directory. What follows is prose about the
four tools that surround everything else in this repository: one that turns a
folder of files into a queryable map, one that keeps the notes durable and
readable, one that fine-tunes a model, and one that inspects software nobody
gave you the source for. They are not the subject of the learning — they are
the instruments, and an instrument you cannot explain is an instrument you
operate blind.

Each entry has the same six parts, in the same order, every time: the need
that justifies the tool (**Why.**), a precise statement of what it is
(**What.**), the mechanics — pipelines, tables, command blocks — that make it
concrete (**How.**), what you gain (**Advantages.**), what it costs in
practice (**Disadvantages.**), and the commands you actually type
(**Usage.**). The five-label contract is the one [llm](../llm/README.md) and
[rl](../rl/README.md) use, plus a sixth for the command surface, because
these entries are worthless if they stop short of the prompt.

Read them top to bottom the first time — entry 1 defines the graph vocabulary
the rest reuse — and after that they work as reference. There is no test
suite behind this section: the commands are the specification, and where a
command and a paragraph disagree, the command is the claim you can run.

The four are one pipeline rather than four topics:

```
graphify   folder of files  →  graph.json + report + HTML + vault
Obsidian   vault of markdown →  durable, human-editable notes with backlinks
Soup       base model + data →  adapted model, merged and exported
REA        shipped artifact  →  cited evidence about what it really does
```

Map, notebook, model, magnifier. The design patterns, language features and
system designs live where they always did — [concepts](../concepts/README.md),
[sdesign](../sdesign/README.md), [distributed](../distributed/README.md) —
and the theory behind entry 3 is written out in
[llm](../llm/README.md) entries 7 and 8.

---

## 1. graphify: a persistent knowledge graph over any corpus

**Why.** Grep reads files, and an agent answering "how does this work?"
re-reads a few hundred of them every session: the cost grows with the corpus,
the answer carries nothing forward, and the conclusion from yesterday's
session is gone. What is missing is a *derived artefact* — something built
once from the files, cheap to reload, small enough to hold a scoped subgraph
rather than the whole tree, and honest about where every claim came from.
Without it, "the architecture is X" is a story retold from scratch each time.

**What.** graphify (PyPI package `graphifyy`, 0.9.85 at time of writing) is
a CLI, an agent skill and an MCP server that turns any folder — code,
markdown, PDFs, images, audio and video (transcribed first) — into a
knowledge graph at `graphify-out/graph.json`, plus a plain-language
`GRAPH_REPORT.md` and an interactive `graph.html`. Nodes are entities:
symbols, files, concepts, rationales. Edges are typed relations
(`calls`, `implements`, `references`, `cites`, `shares_data_with`,
`conceptually_related_to`, `semantically_similar_to`, `rationale_for`) and
every one carries a confidence label — **EXTRACTED**, **INFERRED** or
**AMBIGUOUS** — and a numeric score. Leiden community detection partitions
the graph; the ten most-connected real entities are the *god nodes*.

**How.** One pipeline, six stages, and the split that decides the bill:

```
detect    classify files: code / document / paper / image / video
extract   Part A: AST over code — deterministic, free, no API key
          Part B: semantic over docs, papers, images — host LLM or Gemini
build     merge both, resolve ids, build the NetworkX graph
cluster   Leiden communities, degree-percentile hub suppression
analyze   god nodes · surprising connections · suggested questions
label     2-5 word plain-language community names → re-export
export    html (always) · obsidian, wiki, neo4j, falkordb, svg, graphml, mcp
```

The economic split is the first thing to understand. Code is extracted
structurally: an AST walk finds imports, definitions and call edges with no
model and no key, so the common case — `/graphify .` on a repository — costs
nothing and cannot hallucinate an import that is not there. Semantic
extraction only runs over documents, papers and images, where no syntax tree
exists, and it uses the running agent as the model unless `GEMINI_API_KEY`
or `GOOGLE_API_KEY` is already set. graphify reads no other provider key and
never blocks on one.

Every semantic edge is labelled, and the labels are the audit trail:

| Label | Meaning | Score | Where it comes from |
|---|---|---|---|
| EXTRACTED | explicit in the source | 1.0 always | an import, a call, a citation, "see §3.2" |
| INFERRED | reasonable inference | one of 0.95, 0.85, 0.75, 0.65, 0.55 | shared data structure, functional alignment |
| AMBIGUOUS | uncertain, flagged for review | 0.1 – 0.3 | surface co-occurrence, an unreadable figure |

The discrete INFERRED scores exist because a continuous range collapsed to a
binary in production — models parked on 0.5 — so a rubric of five named
values is what actually gets followed. The standing rule is *never invent an
edge*: if it is not there, it is AMBIGUOUS or it is absent.

Node ids are derived from the repo-relative path plus the symbol —
`src/auth/session.py` + `ValidateToken` → `src_auth_session_validatetoken` —
so the same name in two directories never merges into one ghost. That
determinism is what makes `--update` possible: the manifest stores a
per-file semantic hash, only files that actually produced output are stamped,
and a re-extract replaces rather than accumulates. Two guards sit in front of
the write: an empty extraction cannot clobber an existing graph, and the
export refuses to overwrite `graph.json` with a *smaller* one — a partial
re-run cannot silently shrink the map.

Analysis is where the graph pays for itself:

| Output | Definition | Why it is not just "degree" |
|---|---|---|
| God nodes | top-10 most-connected **real** entities | file nodes, concept nodes and JSON-key nodes are excluded — they accumulate edges mechanically |
| Surprising connections | cross-file edges, ranked AMBIGUOUS → INFERRED → EXTRACTED; or betweenness bridges in single-source corpora | concept nodes are excluded: they were *put* there, they were not discovered |
| Suggested questions | questions crossing community boundaries | generated from the real community labels, regenerated after labelling |
| Cohesion | per-community density score | a community that is really two will show it here |

The query surface is deliberately a *subgraph*, not a file list:

```bash
graphify query "how does auth reach the database"   # BFS, broad context
graphify query "trace the retry path" --dfs          # one chain, depth 6
graphify query "..." --budget 1500                   # cap the answer
graphify path "AuthModule" "Database"                # shortest path
graphify explain "RateLimiter"                       # one node, plain language
```

Matching is case-folded substring plus IDF — no stemming, no synonyms, no
cross-language mapping — so a question in different vocabulary than the graph
returns nothing. The skill's fix is *constrained query expansion*: pull the
token vocabulary out of node labels, pick at most twelve tokens **from that
list**, print the expansion so the reader can audit it, and stop if nothing
matches rather than fabricate a search.

Behind MCP the same traversal is seven tools — `query_graph`, `get_node`,
`get_neighbors`, `get_community`, `god_nodes`, `graph_stats`,
`shortest_path` — over `graph.json`, which is why a rules file can tell an
agent "ask the graph first" and mean something specific by it.

**Advantages.** AST extraction is free and deterministic, so a code-only
corpus needs no key, no model and no budget. Queries return a scoped subgraph
instead of files, which bounds context the way nothing else does. Every edge
carries a source file, a confidence label and a score, so a claim can be
checked instead of believed. Communities surface cross-document connections
you would not have thought to ask about — the reason the report ends with
*suggested questions*. Incremental update, `--watch` and a post-commit hook
keep the map from rotting without a full rebuild. And the outputs are
exchangeable: HTML for a browser, a vault for Obsidian, Cypher for Neo4j or
FalkorDB, GraphML for Gephi, a wiki index for another agent.

**Disadvantages.** The graph is derived, so it is stale the moment the code
moves until `--update` runs — a derived artefact is one more thing to keep
in sync, and the shrink guard only protects against losing data, not against
being out of date. INFERRED edges are the extracting model's judgement: the
labels make that visible but do not make it true, and a report read as fact
is a report misread. The matcher's lack of stemming and synonyms means a
paraphrased or non-English question needs explicit vocabulary expansion or it
returns noise. Leiden resolution moves community boundaries between runs, so
names and clusters shift when the corpus changes — the map is not a stable
identifier. Large corpora (over 500 files or 2M words) stop and ask which
subfolder to build. And `graphify-out/` is written into the working tree, so
a repository has to decide whether to ignore it or share it.

**Usage.**

```bash
uv tool install graphifyy          # or: pipx install graphifyy

graphify .                         # full pipeline on the current directory
graphify . --mode deep             # richer INFERRED edges, more inference
graphify . --update                # incremental: only new/changed files
graphify . --watch                 # rebuild on change, no LLM involved
graphify . --obsidian              # also write a vault (see entry 2)
graphify . --wiki                  # index.md + one article per community
graphify . --neo4j-push bolt://localhost:7687
graphify hook install              # rebuild after every git commit
graphify claude install            # always-on section in CLAUDE.md

graphify query "what reads the WAL" --budget 2000
graphify path "raft" "election"
graphify explain "saga"

python -m graphify.serve graphify-out/graph.json      # MCP stdio server
graphify benchmark                                    # token-reduction numbers
```

Outputs land in `graphify-out/`:

```
graph.json          the graph — nodes, typed edges, communities, confidence
GRAPH_REPORT.md     god nodes, surprising connections, suggested questions
graph.html          interactive visualisation (community view past 5000 nodes)
obsidian/           a vault, only if --obsidian was given
wiki/               agent-crawlable markdown, only if --wiki was given
cost.json           per-run and cumulative token accounting
```

---

## 2. Obsidian: the vault the map and the notes live in

**Why.** Anything an agent produces is one tool-version away from being
unreadable: a session's context is discarded, a database export needs its
database, a graph format needs its graph. What is needed is a durable,
local, *plain-text* store that survives the churn — files you can edit by
hand, diff in git, and hand to any other program — which also shows you the
relationships you recorded without meaning to.

**What.** Obsidian is a local-first markdown editor. A **vault** is nothing
more than a folder of `.md` files; on top of the files it computes links
(`[[wikilinks]]`), backlinks, a graph view, YAML properties, outline and
search, and it offers a plugin ecosystem (Dataview, Kanban, Excalidraw and
the rest). The data is the folder. Sync and Publish are optional paid
services that move those files around; nothing about the format requires
them.

**How.** Obsidian is the reading and writing surface for graphify's output,
and the division of labour is the whole point:

| | graphify | Obsidian |
|---|---|---|
| Holds | derived graph, regenerated | authored notes, edited by hand |
| Unit | node and typed edge | file and wikilink |
| Provenance | `source_file`, confidence label, score | frontmatter, tags, the prose itself |
| When it changes | `--update` rewrites it | you do |
| Trust | reproducible from the corpus | curated, and fallible |

`graphify export obsidian` writes one file per node into
`graphify-out/obsidian` (or wherever `--obsidian-dir` points): nodes become
notes, edges become wikilinks, communities become the folder structure. Open
that folder as a vault and the graph you could previously only query is now
something you can browse, annotate and link onward — and because wikilinks
resolve by filename and backlinks are *computed* from the files rather than
stored in a table, there is no link state to corrupt and nothing to resync
when a note moves.

That last property is what makes a vault a good substrate for an agent:
there is no API between the writer and the content. An agent adds a note with
ordinary file tools, a human edits it in Obsidian, a git commit records both,
and neither has to know about the other. The graph view then gives the same
node-and-edge picture at note granularity — useful precisely because it is
the *curated* subset, not the full extracted corpus.

On this machine the pattern is live: the vault Obsidian last had open is
`iot/espailocal/graphify-out` — graphify's output directory opened directly
as a vault, with no export step in between.

**Advantages.** Plain markdown on local disk means no lock-in, no database,
no export problem: the vault outlives Obsidian itself. Backlinks are derived
from the files, so the relationship index cannot go stale or drift from the
content. Git diffs cleanly over it, which is what makes notes reviewable.
The graph view and Dataview-style queries over frontmatter turn a folder of
files into something navigable without a server. And because it is just
files, both humans and agents read and write the same store with no
translation layer.

**Disadvantages.** It is a single-user, file-based system: no realtime
collaboration, and concurrent edits are a merge conflict like any other
files. The application is proprietary even though the data is open, so the
format's future and the app's future are not the same question. Wikilinks
resolve on filename, so a rename is a breaking change unless the editor
rewrites every reference — a moving graph makes this routine. The plugin
ecosystem means two vaults behave differently, so a note that works here may
not work there. The graph view does not scale to tens of thousands of
notes — which is exactly why you import a graphify graph *by community*
rather than node by node. And a generated vault is a derived artefact like
any other: regenerate it, do not hand-edit it, or your changes vanish on the
next export.

**Usage.**

```bash
# build a vault out of the graph
graphify <path> --obsidian                            # → graphify-out/obsidian
graphify <path> --obsidian --obsidian-dir ~/vaults/proj   # into an existing vault
graphify export obsidian                              # re-export an existing graph
graphify export obsidian --dir ~/vaults/proj
```

```
Obsidian → "Open folder as vault" → graphify-out/obsidian
```

Conventions that keep the vault honest:

- One node, one file; the filename *is* the node id's human form.
- Regenerate rather than edit — the export is the source of truth.
- Put authored commentary in frontmatter-tagged notes that link *to* the
  generated ones, so a rebuild never overwrites it.
- Commit the vault if it is a team artefact; `.gitignore` it if it is a
  scratch build.

---

## 3. Soup: fine-tuning and post-training in one command

**Why.** Fine-tuning is usually sold as a model problem and delivered as an
infrastructure project: SSH into a GPU box, a training script copied from a
blog post, quantisation flags set by folklore, batch size guessed, and the
actual configuration living in someone's shell history. The need is one
declarative file and one command that goes from *base model plus JSONL* to a
merged, exportable artefact — so that the time goes into the data and the
loss curve instead of into plumbing.

**What.** Soup (`soup-cli`, Apache-2.0, 0.75.0 at time of writing) is a
CLI and Python library for fine-tuning and post-training LLMs. A single
`soup.yaml` names the base model, the task, the data and the training
section; `soup train` runs it — SFT, and the preference family DPO, GRPO,
PPO, KTO, ORPO, SimPO, IPO, BCO — with LoRA or QLoRA, automatic batch sizing
and GPU detection. Backends are `transformers` (default), Unsloth (`[fast]`,
2–5×) and MLX (Apple Silicon). The heavy stack is an *extra*: the core
install is the CLI, the config system and the data tools with no PyTorch at
all.

**How.** The config is the entire interface, and its schema is the single
source of truth — which produces the design decision worth stating out loud:
since v0.75 an **unknown config key refuses to load**. A typo like
`quantizaton`, or a field that only exists on a newer Soup, used to validate
cleanly and then be silently discarded while the run proceeded with the
setting not applied. It now fails at the CLI with exit 1 and names the field
you probably meant. A configuration that can be wrong *quietly* is worse
than one that cannot be loaded.

The whole lifecycle on one command surface:

```
soup init --template chat        → soup.yaml (17 templates: chat, code,
                                    tool-calling, reasoning, kto, orpo, …)
soup data inspect ./train.jsonl  → format auto-detected, quality stats
soup train  --config soup.yaml   → ./output: adapter, metrics, checkpoints
soup chat   --model ./output     → talk to it
soup eval   benchmark --model ./output
soup serve  --model ./output     → OpenAI-compatible API server
soup merge  --adapter ./output   → fold the LoRA delta into the base
soup export --model ./output --format gguf --quant q4_k_m
soup push   --model ./output --repo you/my-model
```

The memory question is answered with a table rather than a hope:

| VRAM | Max model (QLoRA 4-bit) | Example |
|---|---|---|
| 8 GB | ~7B | Llama-3.1-8B, Mistral-7B |
| 16 GB | ~14B | Phi-4-14B, Qwen2.5-14B |
| 24 GB | ~34B | CodeLlama-34B, Yi-1.5-34B |
| 48 GB | ~70B | Llama-3.3-70B |
| 80 GB+ | 70B+ full, or MoE | Mixtral-8x22B, DeepSeek-V3 |

The headline feature is **layer streaming** (`stream_layers: true`, opt-in,
beta): the frozen base stays in host RAM and is fed to the GPU one decoder
layer at a time, so the card never holds the model. The README's measurement
is Llama-3.1-8B-Instruct in NF4 on a 4 GB RTX 3050 laptop at **119.6 tok/s
and 3.32 GB peak**, bit-identical to a normal resident run and reproduced
independently at 113.00 tok/s in the same 3.32 GB on an H100 — with the
honest caveat the project itself prints: both figures were measured on
v0.72.2, before the v0.73.0 correctness repair, and have not been re-run on
a 4 GB card since (issue #361). Bit-exactness is checkable, not asserted —
the repository ships a Colab notebook that caps the process at 4 GB and
asserts a streamed model equals a resident one.

Data is the other half, and it is where a CLI earns its keep. Formats —
Alpaca, ShareGPT, ChatML, preference pairs, vision, audio, plaintext,
embedding — are auto-detected from JSONL, JSON, CSV, Parquet or TXT, so in
most cases `data.train` is the only key you touch. Recipes (`soup recipes
list`, 100+) supply ready configurations for model families; `soup doctor`
checks the GPU and dependency state before a run rather than during one.

This is the tool that implements [llm](../llm/README.md) entry 7: the same
LoRA maths — W′ = W + (α/r)·BA, B zero-initialised so the delta starts at
zero — behind a config file instead of a training script.

**Advantages.** One config, one command, no SSH and no bespoke launcher;
the schema validates loudly instead of dropping keys. The extras split keeps
the light install genuinely light, so data inspection and config work do not
drag in torch. Auto format detection and the built-in data tools remove the
most common source of silent training bugs — a dataset in the wrong shape.
It is local-first: your own GPU, or the published Docker image, with nothing
required from a cloud. And train → chat → eval → serve → merge → export all
happen inside one tool, so the handoffs between stages stop being new
projects.

**Disadvantages.** An abstraction that hides the plumbing also hides the
cause when a run diverges: a learning rate picked wrong still needs you to
read the loss, and no config key will tell you your thousand examples are
mediocre — the loss descends happily either way. Python is bounded to
3.10–3.12, because 3.13+ resolves PyTorch wheels nobody has validated and
they crash inside the native extension before any Soup code runs. The
`[train]` extra is a large dependency set, and layer streaming is explicitly
beta with published numbers predating a correctness repair. The config schema
moves: v0.75 rejected unknown keys and changed the GSPO objective, so an old
config is a reproducibility risk and a `soup.yaml` written last year may no
longer describe the run you think it does. And a CLI cannot replace judging
data — it will train on bad data as willingly as good.

**Usage.**

```bash
pipx install "soup-cli[train]"      # or: uv tool install "soup-cli[train]"
pipx install "soup-cli[all]"        # train + serve + ui + data
pip install "soup-cli[train]"       # inside a venv / Colab / Docker
```

```yaml
# soup.yaml — the whole interface
base: meta-llama/Llama-3.1-8B-Instruct
task: sft
# backend: unsloth          # 2-5x faster, needs "soup-cli[fast]"

data:
  train: ./data/train.jsonl
  format: alpaca            # optional — usually auto-detected
  val_split: 0.1

training:
  epochs: 3
  lr: 2e-5
  batch_size: auto
  lora:
    r: 64
    alpha: 16
  quantization: 4bit

output: ./output
```

```bash
soup doctor                          # GPU, deps, environment, before you start
soup init --template chat            # or start from a template
soup data inspect ./data/train.jsonl # what you are actually training on
soup train  --config soup.yaml
soup chat   --model ./output
soup eval   benchmark --model ./output
soup merge  --adapter ./output
soup export --model ./output --format gguf --quant q4_k_m
soup ui                              # local dashboard at 127.0.0.1:7860
```

---

## 4. REA: reverse-engineering shipped artefacts

**Why.** Source-repository analysis answers "what does the code say"; it
cannot answer "what does the shipped binary do". Artefacts are repacked,
minified, obfuscated and signed; a feature can exist in a build while the
repository in front of you does not contain it. What is needed is a way to
make claims about *artifacts* — an ELF, an APK, an Electron asar, a firmware
image, a captured HAR — with evidence you can cite, and with the boundary
between what was observed and what was inferred kept explicit rather than
smoothed over.

**What.** REA (reverse-engineer-anything) is an agent skill — version 37,
170 tools at time of writing — together with an MCP server and a `rea` CLI
(`npx -y rea-agents@6.4.0` when nothing is installed). It routes a target to
the right analysis engine: format-aware parsers for archives and packages,
static JavaScript analysis for asar and Electron trees, Ghidra for native
code, IDA through `ida-pro-mcp`, JADX for Android, pwntools for offline ELF
layout, Binwalk and Unblob for firmware. Results come back as **Evidence**
records with IDs, accumulated in a session ledger that later steps reference
instead of re-fetching.

**How.** The first move is always to route the target, because the target
determines the tool:

| Target supplied | First tool |
|---|---|
| ASAR or extracted JS / Electron tree | `analyze_javascript_application` |
| ZIP / APK / IPA / MSIX / DMG member inventory | `open_binary`, then `inspect_artifact` |
| Android APK code, classes, references | `inspect_android_package` |
| Managed PE / CLI assembly | `inspect_managed_artifact` |
| Firmware image | `inspect_firmware_regions`, or `extract_firmware` |
| Local ELF — layout, symbols, relocations | `inspect_binary_layout` (target-free, pwntools) |
| Supplied x86-64 core file | `inspect_recorded_crash` |
| Open user-owned browser page / Electron process | `list_browser_targets` / `list_electron_targets` |
| Retained HAR or mitmproxy capture | `inspect_web_network_capture` |
| Explicit EVM bytecode carrier | `inspect_evm_interface` |
| Native executable or analysis database | `open_binary`, then `binary_overview` |

Three disciplines carry the rest of the method:

*Summary first.* Ask for `detail: "summary"`, take the returned
`parent_evidence_id`, and drill into one section with
`inspect_analysis_view`. A large artefact is paged through retained Evidence
rather than re-requested whole — and because a client can truncate a
successful result without an MCP error, a preview is never treated as
canonical JSON.

*Evidence, not impressions.* Every conclusion cites Evidence IDs and
separates observations from inferences from unknowns. Static analysis never
implies execution: a decompiler shows the shape of the code, not what the
process did at 3 a.m. Runtime observation is used only when static evidence
cannot answer and the host runtime and OS access allow it. Bounded negative
stays bounded — "searched these strings, found nothing" is a finding;
"nothing there" is not.

*Availability per operation.* A complete tool catalog does not mean every
operation is available. `binary_session` returns `tool_availability` with a
reason, so "the tool failed" becomes "this route needs Ghidra, which is not
installed" — a named prerequisite instead of a mystery.

Connection is a separate concern from capability, and the skill is emphatic
about it: installing the skill supplies instructions, it does **not** register
the MCP server or install an engine. Registration is diagnosed and repaired
per client:

```bash
rea doctor --client codex --json          # is the registration aligned?
rea setup  --client codex --dry-run --json  # what exactly would change?
rea setup  --client codex --yes           # apply, with backups
```

Supported client IDs include `claude_code`, `claude_desktop`, `codex`,
`cursor`, `gemini_cli`, `windsurf`, `opencode`, `copilot_cli`, `vscode`,
`hermes` and others. Two rules follow from that separation. A client
approval rejection (`MCP tool call requires approval, but approval policy is
never`) is a host policy decision, not a broken install — repairing
registration will not fix it, and REA's own tools carry no permission
handshake. And setup never installs or upgrades unrelated software: Node,
Ghidra, IDA, JADX, Binwalk, Unblob and Hopper are bring-your-own unless
separately approved, because an analysis tool that quietly installs a
toolchain is a supply-chain decision the operator did not make.

**Advantages.** One routing surface over many engines means the agent picks
by target shape rather than by which decompiler happens to be on the PATH.
Evidence IDs make a conclusion citable, re-checkable and pageable, which is
what stops a long investigation from becoming a summary of a summary.
Summary-first traversal keeps multi-gigabyte artefacts tractable inside a
context window. The explicit observation / inference / unknown taxonomy is
the direct antidote to a confident wrong answer — the characteristic failure
mode of reverse engineering by language model. Target-free operations (PE
resource trees, offline ELF layout, core-file threads) give real evidence
with no disassembler database at all. And per-tool availability reporting
turns an opaque failure into a specific, fixable prerequisite.

**Disadvantages.** The engines are bring-your-own: without Ghidra, IDA or
JADX those routes are unavailable while the catalog still lists them, so the
tool list overstates what this host can do until you read
`tool_availability`. Per-client registration is fiddly — PATH, stale
`mcp.json`, sandbox launchers, approval policy — and a skill install alone
registers nothing, which is the most common way to conclude "it does not
work" when it was never connected. Client-side truncation can silently
preview a large result, so anything reused later must go through Evidence
IDs rather than the message you received. Static analysis answers about code
shape and never about the servers the binary calls, the config it fetches or
the flags it flips at runtime — behavioural questions still need a runtime
you may not be allowed to run. And a decompiler is a model of the binary,
not the binary: where its output and a byte-level read disagree, the bytes
win.

**Usage.**

```bash
# capability first, always
rea doctor --client opencode --json
rea setup  --client opencode --dry-run --json
rea setup  --client opencode --yes

# nothing installed yet, inside an authorized scope
npx -y rea-agents@6.4.0

# MCP registration is per client — the skill install alone does not do it
# (the doctor output prints the exact block for your client)
```

Typical session shape:

```
route the target   → open_binary / analyze_javascript_application / ...
summarize          → detail: "summary"
drill              → inspect_analysis_view with the parent_evidence_id
corroborate        → static first, runtime only when static cannot answer
record             → cite Evidence IDs, mark inference and unknown separately
finish             → re-walk the question checklist: answered / partial / open
```

---

## How the four fit together

They are stages, not a collection. graphify reads a corpus and emits a
graph; Obsidian holds that graph as browsable notes alongside the ones you
wrote by hand; Soup takes a base model and a dataset and produces the model
you actually want to deploy; REA takes the thing that shipped and tells you,
with evidence, what it really contains. Map, notebook, model, magnifier.

Two boundaries matter more than the overlap. graphify's output and an
Obsidian vault generated from it are *derived* — regenerate them, do not
hand-edit them, or the next `--update` or export will quietly discard your
work; keep authored commentary in notes that link to the generated ones. And
Soup and REA sit on opposite sides of the build: one produces an artefact
from source and data, the other inspects an artefact whether or not you have
that source — which is exactly why the same vocabulary (observations,
inferences, unknowns) is worth holding both of them to.

The theory underneath entry 3 — what pretraining, LoRA, quantisation and
preference optimisation actually are — is written out in
[llm](../llm/README.md); the schedulers and hardware it runs on are in
[gpu](../gpu/README.md) and [distributed](../distributed/README.md); and the
way these tools get misused belongs in [mistakes](../mistakes/README.md).

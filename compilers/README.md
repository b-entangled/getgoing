# Compilers — how source code becomes hardware's instructions

Most engineers use a compiler the way they use a router: it sits in the
path, it is assumed to work, and it gets blamed when the packet does not
arrive. This section is for people whose code other machines execute —
kernels, parsers, numeric loops, generic libraries — and who have never
read a compiler from front to back. This reference explains what happens
between the file you edited and the instructions that retire, so that a
strange assembly listing, a 3x slowdown at -O0, or an optimiser that
quietly deletes your null check reads as a mechanism with a name instead
of a mystery. For an executable build-it-yourself path in Go, see
[Building a DSL, compiler and assembler](BUILDING_A_LANGUAGE.md) and the
[`minilang` implementation](https://github.com/b-entangled/getgoing/tree/main/compilers/minilang).

Every entry has the same four parts, in the same order: the need a
mechanism fills (**Why.**), the mechanics that fill it (**How.**), the
ways it fails in production (**Where it breaks.**) and the flags,
commands, tools and metrics that govern it (**In practice.**). Read them
top to bottom the first time — entry 1's pipeline is the map the other
fifteen stand on — and after that they work as reference. The overview is
prose; the companion `minilang` compiler has an executable test suite
covering its grammar, assembler and bytecode behavior.

## Runnable implementation — [`minilang/`](minilang)

Expand the source panel below to read the Go implementation that accompanies
the compiler walkthrough.

The other sections pick up where this one hands the program off: the GPU
runtime, launches and profiling are [gpu](../gpu/README.md); the kernel
underneath them and the measurement tools are
[linux](../linux/README.md), with composed kernel-path walkthroughs in
[linux/DESIGNS](../linux/DESIGNS.md); the executable format, ELF loader
and secure process startup are traced in
[program execution](../execution/README.md); Go's own compiler, escape analysis
and runtime are [internals](../internals/README.md) and
[concepts](../concepts/README.md); where ML compilers ship their kernels
is [rl](../rl/README.md) and [llm](../llm/README.md); SQL planning lives
in [database](../database/README.md); and the ways people get all of
this wrong are [mistakes](../mistakes/README.md).

---

## 1. What a compiler is: the translator that buys your cycles

**Why.** Two costs sit between your source and your running program: the
one you pay at build time and the one every user pays at run time, and
the entire discipline is the trade between them. The need is one job
description for that trade, so that when someone says "the optimiser
reordered this" you can name the stage, the machine and the guarantee
they are arguing about.

**How.** A compiler translates and optimises in one job. Translation is
meaning-preserving: the same program, re-encoded into a lower-level
language. Optimisation is not meaning-preserving but meaning-tightening
— it may only change what no defined behaviour can observe. The stages
they share are conventional, and this entry exists so the other fifteen
have a map to stand on:

```
source: text → lex → parse → semantic analysis → typed AST
→ typed AST → lower to SSA IR → optimise passes → clean IR
→ clean IR → instruction select → schedule → allocate registers
→ object files → linker resolves symbols → relocated → executable
```

Front loading the vocabulary: lexing and parsing build the tree
(entries 2-3), the IR is the language optimisation speaks (entry 4), the
optimiser is where the cycles are actually bought (entry 5), and the
backend plus the linker turn the result into bytes for one instruction
set (entries 6 and 8). Everything after entry 6 is specialisation: same
pipeline, different machine.

Who pays, when, and what the compiler is allowed to know decides the
mode you are in:

| Mode | Who pays | When | Feedback |
|---|---|---|---|
| Ahead-of-time (C, Go, Rust) | build machine | each build | source, no profile |
| JIT (HotSpot, V8, LuaJIT) | user's process | first runs | hot types and branches |
| Interpreted (CPython) | user, per opcode | every run | everything, no codegen |
| AOT plus profile (PGO) | CI, then prod | two phases | measured, fed back |

The frame for the whole section is this: every optimisation eventually
cashes out as fewer instructions retired, fewer cache misses, or fewer
bytes moved — on one specific machine, under one specific memory model.
The compiler is the contract between what the programmer meant and what
the silicon will actually do, and each entry below is a clause in it.

**Where it breaks.** Blaming the wrong stage: a 10x algorithmic loss is
not an -O2 problem, and no pass will recover a cache-hostile data
structure. Expecting translation to fix a design — moving Python to C
keeps the N^2 loop, it just makes it a faster N^2. Treating -O0 output
as "the language": -O0 is a debugging build with every temporary spilled
to the stack, and optimised builds may legally differ in timing, in
floating-point detail and in which error paths exist at all. Mixing
toolchains — a front end from one vendor, headers from another, an ABI
from a third — produces programs that compile and then fail at the first
call. Assuming the compiler sees what you see: macros, headers and
inlining mean the source line on the stack trace may belong to a
function that no longer exists as a function.

**In practice.** Read the machine before reading the passes: `gcc -O2 -S
x.c -o x.s` for the output, `clang -O2 -Rpass=inline -c x.c` for what
the optimiser claims it did, `go build -gcflags='-m=2' ./...` for Go's
inlining and escape decisions. Measure the cost with `perf stat -d
./app` — instructions, cache misses and branch misses as separate
numbers, because they name different cures. Compiler Explorer
(godbolt) is the fastest way to go from question to assembly in one
browser tab, and entry 15 is the workflow for reading what comes back.

---

## 2. Lexing and parsing: tokens, grammar and the first honest tree

**Why.** Before anything can be checked or optimised, a byte stream has
to become structure: first a sequence of tokens, then a tree that
matches a grammar. The need is a cheap deterministic first pass whose
failures are readable, because parsing is the smallest phase in the
pipeline and the one users judge the compiler by.

**How.** Lexing is regular. A finite automaton over characters decides
that `x9` is an identifier, `42` is a number and `->` is an arrow, and
it does so with no knowledge of context. Grammars are context-free:
each rule names a nonterminal and the sequences of terminals and
nonterminals that may stand for it. Parsing matches the token stream
against those rules and returns the tree recording which rule matched
where — the first structure in the pipeline that is honest about the
program's shape rather than about its characters.

Recursive descent is the standard implementation: one function per
nonterminal that consumes tokens and returns a subtree, and it wins in
practice for a boring reason. A hand-written parser can produce a
useful message and resynchronise at the right token; a generated
parser's message is a table index and its recovery is a crash. Go's
parser is a single pass over the token stream producing the AST
directly, no lookahead buffers and no backtracking, and compile speed
is treated as a feature there rather than a side effect.

A toy grammar and the stream that exercises it:

```text
stmt   ::= "if" expr "then" stmt ["else" stmt]
         | ident "=" expr ";"
         | "{" stmt* "}"
expr   ::= ident | number | expr "+" expr

tokens: if a then x = 1 ; else x = 2 ;
tree:   stmt[ if expr[a] then stmt[ x = expr[1] ]
                else stmt[ x = expr[2] ] ]
```

Ambiguity is where grammars earn their keep. The dangling else is the
classic: `if a then if b then x else y` has two parse trees, because
nothing says which `if` the `else` belongs to. Every language resolves
it by rule — C, Java and Go bind `else` to the nearest unmatched `if`,
which is why deeply nested conditionals are reformatted with braces as
a matter of style rather than of correctness. Error recovery is the
other half: report the first error, skip to the next `;` or `}`, and
keep going, so a missing brace does not produce 400 cascading messages
about a function that was never closed.

**Where it breaks.** Cascading diagnostics, the sign of a parser that
gives up instead of synchronising: one missing comma reported as 30
errors, each blaming a different line. Off-by-one at end of file, where
the "unexpected token" is actually "unexpected end of input" and the
message names the wrong construct entirely. Hand-written lexers and
unicode: identifiers with non-ASCII letters, normalisation differences
between editors, or a number lexer that accepts `0x` with nothing after
it. Context-sensitive corners bolted onto a context-free grammar — Go's
automatic semicolon insertion, JavaScript's, and C's preprocessor, which
runs before the lexer and can paste two tokens into one that no grammar
expected. Error messages that name the rule instead of the code: "parse
error at 432, 17" tells a user nothing their editor cannot already
underline.

**In practice.** `clang -fsyntax-only -Wall x.c` runs exactly this far
and stops — the cheapest syntax check there is. `clang -Xclang
-ast-dump -fsyntax-only x.c` prints the tree the parser built, which is
the fastest way to see what your macro actually expanded to. Compiler
Explorer is also a parser laboratory: change the grammar's output
language and watch the same source re-elaborate. When writing a parser,
prefer error messages that quote the user's line back with a caret over
messages that name your grammar's nonterminals, and test the recovery
path deliberately — feed it a file with three separate errors and count
the diagnostics it emits.

---

## 3. Semantic analysis: types, names and binding

**Why.** A tree that parses can still name things that do not exist, use
values in ways nothing supports, or call a function with the wrong
number of arguments. The need is a pass that decides what the program
means, in full, before anyone is allowed to optimise it — and that
reports its rejections in the language's own vocabulary.

**How.** Symbol tables carry the pass: a stack of scopes, each a map
from name to declaration, pushed on block entry and popped on exit, so
that `x` in an inner block shadows `x` in an outer one exactly as the
specification says. When names bind is a design choice with visible
consequences. Early binding — a name resolved at compile time to one
declaration — lets the compiler check everything and reject typos;
late binding — resolved at run time through a vtable or a method
dictionary — lets code be added after the program is built. Most
languages do both: variables early, methods late.

Type checking then decides which operations each value supports.
Structural typing checks the shape of a type and ignores its name, so
any value with the right methods satisfies an interface; nominal typing
checks the name and requires a declaration:

| Question | Structural (Go, TypeScript) | Nominal (Java, C++, Rust) |
|---|---|---|
| Usable as U? | if the methods match | only if declared so |
| Typos caught | at the use site | at the declaration |
| Retrofitting | works on types you do not own | needs editing or wrapping |
| Refactor safety | accidental adoption is possible | adoption is explicit |

Generics are where the cost model becomes explicit, because there are
only three real strategies and each pays differently:

| Strategy | Used by | What is emitted | Cost |
|---|---|---|---|
| Monomorphisation | Rust, C++ | one clone per type | fast calls, code grows |
| Dictionary passing | Go, Swift | one copy, type methods | one body, indirect |
| Erasure | Java, C# | one body, values boxed | simplest, boxing and casts |

```go
// Go: one instantiation per shape, plus a dictionary of type methods.
func Sum[T int | float64](xs []T) T {
    var t T
    for _, x := range xs {
        t += x
    }
    return t
}
```

Constant evaluation begins here too: `enum` values, array lengths,
`constexpr` and `const` initialisers are evaluated by the semantic phase
using the type rules it has just established, so a division by zero in a
constant expression is a compile error while the same division at run
time is a crash. This phase produces the clearest diagnostics in the
whole pipeline, because it is the only one that still knows your names.

**Where it breaks.** Inference failures reported at the use site rather
than the cause: "cannot infer T" three frames deep in a library. Generic
monomorphisation exploding — a recursive generic or a deeply nested
container chain emitting thousands of clones, doubling compile time and
instruction-cache pressure at run time. Shadowing the language allows
and the reader does not: a `for` variable hiding an outer one, legal and
invisible. Floating-point constants folded at a different precision than
run-time evaluation, so a check passes at compile time and fails at run
time — C's excess precision and the x87 history are the canonical case.

**In practice.** Turn on the shadow and conversion warnings: `-Wall
-Wextra -Wshadow -Wconversion` in clang and gcc, `go vet` for the Go set,
`clippy` for Rust — the cheap proxies for this phase's misses.
`-ftime-trace` (clang) and `-ftime-report` (gcc) show what semantic
analysis and instantiation cost, which matters once generics enter the
tree. In an IDE, "go to definition" is this phase exposed: if it
resolves, the symbol table saw the name; if it spins, the solver is
searching.

---

## 4. Intermediate representations: SSA and the lingua franca

**Why.** A front end for every language crossed with a backend for every
instruction set is N times M compilers; nobody maintains that. The need
is one representation in the middle, expressive enough for any source
language and simple enough that optimisations written once apply to all.

**How.** The first useful IR is three-address code: one operation and
one result per instruction, so `a = b + c * d` becomes a multiply
followed by an add. That shape makes the data flow explicit — you can
see which value feeds which operation — and it is what the tree is
lowered into before optimisation starts.

Static single assignment goes one step further: a value's name
identifies its definition and each use points straight at it, so
propagation becomes a lookup. Joins need a way to say "this value came
from one of two paths", which is what the phi node is for:

```llvm
; before SSA (pseudocode) — one name, many values across the loop
i = 0
loop:  i = i + 1
       if i < n goto loop
; after SSA — one definition per value, phi at the join
loop:
  %i = phi i32 [ 0, %entry ], [ %i.next, %loop ]
  %i.next = add i32 %i, 1
  %cmp = icmp ult i32 %i.next, %n
  br i1 %cmp, label %loop, label %exit
```

SSA makes optimisation tractable because def-use chains are trivial to
walk: propagation is a lookup, and two identical operations on identical
operands are duplicates that global value elimination deletes:

```llvm
%a = add i32 %x, %y
%b = add i32 %x, %y
%c = mul i32 %b, %z
```

Dead code becomes "a definition with no uses" rather than a search
through control flow. One structural fact completes the picture: block A
dominates block B when every path to B runs through A, and the dominance
frontier of a definition is where phi nodes are inserted.

LLVM IR is the industry's lingua franca: clang, the Rust front end, HIP,
Swift and dozens of vendor tools lower into it, and its x86-64, AArch64,
AMDGPU and NVPTX back ends turn it back into machine code. Rust keeps a
mid-level IR (MIR) first, because borrow checking needs structure LLVM
does not carry.

```
front ends: clang → rustc → Swift → HIP → one LLVM IR module
→ optimisers: SSA passes run once, target-independent, shared
→ back ends: x86-64 → AArch64 → AMDGPU → NVPTX → machine code
→ cost: N + M compilers instead of N × M, and one bug fixed everywhere
```

**Where it breaks.** IRs too rich, so front ends disagree about what a
construct means and the optimiser special-cases dialects — which is why
MLIR added named dialects instead of stretching LLVM IR (entry 14). IRs
too low, encoding one machine's quirks, so a second backend inherits
them as assumptions. Tracing an optimisation bug back through lowering
to the pass that chose it, which is what `-print-after-all` is for.
Version skew: bitcode from an older clang is not always readable by a
newer one, so shipping IR is a compatibility decision.

**In practice.** Emit the IR yourself: `clang -S -emit-llvm x.c -o -` for
the front end's output, `opt -passes='default<O2>' -S x.ll -o -` to run
the pipeline, `llc x.ll -o -` for code selection, and `clang -Xclang
-disable-llvm-passes` as the control: what the optimiser was handed.
`clang -O2 -mllvm -print-passes` lists every pass name, and pairing it
with `-Rpass=` (entry 5) turns the pipeline into a log.

---

## 5. The optimiser: the actual cleverness

**Why.** Source-level clarity and machine-level speed are in tension at
every line, and someone has to resolve the tension mechanically a
thousand times per build. The need is a pipeline of provably safe
rewrites that removes work nobody can observe, without ever being
allowed to remove work someone can.

**How.** Passes are organised in waves: cheap intraprocedural rewrites
first, then inlining and loop work, then module-level clean-up, with
later passes feeding on the opportunities earlier ones opened. The flag
levels encode an attitude rather than a menu. -O0 runs almost nothing,
because a debugging build wants one statement per instruction. -O1 does
the cheap local work fast. -O2 is the bargain almost everything ships
with: inlining, GVN, loop optimisation, vectorisation. -O3 unrolls
aggressively and inlines more, trading code size for latency — and
occasionally being slower, because a bigger function missed the
instruction cache.

The classics, one line each: constant folding evaluates expressions at
compile time (`3 * 4` becomes `12`); common subexpression elimination,
in its global form GVN, computes one value per distinct operand tuple;
strength reduction replaces expensive operations with cheaper ones
(`x * 8` becomes `x << 3`, `x % 2` becomes `x & 1` on unsigned);
loop-invariant code motion hoists work that does not depend on the
induction variable out of the loop; dead-store elimination removes
writes that nothing reads before the next write; and inlining, the
queen of the passes, replaces a call with the callee's body so that
every local pass afterwards sees the whole picture. An uninlined helper
is a wall; remove the wall and constant propagation walks straight
through it.

Whether a rewrite is legal depends on memory. Alias analysis decides
whether two pointers may name the same object, and type-based alias
analysis (TBAA) goes further and bets that an `int *` and a `float *`
never do — which is why C hands you the contract as `restrict` (or
`__restrict`): promise no aliasing and the compiler reorders your loads
and stores freely. At the bottom of all of it sits the as-if rule: the
compiled program may differ from the source in every way except
observable behaviour — I/O, volatile accesses and atomics are the
boundary where "as if" stops, and entries 7 and 16 are about what
happens when code leans on the wrong side of it.

Remark output is the window into all of this:

```text
remark: 'sum' inlined into 'total' (cost=always, threshold=100)
remark: loop not vectorized: unsafe dependent memory operations
remark: couldn't unroll: trip count could not be determined
```

**Where it breaks.** Inlining heuristics blowing the code size budget:
a hot path that fit in 16 KB of instruction cache is now 60 KB and runs
slower than the call it replaced. Alias analysis being wrong for a good
reason — a hand-rolled container, a cast, or a union — so reordering
produces stale reads that appear only under -O2. TBAA meeting type
punning, which is entry 16's whole subject. A loop that refuses to
vectorise because one pointer's provenance is unknown, silently leaving
8x on the table; the pass does not warn unless you ask. Dead-store
elimination removing a write you were using as a side channel — a
timestamp, a debug buffer, a memory-mapped register — because from the
language's point of view it was dead. Passing -Ofast to numerical code
and discovering that the associative rewrites changed the answer.

**In practice.** Ask the optimiser: clang takes `-Rpass=<pass>
-Rpass-missed=<pass> -Rpass-analysis=<pass>`, GCC takes
`-fopt-info`, `-fopt-info-vec-optimized` and `-fopt-info-inline`, and
the missing variants matter more than the successes. Narrow a question
to one pass with `opt -passes='inline' -S in.ll -o -` so you see its
effect alone. For vectorisation specifically, `-Rpass=loop-vectorize`
plus the miss reason tells you whether the fix is a restrict qualifier,
an alignment hint or a trip count you know and the compiler does not.
When a build gets slower after a flag change, diff the remark logs
before diffing the code — the pass decision usually moved first.

---

## 6. Instruction selection and register allocation: IR meets the ISA

**Why.** The IR speaks in abstract operations and infinite registers; the
machine speaks in a fixed register file, an encoding and a set of
addressing modes. The need is a lowering that both finds the cheapest
legal encoding for each operation and decides where each live value
physically lives — the two decisions that most determine whether the
result is fast.

**How.** Instruction selection is pattern matching: the optimised IR is
built into a directed acyclic graph per basic block, and the selector
covers that graph with the largest machine instructions it can find.
The win is visible immediately — three IR operations (load, add,
address) collapse into one `mov` with a scaled index:

```asm
; IR:   t = load(base); u = add(t, 16); v = load(u)
; x86 folds the addressing mode into the operation:
mov    rax, QWORD PTR [rbx + rcx*8 + 16]
; and a spill, when the allocator ran out of registers:
mov    QWORD PTR [rbp - 40], r12
add    rax, QWORD PTR [rbp - 40]
```

Register allocation is the harder half. The classic solution is graph
colouring: build an interference graph where two values connected by an
edge are live at the same time, then colour it with as many colours as
there are registers — each colour is a register, and a node that cannot
be coloured gets spilled to the stack frame. Linear scan trades some of
that quality for O(n) time over sorted live intervals, which is why JITs
use it and ahead-of-time compilers use colouring. Spills are the tax:
every spilled value becomes an extra store and an extra load, and a hot
loop that spilled one accumulator often loses more than it gained from
whatever restructuring caused the pressure.

Scheduling then reorders instructions within basic blocks to hide
latency and fill issue slots: independent work moves up into the delay
of a load, and on a modern x86 core the scheduler is aiming at
specific ports — an integer add wants the ALU, a multiply wants port 1,
two loads per cycle — because the machine models itself in microseconds
and the compiler has tables for it. The ABI is the fixed part of all
this: calling conventions decide which registers carry arguments and
return values, the prologue and epilogue save and restore what the
function uses, SysV x86-64 requires a 16-byte stack alignment before
each `call`, and structs larger than 16 bytes travel in memory rather
than registers. Violate any of it and the code still runs — until a
callback from another compiler's object file arrives.

| ISA | Encoding | Shape | Backend consequence |
|---|---|---|---|
| x86-64 | 1-15 bytes | memory operands in ALU ops | dense decode, rich addressing |
| AArch64 | 4 bytes | load-store, 32 GPR + 32 SIMD | simple decode, explicit loads |
| RISC-V | 4 bytes (2 with C) | load-store, minimal base | small core, extensions |

**Where it breaks.** Register pressure from a rewrite that looked free:
an aggressive inline doubles live values, the allocator spills, and the
"optimisation" costs 20%. ABI mismatch — a caller passing a small struct
in a register while the callee reads it from the stack — corrupting
arguments only on some call paths, typically the callbacks. Vector ABI
divergence: AVX-512 leaves change whether a float travels in an XMM
register or in memory, so two objects built with different `-march`
flags disagree inside one program. Red-zone surprises on x86-64: the 128
bytes below the stack pointer belong to leaf functions, and signal
handlers that assume otherwise get overwritten. Scheduling against a
latency table that does not match the silicon shipped on.

**In practice.** Look at what was selected and where values landed:
`gcc -S -fverbose-asm x.c` annotates assembly with source lines and
register decisions, `clang -S -masm=intel x.c` for readable operands,
`objdump -d -M intel app` for what shipped. `-fstack-usage` writes a
per-function stack file, the cheapest spill-pressure alarm there is.
`llvm-mca` simulates a snippet's throughput and port pressure for a
named microarchitecture without running it — the right tool when the
question is latency-bound or port-bound. Changing a calling convention
means rebuilding every object in the program, not one flag.

---

## 7. The compiler and memory: layout, caches and the memory model

**Why.** The memory model is where hardware interaction stops being
abstract: structure padding, cache lines and instruction reordering are
decisions made by your compiler on your behalf, and a wrong one shows up
as a 10x penalty or a data race that the source says cannot happen. The
need is to know which of these are yours, which are the compiler's, and
where the language specification stops and the silicon begins.

**How.** Layout comes first because it is free speed. Every field is
padded to its alignment — `struct {char a; int b; char c;}` is 12 bytes
on x86-64, not 6 — so struct order is a performance decision no profiler
will attribute to you. The bigger choice is array-of-structs against
struct-of-arrays: a particle loop that touches only `x` reads every cache
line of every struct to get 4 useful bytes, while the same data as three
parallel arrays reads exactly the bytes it needs.

Cache behaviour is then a property of the loop nest. A 64-byte line
holds 16 floats, so a naive `ijk` matrix multiply streaming down a
column re-fetches the same line once per row, and the fix is tiling:
rewrite the loops so the working set for a tile fits in L1 and every
loaded line is reused before it is evicted.

```
naive: stream a column → one useful float → 60 of 64 bytes wasted
→ next row refetches the same line → memory-bound at 3% of peak
tiled: load a 64×64 tile of A and B → compute the tile → 16 uses per line
→ resident set ≈ 2 × 16 KB → stays in L1 → bandwidth demand drops 50x
```

The compiler's part of the same problem is vectorisation and unrolling,
and both have preconditions: a bounded trip count, proof that the
pointers do not alias, and useful alignment so the vectoriser can peel
the first iteration instead of running a scalar prologue. Unrolling
duplicates the body to expose instruction-level parallelism and shorten
branch overhead; `-fno-alias`, `restrict` and `__builtin_assume_aligned`
supply the proof the compiler cannot derive.

Then the boundary. `volatile` stops the compiler from caching a value in
a register and from deleting a write — nothing more. It is not atomic,
it orders nothing, and the ways people get this wrong are catalogued in
[mistakes](../mistakes/README.md). Atomics are the real mechanism, and
their orders are a table you should be able to recite:

| Order | What it guarantees | Typical use | Cost on x86 / ARM |
|---|---|---|---|
| seq_cst | global total order | lock-free algorithms | full fence / expensive |
| acquire/release | paired hand-offs | queues, once-init flags | plain mov / DMB |
| relaxed | atomicity only | counters, statistics | plain mov / plain mov |

Release stores and acquire loads stop the compiler — and the hardware —
from moving a load past the publication of the data it reads, which on
real architectures means emitting fences and forbidding reordering the
compiler would otherwise be entitled to perform.

**Where it breaks.** Padding you did not ask for: a struct that grew by
one field and crossed a cache-line boundary, so writes from two threads
now share a line and ping-pong — false sharing, invisible in source and
obvious in a `perf c2c` report. Assuming `volatile` gives atomicity or
ordering: it gives you neither, and it defeats the caching that made
your loop fast. Over-ordering — `memory_order_seq_cst` everywhere
because it feels safer, paying full fences on ARM for counters nobody
reads in order. The vectoriser silently declining because one pointer's
provenance is unknown, with no warning unless you asked for remarks.
Tiling for FLOPs instead of cache, so arithmetic improves and the
working set no longer fits.

**In practice.** Read the layout before the algorithm: `pahole -C
'my_struct' prog` prints offsets and padding, and `clang -Xclang
-fdump-record-layouts -c x.c` does it before you have a binary. Check
vectorisation with `-Rpass=loop-vectorize -Rpass-missed=loop-vectorize`
and treat every miss as a bug report. False sharing: `perf c2c record
./app && perf c2c report` names the contended cache lines. Choose atomic
orders from the table above and record the reason next to the call; on
ARM, compare wall clock between acquire/release and seq_cst queues.

---

## 8. Linking: the tiny second compiler

**Why.** Compilation produces fragments: one object file per
translation unit, each full of calls it cannot yet resolve and
addresses it does not know. The need is a second, smaller compiler that
runs after all the others, resolves those references across the whole
program, and produces one image the loader can map.

**How.** An object file is a container — ELF on Linux, Mach-O on macOS,
PE on Windows — with sections for code (`.text`), initialised data
(`.data`), zero-initialised data (`.bss`) and a symbol table listing
what it defines and what it needs. Static linking archives those
fragments into one binary: the linker takes whichever object defines an
unresolved symbol, copies it in, and repeats until nothing is left
open — which is why archive order matters and why a static library with
circular references needs to be named twice. Dynamic linking defers the
work: the linker records the dependency and leaves a hole filled at load
time by the dynamic loader, so one `libc.so.6` update fixes every
program that uses it.

Position-independent code is the awkward part. A shared object cannot
know its own address, so global data is reached through the global
offset table and external calls through the procedure linkage table: the
call site jumps to a stub, the stub loads the real address from a table
entry on first use, and a lazy binding resolver fills it in — the GOT/PLT
dance, which costs one indirect jump until resolved and buys you a
library that can be mapped anywhere.

Relocation records are how any of this is expressed: each object lists
"at offset 37 in `.text`, there is a call needing symbol `foo` minus
this address", and the linker patches the bytes once it knows the final
layout. Link-time optimization takes this literally — the linker gets
the optimiser's IR instead of finished machine code, re-runs inlining
and dead-code elimination across translation-unit boundaries, and then
does the real code generation. Incremental relinking and split objects
exist because a full link of a large C++ program can cost seconds on
every save:

```
objects: a.o defines main, needs parse → a.o, b.o, libc.so.6
→ relocations: fix up calls and data refs once addresses are known
→ image: .text + .data + .bss + dynamic table, mapped at a chosen base
→ load: loader maps segments, resolves the dynamic table, runs init
```

**Where it breaks.** Symbol clashes, the oldest failure: two objects
each defining `parse`, and the linker silently picks the first — or
errors with "multiple definition" if both are strong. One Definition
Rule violations in C++ — the same class defined differently in two
translation units — where the linker keeps one layout and the other code
reads fields at the wrong offsets, corrupting memory with no type error
anywhere. ABI mismatches across a `.so`: object files built with
different `-D_GLIBCXX_USE_CXX11_ABI` values, or a library rebuilt with a
newer compiler and a changed struct layout, failing at the first call
that crosses the boundary. Static initialisation order, where two
translation units' global constructors run in an order nobody specified
and one initialises from a table the other has not built yet. Name
mangling is what makes all of this visible: C++ symbols encode the
namespace, class and parameter types, so an unresolved reference reads
`_ZN3foo3barEii` instead of `foo` — the error is unreadable and the
information is complete.

**In practice.** Ask the linker what it did: `nm -C a.o` prints
demangled symbols and their binding, `readelf -r a.o` shows relocation
types, `readelf -d prog` lists dynamic dependencies, `ldd prog` resolves
them, and `c++filt` decodes any mangled name you find. Catch missing
symbols at link time instead of run time with `-Wl,-z,defs`, and
catch accidental exports with `-Wl,--no-undefined`. For LTO, build both
objects with `-flto` (`gcc -flto -O2`) and use `-Wl,--print-gc-sections`
to watch unused functions disappear — if nothing is removed, the
visibility of your symbols is hiding them from the collector.

---

## 9. JIT compilation: compilers that run while you do

**Why.** An ahead-of-time compiler must commit before it knows anything
about the program it is compiling; the running program knows its own
types, its own hot branches and its own dead paths. The need is a
compiler that spends its effort where the evidence is — and that can
undo, safely, any bet the evidence stops supporting.

**How.** Production JITs are tiered, because a fast-starting bad
compiler and a slow-starting good one serve different moments. A
profiling interpreter or template baseline JIT compiles everything
immediately and poorly while counting calls and recording observed
types; once a function crosses a threshold — HotSpot's tiered system
moves through C1 into C2 — an optimizing compiler inlines, unrolls and
specialises it, using the recorded profile as if it were a fact.

That "as if" is speculative optimisation, the single most important JIT
concept. The compiler sees that a call site has always seen a
`Rectangle`, so it specializes to `Rectangle`, guards the assumption
with a cheap type check, and keeps the generic path as a fallback. When
the guard fails the process performs deoptimisation: it reconstructs the
interpreter frame from the optimised one, drops to the slow tier at
exactly that program point, and lets the profiler see the new evidence.
On-stack replacement does the same for a long-running loop whose
specialised version becomes valid mid-iteration.

```
cold: function entered → interpreter counts → profile records types
→ tier up: baseline JIT emits code quickly → call counter reaches limit
→ hot: optimizing JIT inlines and specializes → guards added
→ miss: guard fails → deoptimise → interpreter resumes → re-profile
```

Method JITs compile whole functions, which is simple and matches
debuggers; tracing JITs record the path actually taken and optimise the
linear trace, which fits branchy code like JavaScript and was LuaJIT's
signature. Either way the generated code lives in a code cache with its
own lifecycle, and warmup is the visible cost: throughput climbs as
tiers are crossed, then may dip briefly when a deoptimisation sends hot
code back down. HotSpot's C1/C2 pair, the JavaScript engines' baseline
plus optimizing tiers, and LuaJIT are the three shapes worth knowing.
Go deliberately took the other road — ahead of time, no tiers, no
warmup, which is why its build times and startup latency are the
headline and its peak optimisation is bounded by what CI could know; the
runtime that supports that choice is in
[internals](../internals/README.md).

**Where it breaks.** Deoptimisation storms, where a guard fails
repeatedly and the process spends its time rebuilding frames instead of
executing — a type that is polymorphic in practice but monomorphic in
the profile. Cold start on a serverless function or a CLI that now
pays a JIT warmup before the first request, which is why some engines
ship a pre-warm snapshot. Code-cache pressure: too much specialised code
evicts or blocks compilation, and the engine falls back to the baseline
tier under load, so performance degrades exactly when traffic rises.
Profiles polluted by measurement — a benchmark that times the warmup
window reports a 5x regression that is really a tiering artefact. And
silent miscompilation risk: speculative optimisation enlarges the
surface where a wrong assumption produces wrong results rather than a
slow path.

**In practice.** Watch the tiering instead of guessing: HotSpot takes
`-XX:+PrintCompilation`, plus `-XX:+UnlockDiagnosticVMOptions
-XX:+PrintInlining` for the decisions behind them; V8-style engines take
`--trace-opt --trace-deopt` for optimisation and bailout events in order.
Measure after warmup and report the warmup separately — run the workload
long enough that the compilation queue drains, then take the steady-state
number. Code-cache pressure shows up as a falling ratio of optimized to
baseline frames while CPU stays flat. For a language with no JIT, the
equivalent discipline is the build: keep Go's link step and startup
measured, since there is no warmup to hide a regression behind.

---

## 10. GPU codegen: how SIMT changes the backend

**Why.** Every backend assumption from the CPU world — one register per
value, a branch predictor worth feeding, latency hidden by speculation —
is wrong on a SIMT machine. The need is a different set of choices in
instruction selection, register allocation and scheduling, made for 32
lanes that share one program counter.

**How.** Branches become predication. A short conditional is compiled as
one instruction carrying per-lane predicates: the arithmetic for both
sides may be emitted and masked, so a divergent warp executes both paths
serially under lane masks and pays for lanes that did not want that
path. There is no branch predictor to feed and none to keep warm, so
the compiler's job is not to guess the direction but to make the taken
and untaken paths cheap when 32 lanes disagree.

Register allocation changes scale, not shape. A register is per lane: a
kernel using 64 registers for 1,024 threads needs 65,536 of the SM's
register file, and every value the allocator spills becomes a
per-thread trip to local memory that the memory system serves out of
the cache hierarchy. Pressure is multiplied by 32 in the only place it
matters — how many warps can stay resident.

Scheduling is repurposed for latency hiding. The instruction scheduler
reorders independent work into the delay of a memory access and packs
dual-issue slots, not to make one thread fast but to keep the issue
queue non-empty when a warp stalls, because the real latency-hiding
mechanism is the scheduler picking another resident warp. Occupancy is
therefore a codegen outcome as much as a launch setting: registers per
thread and static shared memory per block are decided by the compiler,
then combined with the block size you launch to produce the resident
warp count — the whole chain is in
[gpu](../gpu/README.md) entries 8-10.

```asm
; PTX — predicated instead of branched: the lane mask decides
setp.ge.s32   p1, %r1, %n;
@p1           add.s32 %r2, %r1, 1;    ; only active lanes execute
@!p1          mul.s32 %r2, %r1, 2;
```

The honest tension is that the compiler optimises for throughput across
a whole grid while you reason about one thread's control flow, and the
two perspectives disagree on exactly the code that is hard to read:
branches, early exits and per-thread divergence look tidy in source and
become serial execution on the wire.

**Where it breaks.** Divergence in algorithms that parallelise neatly on
paper — tree walks, hash probes, early-exit search — dropping branch
efficiency to 20-40% and making each warp perform several lanes' worth
of serial work. Register pressure from a convenient local array or a
deeply inlined helper, which pushes occupancy down and shows up first as
"memory latency" in the stall breakdown. Assuming CPU branch costs: an
`if` that is free on a core becomes a whole warp's worth of masked
execution here, so restructuring the branch into arithmetic sometimes
wins outright. Forgetting that a register choice made by the compiler,
not by you, is what sets the ceiling — the same source with a different
`-O` or a `__launch_bounds__` hint can land at 60% occupancy instead of
100%.

**In practice.** Run `nvcc -Xptxas -v kernel.cu` before anything else:
registers per thread, spill counts and static shared memory decide the
occupancy ceiling in three lines. Add `__launch_bounds__(maxThreads,
minBlocksPerSM)` to tell the compiler the shape you intend, so it
budgets registers toward your launch instead of assuming the maximum.
Read occupancy and stall reasons together in Nsight Compute — a kernel
at 12% occupancy that is 85% `stall_long_scoreboard` has a memory
problem, not a core problem. When a branch is suspected, restructure it
into predicable arithmetic (selects, masks, clamped indices) and
re-measure rather than micro-optimising the arithmetic inside it.

---

## 11. The CUDA compilation pipeline: nvcc, PTX and SASS ★

**Why.** "Compiling a CUDA kernel" names at least four artefacts and
two compilers, and picking the wrong one is the difference between a
binary that runs here and one that fails where you ship it. The need is
a map of stages and the flags that choose each fork.

**How.** nvcc is a driver, not a compiler: it sends the host C++ to the
host compiler (gcc or clang, ordinary object code) and takes the device
code through its own front end — clang, historically EDG — into NVVM,
LLVM IR with CUDA intrinsics. From there the backend writes PTX.

PTX is a virtual ISA: a text format for a virtual RISC register machine
with an unbounded register count, generic addresses and no notion of a
particular chip. It exists to be portable forward — PTX written today
compiles for a GPU that does not exist yet — and it is where CUDA stops
being C++. The backend then lowers PTX to SASS, the real machine code
for one architecture: its register count, predication model, shared
memory banks, encodings.

```
cuda source: kernel.cu → front end → NVVM/LLVM IR → PTX
→ PTX → ptxas backend → sm_90 SASS → cubin for one architecture
→ fatbin: sm_80 + sm_90 + embedded PTX → driver → module load
→ launch: driver JIT if only PTX was available → resident → warps run
```

Those artefacts are packaged in containers, and the flags select them:

| Artefact | What it is | Contains | When it is used |
|---|---|---|---|
| PTX | virtual ISA, text | one kernel, no fixed registers | forward compat, NVRTC |
| cubin | ELF object for one arch | SASS for sm_XX | exact silicon, no JIT |
| fatbin | container | cubins plus embedded PTX | shipping to unknown GPUs |
| fatbin.h | C header | fatbin as a byte array | embedding in the binary |

`-arch=sm_90` means "emit SASS for an H100"; `-arch=compute_90` means
"emit PTX and let the driver compile it at run time". A release build
repeats `-gencode=arch=compute_90,code=sm_90` once per architecture and
adds one `code=compute_90` entry, so a future GPU can still load the
kernel. The driver JIT runs at first launch and caches its SASS under
the CUDA compute cache — which is why the first run is slower.

Separate compilation changes the build: with `-dc` each translation
unit emits relocatable device code, and a device link step (`-dlink`)
resolves references across `.cu` files.

At the other end, NVRTC compiles CUDA C++ at run time into PTX, for
kernels chosen or generated after the binary shipped:

```c
#include <nvrtc.h>
const char *src = "__global__ void k(float *a) {"
                  " a[threadIdx.x] *= 2.0f; }";
nvrtcProgram p; nvrtcCreateProgram(&p, src, "k.cu", 0, NULL, NULL);
nvrtcCompileProgram(p, 0, NULL);            /* log on failure */
size_t n; nvrtcGetPTXSize(p, &n);
char *ptx = malloc(n); nvrtcGetPTX(p, ptx); /* PTX, not SASS */
CUmodule m; cuModuleLoadData(&m, ptx);      /* driver JIT to SASS */
```

The analogy to write down is Java bytecode: PTX is the class file, a
stable intermediate the driver JIT compiles for the hardware present.
The difference — PTX is also AOT-friendly, because ptxas can do the same
job ahead of time and save the first-launch stall.

```bash
# SASS for the exact chip, plus PTX kept for the ones you do not have
nvcc -O3 -gencode=arch=compute_90,code=sm_90 \
     -gencode=arch=compute_90,code=compute_90 kernel.cu -o app
# separate compilation, then a device link
nvcc -dc -O3 a.cu b.cu && nvcc -dlink a.o b.o -o dlink.o
# what actually shipped
cuobjdump -sass app | less
nvdisasm -c app.cubin
```

**Where it breaks.** Shipping `-arch=sm_80` to a lab full of H100s: the
driver finds no PTX for sm_90 and reports "no kernel image is available
for execution" — at first launch, not at build. Omitting the
`code=compute_XX` entry from every `-gencode`, so the fatbin carries
SASS for today's cards and nothing for tomorrow's: forward compatibility
silently gone. First-run latency in a service nobody warmed, paying the
driver JIT on the request path. `-rdc=true` forgotten until the device
link fails on unresolved symbols, or kept without `-dlink`, so host
objects reference symbols that were never relocated. NVRTC output loaded
as if it were SASS, or a fatbin.h for the wrong architecture.

**In practice.** Build releases with a `-gencode` line per supported
architecture plus one PTX entry, recorded next to the driver version you
test against. Warm the compute cache at start-up before latency targets
are measured. Inspect what shipped with `cuobjdump -sass`, `cuobjdump
-ptx` and `nvdisasm -c`, and check the embedded arch list with
`cuobjdump -elf`. Log `nvrtcGetProgramLog` unconditionally — it is the
only error channel. In CI, compile the fatbin on a machine with no GPU.

---

## 12. Inside nvcc on your kernel: what the optimiser actually does ★

**Why.** Two kernels that differ by a refactor can differ by 4x in
throughput, and the cause is almost always a pass decision you never
saw. The need is to know what the device optimiser routinely does to
your code, what contracts it is allowed to assume, and how to read the
reports it prints about the result.

**How.** The device pipeline is recognisably the CPU one with different
priorities. Loop unrolling is more aggressive: there is no cache-pressure
per se for instruction fetch across a whole SM's worth of work, so the
compiler duplicates bodies to hide memory latency, shorten the branch
overhead and give the scheduler more independent instructions to slot
into a warp's issue queue. Inlining is aggressive to the point that
small `__device__` functions usually do not exist in the output — a
helper called from two sites becomes two copies of its body, which is
exactly what you want for a three-line index computation and exactly
what you do not want for a large shared routine.

Instruction scheduling then reorders across memory latency, aiming to
have the next independent operation ready when the previous load lands,
and it does so against a model of the SM's issue slots rather than a
single thread's timeline. Maths passes follow, and this is where the
first real contract appears. `-use_fast_math` is not a hint, it is a
set of promises on your behalf: flush denormals to zero in and out
(FTZ/DAZ), replace `sqrtf(x)` with `rsqrtf`-style approximations and
reciprocals, allow the standard library's approximate `sinf` and `cosf`,
and permit FMA contraction — fusing `a*b+c` into one instruction that
rounds once. What breaks, precisely: results in the denormal range
become zero, which can stall iterative solvers that converge through
them; reciprocal and sqrt approximations trade roughly 1-2 ulp of error
for throughput; and signed zero and NaN distinctions are no longer
respected. `--fmad=true` is the narrower version — contraction only,
IEEE rounding everywhere else — and it is the default, so you get FMA
merging whether or not you asked.

Register allocation closes the pipeline, and its failure mode is the
one worth memorising: too many live values force spills into local
memory, which is global memory wearing a different name, and the kernel
suddenly pays memory traffic for values that lived in registers in your
mental model. The occupancy arithmetic afterwards is unforgiving — 64
registers per thread against a 65,536-register SM gives 1,024 threads,
half the 2,048 cap, and the profiler will report that as the ceiling:

```text
ptxas info: Compiling entry function '_Z6kernelPf' for 'sm_90'
ptxas info: Function properties for '_Z6kernelPf'
    64 registers, 392 bytes stack, 0 bytes spill memory
ptxas info: Used 64 registers, 1120 bytes cubin
ptxas warning: Uses 96 registers, 104 bytes spill stores, 88 bytes load
```

The warning line is the whole game in one sentence: 96 registers push
residency to 672 threads (21 warps, 33% of cap), and the spill traffic
is now part of the kernel's memory profile.

There is no Compiler Explorer for CUDA, so reading the result means
disassembling what shipped and profiling what ran:

```bash
nvcc -O3 -Xptxas -v,-warn-spill kernel.cu -o app   # regs, spills, size
cuobjdump -sass app | grep -A20 'Body of main'     # real SASS mnemonics
nvdisasm -g app.cubin                              # SASS with source lines
clang -x cuda --cuda-device-only -S -emit-llvm \
      --cuda-gpu-arch=sm_90 kernel.cu -o -         # LLVM-level view
```

**Where it breaks.** Fast math passing through a convergence test:
`-use_fast_math` on a conjugate-gradient loop that stalls at iteration
4,000 because denormals were flushed on the way to zero — correct on
every input that stayed in the normal range, wrong on the one that
mattered. Silent register pressure from an aggressive inline: the
refactor looks free, `-Xptxas -v` reports 40 registers climbing to 92,
and throughput drops because occupancy halved. Spills described as a
memory problem: the stall breakdown says `stall_long_scoreboard`, so the
team tunes memory layout while the real cause is a spilled accumulator.
Assuming a `__device__` function is shared code: it is copied per call
site, and a large one bloats the cubin and the instruction cache.
Trusting the PTX you printed instead of the SASS you shipped — the
backend may fold, schedule or fuse something the IR view suggested was
impossible, and only the disassembly is the truth.

**In practice.** Make `-Xptxas -v` part of every build log and grep it
for `registers` and `spill`; treat any spill line as a defect. Bound
pressure deliberately with `__launch_bounds__` rather than
`--maxrregcount`, because the former tells the compiler your launch
shape and lets it trade registers against the target occupancy. Judge
the maths contract by workload: keep `-use_fast_math` off for anything
iterative or tolerance-bound, and leave FMA contraction on for the
rest. For the outcome, use Nsight Compute rather than eyeballs — read
achieved occupancy, register count, spill throughput and the stall
breakdown together, since they are one causal chain (details in
[gpu](../gpu/README.md)). When a kernel is mysteriously slower after a
small edit, diff the two SASS dumps; the difference is usually a handful
of spills, not a change in arithmetic.

---

## 13. Portability: LLVM everywhere, HIP, SYCL, SPIR-V

**Why.** Writing a kernel twice is a tax, and writing it once for one
vendor is a different tax paid later. The need is an intermediate form
that several front ends can target and several drivers can consume, so
that portability is a matter of changing a compiler instead of a
language.

**How.** LLVM is the common substrate beneath most of this. The NVPTX
backend turns LLVM IR into PTX, which is why clang can compile CUDA
directly (`-x cuda`) and why one optimiser stack serves both ecosystems;
the AMDGPU backend does the same for RDNA and CDNA silicon, which is how
ROCm compiles HIP. HIP itself is mostly a source-compatibility layer —
`hipcc` is a clang wrapper, `hipMalloc` maps onto `cudaMalloc`, and a
large share of CUDA ports are mechanical renames plus a build-file
change.

SYCL and its main implementation, DPC++, take the single-source route:
C++ with kernel lambdas compiled by clang into SPIR-V, a Khronos
intermediate format for compute and graphics that vendor drivers accept
and lower to their own ISA. Metal's AIR is the same idea inside Apple's
walled garden. The other great production compiler family is the shader
pipeline — HLSL or GLSL compiled to DXIL or SPIR-V, then a vendor driver
compiler that does the final lowering to GPU machine code — and WGSL
plays that role for WebGPU in the browser.

```
sources: CUDA C++ → HIP C++ → SYCL C++ → HLSL → WGSL
→ intermediates: PTX → LLVM IR → SPIR-V → DXIL → AIR
→ driver: vendor backend compiles once more → that silicon's ISA
→ constant: every path ends at vendor machine code you never see
```

Choosing a path is a trade of peak performance against lock-in and
toolchain weight:

| Path | Peak on NVIDIA | Peak on AMD | Lock-in | Toolchain |
|---|---|---|---|---|
| CUDA | native, full feature set | not portable | high | nvcc, one vendor |
| HIP | hipify, near-native | native | medium | hipcc (clang + LLVM) |
| SYCL | DPC++ (CUDA backend) | AdaptiveCpp, hipSYCL | low | per-vendor compilers |
| Triton | PTX via NVPTX | LLVM AMDGPU | low-medium | Python + MLIR |

**Where it breaks.** Warp-size assumptions baked into indexing maths —
code that computes `threadIdx.x >> 5` to get a warp id will be wrong
the moment the model changes, and `__shfl`-style primitives have
different widths and different semantics per vendor. Intrinsics with no
portable spelling: assembly fragments, inline PTX, architecture-specific
math built-ins and vendor libraries (cuBLAS, rocBLAS) that have
different tuning and occasionally different results. libdevice and
friends: a `sinf` implementation that is approximate on one platform and
IEEE on another changes numeric output across ports. Divergent
optimiser maturity — the same source may vectorise, unroll or spill
differently on two back ends, so a performance fix on one does not
transfer and neither does a regression. SPIR-V version skew between the
compiler that emitted it and the driver that must consume it, which
surfaces as a load failure rather than a compile error.

**In practice.** Compile for the intermediate and inspect it: `clang -x
cuda --cuda-device-only -S -emit-llvm kernel.cu -o -` shows the NVPTX
input, and `hipcc --offload-arch=gfx90a -S -emit-llvm a.hip -o -` the
AMDGPU equivalent. Validate SPIR-V with `spirv-val` before blaming the
driver, and check what your SYCL build actually emitted with
`-fsycl-device-code-split=off -Xclang -fsycl-allow-device-libraries`.
Keep one reference kernel per port and one numeric tolerance test that
runs on both, because the first sign of a portability problem is
usually a silent precision difference, not a build failure. Benchmark
each port separately: the flag sets that win on one backend are neutral
or harmful on the other.

---

## 14. Domain compilers: tiles, tensors and MLIR

**Why.** Abstraction usually costs you control — but tensor and tile
programs are regular enough that a compiler can make the hardware
decisions you would otherwise hand-write, without surrendering the
schedule entirely. The need is a language level where the algorithm is
stated once and the mapping to memory and lanes is stated separately.

**How.** Domain compilers boomed because the gap they fill is real: a
matrix multiply expressed as tiles, reductions and broadcasts contains
enough structure for the compiler to choose tiling, coalescing and
shared-memory staging, while a general C compiler sees only nested loops
over pointers it cannot prove apart. MLIR is the framework this
generation shares — a configurable stack of dialects where a program
descends from linalg-style tile operations through loop-level IR into
LLVM, NVVM or GPU dialects, and one set of optimisation passes applies at
every level instead of being rewritten per language:

```
python: tile program → Triton DSL → MLIR linalg → loop dialect
→ MLIR: LLVM dialect / NVVM / GPU dialect → LLVM IR
→ backend: NVPTX or AMDGPU → PTX or RDNA ISA → one kernel
→ same framework: CPU lowering from the same linalg, different target
```

Triton is the best-known example: the programmer writes tile-level code
in Python — load a tile, compute on it, store it — and the compiler
does the coalescing, the shared-memory staging and the synchronisation,
emitting PTX. Those kernels are what actually ship in the training and
inference stacks described in [llm](../llm/README.md). Halide made the
related split famous in one line: the algorithm says what to compute and
the schedule says how — which tile size, which fusion, which
parallelisation — and the same algorithm can carry many schedules.

Autotuners complete the picture by replacing the compiler's heuristics
with search: TVM and its relatives generate many candidate schedules,
benchmark them on the target, and keep the winner, turning compilation
into an empirical optimisation loop with a measured cost. The trade is
explicit — search costs build time and machine time, and the winner is
tuned to one device.

**Where it breaks.** The debuggability floor: when a generated kernel is
wrong or slow, the source you wrote is three dialects above the code
that misbehaves, and stepping through it means reading IR you did not
write. Dialect and version churn — APIs and pass names move quickly,
pinned versions drift from the drivers, and a reproducer needs a
toolchain container rather than a compiler flag. Autotuner overfitting:
a schedule tuned on one GPU generation and one batch size regresses
elsewhere, and nobody re-tunes because the search is expensive.
Assumptions the DSL cannot express — an odd tensor shape, a fusion the
scheduler declines, a layout change that breaks coalescing — surfacing
as a cliff in performance rather than a compile error. And the
productivity illusion: DSL code is short, so teams underestimate the
expertise needed to read what the compiler did with it.

**In practice.** Print the IR rather than guessing, and keep generated
PTX under the same inspection workflow as entry 12:

```bash
mlir-opt -mlir-print-ir-after-all x.mlir       # one dump per pass
mlir-translate -mlir-to-llvmir x.mlir -o x.ll  # down to LLVM IR
triton.compile(src, arch="sm_90")              # kernel source to PTX
cuobjdump -ptx app | less                      # what shipped
```

Treat the autotuner as part of the build: record the device, the batch
shape and the tuner version with the artefact, and re-run it when any of
the three changes. Keep one hand-written reference kernel for the hot
path, because a generated kernel with no reference implementation cannot
be regression-tested for correctness, only for speed.

---

## 15. Reading what the compiler did

**Why.** Every claim in this section is checkable in about a minute, and
most performance arguments in code review are not being checked at all.
The need is a repeatable loop for going from a suspicion to the
assembly, the remarks and the measurement that settle it.

**How.** Compiler Explorer (godbolt) is the front door for C, C++, Rust
and Go: paste a function, pick a version and flag set, and read the
assembly beside the source with the mapping drawn for you. It answers
"did this inline?", "is this loop vectorised?", "what does this struct
access cost?" without a local toolchain, and builds minimal repros fast.

For your own tree, go one level deeper. `clang -S -emit-llvm x.c -o -`
shows the IR, and `-Xclang -disable-llvm-passes` gives the unoptimised
control so you can attribute a change to the front end or to a pass.
Remarks are the optimiser's own report card: clang's `-Rpass=inline`,
`-Rpass-missed=vectorize` and `-Rpass-analysis=`, GCC's `-fopt-info` and
its `-fopt-info-vec-optimized` specialisation, each naming the pass, the
function and the reason:

```text
x.c:12:3: remark: loop not vectorized: cannot prove pointer
      independence [LexicalDependence]
x.c:20:1: remark: 'helper' inlined into 'run' (cost=120, threshold=0)
```

Then connect it to the machine: `objdump -d -M intel app` prints the
instructions and `perf annotate ./app` overlays hit counts, tying the hot
instruction back to a source line. The flags themselves are a menu:

| Flag | Buys | Costs |
|---|---|---|
| -O0 | fast builds, one statement per instruction | spills, no inlining |
| -O1 | cheap local rewrites | most wins left on the table |
| -O2 | the standard bargain: inline, GVN, vectorise | longer builds |
| -O3 | more inlining and unrolling | code size, occasionally slower |
| -Os / -Oz | fit in cache or a flash chip | throughput |
| -Ofast | -O3 plus non-IEEE rewrites | not for numerics you must trust |

`-march=native` produces binaries that SIGILL on older machines — fatal
for CI images and packages, fine for one benchmark box. LTO (`-flto`)
moves optimisation past the object-file boundary so cross-file inlining
happens at all. `-ffast-math` is the same class of contract as entry
12's `-use_fast_math`: speed bought by assuming IEEE edge cases do not
matter, a decision to write down rather than a flag to add.

The disciplined loop is short: state the hypothesis ("this call blocks
inlining"), look at the IR or the remarks, then measure on the real
workload with the tools in [linux](../linux/README.md) — instructions,
cache misses and wall clock as separate numbers. Read the assembly when
you have a reason; reading it for sport is an afternoon spent proving
the compiler did what the remarks already said.

**Where it breaks.** Optimising from the compiler explorer window
instead of the workload: a three-line function that looks perfect and
runs once per request. Chasing the wrong artefact — reading -O0 output,
or the IR of a build whose flags differ from production. Flags that make
one function faster and the binary slower: -O3 unrolling past the
instruction cache, `-march=native` enabling AVX-512 downclocking on a
mixed fleet. Treating remarks as guarantees: "vectorized" says the loop
was vectorised, not that it became faster. And the classic inversion —
measuring once, then hard-coding a structural change (manual unrolling,
forced inline attributes) the compiler was already making.

**In practice.** Standardise a four-command routine: `gcc -O2 -S
-fverbose-asm x.c -o x.s` for annotated assembly, `clang -O2
-Rpass=inline -Rpass-missed=loop-vectorize -c x.c` for decisions,
`go build -gcflags='-m=2' ./...` for Go's inlining and escape results,
and `perf stat -d ./app` for the effect. Keep Compiler Explorer links
in review comments — they are reproducible evidence with a compiler
version attached. When a regression lands, diff the remark logs first
and the source second; the pass decision usually moved before the code
did.

---

## 16. When the compiler surprises you: undefined behaviour and the trust boundary

**Why.** Optimisation is only possible if the compiler may assume things
about your program, and undefined behaviour is where those assumptions
get their teeth: no defined execution reaches the bad path, so no
defined behaviour constrains what the compiler does with it. The need is
to know where the language stops trusting you, because past that line
the compiler is not slow or aggressive — it is free.

**How.** The rule is simple to state and easy to forget: the compiler
assumes the program is defined, so it may delete anything that only an
undefined execution could reach. The classic consequence is the null
check. If a function dereferences its pointer unconditionally, then any
execution where the pointer is null is already undefined, and therefore
no defined execution needs the later check:

```c
int get(struct buf *b) {
    int v = b->len;      /* unconditional dereference */
    if (b == NULL)       /* dead: NULL was already UB above */
        return 0;
    return v;
}
```

Once inlined into a caller that can pass null, the check is gone and a
crash becomes a memory-safety hole — which is why this pattern shows up
in advisories rather than bug reports. Strict aliasing is the other
load-bearing assumption: an `int *` and a `float *` may not alias, so
the compiler may reorder loads, keep values in registers and prove
things that type punning makes false:

```c
float as_float(unsigned u) {
    return *(float *)&u;   /* violates strict aliasing: may read stale */
}
```

Signed overflow is a third: no wrap is assumed, so a loop whose counter
could overflow may have the "impossible" comparison folded away, and
length computations guarded by `if (a + b < a)` lose exactly the guard
that made them safe. And `volatile`, which entry 7 covered, is not the
escape hatch: it stops caching and deletion of accesses, it does not
make them atomic and it does not order them — echoing entry 7 exactly,
because the mistake appears twice in almost every low-level codebase.

```
defined: a path exists in the language → the compiler must preserve it
→ undefined: no defined execution reaches the path → it is deleted
→ result: the guard disappears at -O2 → a crash becomes an exploit
```

**Where it breaks.** Works at -O0, breaks at -O2: the single most
diagnostic symptom there is, and it means an assumption was false rather
than that the optimiser erred. Heisenbugs that change with inlining,
which moves the undefined operation next to the code that reasoned about
it. "Compiler bug" reports that are not — a minimal repro on Compiler
Explorer shows the same source compiling correctly under one flag set
and collapsing under another, which is the answer. Type punning through
the wrong pointer type after a refactor that changed a union into a
cast. Uninitialised reads that work in a debug build because the stack
was zeroed, then branch on garbage in production. Size arithmetic that
wraps past 2 GB, turning a bounds check into proof the buffer is big.

**In practice.** Make the undefined cases fail loudly: build with
`-fsanitize=address,undefined -fno-omit-frame-pointer` and run the suite
under ASan and UBSan, which convert most of these from silent wrong code
into a stack trace at the exact line. Treat `-Wall -Wextra -Wconversion`
warnings as failing tests in CI rather than as a backlog. Reproduce any
suspicious behaviour with the smallest possible snippet on Compiler
Explorer at -O0, -O2 and -O3 before blaming anyone — if the three
outputs differ, the diff is your answer. Prefer `-fno-strict-aliasing`
when a codebase genuinely relies on punning and cannot be fixed yet, and
prefer languages that shrink the surface instead: Rust and Go remove
much of the aliasing and overflow freedom that makes C/C++ optimisation
dangerous. The catalogue of these mistakes is in
[mistakes](../mistakes/README.md).

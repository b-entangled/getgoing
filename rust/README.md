# Rust: from ownership basics to systems programming

Rust is a compiled systems language designed to make memory safety and
thread-safety properties visible to the compiler without requiring a
tracing garbage collector. Its signature tools—ownership, borrowing,
lifetimes, algebraic data types, traits and `Result`—are not isolated
features. Together they make invalid states and unsynchronized mutation
harder to express.

This is a fundamentals-to-advanced field guide for stable Rust with the
2024 edition. It covers the language, Cargo, the compiler/runtime model,
ownership and lifetimes, traits/generics, error handling, async,
concurrency, `unsafe`, FFI and performance. It is a systems view rather
than a replacement for the official Rust Book. Code samples are written
for stable Rust; runtime and compiler internals are implementation
details and may change across toolchain releases.

---

## 1. Why Rust: control without giving up safety

**Why.** C and C++ offer direct control of memory and machine resources,
but use-after-free, data races and buffer errors remain common sources
of defects. Garbage-collected languages reduce manual lifetime work,
but a tracing collector and runtime pauses are not suitable for every
system, embedded target or latency budget. Rust aims to provide low-level
control with compile-time checks for a large class of memory and
concurrency errors.

**What.** Rust is ahead-of-time compiled to native code (or a selected
target such as WebAssembly). Safe Rust enforces ownership and borrowing
rules that let the compiler prove references are valid and mutable state
is not aliased unsafely. The language has no tracing GC in its standard
runtime; values are dropped deterministically when their owners leave
scope. Heap allocation still exists and is managed by allocators such as
`System`.

**How.** Rust makes ownership part of the type system. A value has one
owner; ownership can move, references temporarily borrow, and a mutable
borrow is exclusive while it is live. Types such as `Option<T>` and
`Result<T, E>` make absence and failure explicit. Traits express shared
behavior, and the compiler checks generic constraints before producing
machine code.

```text
source → rustc front end → type/borrow checks → MIR → monomorphization
       → LLVM/code generation → native executable
ownership + lifetimes → static safety checks; no GC required at runtime
```

**Advantages.** Safe Rust prevents many dangling-reference, double-free
and data-race bugs before a binary is produced. Ownership gives predictable
destruction; generics and traits support abstraction with little or no
dynamic dispatch when statically known.

**Disadvantages.** The ownership model introduces a learning curve and
may require APIs to make lifetimes and ownership explicit. Compile times,
binary size and dependency/toolchain complexity can be significant. Rust
does not eliminate logic bugs, deadlocks, denial of service, incorrect
authorization or unsound `unsafe` code.

---

## 2. Start a project: rustup, Cargo and editions

Install Rust through `rustup`, which manages toolchains and target
components. Cargo is the build tool, package manager, test runner and
workspace manager shipped with the standard toolchain.

```bash
rustup toolchain install stable
rustup default stable
rustc --version
cargo --version

cargo new hello-rust
cd hello-rust
cargo run
cargo test
```

The project has a `Cargo.toml` manifest and a `src/main.rs` executable
root. A library starts at `src/lib.rs`. Set the edition explicitly when
learning or maintaining an application:

```toml
[package]
name = "hello-rust"
version = "0.1.0"
edition = "2024"

[dependencies]
```

An edition selects a set of language and lint rules; it is not a separate
language version and does not fragment the ecosystem. Cargo resolves
dependencies, records exact selected versions in `Cargo.lock` for binary
applications, builds dependency graphs, and supports workspaces for
multiple related crates. `rustup toolchain` and `rust-toolchain.toml` can
pin a version for reproducible development and CI.

Use `cargo fmt`, `cargo clippy`, `cargo test`, `cargo build --release`,
`cargo doc --open` and `cargo tree` as the everyday tool loop. Add a
dependency only after checking its maintenance, license, feature set and
transitive dependency tree.

---

## 3. Bindings, mutability, scalar types and control flow

Variables are immutable by default. `let mut` explicitly permits
mutation; `const` is a compile-time constant; `static` names storage with
a program lifetime. Shadowing reuses a name for a new binding and can
change its type, unlike assignment.

```rust
let text = "42";
let text = text.parse::<u32>().expect("numeric input"); // shadow, new type
let mut attempts = 0;
attempts += 1;
const DEFAULT_PORT: u16 = 8080;
```

Rust has booleans, signed/unsigned fixed-width integers, pointer-sized
`isize`/`usize`, IEEE floating-point types, `char` (a Unicode scalar
value), and UTF-8 `str`/`String`. Numeric casts with `as` can truncate or
wrap; prefer checked conversions such as `TryFrom` at trust boundaries.
Arrays have fixed length (`[T; N]`); tuples group heterogeneous values.

`if` and `match` are expressions and can return values. `loop`, `while`
and `for` cover looping. `match` must be exhaustive unless a wildcard
case handles the remainder. `?` propagates a compatible error from a
function returning `Result` or `Option`.

```rust
fn classify(n: i32) -> &'static str {
    match n {
        i32::MIN..=-1 => "negative",
        0 => "zero",
        1..=i32::MAX => "positive",
    }
}

fn first_even(values: &[i32]) -> Option<i32> {
    for &value in values {
        if value % 2 == 0 {
            return Some(value);
        }
    }
    None
}
```

Patterns destructure tuples, enums, structs and references. `if let` and
`while let` are concise forms for matching one interesting case. Rust
2024 follows Rust 1.22+'s per-iteration semantics for `for` loop bindings
declared with `let`; binding modes in patterns are edition-sensitive, so
use the edition guide when migrating older code.

---

## 4. Ownership: the rule behind memory safety

**Why.** Heap objects need an owner responsible for deciding when they
can be destroyed. Reference counting pays per reference and can leak
cycles; tracing GC pays to discover reachability. Rust's ownership model
uses compile-time move/borrow rules and deterministic destruction.

**What.** Each value has one owner. Moving a non-`Copy` value transfers
ownership and makes the old binding unusable. When the owner leaves its
scope, its value is dropped. Types composed of small copyable values may
implement `Copy`; assignment then copies bits instead of moving the
original. `Clone` is an explicit potentially expensive duplication.

**How.** `String` owns a heap allocation; `&str` borrows a view of text.
Passing a `String` by value moves it, passing `&String` or `&str` borrows
it, and `.clone()` creates a separate owned value.

```rust
let owned = String::from("rust");
let borrowed: &str = &owned;
println!("{borrowed}");
let moved = owned; // ownership transfers
// println!("{owned}"); // compile error: use after move
drop(moved); // explicit early destruction; usually scope exit is clearer
```

The same rule makes resource management predictable: files, locks,
sockets and temporary directories can release resources in `Drop` when
their owner leaves scope. Rust's memory safety does not mean “nothing can
leak”: `mem::forget`, reference cycles (`Rc`/`Arc`) and intentionally
retained caches can keep resources alive.

**Advantages.** There is one clear owner and deterministic cleanup; no
GC pause is required to find ordinary unreachable objects. Ownership
errors often become compile errors close to the code that caused them.

**Disadvantages.** API design must make ownership transfer and sharing
explicit. Long-lived shared mutable graphs may require `Arc<Mutex<T>>`,
which is safe but can reintroduce contention and lock-order problems.

---

## 5. Borrowing and lifetimes: references with proven validity

**Why.** Most functions should inspect or temporarily modify data
without taking ownership. References enable that, but a reference must
not outlive its referent and mutable aliases must not race.

**What.** A borrow is `&T` (shared, read-only) or `&mut T` (exclusive,
mutable). For a particular value, safe Rust permits many live shared
borrows or one live mutable borrow, not both at the same time. Lifetimes
describe relationships between references; they are compile-time
constraints and usually introduce no runtime field or check.

**How.** The compiler infers most local lifetimes. An explicit lifetime
is needed when a function's returned reference is borrowed from an input
and inference cannot otherwise identify which input it follows:

```rust
fn longer<'a>(left: &'a str, right: &'a str) -> &'a str {
    if left.len() >= right.len() { left } else { right }
}
```

`'a` does not mean “keep this alive for a duration”; it states that the
returned reference cannot outlive the shared lifetime of the inputs.
`'static` means a reference valid for the entire program, or a type with
no non-static borrows when used as a bound. Non-lexical lifetimes let the
compiler end a borrow after its last use rather than at the end of the
whole enclosing block.

Slices such as `&[T]` and `&str` are borrowed views that carry pointer and
length, not owners. Returning a slice into a local `Vec` is rejected
because the vector would be dropped before the reference could be used.

**Advantages.** Borrow checking prevents dangling references and many
aliasing bugs without runtime reference-count overhead for ordinary
borrows.

**Disadvantages.** Lifetime relationships can make abstraction-heavy
APIs harder to read. Interior mutability and raw pointers exist for cases
the static borrow model cannot express directly, but they move checks to
runtime or to the `unsafe` author.

---

## 6. Structs, enums, `Option` and pattern matching

Structs are product types: one value contains all named fields. Enums
are sum types: one value is exactly one of several variants, each of
which may carry data. `Option<T>` represents present/absent data;
`Result<T, E>` represents success/failure. The compiler requires
exhaustive matches, so adding an enum variant can reveal every place that
needs a decision.

```rust
#[derive(Debug)]
struct User {
    id: u64,
    name: String,
}

enum Load<T> {
    Ready(T),
    Missing,
    Failed(std::io::Error),
}

fn user_name(load: Load<User>) -> Result<String, String> {
    match load {
        Load::Ready(user) => Ok(user.name),
        Load::Missing => Err("user not found".into()),
        Load::Failed(err) => Err(err.to_string()),
    }
}
```

Methods are declared in `impl` blocks. `self`, `&self` and `&mut self`
respectively consume, borrow or mutably borrow the receiver. Derive
macros can implement common traits such as `Debug`, `Clone`, `PartialEq`
and `Default`, but deriving is appropriate only when its semantics match
the type's invariants.

---

## 7. Collections, strings, iterators and closures

The standard collections have different ownership and complexity
properties:

| Type | Owns | Typical use |
|---|---|---|
| `[T; N]` | Inline fixed-size elements | Small fixed-size data |
| `Vec<T>` | Growable contiguous buffer | Default owned sequence |
| `String` | Growable UTF-8 bytes | Owned text |
| `HashMap<K, V>` | Hash table entries | Key/value lookup without order |
| `BTreeMap<K, V>` | Ordered tree entries | Sorted keys and range operations |
| `HashSet<T>` / `BTreeSet<T>` | Unique keys | Membership and set operations |

Rust strings are UTF-8 and indexing by integer is not provided because
an index could split a multibyte character. Use `chars`, `char_indices`,
`bytes`, or domain-specific parsing. Iterators are lazy adapters; a
terminal operation such as `collect`, `sum` or `for_each` drives them.
Closures capture values by shared borrow, mutable borrow or move,
depending on what the body uses and the closure's `move` keyword.

```rust
let words = vec!["safe", "systems", "rust"];
let long: Vec<String> = words.iter()
    .filter(|word| word.len() >= 5)
    .map(|word| word.to_uppercase())
    .collect();
```

The iterator pipeline is not inherently faster than a loop or inherently
slower: optimization and inlining determine the machine code. Measure
large hot loops. `HashMap` iteration order is not a stable ordering
contract; sort if deterministic output matters.

---

## 8. Traits, generics, static dispatch and trait objects

Traits define behavior. A type implements a trait with `impl Trait for
Type`; generic bounds require behavior at compile time. Rust's coherence
rules (including the orphan rule) prevent conflicting implementations
from unrelated crates.

```rust
trait Summary {
    fn summary(&self) -> String;
}

fn announce<T: Summary>(item: &T) {
    println!("{}", item.summary());
}
```

Generic calls are normally monomorphized: the compiler generates code
for concrete type substitutions, enabling static dispatch and
optimization at the cost of compile time and potentially code size. A
trait object (`dyn Trait`) erases the concrete type behind a data pointer
and vtable, enabling runtime dispatch and heterogeneous collections:

```rust
let items: Vec<Box<dyn Summary>> = vec![Box::new(article), Box::new(video)];
```

Associated types, default methods, supertraits, blanket implementations,
`where` clauses, const generics, higher-ranked trait bounds and generic
associated types support advanced abstraction. Use each when it makes
an API clearer, not just to demonstrate type-system sophistication.

Traits such as `Send`, `Sync`, `Copy`, `Clone`, `Unpin` and `Sized` have
special meaning to the compiler and standard library. `Send` means a
value may be transferred between threads; `Sync` means shared references
may be sent between threads. They are automatically implemented when
the contained types make the property sound, except where explicitly
withheld.

---

## 9. Error handling: `Result`, `Option` and panic boundaries

Use `Option<T>` when a value may be absent and `Result<T, E>` when an
operation may fail with a reason. The `?` operator returns early on an
error and converts it when the function's error type permits. Errors
should retain useful context; libraries often expose typed errors while
applications decide how to report or retry them.

```rust
use std::{fs, io};

fn read_config(path: &str) -> Result<String, io::Error> {
    let text = fs::read_to_string(path)?;
    Ok(text)
}
```

`panic!` indicates a violated invariant or unrecoverable bug. It unwinds
by default on many targets (running destructors) but can be configured
to abort. `catch_unwind` is not general exception handling; it catches
only unwinding panics and does not make an inconsistent program state
safe. Avoid panicking for expected invalid user input.

---

## 10. Modules, crates, Cargo and build profiles

A package is a compilation unit; a crate is a compiler input/output
root. A binary crate has `main`; a library crate exposes a public API.
Modules (`mod`, `pub mod`, `use`) organize names and privacy inside a
crate. Cargo packages group one or more crates and metadata in
`Cargo.toml`; workspaces share dependency resolution and build artifacts
across packages.

```text
my-app/
├── Cargo.toml
├── Cargo.lock
├── src/main.rs
├── src/lib.rs
└── crates/protocol/src/lib.rs
```

Cargo features are compile-time configuration flags, not runtime
booleans; feature combinations can affect API and dependency selection.
Use `default-features = false` deliberately and test supported feature
combinations. Release profiles control optimization, debug info and
panic strategy; use reproducible lockfiles for applications and review
dependency updates for libraries.

---

## 11. Smart pointers, ownership sharing and interior mutability

Smart pointers add a specific ownership policy:

| Type | Ownership policy | Use when |
|---|---|---|
| `Box<T>` | One owner, heap allocation | Recursive types, large owned values, trait objects |
| `Rc<T>` | Shared single-thread reference count | Shared immutable graph within one thread |
| `Arc<T>` | Atomic shared reference count | Shared ownership across threads |
| `Cell<T>` | Copy in/out mutation through `&Cell<T>` | Small `Copy` data, single-thread interior mutation |
| `RefCell<T>` | Runtime-checked borrowing | Single-thread shared ownership with dynamic borrow rules |
| `Mutex<T>` / `RwLock<T>` | Synchronized shared access | Mutable data shared between threads |
| `Cow<'a, T>` | Borrow until mutation requires ownership | Avoid copies on read-mostly paths |
| `Pin<P>` | Prevent movement of a pinned pointee | Self-referential futures and select low-level APIs |

`Rc<RefCell<T>>` is a common single-thread shared-mutable pattern;
`Arc<Mutex<T>>` is its thread-safe counterpart. `RefCell` checks at
runtime and panics on conflicting borrows; a mutex blocks or reports
poisoning depending on API. Reference-counted cycles are not reclaimed
automatically; use `Weak` for back edges. `Pin` is a guarantee that a
value will not move through that pinned pointer, not a general-purpose
“make this safe” wrapper.

---

## 12. Concurrency: threads, channels, `Send` and `Sync`

Rust makes thread ownership explicit. A `thread::spawn` closure must be
`'static + Send` because it may outlive the current stack frame and run
on another thread. `Arc<T>` shares ownership; `Mutex<T>` or atomics
coordinate mutation. Channels transfer ownership of messages between
threads.

```rust
use std::thread;

let worker = thread::spawn(|| 2 + 2);
let answer = worker.join().expect("worker panicked");
assert_eq!(answer, 4);
```

The compiler rejects many data races in safe code because `Send` and
`Sync` constrain what crosses or is shared across threads. Deadlocks,
starvation, lock-order bugs and logical races are still possible.
`unsafe impl Send` or `unsafe impl Sync` is a promise to every caller and
must be justified by a sound synchronization/ownership invariant.

---

## 13. Async Rust: futures, executors, `Pin` and cancellation

`async fn` returns a future; calling it constructs a value and does not
run its body to completion. An executor polls futures, and a future
returns `Pending` with a wake mechanism when it cannot progress or
`Ready(value)` when finished. The compiler lowers each async function
into a state machine that stores data live across `.await` points.

The standard library defines `Future` but does not provide a general
async I/O runtime. Applications commonly select an ecosystem runtime
(such as Tokio or async-std) for task scheduling, timers, networking and
drivers. Async is concurrency, not automatic parallelism: CPU-heavy work
still occupies the thread polling it unless explicitly moved to a
blocking pool or worker thread.

```rust
async fn fetch_two(client: &Client) -> Result<(Page, Page), Error> {
    let (left, right) = tokio::try_join!(
        client.get("https://a.example"),
        client.get("https://b.example"),
    )?;
    Ok((left, right))
}
```

Dropping a future cancels its remaining work unless the runtime or a
separate task owns it. Cancellation may occur at an `.await`; release
locks/resources before yielding where possible. `Pin` is used because a
future state machine can contain references to its own stored fields
after polling; most application code uses runtime/library abstractions
and does not manually implement `Future` or `Pin`.

---

## 14. Macros and compile-time code generation

`macro_rules!` declarative macros match token-tree patterns and expand
before type checking. Procedural macros are Rust functions compiled as
host-side plugins that transform token streams; derive macros generate
trait implementations and attribute macros transform annotated items.
Macros are useful when functions/traits cannot express syntax
transformation, but they can make generated code hard to inspect.

```rust
macro_rules! make_pair {
    ($left:expr, $right:expr) => { ($left, $right) };
}

let pair = make_pair!("host", 8080);
```

Prefer ordinary functions, generics and traits when they fit. Inspect
macro expansion with `cargo expand` (an external tool) and keep proc-macro
dependencies small because they execute during compilation on the build
host.

---

## 15. Unsafe Rust, FFI and sound abstractions

`unsafe` does not turn off the borrow checker or make code faster by
itself. It permits a small set of operations whose contracts the
compiler cannot verify: dereferencing raw pointers, calling unsafe
functions, accessing mutable statics, implementing unsafe traits and
some union operations. The programmer must uphold each contract and
prevent undefined behavior for all safe callers.

Keep unsafe blocks small and document the invariant immediately next to
them. Encapsulate raw pointers behind a safe API whose preconditions are
enforced. Miri can detect many classes of undefined behavior in executed
tests, but it is not a proof of soundness.

For C FFI, use `extern "C"` and `#[repr(C)]` for ABI-visible structures,
make ownership/freeing rules explicit, and treat pointers from the other
language as untrusted. Rust's native ABI and default struct layout are
not stable interfaces. A Rust `String` is not a C string; use explicit
conversion and lifetime ownership at the boundary.

---

## 16. Compiler internals and performance model

The stable user contract is the language reference, not the compiler's
current internal representation. Conceptually, `rustc` parses source,
expands macros, resolves names, type-checks, lowers to MIR, checks
borrows and moves, monomorphizes generic code, optimizes and emits
machine code through a backend (commonly LLVM). Internal passes and
exact layout are version-dependent.

This pipeline explains common tradeoffs:

- Generic code usually gets static dispatch and specialization through
  monomorphization, at possible compile-time/binary-size cost.
- `dyn Trait` uses runtime dispatch and a vtable; it can reduce code
  duplication but may inhibit inlining.
- `#[repr(C)]` is for a specified C-compatible layout; default Rust
  layout is not a stable ABI or serialization format.
- Bounds checks are normally retained unless the compiler proves them
  redundant; safe abstractions are not automatically slower.
- Ownership itself is mostly a compile-time property. `Arc`, `Box`,
  allocations, atomics and locks have runtime costs because their
  semantics require them.

Measure release builds, not intuition:

```bash
cargo test
cargo test --release
cargo bench
cargo build --release
```

Use a profiler and allocation-aware benchmarks. Avoid changing
optimization flags, unsafe code or data representation before identifying
the measured hot path. Rust avoids a tracing GC, but it still has
allocator costs, cache misses, lock contention, syscalls and scheduling
overhead.

---

## 17. Tests, docs, linting and supply-chain checks

Rust supports unit tests in source modules, integration tests under
`tests/`, documentation tests in Markdown comments, and compile-fail
tests through external tools. `cargo test` runs them; `cargo fmt --check`
checks formatting; `cargo clippy --all-targets --all-features` catches
common mistakes. `cargo audit` or equivalent tooling can check known
dependency advisories, but the policy and database are external to Cargo.

```rust
/// Adds two values.
///
/// ```
/// assert_eq!(my_crate::add(2, 3), 5);
/// ```
pub fn add(left: i32, right: i32) -> i32 {
    left + right
}
```

Use `cargo tree -e features` to review feature activation and
`cargo metadata` in build automation. Lock dependencies intentionally,
review build scripts and proc macros (they execute during compilation),
and minimize the code compiled with `unsafe`.

---

## 18. `no_std`, embedded, WebAssembly and platform boundaries

`#![no_std]` removes the standard library and leaves `core`; a program
that allocates can add `alloc` with a platform-provided global allocator.
Embedded programs may provide their own entry point, panic handler and
target support. `no_std` is not “no runtime code” in every sense; compiler
builtins, startup code and target-specific support may still be needed.

Cross-compilation uses an explicit target triple and installed target
component:

```bash
rustup target add wasm32-unknown-unknown
cargo build --target wasm32-unknown-unknown --release
```

The target determines ABI, pointer width, atomic support, linker and
available libraries. `cfg(target_os = ...)` and feature flags are
compile-time selection; test each supported target in CI. For C or
operating-system integration, keep platform-specific unsafe code behind
a small module boundary.

---

## 19. When Rust is a good fit—and its limits

Rust is a strong fit for parsers, compilers, storage engines, network
services, command-line tools, embedded software and performance-sensitive
libraries where memory safety and predictable control are valuable. It
is also useful in a polyglot architecture for a narrow, measured hot path
or a security-sensitive component.

The language does not remove the need to understand algorithms, data
structures, cache behavior, concurrency, protocol security or
operational failure. A correct Rust program can still have a deadlock,
bad retry policy, unbounded queue, SQL injection or flawed authorization.
Use the compiler to prevent classes of bugs, then test and observe the
system it cannot prove correct.

## Further reading

- [The Rust Programming Language](https://doc.rust-lang.org/stable/book/)
- [The Rust Reference](https://doc.rust-lang.org/stable/reference/)
- [The Rustonomicon](https://doc.rust-lang.org/stable/nomicon/)
- [Rust 2024 Edition Guide](https://doc.rust-lang.org/stable/edition-guide/rust-2024/)
- [Cargo Book](https://doc.rust-lang.org/stable/cargo/)

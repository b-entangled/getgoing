# Go basics: a practical first pass

This guide is the foundation for the more focused examples in
[`concepts/README.md`](README.md), the concurrency patterns, and the
measured runtime internals. It covers the everyday language model: source
files and packages, values and types, control flow, functions, data
structures, methods, interfaces, errors, generics, concurrency and tests.

The examples use Go 1.26 syntax. The language evolves under the Go 1
compatibility promise, but toolchain/runtime implementation details are
version-specific; see the linked internals guide when performance depends
on them. Snippets omit repeated package and import boilerplate unless a
whole source file is shown.

---

## 1. A Go program is packages built by the toolchain

A `.go` file declares a package. Files in one directory normally form one
package and share declarations. `package main` is executable; a
`func main()` in that package is the program entry point. Imports name
packages, and imported names are qualified by the package name:

```go
package main

import "fmt"

func main() {
	fmt.Println("hello, Go")
}
```

A module is a versioned collection of packages rooted at a `go.mod` file.
Start one, build it and test it with the standard toolchain:

```bash
mkdir hello && cd hello
go mod init example.com/hello
go run .
go build ./...
go test ./...
```

Package names are usually short, lowercase and singular (`net`, `json`,
`widget`). An identifier beginning with an uppercase Unicode letter is
exported from its package; lowercase names are package-private. This is
Go's visibility rule—there are no `public` or `private` keywords.

---

## 2. Values, declarations and zero values

Every variable has a type and a value. `var` declares explicitly;
`:=` infers a type for a new local variable inside a function. The zero
value is useful and intentional: numbers are zero, booleans false,
strings empty, pointers/maps/slices/functions/channels/interfaces nil,
and structs/arrays contain their fields' zero values.

```go
var attempts int          // 0
var name string           // ""
var labels map[string]int // nil, safe to read, not safe to write

limit := 10                // int, local declaration
const defaultPort = 8080   // compile-time constant
```

| Type family | Examples | Basic property |
|---|---|---|
| Boolean | `bool` | `true` or `false` |
| Integers | `int`, `int64`, `uint32` | `int` width is architecture-dependent; fixed-width types are explicit |
| Floating point / complex | `float64`, `complex128` | IEEE floating point; compare approximate results with tolerances |
| Text units | `byte` (`uint8`), `rune` (`int32`), `string` | Strings are immutable UTF-8 bytes; `range` decodes runes |
| Arrays / slices | `[4]byte`, `[]byte` | Array length is in the type; a slice views a backing array |
| Maps / structs | `map[string]int`, `struct{ ID int }` | Hash lookup; named fields grouped into a value |
| References / behavior | `*T`, `func`, `interface` | Pointers, callable values, and method contracts |
| Concurrency | `chan T` | Typed communication and synchronization |

Go does not silently convert between distinct numeric types. Use an
explicit conversion and consider overflow/precision when narrowing:

```go
var count int64 = 42
small := int(count) // conversion, not a type assertion
```

A defined type creates a distinct type with its own method set; a type
alias (`type UserID = int64`) is another name for the identical type.
Constants can be untyped until a context gives them a type; `iota` is a
convenience for enumerated constant sequences.

```go
type UserID int64
type DatabaseID = int64

const (
	StateNew = iota
	StateReady
	StateClosed
)
```

`make` initializes slices, maps and channels and returns the value itself;
`new(T)` allocates a zero `T` and returns `*T`. Composite literals are
usually clearer when a value needs specific fields or elements.

---

## 3. Control flow and iteration

Go has `if`, `switch`, and `for`; there is no `while` keyword. Conditions
do not require parentheses, but braces are mandatory. `switch` cases do
not fall through unless `fallthrough` is written. `for range` iterates
over arrays, slices, strings, maps, channels, integer ranges and iterator
functions supported by the selected language version.

```go
for i := 0; i < 3; i++ { /* ... */ }
for condition { /* while-shaped loop */ }
for { break } // deliberate infinite loop

for index, value := range values {
	_ = index
	_ = value
}
```

Important iteration rules: map order is unspecified; ranging over a string
decodes UTF-8 and yields byte offsets plus runes; ranging over a slice
copies each element into the iteration value; and ranging over a channel
ends when the channel is closed and drained. Since Go 1.22, loop variables
declared by `:=` in the loop are per-iteration for modules using that
language version. Assignment to a variable declared outside the loop still
reuses that variable.

`break` exits the innermost loop/switch/select unless a label names an
outer construct. `continue` advances a loop. Labels are legal but best
kept for cases where nested control flow is genuinely clearer.

---

## 4. Functions, multiple results and methods

Functions are first-class values. They can take functions, return
functions, close over variables, and return more than one result. The
common two-result shape is `(value, ok)` for optional lookup or
`(value, error)` for operations that can fail.

```go
func lookup(table map[string]int, key string) (int, bool) {
	value, ok := table[key]
	return value, ok
}

func parsePort(text string) (int, error) {
	port, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse port %q: %w", text, err)
	}
	return port, nil
}
```

Variadic parameters (`...T`) arrive as a slice. A method is a function
with a receiver. A value receiver sees a copy; a pointer receiver can
modify the original and avoids copying large values. The method set of
`T` and `*T` differs, which matters when satisfying interfaces.

```go
type Counter struct{ n int }

func (c *Counter) Add(delta int) { c.n += delta }
func (c Counter) Value() int     { return c.n }
```

For an addressable value `c`, `c.Add(1)` can be shorthand for
`(&c).Add(1)`. That convenience does not change method sets: an interface
requiring `Add` is satisfied by `*Counter`, not by `Counter`.

Closures capture variables, not snapshots; deferred-call arguments are
evaluated when `defer` runs and deferred calls execute in LIFO order. The
dedicated [`closures`](closures/README.md) and [`deferred`](deferred/README.md)
examples demonstrate the less obvious cases.

---

## 5. Pointers, arrays, slices, maps and structs

Pointers hold addresses; `&x` takes an address and `*p` dereferences one.
Go has no pointer arithmetic in ordinary safe code. `new(T)` returns a
pointer to a zero-valued `T`; composite literals are usually more
expressive (`&T{...}`). A nil pointer cannot be dereferenced.

An array has a fixed length that is part of its type and assigning it
copies all elements. A slice is a descriptor over a backing array:
pointer, length and capacity. Slicing usually shares the backing array;
`append` may reuse that array or allocate a larger one. A map is a
reference-like built-in hash map; its zero value is readable but must be
initialized before insertion.

```go
var fixed [3]int
items := []int{2, 4, 6}
view := items[:2] // shares items' backing array
view[0] = 9       // items[0] is now 9

counts := make(map[string]int)
counts["gopher"]++
```

Maps are not safe for concurrent mutation. Protect shared maps with a
mutex or use a concurrency-safe abstraction; a race-free read-only map
may be shared after construction and publication.

Structs group named fields. Composite literals make initialization
visible; keyed fields avoid dependence on field order. Struct assignment
copies fields, but pointers, slices, maps, channels, functions and
interfaces inside those fields still refer to shared state.

```go
type Server struct {
	Address string
	Port    int
}

s := Server{Address: "127.0.0.1", Port: 8080}
```

The details of slice aliasing, map iteration and memory layout are
measured in [`internals`](../internals/README.md).

---

## 6. Interfaces: behavior without inheritance

An interface specifies methods. Any type whose method set satisfies those
methods implements the interface automatically; it does not declare
`implements`. This keeps interfaces small and lets the consumer define
the behavior it needs.

```go
type Writer interface {
	Write([]byte) (int, error)
}

func save(w Writer, data []byte) error {
	_, err := w.Write(data)
	return err
}
```

An interface value carries a dynamic type and a dynamic value. The
interface itself may be non-nil while holding a typed nil pointer:

```go
var p *bytes.Buffer
var w io.Writer = p
fmt.Println(w == nil) // false: it has dynamic type *bytes.Buffer
```

Use a type assertion when one concrete behavior is required and a
type-switch when several types are intentionally handled. Prefer
interfaces with the fewest methods needed at the call site. The
[`iface`](iface/README.md) notes cover method sets, typed nil and the
runtime representation.

---

## 7. Errors, `defer`, panic and recovery

Errors are ordinary values returned by functions. Check them at the point
where the program can add useful context or decide what to do. Wrap with
`%w` when callers should still match the cause via `errors.Is` or extract
it via `errors.As`.

```go
data, err := os.ReadFile(path)
if err != nil {
	return fmt.Errorf("read config %q: %w", path, err)
}
```

`defer` is suited to resource cleanup because it runs when the function
returns, including while a panic unwinds. `panic` is for unrecoverable
programmer/runtime conditions, not ordinary validation or I/O errors.
`recover` only catches a panic when it is called directly by a deferred
function during unwinding in the same goroutine; it is commonly used at a
deliberate boundary such as an HTTP handler or worker boundary, with a
stack trace and an error response.

---

## 8. Generics: reuse with compile-time types

Type parameters let functions and types operate over a family of types
while preserving static checking. Constraints describe required
operations. Inference often means callers do not need to spell out type
arguments.

```go
func Map[S ~[]E, E, R any](input S, convert func(E) R) []R {
	result := make([]R, len(input))
	for i, value := range input {
		result[i] = convert(value)
	}
	return result
}
```

Use generics when the same algorithm applies to multiple concrete types;
use an interface when the abstraction is about runtime behavior or when
different implementations must be selected dynamically. Generics do not
automatically make an abstraction faster: measure the compiled workload.
See [`generics`](generics/README.md) for constraints, inference and
compile-time checks.

---

## 9. Packages, modules and dependency boundaries

Packages are the unit of names, compilation and access control. Modules
are the versioned unit used by the `go` command. Keep packages focused,
avoid import cycles, and put an interface near the code that consumes it
when it clarifies the dependency direction.

```text
module example.com/shop

cmd/server/       package main: process entry point
internal/orders/  package orders: private to this module
api/              package api: stable contracts
```

`internal/` is enforced by the compiler's import rules. `go.mod` records
the module path and dependency requirements; `go.sum` records checksums
for downloaded modules. Useful commands include `go list -m all`,
`go mod graph`, `go mod tidy`, `go test ./...` and `go vet ./...`.

---

## 10. Concurrency: goroutines, channels and synchronization

A goroutine is a function executing concurrently, scheduled by the Go
runtime onto operating-system threads. A channel transfers values and
synchronizes sender/receiver; a mutex protects shared mutable state; an
atomic is for narrow lock-free state transitions. Choose one ownership
model and make cancellation explicit with `context.Context`.

```go
func produce(ctx context.Context, out chan<- int) {
	defer close(out) // the sender owns closing this channel
	for n := 0; ; n++ {
		select {
		case out <- n:
		case <-ctx.Done():
			return
		}
	}
}
```

Closing a channel broadcasts “no more values”; it does not cancel a
receiver's work by itself. Never close a channel from a receiver or send
on a closed channel. The [concurrency guide](../concurrency/README.md)
covers worker pools, pipelines, task groups and cancellation in depth.

---

## 11. I/O, JSON and tests

The standard library favors small interfaces: `io.Reader` reads bytes,
`io.Writer` writes bytes, and `encoding/json` maps exported struct fields
to JSON by default. Use `json` tags to define a public shape and validate
untrusted input after parsing; successful decoding does not establish
business validity.

Tests live beside implementation in `_test.go` files. `go test` compiles
the package, runs `TestXxx`, benchmarks `BenchmarkXxx`, examples and fuzz
targets. Table-driven tests make edge cases visible; `t.TempDir`,
`t.Setenv`, `t.Cleanup` and `t.Parallel` manage test-owned state safely.

```go
func TestPortRange(t *testing.T) {
	for _, tc := range []struct {
		port int
		ok   bool
	}{{80, true}, {0, false}, {70000, false}} {
		if got := validPort(tc.port); got != tc.ok {
			t.Errorf("validPort(%d) = %v, want %v", tc.port, got, tc.ok)
		}
	}
}
```

Run `gofmt` on source, `go vet` for suspicious constructs, and
`go test -race ./...` for data races in exercised paths. The race detector
does not prove that every possible schedule is race-free; tests must drive
the relevant concurrency.

---

## Continue deeper

- [Focused language/library concepts](README.md)
- [Runtime internals, reflection, GC and arenas](../internals/RUNTIME.md)
- [Go construction idioms](../idioms/README.md)
- [Common Go pitfalls](../mistakes/README.md)

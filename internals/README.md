# Internals (how the runtime behaves)

What the Go toolchain actually does underneath the language: where values
live, what an interface is, what `defer` costs, how the collector behaves.
The packages below pin observable properties in tests — allocation counts via
`testing.AllocsPerRun` with exact totals, layout via `unsafe`, collection
via `weak.Pointer` and `runtime.SetFinalizer`.
For the broader runtime architecture, reflection, GC evolution and arena
status, see the [runtime deep dive](RUNTIME.md).

```bash
go test -race ./internals/...
```

---

## Escape analysis — [`escape/`](escape)

**Intent.** The compiler's split decision per local: stays on the stack
(zero cost) or escapes to the heap (one allocation). Passing a pointer to
a callee that does not keep it is *not* escape; storing a local in a
package variable is.

**Proving tests.**

| Test | Property |
|---|---|
| `TestNonEscapingWorkAllocatesNothing` | 100 stack-only calls total 0 allocs |
| `TestPointerArgumentsThatDoNotEscapeStayOnTheStack` | non-leaking callees keep locals on the stack |
| `TestEscapingWorkAllocatesPerCall` | package-variable stores allocate every call |
| `TestReturningByPointerAllocates` | returning an address into a package sink allocates |

## Memory layout — [`memlayout/`](memlayout)

**Intent.** Structs are laid out by field size and alignment with padding
between fields — reorder fields and the size changes; every word-sized
field is word-aligned; a `string` header is 2 words, a slice header is 3,
`struct{}` is 0 bytes.

**Proving tests.**

| Test | Property |
|---|---|
| `TestFieldOrderChangesStructSize` | `{byte,int32,byte}` = 12, `{int32,byte,byte}` = 8 |
| `TestFieldOffsetsAreAligned` | each field starts at a multiple of its alignment |
| `TestPointerAlignmentDominates` | a pointer field forces word alignment |
| `TestSizeIsAMultipleOfAlignment` | struct size rounds up to its alignment |
| `TestHeadersAreFixedNumberOfWords` | string = 2 words, slice = 3 words |
| `TestZeroSizeStructCostsNothing` | `struct{}` occupies 0 bytes |

## Interface representation — [`iface/`](iface)

**Intent.** An interface value is `{type, data}`: the type word is a
pointer to the dynamic type's descriptor (same type ⇒ same word), pointer
values are stored *directly* in the data word, other values indirectly —
and boxing only allocates when the box escapes.

**Proving tests.**

| Test | Property |
|---|---|
| `TestTypeWordIdentifiesTheDynamicType` | int and string descriptors differ |
| `TestPointerIsStoredDirectlyInTheDataWord` | data word = the pointer itself |
| `TestValueBoxesAreDistinctStorage` | two live int boxes are two addresses |
| `TestNonEscapingBoxStaysOnTheStack` | 100 non-escaping boxes total 0 allocs |
| `TestEscapingBoxGoesToTheHeap` | escaping boxes allocate per box |
| `TestAssertionRecoversTheValue` | round-trip through the type word works |

## Reflection — [`reflectx/`](reflectx)

**Intent.** Reflection reads runtime type metadata and operates on values
whose concrete types are known only at runtime. `TypeFor`, Go 1.26 field
iterators, `TypeAssert`, addressability and `CanSet` are demonstrated with
checked operations that return errors instead of panicking.

**Proving tests.**

| Test | Property |
|---|---|
| `TestFieldNamesUsesGenericTypeMetadata` | `TypeFor` and field iteration preserve type structure |
| `TestReflectTypeAssertMatchesDynamicType` | dynamic assertion succeeds only for compatible types |
| `TestSetFieldRequiresAssignableExportedField` | reflection mutation checks settable and exact assignability |
| `TestSetFieldCanZeroNilableField` | nil can clear only a nilable field |

## Defer cost — [`defer/`](defer)

**Intent.** Two implementations of `defer`: open-coded (top-level defers,
0 allocs, compiled into the function's exit path) and the heap path for
defers *inside loops* (1 allocation per deferred call). LIFO order is
identical either way.

**Proving tests.**

| Test | Property |
|---|---|
| `TestTopLevelDefersDoNotAllocate` | 100 open-coded calls total 0 allocs |
| `TestDefersInLoopAllocatePerIteration` | 100 × 10 loop defers ≥ 1000 allocs |
| `TestOnlyLoopDefersAllocateInAMixedFunction` | the top-level defer stays free |
| `TestBothShapesRunInLIFOOrder` | mechanism differs, order does not |

## String memory — [`stringmem/`](stringmem)

**Intent.** A string is a two-word `{ptr,len}` header over immutable
bytes: `unsafe.String`/`unsafe.Slice` *share* those bytes (zero copy,
read-only contract), the safe conversions copy — and repeated `+=` in a
loop allocates once per append while a pre-grown `strings.Builder` does
the line in one.

**Proving tests.**

| Test | Property |
|---|---|
| `TestUnsafeStringSharesTheUnderlyingBytes` | same address for `[]byte` and the string |
| `TestUnsafeBytesShareTheStringsBytes` | same address the other direction |
| `TestSafeConversionCopies` | mutations of the source cannot corrupt the string |
| `TestNaiveJoinAllocatesMoreThanBuilder` | `+=` loop ≫ builder over 100 lines |
| `TestBothJoinsProduceIdenticalOutput` | the choice is memory, not bytes |

## Slices — [`slice/`](slice)

**Intent.** A slice is `{ptr,len,cap}` over a backing array: slicing
shares it until an append hits capacity and allocates; the three-index
form pins growth; growth is geometric (amortised O(1) append), and nil
slices behave like empty ones.

**Proving tests.**

| Test | Property |
|---|---|
| `TestSlicingSharesTheBackingArray` | writes through one view appear in the other |
| `TestPinnedViewIsolatesAppends` | `s[:n:n]` forces the next append to copy |
| `TestGrowthIsGeometricNotLinear` | capacity doubles, not increments |
| `TestNilAndEmptySlicesAreBothUsable` | `nil` slice: len 0, appendable, range-able |
| `TestCopyProducesAnIndependentArray` | `copy` does not share storage |

## Maps — [`map/`](map)

**Intent.** A nil map reads (zero values) but panics on write; range
order is randomised on purpose — sort the keys if you need an order —
range visits every present key exactly once across growth, and entries
deleted before they are reached are never produced (spec guarantee).

**Proving tests.**

| Test | Property |
|---|---|
| `TestNilMapIsReadable` | read/len/range on nil are all safe |
| `TestNilMapWritePanics` | writes panic with "nil map" |
| `TestRangeVisitsEveryKeyExactlyOnce` | 1000 keys after growth: no loss, no dupes |
| `TestRangeOrderIsRandomised` | 50 runs produce ≥ 2 distinct orders |
| `TestEntriesDeletedDuringIterationAreNotProduced` | delete-all-on-first-visit ⇒ exactly 1 visit |
| `TestKeysSortedIsDeterministic` | sorting is the sanctioned fix for order |

## Garbage collection — [`gc/`](gc)

**Intent.** The collector traces reachability from roots: cycles without
external references *are* collected (prove it with `weak.Pointer`, not
finalizers — the runtime documents that a finalizer inside a cycle may
never run). Finalizers run after unreachability, in dependency order for
chains; dropped garbage returns to the heap.

**Proving tests.**

| Test | Property |
|---|---|
| `TestFinalizerRunsAfterTheObjectBecomesUnreachable` | finalizer fires once the object dies |
| `TestFinalizersRunInDependencyOrder` | chain `a → b`: a first, b after a is freed |
| `TestUnreachableCyclesAreCollected` | weak pointer to a dropped cycle goes nil |
| `TestHeapFallsAfterDroppingGarbage` | ~16 MiB dropped ⇒ heap falls by at least half |

## Goroutines — [`goroutine/`](goroutine)

**Intent.** Goroutines are cheap but not free: a spawn costs a few
hundred bytes of heap, stacks start small and grow on demand (deep
recursion just works), and `GOMAXPROCS ≥ 1` bounds OS-level parallelism.

**Proving tests.**

| Test | Property |
|---|---|
| `TestEverySpawnedGoroutineRuns` | 100 spawns ⇒ 100 executions |
| `TestSpawnCostIsHundredsOfBytesPerGoroutine` | ~170–270 B each (generous under `-race`) |
| `TestGOMAXPROCSIsAtLeastOne` | the scheduler limit is sane |
| `TestDeepRecursionGrowsTheStack` | 100 000 frames ≈ 8 MB completes |

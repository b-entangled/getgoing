# Concepts (Go language)

Working examples of the language and library features every Go program is
built from — closures, `defer`, interfaces, generics, errors, channels,
sync, JSON, text, and the testing package itself. Where the pattern groups
answer *how do I structure this*, these packages answer *what does the
language actually do*.

New to Go? Start with the [basics guide](BASICS.md), then use the focused
packages below as a behavior reference. Runtime representation and
implementation changes such as reflection, the GC and allocation live in
[`../internals/RUNTIME.md`](../internals/RUNTIME.md).

Every package passes `go test -race ./concepts/...`; each entry below links
the tests that pin down the behaviour that surprises people.

---

## Closures — [`closures/`](closures)

**Intent.** Captured variables are shared references, not copies — and
loop variables are per-iteration since Go 1.22. Two closures over the same
variable see each other's writes; a closure made in a loop captures the
value *of that iteration*.

**Proving tests.**

| Test | Property |
|---|---|
| `TestAdderCapturesTheValuePassedIn` | each call closes over its own argument |
| `TestCounterClosesOverItsOwnState` | state persists across calls |
| `TestTwoClosuresCanShareOneVariable` | writes through one are visible in the other |
| `TestClosureSeesLaterAssignment` | capture is by reference to the variable |
| `TestLoopVariableIsPerIteration` | Go 1.22+ loop variables are not shared |
| `TestSharedVariableCaptureSeesFinalValue` | the outer variable, not a copy, is read |

## Deferred calls — [`deferred/`](deferred)

**Intent.** `defer` runs LIFO at function exit (or on panic), arguments
are evaluated *when `defer` is executed*, and a `defer` closure can repair
the named return value — the basis of the "naked return" and trace idioms.

**Proving tests.**

| Test | Property |
|---|---|
| `TestDefersRunLIFO` | last deferred, first run |
| `TestArgumentsAreEvaluatedWhenDeferRuns` | arguments are fixed at the `defer` line |
| `TestClosureDefersEvaluationUntilRun` | a closure body runs later, reads current values |
| `TestDefersRunOnPanicAndRecoverConvertsIt` | defers fire during panic unwinding |
| `TestDeferredCallCanReplaceNamedReturn` | a deferred closure can change the result |

## Interfaces — [`iface/`](iface)

**Intent.** Satisfaction is structural, not declared; the method *set*
(value vs pointer receiver) decides what an interface can hold; a type
switch dispatches on the dynamic type; a failed assertion is a recoverable
two-value result, never a panic.

**Proving tests.**

| Test | Property |
|---|---|
| `TestTotalAreaAcceptsAnyImplementer` | any type with the methods fits |
| `TestMethodSetRules` | pointer-receiver methods only via `*T` |
| `TestEmbeddedInterfacePromotesMethods` | embedding composes interfaces |
| `TestTypeSwitchDispatchesOnDynamicType` | `switch x := v.(type)` selects the case |
| `TestAssertionFailureIsRecoverableNotFatal` | `v.(T)` in comma-ok form never panics |

## Generics — [`generics/`](generics)

**Intent.** Type parameters with inference, `constraints` (comparable,
ordered, custom), and the compile-time enforcement that keeps generic code
honest — no runtime type assertions needed.

**Proving tests.**

| Test | Property |
|---|---|
| `TestMapInfersBothTypeParameters` | results infer `T`→`U` without annotation |
| `TestFilterKeepsMatchingElements` | predicate filters without mutating input |
| `TestSumAcceptsNamedNumericTypes` | defined types over `int` satisfy the constraint |
| `TestIndexOfUsesComparability` | `comparable` bounds `==` |
| `TestGenericStackHasNoTypeAssertions` | no `interface{}` casts in the implementation |
| `TestCountByGroupsWithAKeyFunction` | constraint-based grouping is type-safe |
| `TestConstraintsAreEnforcedAtCompileTime` | wrong types fail the build, not the test |

## Errors — [`errx/`](errx)

**Intent.** Errors are values: sentinel errors matched with `errors.Is`,
structured errors extracted with `errors.As`, wrapping with `%w` that
keeps the chain reachable, and `errors.Join` for multi-error results.

**Proving tests.**

| Test | Property |
|---|---|
| `TestSentinelIsReachableThroughWrapping` | `%w` chains survive wrapping |
| `TestTypedErrorIsExtractableWithFields` | `errors.As` pulls the concrete type |
| `TestInnerSentinelSurvivesOuterContext` | context wrapping does not hide the cause |
| `TestWrappedStdlibErrorMatches` | stdlib sentinels match through wrap layers |
| `TestJoinSurfacesEveryError` | `errors.Join` reports all causes |
| `TestValidInputProducesNoError` | the happy path returns exactly `nil` |

## Channels — [`channels/`](channels)

**Intent.** The full state machine: unbuffered rendezvous, buffered
capacity, close semantics (zero value, then `false`), `range` terminating
on close, `select` on ready cases and timeouts, and nil channels blocking
forever.

**Proving tests.**

| Test | Property |
|---|---|
| `TestUnbufferedSendBlocksUntilReceive` | rendezvous needs both sides |
| `TestBufferedChannelAcceptsUpToCapacity` | capacity `n` buffers `n` sends |
| `TestClosedChannelYieldsZeroValueThenFalse` | receive after close never blocks |
| `TestRangeTerminatesWhenSenderCloses` | `range` exits on close |
| `TestNilChannelIsNeverReady` | nil channel is permanently not-ready |
| `TestSelectTakesTheReadyCase` | `select` picks a runnable case |
| `TestTimeoutCaseFiresWhenNoValueArrives` | `time.After` in `select` unblocks |

## Sync primitives — [`syncx/`](syncx)

**Intent.** `sync.Mutex` for exact mutual exclusion, `sync.WaitGroup` for
join points, `sync.Once` for one-time initialization, and `sync.RWMutex`
where readers block writers only against each other.

**Proving tests.**

| Test | Property |
|---|---|
| `TestMutexAndAtomicCountersAreExact` | no lost updates under contention |
| `TestWaitGroupWaitsForEveryGoroutine` | `Wait` returns only after all `Done`s |
| `TestOnceInitializesExactlyOnce` | concurrent `Once` bodies run once total |
| `TestWriterBlocksUntilReaderReleases` | writer excludes readers both ways |

## JSON — [`jsonx/`](jsonx)

**Intent.** encoding/json idioms: decimal precision via a `json.Number`
type, `omitempty`, `UseNumber` for raw numbers, `json.RawMessage` for
deferred decoding, flattened embedded fields — and the pitfalls of default
float decoding.

**Proving tests.**

| Test | Property |
|---|---|
| `TestMoneyRoundTripsThroughJSON` | custom marshal keeps exact cents |
| `TestMoneyRejectsMalformedInput` | malformed amounts fail loudly |
| `TestOmitEmptyDropsZeroValues` | `omitempty` omits the documented zeros |
| `TestDefaultDecodingLosesIntegerPrecision` | float64 decoding corrupts big ints |
| `TestUseNumberPreservesIntegerPrecision` | `UseNumber` round-trips exactly |
| `TestRawMessageDefersDecoding` | `RawMessage` holds bytes until decoded |
| `TestEmbeddedStructFieldsAreFlattened` | embedded fields inline in the object |

## Text — [`stringsx/`](stringsx)

**Intent.** Strings are immutable byte slices, not runes: `len` counts
bytes, `range` yields byte offsets, invalid UTF-8 becomes `RuneError`, and
`strings.Builder` is the allocation-conscious concatenator.

**Proving tests.**

| Test | Property |
|---|---|
| `TestByteLengthDiffersFromRuneCount` | `len` ≠ rune count for non-ASCII |
| `TestRangeYieldsByteOffsets` | iteration offsets are bytes, not runes |
| `TestInvalidUTF8BecomesRuneError` | bad sequences decode to U+FFFD |
| `TestConversionFromBytesCopies` | `string(b)` does not alias `b` |
| `TestBuilderAvoidsRepeatedAllocation` | builder concatenates in ~O(1) allocs |
| `TestCutSplitsOnFirstSeparatorOnly` | `strings.Cut` splits once |

## Testing — [`testingx/`](testingx)

**Intent.** The testing package's own idioms: table-driven tests, helpers
that use `t.Helper()` for caller-pointing failures, `t.Setenv`/`t.TempDir`
scoping with `t.Cleanup` (LIFO), fuzz seeds as regression tests, and
examples that compile as tests.

**Proving tests.**

| Test | Property |
|---|---|
| `TestSlug` | the function under test, table-driven |
| `TestHelperAssertionReportsCallerContext` | `t.Helper()` reports the caller's line |
| `TestSetenvIsScopedToTheSubtest` | env changes are reverted automatically |
| `TestCleanupRunsInLIFOOrder` | cleanups unwind in reverse registration order |
| `TestTempDirIsUsableAndIsolated` | per-test directories do not collide |
| `FuzzSlug` / `ExampleSlug` | fuzz seeds and runnable documentation |

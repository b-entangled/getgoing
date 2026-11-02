# Mistakes (Go pitfalls)

Each package documents one recurring Go mistake with a **Bad** and a
**Good** version of the same code, then proves the difference in a test —
so the failure mode is executable, not folklore.

```bash
go test -race ./mistakes/...
```

---

## Loop variable capture — [`loopvar/`](loopvar)

**The mistake.** Capturing a loop variable in a closure or goroutine used
to capture *one shared* variable for the whole loop (pre-1.22 semantics —
still live in `range` over index-variables written by hand, and in code
that must reason about outer variables). Go 1.22 gave `for` variables
per-iteration scoping; the outer-variable case still bites.

**The fix.** Rely on per-iteration variables, or bind your own copy when
the variable is declared outside the loop.

**Proving tests.**

| Test | Property |
|---|---|
| `TestClosuresCapturePerIterationVariable` | each closure sees its own iteration |
| `TestGoroutinesCapturePerIterationVariable` | same guarantee for goroutines |
| `TestCapturingSharedOuterVariableStillBites` | the outer variable is still shared |
| `TestPerIterationCopyFixesSharedCapture` | an explicit copy fixes the outer case |

## Variable shadowing — [`shadows/`](shadows)

**The mistake.** `if err := f(); err != nil` inside a block that also has
an outer `err` creates a *new* `err`: the inner `return err` returns the
inner value while the outer one — possibly the interesting one — is
silently dropped.

**The fix.** Declare the condition variable once, assign (not re-declare)
inside nested blocks, or return from the block where the error was born.

**Proving tests.**

| Test | Property |
|---|---|
| `TestShadowingHidesInnerError` | the inner `err` masks the outer |
| `TestExplicitAssignmentPreservesInnerError` | assignment instead of declaration fixes it |
| `TestOuterErrorStillReported` | the good variant reports both layers |
| `TestShortStatementVariableIsScopedToTheIf` | the `if`-init variable dies with the block |
| `TestSiblingBlocksDeclareSeparateVariables` | sibling blocks do not see each other |

## Slice aliasing on append — [`slicecap/`](slicecap)

**The mistake.** `append` writes *in place* when spare capacity exists —
two slices sharing a backing array then overwrite each other through
appends that "obviously" create a new value.

**The fix.** Copy explicitly (`append([]T(nil), s...)`, `slices.Clone`),
or bound the view with the three-index full-slice expression `s[low:high:cap]`
so the next append must reallocate.

**Proving tests.**

| Test | Property |
|---|---|
| `TestAppendWithSpareCapacityWritesIntoTheOriginal` | in-place growth is visible through the alias |
| `TestZeroCapacityAppendIsolatesTheCopy` | `s[0:1:1]` forces a copy |
| `TestFullSliceExpressionPreventsInPlaceAppend` | the three-index form caps future growth |
| `TestAppendAtFullCapacityIsSafe` | full capacity appends allocate fresh |
| `TestAppendToNilSliceIsSafe` | nil behaves as an empty appendable slice |

## Defer timing — [`deftime/`](deftime)

**The mistake.** Two timing facts about `defer`: arguments are evaluated
immediately (so `defer f(close(fp))` closes *now*), and deferred cleanup
inside a loop runs only when the function returns — holding every
resource until the end.

**The fix.** Pass the file to a closure (`defer func() { close(fp) }()`),
or restructure loop-body resources into their own scope/function so each
iteration releases immediately.

**Proving tests.**

| Test | Property |
|---|---|
| `TestDeferCapturesTheArgumentImmediately` | arguments are fixed at the `defer` line |
| `TestClosureDefersTheArgumentToo` | wrapping in a closure defers the evaluation |
| `TestDeferInLoopReleasesEverythingAtTheEnd` | all N resources live until function exit |
| `TestReleasingInsideTheLoopFreesAsItGoes` | per-iteration scopes release per iteration |

## Closing channels — [`closechan/`](closechan)

**The mistake.** Closing twice panics; a *receiver* closing a channel it
does not own panics the producer; and close-from-anywhere makes ownership
unauditable.

**The fix.** Only the sender closes. For multi-producer cases, use
`sync.Once`, a WaitGroup, or `context` to coordinate exactly-once close.

**Proving tests.**

| Test | Property |
|---|---|
| `TestClosingTwicePanics` | double close is a panic |
| `TestReceiverClosingMakesTheProducerPanic` | receiver-side close panics the sender |
| `TestReceivingFromClosedChannelIsSafe` | receive-after-close is defined and safe |
| `TestSafeCloserClosesExactlyOnceFromManyGoroutines` | `sync.Once` close survives N closers |

## Typed nil in an interface — [`typednil/`](typednil)

**The mistake.** A `(*T)(nil)` stored in an `error`/interface is **not**
`== nil`: the interface holds a type and a nil pointer, so `if err != nil`
passes and callers dereference a typed nil. The classic version returns a
concrete `*MyError` from a function returning `error`.

**The fix.** Return `nil` explicitly when there is no error; never let a
nil pointer leak into an interface-typed result.

**Proving tests.**

| Test | Property |
|---|---|
| `TestInterfaceHoldingNilPointerIsNotNil` | `err != nil` even though the pointer is nil |
| `TestBadFindReportsNotAsAnError` | the bad pattern produces a non-nil error |
| `TestGoodFindReturnsNilOnNotFounD` | the good pattern returns a true `nil` |
| `TestCallerCannotDistinguishByNilComparisonAlone` | `== nil` cannot tell the cases apart |
| `TestErrorMethodOnNilReceiverDoesNotPanic` | why `(*T).Error()` on nil "works" |

## Swallowed errors — [`swallowerr/`](swallowerr)

**The mistake.** Ignoring or overwriting an error turns a partial failure
into a silent success: `f.Write(...)` with the error dropped, `Close`
after a failed `Write` hiding both, a second write's `err =` clobbering
the first.

**The fix.** Check at the point of failure; accumulate with `errors.Join`
when several operations must all run; never overwrite an existing error
with a later `nil`.

**Proving tests.**

| Test | Property |
|---|---|
| `TestSwallowedWriteErrorReportsSuccess` | ignoring the error reports a false success |
| `TestSwallowedCloseErrorIsLost` | dropped `Close` hides the final failure |
| `TestGoodCloseReportsNilWhenEverythingSucceeds` | the good path is honest both ways |
| `TestIgnoredSecondWriteHidesPartialFailure` | overwriting `err` loses the first failure |

## Goroutine leaks — [`goleak/`](goleak)

**The mistake.** Workers blocked forever on a channel nobody closes, or
work arriving after shutdown, accumulate goroutines — memory and CPU
leaks that only show up under load.

**The fix.** Bound the work, close channels from the owner, and drain via
context or a completion signal so every goroutine has an exit path.

**Proving tests.**

| Test | Property |
|---|---|
| `TestLeakedWorkerNeverExits` | the bad worker blocks forever |
| `TestLeakedWorkersAccumulate` | each round of work adds another stuck goroutine |
| `TestCancelledWorkerExitsAndCountRecovers` | cancellation returns the count to baseline |
| `TestBoundedWorkReturnsAndDoesNotHang` | bounded work drains to completion |

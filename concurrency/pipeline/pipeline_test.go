package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func lines() []Line {
	return []Line{
		{N: 0, Text: "INFO svc-a started"},
		{N: 1, Text: "DEBUG svc-a cache warm"},
		{N: 2, Text: "ERROR svc-b db timeout"},
		{N: 3, Text: "WARN svc-a slow query"},
		{N: 4, Text: "nonsense"},
		{N: 5, Text: "FATAL svc-c crashed"},
		{N: 6, Text: "error svc-b retried"},
	}
}

func TestParseValidLine(t *testing.T) {
	e, err := Parse(Line{N: 3, Text: "INFO svc-a hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Level != "info" || e.Source != "svc-a" || e.Msg != "hello world" {
		t.Fatalf("got %+v", e)
	}
}

func TestParseIsCaseInsensitiveOnLevel(t *testing.T) {
	e, err := Parse(Line{Text: "ErRoR svc x"})
	if err != nil || e.Level != "error" {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestParseRejectsBadLevel(t *testing.T) {
	_, err := Parse(Line{N: 1, Text: "FATAL svc crashed"})
	if !errors.Is(err, ErrBadLevel) {
		t.Fatalf("want ErrBadLevel, got %v", err)
	}
}

func TestParseRejectsTooFewFields(t *testing.T) {
	if _, err := Parse(Line{N: 9, Text: "just-one"}); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "line 9") {
		t.Fatalf("error should carry the line number: %v", err)
	}
}

// The core property: stages run concurrently and every channel closes, so
// the sink terminates without a timeout.
func TestPipelineRunsToCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rejected := &Counter{}
	in := Source(ctx, lines())
	parsed := Run(ctx, in, ParseStage(rejected))
	filtered := Run(ctx, parsed, Filter("warn"))
	out := Run(ctx, filtered, Map(func(e Entry) Entry {
		e.Msg = strings.ToUpper(e.Msg)
		return e
	}))

	got, err := Collect(ctx, out)
	if err != nil {
		t.Fatal(err)
	}

	// From 7 lines: 2 malformed (nonsense, FATAL), 3 below warn (info,
	// debug, error→ no: error is rank 4 ≥ warn).
	// Levels: info(0) debug(1) error(2) warn(3) — reject line4, line5;
	// keep error, warn, error → 3 entries.
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(got), got)
	}
	if rejected.Get() != 2 {
		t.Fatalf("rejected=%d", rejected.Get())
	}
	for _, e := range got {
		if e.Msg != strings.ToUpper(e.Msg) {
			t.Fatalf("map stage did not run: %+v", e)
		}
	}
}

func TestFilterDropsLowerLevels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	in := Source(ctx, []Line{
		{N: 0, Text: "INFO a x"},
		{N: 1, Text: "WARN b y"},
		{N: 2, Text: "ERROR c z"},
	})
	out := Run(ctx, Run(ctx, in, ParseStage(nil)), Filter("error"))
	got, err := Collect(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Level != "error" {
		t.Fatalf("got %+v", got)
	}
}

func TestEmptyInputProducesEmptyOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := Run(ctx, Source(ctx, nil), ParseStage(nil))
	got, err := Collect(ctx, out)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

// Cancellation must unwind the whole chain; nothing may block forever.
func TestCancellationStopsPipeline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// A source that will produce far more than the consumer reads.
	big := make([]Line, 10_000)
	for i := range big {
		big[i] = Line{N: i, Text: "INFO svc x"}
	}

	in := Source(ctx, big)
	out := Run(ctx, Run(ctx, in, ParseStage(nil)), Filter("info"))

	// Read one value, then cancel.
	select {
	case <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline produced nothing")
	}
	cancel()

	// Collect must return promptly with a cancellation error rather than
	// hanging: the consumer side observes ctx directly.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Collect(ctx, out)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Collect did not stop after cancel")
	}
}

func TestCountByLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	in := Source(ctx, lines())
	counts, err := CountByLevel(ctx, Run(ctx, in, ParseStage(nil)))
	if err != nil {
		t.Fatal(err)
	}
	// info, debug, error, warn, error → info1 debug1 error2 warn1
	if counts["error"] != 2 || counts["info"] != 1 || counts["debug"] != 1 || counts["warn"] != 1 {
		t.Fatalf("got %v", counts)
	}
	if len(SortedKeys(counts)) != 4 {
		t.Fatalf("keys=%v", SortedKeys(counts))
	}
}

// Fan-out gives parallelism for a CPU-heavy stage; correctness is
// unaffected because the stage is pure.
func TestFanOutProcessesEverythingExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const n = 500
	src := make([]Line, n)
	for i := range src {
		src[i] = Line{N: i, Text: "INFO svc x"}
	}

	in := Source(ctx, src)
	out := FanOut(ctx, 4, ParseStage(nil), in)

	got, err := Collect(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("want %d entries, got %d", n, len(got))
	}

	// No duplicates, no losses.
	seen := make(map[int]bool, n)
	for _, e := range got {
		if seen[e.N] {
			t.Fatalf("duplicate entry %d", e.N)
		}
		seen[e.N] = true
	}
	for i := range n {
		if !seen[i] {
			t.Fatalf("missing entry %d", i)
		}
	}
}

func TestFanOutWithZeroWorkersStillWorks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := FanOut(ctx, 0, ParseStage(nil), Source(ctx, lines()))
	got, err := Collect(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 { // 7 lines − 2 malformed
		t.Fatalf("got %d", len(got))
	}
}

func TestStageIsPureSoItIsTestableWithoutGoroutines(t *testing.T) {
	// Parse is a plain func: no channel, no goroutine, no timing.
	e, err := Parse(Line{N: 0, Text: "WARN a b"})
	if err != nil || e.Level != "warn" {
		t.Fatalf("%+v %v", e, err)
	}
}

// RunAll with an empty stage list is an identity: the input flows through.
func TestRunAllWithNoStagesPassesThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out := RunAll(ctx, Source(ctx, lines()))
	count := 0
	for range out {
		count++
	}
	if count != len(lines()) {
		t.Fatalf("want %d lines, got %d", len(lines()), count)
	}
}

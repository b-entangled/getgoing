// Package pipeline demonstrates the Go-native Pipeline pattern.
//
// A pipeline is a chain of stages connected by channels, each stage running
// in its own goroutine and transforming or filtering values as they flow.
// There is no GoF equivalent: this pattern exists because goroutines and
// channels are primitives, not because a language feature had to be
// worked around.
//
// Rules that make pipelines correct (each one is a real deadlock class):
//  1. every stage owns its output channel and closes it on exit (defer),
//     so downstream `range` loops terminate;
//  2. every send and receive selects on ctx.Done(), so cancelling the
//     context unwinds the whole chain instead of leaking goroutines;
//  3. only the producer creates channels — nobody else closes them;
//  4. stages never share memory; if they must, they send a value instead.
//
// Domain: parsing and validating log lines, then aggregating counters.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Line is one raw input record.
type Line struct {
	N    int // original position, for stable output
	Text string
}

// Entry is a parsed log record.
type Entry struct {
	N      int
	Level  string
	Source string
	Msg    string
}

// ErrBadLevel is returned by Parse for a malformed level field.
var ErrBadLevel = errors.New("pipeline: unrecognised level")

var knownLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true,
}

// Parse splits "level source message" into an Entry. It is a pure function,
// which is what makes a stage trivially testable without any goroutines.
func Parse(l Line) (Entry, error) {
	fields := strings.SplitN(strings.TrimSpace(l.Text), " ", 3)
	if len(fields) < 3 {
		return Entry{}, fmt.Errorf("pipeline: line %d: want \"level source message\"", l.N)
	}
	level := strings.ToLower(fields[0])
	if !knownLevels[level] {
		return Entry{}, fmt.Errorf("%w: line %d: %q", ErrBadLevel, l.N, fields[0])
	}
	return Entry{N: l.N, Level: level, Source: fields[1], Msg: fields[2]}, nil
}

// Stage is one pipeline link: it reads from in and writes to out, then
// closes out. Every stage has exactly this shape, which is why composing
// them is mechanical.
type Stage[I, O any] func(ctx context.Context, in <-chan I, out chan<- O)

// Run wires one stage: it owns the output channel and closes it when the
// stage returns, so every downstream `range` terminates.
//
// Stages are chained by nesting calls:
//
//	out := Run(ctx, Run(ctx, in, ParseStage(nil)), Filter("warn"))
//
// One stage per Run keeps the generics honest — each stage converts I to O,
// so a chain Line→Entry→Entry is two Run calls with two type instantiations.
// A caller must drain `out` or cancel ctx: an unread pipeline blocks at the
// final stage, which is correct backpressure, not a bug.
func Run[I, O any](ctx context.Context, in <-chan I, stage Stage[I, O]) <-chan O {
	out := make(chan O)
	go func() {
		defer close(out) // rule 1: the owner of a channel closes it
		stage(ctx, in, out)
	}()
	return out
}

// RunAll chains a sequence of stages that all share the same input and
// output type, e.g. several filters. The first stage still converts I→O;
// subsequent ones must be Stage[O,O], so this helper only accepts the
// homogeneous tail.
func RunAll[O any](ctx context.Context, in <-chan O, stages ...Stage[O, O]) <-chan O {
	prev := in
	for _, st := range stages {
		prev = Run(ctx, prev, st)
	}
	return prev
}

// --- concrete stages ---

// Source emits lines then closes the channel. The producer is the only
// party that closes its own channel.
func Source(ctx context.Context, lines []Line) <-chan Line {
	out := make(chan Line)
	go func() {
		defer close(out)
		for _, l := range lines {
			select {
			case <-ctx.Done():
				return
			case out <- l:
			}
		}
	}()
	return out
}

// ParseStage converts Lines to Entries, dropping malformed input.
// Errors are counted rather than fatal — one bad line must not kill a
// consumer that is otherwise making progress.
func ParseStage(countRejected *Counter) Stage[Line, Entry] {
	return func(ctx context.Context, in <-chan Line, out chan<- Entry) {
		for l := range in {
			if ctx.Err() != nil {
				return
			}
			e, err := Parse(l)
			if err != nil {
				if countRejected != nil {
					countRejected.Add(1)
				}
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- e:
			}
		}
	}
}

// Filter drops entries below the given level. Levels are ordered
// debug < info < warn < error.
func Filter(minLevel string) Stage[Entry, Entry] {
	min := levelRank(minLevel)
	return func(ctx context.Context, in <-chan Entry, out chan<- Entry) {
		for e := range in {
			if levelRank(e.Level) < min {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- e:
			}
		}
	}
}

// Map transforms each entry (here: tagging the message with its source).
func Map(fn func(Entry) Entry) Stage[Entry, Entry] {
	return func(ctx context.Context, in <-chan Entry, out chan<- Entry) {
		for e := range in {
			select {
			case <-ctx.Done():
				return
			case out <- fn(e):
			}
		}
	}
}

// Collect drains a channel into a slice. It is the pipeline's sink and the
// only place the pipeline becomes eager.
func Collect(ctx context.Context, in <-chan Entry) ([]Entry, error) {
	var out []Entry
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case e, ok := <-in:
			if !ok {
				return out, nil
			}
			out = append(out, e)
		}
	}
}

// CountByLevel aggregates a stream into per-level totals.
func CountByLevel(ctx context.Context, in <-chan Entry) (map[string]int, error) {
	counts := map[string]int{}
	for {
		select {
		case <-ctx.Done():
			return counts, ctx.Err()
		case e, ok := <-in:
			if !ok {
				return counts, nil
			}
			counts[e.Level]++
		}
	}
}

// --- fan-out / fan-in ---

// FanOut runs `n` copies of a stage concurrently over the same input and
// merges their outputs. This is how a pipeline gets parallelism: the stage
// is a pure function, so several goroutines may range over one channel.
//
// Two wait groups are required, and getting the order wrong panics:
// workers must be drained *concurrently* (an unbuffered worker channel
// deadlocks if you wait for all workers before reading), and `merged` may
// only be closed once every drainer has finished — otherwise a drainer
// sends on a closed channel.
func FanOut[O any](ctx context.Context, n int, stage Stage[Line, O], in <-chan Line) <-chan O {
	if n < 1 {
		n = 1
	}
	merged := make(chan O)

	workers := make([]chan O, n)
	for i := 0; i < n; i++ {
		ch := make(chan O)
		workers[i] = ch
		go func(wOut chan O) {
			defer close(wOut) // worker owns and closes its own channel
			stage(ctx, in, wOut)
		}(ch)
	}

	var drainWG sync.WaitGroup
	for _, w := range workers {
		drainWG.Add(1)
		go func(w chan O) {
			defer drainWG.Done()
			for v := range w {
				select {
				case merged <- v:
				case <-ctx.Done():
					// Stop forwarding. The worker sees the same ctx and
					// returns, closing w, so nothing leaks.
					return
				}
			}
		}(w)
	}

	go func() {
		defer close(merged)
		drainWG.Wait()
	}()

	return merged
}

// --- small utilities used by the examples and tests ---

// Counter is a lock-protected accumulator shared across stages when a
// metric simply must be shared (the exception, not the rule).
type Counter struct {
	mu sync.Mutex
	n  int
}

// Add increments the counter.
func (c *Counter) Add(d int) {
	c.mu.Lock()
	c.n += d
	c.mu.Unlock()
}

// Get reads the current value.
func (c *Counter) Get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func levelRank(l string) int {
	switch l {
	case "debug":
		return 1
	case "info":
		return 2
	case "warn":
		return 3
	case "error":
		return 4
	default:
		return 0
	}
}

// SortedKeys returns a slice's keys in a stable order for assertions.
func SortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

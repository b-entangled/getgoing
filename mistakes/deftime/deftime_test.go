package deftime

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeferCapturesTheArgumentImmediately(t *testing.T) {
	var got []int
	BadStaleArgument(1, func(v int) { got = append(got, v) })
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("recorded %v, want [1] — the argument was fixed at defer time", got)
	}
}

func TestClosureDefersTheArgumentToo(t *testing.T) {
	var got []int
	GoodClosureArgument(1, func(v int) { got = append(got, v) })
	if !reflect.DeepEqual(got, []int{99}) {
		t.Fatalf("recorded %v, want [99] — the closure reads the final value", got)
	}
}

// The resource-leak shape: nothing is released until the function ends.
func TestDeferInLoopReleasesEverythingAtTheEnd(t *testing.T) {
	var events []string
	BadDeferInLoop(5, func(s string) { events = append(events, s) })

	if len(events) != 6 {
		t.Fatalf("events = %v", events)
	}
	// body-done was recorded at the end of the loop body — before any
	// defer ran, so it is first.
	if events[0] != "body-done" {
		t.Fatalf("first event = %q, want body-done (loop recorded nothing yet)", events[0])
	}
	// The releases all fire after the body finished: resources held to
	// the very end, in LIFO order.
	wantOrder := []string{"body-done", "release-4", "release-3", "release-2", "release-1", "release-0"}
	if !reflect.DeepEqual(events, wantOrder) {
		t.Fatalf("events = %v, want %v", events, wantOrder)
	}
	for _, e := range events[1:] {
		if !strings.HasPrefix(e, "release-") {
			t.Fatalf("unexpected event %q", e)
		}
	}
	// And releases run in reverse (LIFO): release-4 first.
	if events[1] != "release-4" {
		t.Fatalf("first release = %q, want release-4 (LIFO)", events[1])
	}
}

// The fix: release as you go.
func TestReleasingInsideTheLoopFreesAsItGoes(t *testing.T) {
	var events []string
	GoodReleaseInLoop(5, func(s string) { events = append(events, s) })

	want := []string{"release-0", "release-1", "release-2", "release-3", "release-4", "body-done"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

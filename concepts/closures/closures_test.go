package closures

import (
	"reflect"
	"testing"
)

func TestAdderCapturesTheValuePassedIn(t *testing.T) {
	add10 := Adder(10)
	add1 := Adder(1)
	if add10(5) != 15 || add1(5) != 6 {
		t.Fatalf("add10(5)=%d add1(5)=%d", add10(5), add1(5))
	}
	if got := SumAll(2, Adder(1), Adder(2), Adder(3)); got != 12 {
		t.Fatalf("SumAll(2, +1,+2,+3) = %d, want 12", got)
	}
}

func TestCounterClosesOverItsOwnState(t *testing.T) {
	a, b := Counter(0), Counter(100)
	for i := 1; i <= 3; i++ {
		if got := a(); got != i {
			t.Fatalf("a() = %d, want %d", got, i)
		}
	}
	if got := b(); got != 101 {
		t.Fatalf("b() = %d, want 101", got)
	}
}

func TestTwoClosuresCanShareOneVariable(t *testing.T) {
	add, sum := NewAccumulator()
	add(1)
	add(2)
	add(3)
	if got := sum(); got != 6 {
		t.Fatalf("sum() = %d, want 6", got)
	}
}

// A closure sees later assignments to the captured variable: it captures
// the variable, not a snapshot of its value.
func TestClosureSeesLaterAssignment(t *testing.T) {
	x := 1
	f := func() int { return x }
	if f() != 1 {
		t.Fatalf("before mutation: %d", f())
	}
	x = 2
	if f() != 2 {
		t.Fatalf("closure must observe the new value, got %d", f())
	}
}

// The per-iteration guarantee (Go 1.22): closures made in a loop capture
// distinct variables — values and addresses.
func TestLoopVariableIsPerIteration(t *testing.T) {
	xs := []int{7, 8, 9}
	got := make([]int, 0, len(xs))
	for _, f := range PerIterationCaptures(xs) {
		got = append(got, f())
	}
	if want := []int{7, 8, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	var ptrs []*int
	for _, x := range xs {
		ptrs = append(ptrs, &x)
	}
	for i, p := range ptrs {
		if *p != xs[i] {
			t.Fatalf("ptrs[%d] = %d, want %d", i, *p, xs[i])
		}
	}
	if ptrs[0] == ptrs[1] {
		t.Fatal("&v must be distinct per iteration")
	}
}

// Capturing any other variable is still capture-by-reference — the
// mistake package's subject, shown here as the contrast.
func TestSharedVariableCaptureSeesFinalValue(t *testing.T) {
	fs := SharedCaptures([]int{1, 2, 3})
	for i, f := range fs {
		if got := f(); got != 3 {
			t.Fatalf("fs[%d]() = %d, want final shared value 3", i, got)
		}
	}
}

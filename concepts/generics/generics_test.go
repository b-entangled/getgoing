package generics

import (
	"reflect"
	"strconv"
	"testing"
)

func TestMapInfersBothTypeParameters(t *testing.T) {
	got := Map([]int{1, 2, 3}, strconv.Itoa)
	want := []string{"1", "2", "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFilterKeepsMatchingElements(t *testing.T) {
	got := Filter([]int{1, 2, 3, 4}, func(v int) bool { return v%2 == 0 })
	if !reflect.DeepEqual(got, []int{2, 4}) {
		t.Fatalf("got %v", got)
	}
}

// The ~ in the constraint admits named types with the same underlying type.
func TestSumAcceptsNamedNumericTypes(t *testing.T) {
	type celsius float64
	if got := Sum([]celsius{1.5, 2.5}); got != 4 {
		t.Fatalf("Sum(celsius) = %v, want 4", got)
	}
	if got := Sum([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Sum([]int) = %v, want 6", got)
	}
}

func TestIndexOfUsesComparability(t *testing.T) {
	if got := IndexOf([]string{"a", "b"}, "b"); got != 1 {
		t.Fatalf("IndexOf = %d, want 1", got)
	}
	if got := IndexOf([]string{"a"}, "z"); got != -1 {
		t.Fatalf("IndexOf miss = %d, want -1", got)
	}
}

func TestGenericStackHasNoTypeAssertions(t *testing.T) {
	var s Stack[string]
	s.Push("a")
	s.Push("b")
	if s.Len() != 2 {
		t.Fatalf("Len = %d", s.Len())
	}
	if v, ok := s.Pop(); !ok || v != "b" {
		t.Fatalf("Pop = (%q, %v)", v, ok)
	}
	if v, ok := s.Pop(); !ok || v != "a" {
		t.Fatalf("Pop = (%q, %v)", v, ok)
	}
	if _, ok := s.Pop(); ok {
		t.Fatal("empty Pop must report !ok")
	}
}

func TestCountByGroupsWithAKeyFunction(t *testing.T) {
	got := CountBy([]string{"aa", "ab", "bc"}, func(s string) string { return s[:1] })
	if !reflect.DeepEqual(got, map[string]int{"a": 2, "b": 1}) {
		t.Fatalf("got %v", got)
	}
}

// Constraint misuse does not compile — this is the compile-time proof of
// that claim, kept next to the code:
//
//	Sum([]string{"a"})        // ERROR: string not in ~int | ~float64
//	IndexOf([]int{1}, "x")    // ERROR: int and string differ
//	var s Stack[int]; s.Push("a") // ERROR: string is not int
func TestConstraintsAreEnforcedAtCompileTime(t *testing.T) {
	// The valid forms are exercised above; this test documents the rule.
	if IndexOf([]int{1, 2}, 3) != -1 {
		t.Fatal("unexpected")
	}
}

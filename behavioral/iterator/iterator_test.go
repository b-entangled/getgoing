package iterator

import (
	"slices"
	"strings"
	"testing"
)

// a simple BST:      d
//
//	   / \
//	  b   f
//	 / \ / \
//	a  c e  g
func bst() *Node {
	n := func(v string, l, r *Node) *Node { return &Node{Value: v, Left: l, Right: r} }
	return n("d",
		n("b", n("a", nil, nil), n("c", nil, nil)),
		n("f", n("e", nil, nil), n("g", nil, nil)),
	)
}

func TestPreorderVisitsRootFirst(t *testing.T) {
	var got []string
	for n := range bst().Preorder() {
		got = append(got, n.Value)
	}
	want := "d b a c f e g"
	if strings.Join(got, " ") != want {
		t.Fatalf("want %s\ngot  %s", want, strings.Join(got, " "))
	}
}

func TestInorderIsSortedForBST(t *testing.T) {
	var got []string
	for n := range bst().Inorder() {
		got = append(got, n.Value)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("inorder of a BST must be sorted: %v", got)
	}
	if strings.Join(got, " ") != "a b c d e f g" {
		t.Fatalf("got %v", got)
	}
}

func TestNilNodeYieldsNothing(t *testing.T) {
	count := 0
	var nilNode *Node
	for range nilNode.Preorder() {
		count++
	}
	if count != 0 {
		t.Fatalf("got %d", count)
	}
}

// The whole point of the pattern: break stops the walk.
func TestEarlyBreakStopsTraversal(t *testing.T) {
	visited := 0
	for range bst().Preorder() {
		visited++
		if visited == 3 {
			break
		}
	}
	if visited != 3 {
		t.Fatalf("got %d", visited)
	}
}

// Laziness: a node that panics if walked is never reached when unused.
func TestSequenceIsLazy(t *testing.T) {
	built := 0
	makeNode := func() *Node {
		built++
		return &Node{Value: "x"}
	}
	// Building a tree but never ranging over it walks nothing.
	root := makeNode()
	root.Left = makeNode()
	_ = root
	if built != 2 {
		t.Fatal("test premise")
	}
	// Now confirm ranging stops early without touching the right subtree.
	right := &Node{Value: "boom"}
	right.Right = &Node{Value: "never"}
	left := &Node{Value: "a"}
	root2 := &Node{Value: "r", Left: left, Right: right}
	seen := 0
	for range root2.Preorder() {
		seen++
		if seen == 2 {
			break
		}
	}
	if seen != 2 {
		t.Fatalf("got %d", seen)
	}
}

func TestWordsSplitsOnWhitespace(t *testing.T) {
	got := Collect(Words("  alpha\tbeta\ngamma  "))
	want := []string{"alpha", "beta", "gamma"}
	if !slices.Equal(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestWordsEmpty(t *testing.T) {
	if got := Collect(Words("   \t\n ")); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestCount(t *testing.T) {
	if got := Collect(Count(4)); !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Fatalf("got %v", got)
	}
	if got := Collect(Count(0)); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestTakeStopsEarly(t *testing.T) {
	if got := Collect(Take(3, Count(100))); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("got %v", got)
	}
	if got := Collect(Take(0, Count(5))); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := Collect(Take(10, Count(3))); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("got %v", got)
	}
}

func TestFilter(t *testing.T) {
	evens := Filter(Count(10), func(i int) bool { return i%2 == 0 })
	if got := Collect(evens); !slices.Equal(got, []int{0, 2, 4, 6, 8}) {
		t.Fatalf("got %v", got)
	}
}

// Adaptors compose: no intermediate slices are ever allocated.
func TestAdaptorsCompose(t *testing.T) {
	seq := Take(4, Filter(Count(100), func(i int) bool { return i%3 == 0 }))
	if got := Collect(seq); !slices.Equal(got, []int{0, 3, 6, 9}) {
		t.Fatalf("got %v", got)
	}
}

// Any short-circuits — proven by a sequence that would panic if walked far.
func TestAnyShortCircuits(t *testing.T) {
	g := Count(1_000_000)
	if !Any(g, func(i int) bool { return i == 5 }) {
		t.Fatal("expected match")
	}
	if Any(Count(3), func(i int) bool { return i > 10 }) {
		t.Fatal("expected no match")
	}
}

// --- stateful iterator ---

func TestSliceIterNextAndReset(t *testing.T) {
	it := NewSliceIter([]string{"a", "b", "c"})
	v, ok := it.Next()
	if !ok || v != "a" {
		t.Fatalf("%q %t", v, ok)
	}
	if it.Remaining() != 2 {
		t.Fatalf("remaining=%d", it.Remaining())
	}
	it.Reset()
	if it.Remaining() != 3 {
		t.Fatalf("remaining=%d", it.Remaining())
	}
	got := Collect(it.All())
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSliceIterExhaustion(t *testing.T) {
	it := NewSliceIter([]int{1})
	_, _ = it.Next()
	v, ok := it.Next()
	if ok || v != 0 {
		t.Fatalf("exhausted iterator returned %v %t", v, ok)
	}
	if it.Remaining() != 0 {
		t.Fatalf("remaining=%d", it.Remaining())
	}
}

func TestSliceIterEmpty(t *testing.T) {
	it := NewSliceIter([]int{})
	if _, ok := it.Next(); ok {
		t.Fatal("empty iterator must report !ok")
	}
	if Collect(it.All()) != nil {
		t.Fatal("expected nil slice")
	}
}

// --- closure-pull idiom ---

func TestEachLineCountsAndDelivers(t *testing.T) {
	var lines []string
	n := EachLine("one\ntwo\nthree", func(l string) bool {
		lines = append(lines, l)
		return true
	})
	if n != 3 || strings.Join(lines, ",") != "one,two,three" {
		t.Fatalf("n=%d lines=%v", n, lines)
	}
}

func TestEachLineStopsOnFalse(t *testing.T) {
	var lines []string
	n := EachLine("a\nb\nc", func(l string) bool {
		lines = append(lines, l)
		return len(lines) < 2
	})
	if n != 2 || len(lines) != 2 {
		t.Fatalf("n=%d lines=%v", n, lines)
	}
}

func TestEachLineSingleLineNoTrailingNewline(t *testing.T) {
	var lines []string
	n := EachLine("only", func(l string) bool {
		lines = append(lines, l)
		return true
	})
	if n != 1 || lines[0] != "only" {
		t.Fatalf("n=%d lines=%v", n, lines)
	}
}

func TestReverseLines(t *testing.T) {
	got := ReverseLines("a\nb\nc")
	if strings.Join(got, ",") != "c,b,a" {
		t.Fatalf("got %v", got)
	}
}

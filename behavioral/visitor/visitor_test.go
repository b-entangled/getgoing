package visitor

import (
	"errors"
	"strings"
	"testing"
)

// sample: (2 + x) * 3
func sample() Node {
	return &Mul{
		L: &Add{L: &Lit{V: 2}, R: &Var{Name: "x"}},
		R: &Lit{V: 3},
	}
}

func TestWalkVisitsEveryNode(t *testing.T) {
	c := NewCounter()
	if err := Walk(c, sample()); err != nil {
		t.Fatal(err)
	}
	// nodes: mul, add, lit(2), var(x), lit(3)
	want := map[string]int{"mul": 1, "add": 1, "lit": 2, "var": 1}
	got := c.Counts()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want %d, got %d (all: %v)", k, v, got[k], got)
		}
	}
	if c.Total() != 5 {
		t.Fatalf("total=%d", c.Total())
	}
}

func TestWalkHandlesNil(t *testing.T) {
	c := NewCounter()
	if err := Walk(c, nil); err != nil {
		t.Fatal(err)
	}
	if c.Total() != 0 {
		t.Fatalf("total=%d", c.Total())
	}
}

// Walk is depth-first, left-to-right — proven by recording visit order.
func TestWalkOrderIsDepthFirstLeftToRight(t *testing.T) {
	rec := &recorder{}
	if err := Walk(rec, sample()); err != nil {
		t.Fatal(err)
	}
	want := []string{"mul", "add", "lit", "var", "lit"}
	if strings.Join(rec.order, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v\ngot  %v", want, rec.order)
	}
}

type recorder struct{ order []string }

func (r *recorder) add(s string) error  { r.order = append(r.order, s); return nil }
func (r *recorder) VisitLit(*Lit) error { return r.add("lit") }
func (r *recorder) VisitVar(*Var) error { return r.add("var") }
func (r *recorder) VisitAdd(*Add) error { return r.add("add") }
func (r *recorder) VisitMul(*Mul) error { return r.add("mul") }
func (r *recorder) VisitNeg(*Neg) error { return r.add("neg") }

// A visitor error aborts the whole walk.
func TestWalkStopsOnVisitorError(t *testing.T) {
	boom := errors.New("boom")
	v := &failOn{kind: "add", err: boom}
	err := Walk(v, sample())
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	// mul was visited before the failure, lit/var after were not.
	if v.visited["var"] {
		t.Fatal("walk continued after an error")
	}
}

type failOn struct {
	kind    string
	err     error
	visited map[string]bool
}

func (f *failOn) hit(kind string) error {
	if f.visited == nil {
		f.visited = map[string]bool{}
	}
	f.visited[kind] = true
	if kind == f.kind {
		return f.err
	}
	return nil
}

func (f *failOn) VisitLit(*Lit) error { return f.hit("lit") }
func (f *failOn) VisitVar(*Var) error { return f.hit("var") }
func (f *failOn) VisitAdd(*Add) error { return f.hit("add") }
func (f *failOn) VisitMul(*Mul) error { return f.hit("mul") }
func (f *failOn) VisitNeg(*Neg) error { return f.hit("neg") }

// --- WalkChecked / exhaustive default arm ---

func TestWalkCheckedHandlesKnownTypes(t *testing.T) {
	c := NewCounter()
	if err := WalkChecked(c, sample()); err != nil {
		t.Fatal(err)
	}
	if c.Total() != 5 {
		t.Fatalf("total=%d", c.Total())
	}
}

type rogue struct{}

func (*rogue) Kind() string         { return "rogue" }
func (*rogue) Children() []Node     { return nil }
func (*rogue) accept(Visitor) error { return nil }

// The GoF "forgetting a case" hazard is recovered as a loud error naming
// the offending type, instead of silently doing nothing.
func TestWalkCheckedRejectsUnknownNodeType(t *testing.T) {
	c := NewCounter()
	err := WalkChecked(c, &rogue{})
	if !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("want ErrUnknownNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "*visitor.rogue") {
		t.Fatalf("error should name the type: %v", err)
	}
}

// --- evaluation (the operation that does NOT fit Visitor) ---

func TestEvaluate(t *testing.T) {
	v, err := Evaluate(sample(), Env{"x": 5})
	if err != nil {
		t.Fatal(err)
	}
	if v != 21 { // (2+5)*3
		t.Fatalf("got %d", v)
	}
}

func TestEvaluateUndefinedVariable(t *testing.T) {
	_, err := Evaluate(&Var{Name: "y"}, Env{})
	if err == nil || !strings.Contains(err.Error(), "undefined variable") {
		t.Fatalf("got %v", err)
	}
}

func TestEvaluateNeg(t *testing.T) {
	v, err := Evaluate(&Neg{X: &Lit{V: 7}}, nil)
	if err != nil || v != -7 {
		t.Fatalf("%d %v", v, err)
	}
}

func TestEvaluateUnknownNode(t *testing.T) {
	if _, err := Evaluate(&rogue{}, nil); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("got %v", err)
	}
}

// --- rendering ---

func TestRenderMinimalParentheses(t *testing.T) {
	tests := []struct {
		node Node
		want string
	}{
		{&Lit{V: 42}, "42"},
		{&Var{Name: "x"}, "x"},
		{&Add{&Lit{V: 1}, &Lit{V: 2}}, "1 + 2"},
		// * binds tighter than + → no brackets needed inside +
		{&Add{&Mul{&Lit{V: 2}, &Lit{V: 3}}, &Lit{V: 4}}, "2 * 3 + 4"},
		// + binds looser → brackets required inside *
		{&Mul{&Add{&Lit{V: 1}, &Lit{V: 2}}, &Lit{V: 3}}, "(1 + 2) * 3"},
		{&Neg{&Lit{V: 5}}, "-5"},
	}
	for _, tt := range tests {
		if got := Render(tt.node); got != tt.want {
			t.Errorf("Render(%s) = %q, want %q", tt.node.Kind(), got, tt.want)
		}
	}
}

// --- constant folding (a transforming visitor) ---

func TestFolderFoldsAdditionOfLiterals(t *testing.T) {
	n := &Add{L: &Lit{V: 2}, R: &Lit{V: 3}}
	f := &Folder{}
	if err := Walk(f, n); err != nil {
		t.Fatal(err)
	}
	if f.Folds() != 1 {
		t.Fatalf("folds=%d", f.Folds())
	}
	lit, ok := n.L.(*Lit)
	if !ok || lit.V != 5 {
		t.Fatalf("left operand not folded: %#v", n.L)
	}
}

func TestFolderAbsorbsMultiplicationByZero(t *testing.T) {
	n := &Mul{L: &Lit{V: 0}, R: &Var{Name: "x"}}
	f := &Folder{}
	if err := Walk(f, n); err != nil {
		t.Fatal(err)
	}
	if f.Folds() != 1 {
		t.Fatalf("folds=%d", f.Folds())
	}
	if l, ok := n.L.(*Lit); !ok || l.V != 0 {
		t.Fatalf("got %#v", n.L)
	}
}

func TestFolderIdentityMultiplication(t *testing.T) {
	x := &Var{Name: "x"}
	n := &Mul{L: &Lit{V: 1}, R: x}
	f := &Folder{}
	if err := Walk(f, n); err != nil {
		t.Fatal(err)
	}
	if n.L != Node(x) {
		t.Fatalf("1*x should become x, got %#v", n.L)
	}
}

func TestFolderFoldsDoubleNegation(t *testing.T) {
	n := &Neg{X: &Neg{X: &Lit{V: 4}}}
	f := &Folder{}
	if err := Walk(f, n); err != nil {
		t.Fatal(err)
	}
	// inner Neg folds first (post-order? no — pre-order). The outer is
	// visited before the inner, so the outer does not fold; the inner does.
	if f.Folds() != 1 {
		t.Fatalf("folds=%d", f.Folds())
	}
}

func TestFolderLeavesVariablesAlone(t *testing.T) {
	n := &Add{L: &Var{Name: "x"}, R: &Var{Name: "y"}}
	f := &Folder{}
	if err := Walk(f, n); err != nil {
		t.Fatal(err)
	}
	if f.Folds() != 0 {
		t.Fatalf("folds=%d", f.Folds())
	}
}

func TestFolderValueIsPreserved(t *testing.T) {
	// Folding must not change what the expression evaluates to.
	n := sample()
	before, err := Evaluate(n, Env{"x": 4})
	if err != nil {
		t.Fatal(err)
	}
	f := &Folder{}
	if err := Walk(f, n); err != nil {
		t.Fatal(err)
	}
	after, err := Evaluate(n, Env{"x": 4})
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("folding changed the value: %d -> %d", before, after)
	}
}

// --- linter (collects instead of failing) ---

func TestLinterCollectsEveryIssueInOnePass(t *testing.T) {
	n := &Add{
		L: &Mul{L: &Var{Name: "a"}, R: &Lit{V: 1}},
		R: &Lit{V: 0},
	}
	l := &Linter{}
	if err := Walk(l, n); err != nil {
		t.Fatal(err)
	}
	got := l.Sorted()
	want := []string{"adding zero", "multiplying by one", `terse variable name "a"`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v\ngot  %v", want, got)
	}
}

func TestLinterFlagsLargeLiterals(t *testing.T) {
	l := &Linter{}
	if err := Walk(l, &Lit{V: 5_000_000}); err != nil {
		t.Fatal(err)
	}
	if len(l.Issues) != 1 {
		t.Fatalf("issues=%v", l.Issues)
	}
}

func TestLinterCleanTreeHasNoIssues(t *testing.T) {
	l := &Linter{}
	n := &Add{L: &Var{Name: "alpha"}, R: &Lit{V: 7}}
	if err := Walk(l, n); err != nil {
		t.Fatal(err)
	}
	if len(l.Issues) != 0 {
		t.Fatalf("issues=%v", l.Issues)
	}
}

// --- adding an operation: the pattern's payoff ---

// Sizer is a *new* operation added after the fact. It is a fresh type
// implementing Visitor: zero edits to the node types and zero edits to
// Walk. That is what the pattern buys.
type Sizer struct{ nodes int }

// VisitLit counts a literal.
func (s *Sizer) VisitLit(*Lit) error { s.nodes++; return nil }

// VisitVar counts a variable.
func (s *Sizer) VisitVar(*Var) error { s.nodes++; return nil }

// VisitAdd counts an addition.
func (s *Sizer) VisitAdd(*Add) error { s.nodes++; return nil }

// VisitMul counts a multiplication.
func (s *Sizer) VisitMul(*Mul) error { s.nodes++; return nil }

// VisitNeg counts a negation.
func (s *Sizer) VisitNeg(*Neg) error { s.nodes++; return nil }

// Nodes returns the size.
func (s *Sizer) Nodes() int { return s.nodes }

// --- alternative form A: method-based ---

func TestMethodBasedString(t *testing.T) {
	// The method form needs no visitor at all, but every node carries it.
	got := (&Add{L: &Mul{L: &Lit{V: 2}, R: &Var{Name: "x"}}, R: &Lit{V: 1}}).String()
	if got != "((2 * x) + 1)" {
		t.Fatalf("got %q", got)
	}
}

func TestMethodAndVisitorAgreeOnLeaves(t *testing.T) {
	// Two independent implementations of "render" must not disagree.
	if (&Lit{V: 9}).String() != Render(&Lit{V: 9}) {
		t.Fatal("method and function render differently for a literal")
	}
	if (&Var{Name: "q"}).String() != Render(&Var{Name: "q"}) {
		t.Fatal("method and function render differently for a variable")
	}
}

func TestNodeKindAndChildren(t *testing.T) {
	n := sample()
	if n.Kind() != "mul" {
		t.Fatalf("kind=%s", n.Kind())
	}
	kids := n.Children()
	if len(kids) != 2 || kids[0].Kind() != "add" || kids[1].Kind() != "lit" {
		t.Fatalf("children=%v", kids)
	}
}

// Each operation is independent: running one does not disturb another.
func TestOperationsAreIndependent(t *testing.T) {
	n := sample()

	c := NewCounter()
	_ = Walk(c, n)

	l := &Linter{}
	_ = Walk(l, n)

	f := &Folder{}
	_ = Walk(f, n)

	if c.Total() != 5 {
		t.Fatalf("counter affected by other ops: %d", c.Total())
	}
	// sample() uses the single-character variable "x", which the linter
	// deliberately flags — the point is that the linter's output is
	// unaffected by running the counter first.
	if len(l.Issues) != 1 || !strings.Contains(l.Issues[0], `"x"`) {
		t.Fatalf("unexpected issues: %v", l.Issues)
	}
}

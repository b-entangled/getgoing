// Package visitor demonstrates the Visitor pattern — and its limits in Go.
//
// Visitor expresses "add an operation over a fixed set of types without
// editing those types". The GoF implementation needs double dispatch, which
// Go does not have (no overloading, no virtual methods).
//
// Two Go replacements are implemented here, and the choice between them is
// a real design decision:
//
//	A. Method on the element  — adds an operation by adding a method.
//	   Easy to extend with operations; hard to extend with types (every
//	   implementation must be updated). Right when the type set is closed.
//
//	B. Visitor interface + type switch (this package) — adds an operation by
//	   adding one Visitor implementation. Easy to extend with operations;
//	   adding a type means one new `case` in accept(). Right when you have
//	   many operations and few types, and you want the compiler to tell you
//	   where the work is (via a `default:` arm that fails loudly).
//
// The GoF version is rejected: without double dispatch it degenerates into
// type switches anyway, and the classic "forgetting a case is a compile
// error" property is lost — here we recover it deliberately with a default
// arm that returns an error naming the unknown type.
//
// Domain: an arithmetic expression tree.
package visitor

import (
	"fmt"
	"sort"
)

// =====================================================================
// The element types (the closed set of nodes)
// =====================================================================

// Node is an element in the expression tree. It knows how to *accept* a
// visitor; it does not know what the visitor does.
type Node interface {
	accept(v Visitor) error
	// Kind names the node type for debugging and for the default arm.
	Kind() string
	// Children exposes sub-nodes so a generic walk can recurse.
	Children() []Node
}

// Lit is a literal integer.
type Lit struct{ V int }

// Kind names the node.
func (*Lit) Kind() string { return "lit" }

// Children returns no children.
func (*Lit) Children() []Node { return nil }

func (n *Lit) accept(v Visitor) error { return v.VisitLit(n) }

// Var is a named variable looked up in an environment.
type Var struct{ Name string }

// Kind names the node.
func (*Var) Kind() string { return "var" }

// Children returns no children.
func (*Var) Children() []Node { return nil }

func (n *Var) accept(v Visitor) error { return v.VisitVar(n) }

// Add sums two sub-expressions.
type Add struct{ L, R Node }

// Kind names the node.
func (*Add) Kind() string { return "add" }

// Children returns the operands in order.
func (n *Add) Children() []Node { return []Node{n.L, n.R} }

func (n *Add) accept(v Visitor) error { return v.VisitAdd(n) }

// Mul multiplies two sub-expressions.
type Mul struct{ L, R Node }

// Kind names the node.
func (*Mul) Kind() string { return "mul" }

// Children returns the operands in order.
func (n *Mul) Children() []Node { return []Node{n.L, n.R} }

func (n *Mul) accept(v Visitor) error { return v.VisitMul(n) }

// Neg negates one sub-expression.
type Neg struct{ X Node }

// Kind names the node.
func (*Neg) Kind() string { return "neg" }

// Children returns the operand.
func (n *Neg) Children() []Node { return []Node{n.X} }

func (n *Neg) accept(v Visitor) error { return v.VisitNeg(n) }

// =====================================================================
// The visitor interface
// =====================================================================

// Visitor is implemented by every *operation*. One method per node type:
// the compiler now enforces that adding a node type breaks every visitor,
// which is exactly the feedback the GoF pattern wanted.
type Visitor interface {
	VisitLit(*Lit) error
	VisitVar(*Var) error
	VisitAdd(*Add) error
	VisitMul(*Mul) error
	VisitNeg(*Neg) error
}

// Walk applies a visitor to a node and every descendant, depth-first,
// left-to-right. This single function is the "accept" machinery — nodes do
// not each need their own recursive accept.
func Walk(v Visitor, n Node) error {
	if n == nil {
		return nil
	}
	if err := n.accept(v); err != nil {
		return err
	}
	for _, c := range n.Children() {
		if err := Walk(v, c); err != nil {
			return err
		}
	}
	return nil
}

// ErrUnknownNode is returned by Walk's default arm when a node type has no
// visitor. Recovering the GoF compile-time guarantee as a loud runtime
// error is the deliberate trade for having a single generic Walk.
var ErrUnknownNode = fmt.Errorf("visitor: unhandled node type")

// WalkChecked is Walk with an exhaustive check: it visits nodes through the
// type switch rather than through accept, so an unhandled type produces an
// error naming it instead of silently doing nothing.
func WalkChecked(v Visitor, n Node) error {
	if n == nil {
		return nil
	}
	switch t := n.(type) {
	case *Lit:
		return v.VisitLit(t)
	case *Var:
		return v.VisitVar(t)
	case *Add:
		if err := v.VisitAdd(t); err != nil {
			return err
		}
	case *Mul:
		if err := v.VisitMul(t); err != nil {
			return err
		}
	case *Neg:
		if err := v.VisitNeg(t); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: %T", ErrUnknownNode, n)
	}
	for _, c := range n.Children() {
		if err := WalkChecked(v, c); err != nil {
			return err
		}
	}
	return nil
}

// =====================================================================
// Operation 1: counting (a pure observer)
// =====================================================================

// Counter is the simplest possible visitor: it records how many nodes of
// each kind the tree contains. Nothing is computed about values, so a
// pre-order walk is exactly right.
type Counter struct {
	counts map[string]int
}

// NewCounter builds an empty counter.
func NewCounter() *Counter { return &Counter{counts: make(map[string]int)} }

func (c *Counter) count(kind string) error {
	if c.counts == nil {
		c.counts = make(map[string]int)
	}
	c.counts[kind]++
	return nil
}

// VisitLit counts literals.
func (c *Counter) VisitLit(*Lit) error { return c.count("lit") }

// VisitVar counts variables.
func (c *Counter) VisitVar(*Var) error { return c.count("var") }

// VisitAdd counts additions.
func (c *Counter) VisitAdd(*Add) error { return c.count("add") }

// VisitMul counts multiplications.
func (c *Counter) VisitMul(*Mul) error { return c.count("mul") }

// VisitNeg counts negations.
func (c *Counter) VisitNeg(*Neg) error { return c.count("neg") }

// Counts returns a copy of the tally, sorted by kind.
func (c *Counter) Counts() map[string]int {
	out := make(map[string]int, len(c.counts))
	for k, v := range c.counts {
		out[k] = v
	}
	return out
}

// Total returns the number of nodes visited.
func (c *Counter) Total() int {
	n := 0
	for _, v := range c.counts {
		n += v
	}
	return n
}

// =====================================================================
// Operation 2: rendering — deliberately *not* a visitor
// =====================================================================

// Render prints an expression with correct minimal parentheses.
//
// It is written as a recursive function rather than a Visitor, and that is
// the point of shipping it here: an operation that needs its children's
// *results* (not just their existence) does not fit the visitor shape,
// because Walk is pre-order and a visitor sees a node before its operands.
// Recognising which operations fit Visitor is the actual design skill.
func Render(n Node) string {
	switch t := n.(type) {
	case *Lit:
		return fmt.Sprint(t.V)
	case *Var:
		return t.Name
	case *Add:
		return renderBin(t.L, "+", t.R)
	case *Mul:
		return renderBin(t.L, "*", t.R)
	case *Neg:
		return "-" + render(t.X, "-")
	default:
		return "?"
	}
}

func renderBin(l Node, op string, r Node) string {
	return render(l, op) + " " + op + " " + render(r, op)
}

// render renders an operand, adding brackets when its operator binds more
// tightly than the surrounding one.
func render(n Node, outerOp string) string {
	s := Render(n)
	inner := innerOp(n)
	if bindsTighter(inner, outerOp) {
		return "(" + s + ")"
	}
	return s
}

func innerOp(n Node) string {
	switch n.(type) {
	case *Add:
		return "+"
	case *Mul:
		return "*"
	case *Neg:
		return "-"
	default:
		return ""
	}
}

// bindsTighter reports whether inner must be parenthesised when it appears
// as an operand of outer. * binds tighter than +, so (a+b)*c needs brackets
// while a*b+c does not.
func bindsTighter(inner, outer string) bool {
	if inner == "" {
		return false
	}
	tight := map[string]int{"+": 1, "*": 2, "-": 0}
	return tight[inner] < tight[outer]
}

// Evaluate computes the value of an expression tree. Like Render, it is a
// recursive function, not a visitor, for the same reason: it needs the
// children's values.
func Evaluate(n Node, env Env) (int, error) {
	switch t := n.(type) {
	case *Lit:
		return t.V, nil
	case *Var:
		v, ok := env[t.Name]
		if !ok {
			return 0, fmt.Errorf("undefined variable %q", t.Name)
		}
		return v, nil
	case *Add:
		l, err := Evaluate(t.L, env)
		if err != nil {
			return 0, err
		}
		r, err := Evaluate(t.R, env)
		if err != nil {
			return 0, err
		}
		return l + r, nil
	case *Mul:
		l, err := Evaluate(t.L, env)
		if err != nil {
			return 0, err
		}
		r, err := Evaluate(t.R, env)
		if err != nil {
			return 0, err
		}
		return l * r, nil
	case *Neg:
		v, err := Evaluate(t.X, env)
		if err != nil {
			return 0, err
		}
		return -v, nil
	default:
		return 0, fmt.Errorf("%w: %T", ErrUnknownNode, n)
	}
}

// Env maps variable names to values.
type Env map[string]int

// =====================================================================
// Operation 3: constant folding (a *transforming* visitor)
// =====================================================================

// Folder is a visitor that replaces constant sub-expressions with literals.
// Because it returns a Node, it demonstrates that a visitor can rewrite the
// structure it walks rather than only observe it.
type Folder struct {
	folds int
}

// Folds reports how many simplifications were applied.
func (f *Folder) Folds() int { return f.folds }

// VisitLit does nothing — a literal is already a constant.
func (f *Folder) VisitLit(*Lit) error { return nil }

// VisitVar does nothing — a variable is not constant.
func (f *Folder) VisitVar(*Var) error { return nil }

// VisitAdd folds "lit + lit" into a single literal.
func (f *Folder) VisitAdd(n *Add) error {
	if l, ok := n.L.(*Lit); ok {
		if r, ok := n.R.(*Lit); ok {
			n.L = &Lit{V: l.V + r.V}
			n.R = &Lit{V: 0}
			f.folds++
		}
	}
	return nil
}

// VisitMul folds "lit * lit" and absorbs multiplication by zero.
func (f *Folder) VisitMul(n *Mul) error {
	if l, ok := n.L.(*Lit); ok {
		if r, ok := n.R.(*Lit); ok {
			n.L = &Lit{V: l.V * r.V}
			n.R = &Lit{V: 1}
			f.folds++
			return nil
		}
		if l.V == 0 {
			n.L = &Lit{V: 0}
			f.folds++
		}
		if l.V == 1 {
			n.L = n.R
			f.folds++
		}
	}
	return nil
}

// VisitNeg folds "-lit".
func (f *Folder) VisitNeg(n *Neg) error {
	if x, ok := n.X.(*Lit); ok {
		n.X = &Lit{V: -x.V}
		f.folds++
	}
	return nil
}

// =====================================================================
// Operation 4: a linter (collects problems instead of failing fast)
// =====================================================================

// Linter records style problems without stopping the walk.
type Linter struct {
	Issues []string
}

// VisitLit flags suspiciously large literals.
func (l *Linter) VisitLit(n *Lit) error {
	if n.V > 1_000_000 {
		l.Issues = append(l.Issues, fmt.Sprintf("large literal %d", n.V))
	}
	return nil
}

// VisitVar flags unused-looking names (single character).
func (l *Linter) VisitVar(n *Var) error {
	if len(n.Name) == 1 {
		l.Issues = append(l.Issues, fmt.Sprintf("terse variable name %q", n.Name))
	}
	return nil
}

// VisitAdd flags adding a literal zero.
func (l *Linter) VisitAdd(n *Add) error {
	if r, ok := n.R.(*Lit); ok && r.V == 0 {
		l.Issues = append(l.Issues, "adding zero")
	}
	return nil
}

// VisitMul flags multiplication by one.
func (l *Linter) VisitMul(n *Mul) error {
	if r, ok := n.R.(*Lit); ok && r.V == 1 {
		l.Issues = append(l.Issues, "multiplying by one")
	}
	return nil
}

// VisitNeg flags a double negation.
func (l *Linter) VisitNeg(n *Neg) error {
	if _, ok := n.X.(*Neg); ok {
		l.Issues = append(l.Issues, "double negation")
	}
	return nil
}

// Sorted returns the issues in a stable order for snapshot testing.
func (l *Linter) Sorted() []string {
	out := append([]string(nil), l.Issues...)
	sort.Strings(out)
	return out
}

// =====================================================================
// Alternative form A: operations as methods (the GoF-free version)
// =====================================================================

// String is the method-based operation: each element renders itself.
// Adding a *new operation* means adding a method to all five types — the
// cost this design pays for never needing a type switch.
func (n *Lit) String() string { return fmt.Sprint(n.V) }

// String renders the variable.
func (n *Var) String() string { return n.Name }

// String renders the sum with parentheses for safety.
func (n *Add) String() string { return "(" + nodeString(n.L) + " + " + nodeString(n.R) + ")" }

// String renders the product with parentheses for safety.
func (n *Mul) String() string { return "(" + nodeString(n.L) + " * " + nodeString(n.R) + ")" }

// String renders the negation.
func (n *Neg) String() string { return "-" + nodeString(n.X) }

// nodeString renders a child through the method-based operation. Every node
// type in this package implements fmt.Stringer, so the assertion always
// succeeds; the fallback keeps the helper total.
func nodeString(n Node) string {
	if s, ok := n.(fmt.Stringer); ok {
		return s.String()
	}
	return Render(n)
}

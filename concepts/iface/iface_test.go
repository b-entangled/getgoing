package iface

import "testing"

// Implicit satisfaction: no registration, both concrete types work.
func TestTotalAreaAcceptsAnyImplementer(t *testing.T) {
	r := &Rect{W: 3, H: 4}
	got := TotalArea(Circle{R: 1}, r)
	want := 3*4 + 3.141592653589793 // 12 + pi
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("TotalArea = %v, want %v", got, want)
	}
}

// The method-set rule: value receivers satisfy the interface as values;
// pointer receivers do not.
func TestMethodSetRules(t *testing.T) {
	if _, ok := AsShape(Circle{R: 1}); !ok {
		t.Error("value receiver: Circle must satisfy Shape")
	}
	if _, ok := AsShape(&Circle{R: 1}); !ok {
		t.Error("value receiver: *Circle must satisfy Shape too")
	}
	if _, ok := AsShape(Rect{W: 1, H: 2}); ok {
		t.Error("pointer receiver: Rect value must NOT satisfy Shape")
	}
	if _, ok := AsShape(&Rect{W: 1, H: 2}); !ok {
		t.Error("pointer receiver: *Rect must satisfy Shape")
	}
}

// Embedding promotes methods from the embedded interface.
func TestEmbeddedInterfacePromotesMethods(t *testing.T) {
	if !IsMeasurable(Circle{R: 2}) {
		t.Error("Circle implements both Area and Perimeter")
	}
	if IsMeasurable(Rect{W: 1, H: 1}) {
		t.Error("Rect value has neither method in its method set")
	}
	if !IsMeasurable(&Rect{W: 1, H: 1}) {
		t.Error("*Rect implements both")
	}
	m := Measurable(&Rect{W: 3, H: 4})
	if m.Perimeter() != 14 {
		t.Fatalf("Perimeter = %v, want 14", m.Perimeter())
	}
}

// A type switch reads on the dynamic type without a chain of assertions.
func TestTypeSwitchDispatchesOnDynamicType(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{Circle{R: 1}, "circle r=1"},
		{&Rect{W: 3, H: 4}, "rect 3x4"},
		{42, "not a shape"},
	}
	for _, c := range cases {
		if got := Describe(c.in); got != c.want {
			t.Errorf("Describe(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAssertionFailureIsRecoverableNotFatal(t *testing.T) {
	s, ok := AsShape("not a shape")
	if ok || s != nil {
		t.Fatalf("failed assertion must return (nil, false), got (%v, %v)", s, ok)
	}
}

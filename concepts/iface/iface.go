// Package iface demonstrates Go's implicit interfaces: satisfaction by
// method set, the value/pointer receiver distinction, embedding, and
// type assertions.
//
// An interface type is satisfied by *any* type whose **method set**
// contains the interface's methods — there is no `implements` keyword and
// no registration. The method set of a value type contains only
// value-receiver methods; the method set of a pointer type contains both.
//
// Domain: geometry. `Circle` uses value receivers, `Rect` uses pointer
// receivers, so the same interface shows both halves of the rule.
package iface

import "math"

// Shape is a consumer-declared interface: it lives next to the code that
// uses it, not next to the implementations.
type Shape interface {
	Area() float64
}

// Measurable embeds Shape, inheriting Area and adding Perimeter.
type Measurable interface {
	Shape
	Perimeter() float64
}

// Circle implements Shape with a **value receiver**, so both Circle and
// *Circle satisfy it.
type Circle struct{ R float64 }

// Area returns the circle's area.
func (c Circle) Area() float64 { return math.Pi * c.R * c.R }

// Perimeter returns the circumference.
func (c Circle) Perimeter() float64 { return 2 * math.Pi * c.R }

// Rect implements Shape with a **pointer receiver**: only *Rect satisfies
// Shape, because a value's method set omits pointer-receiver methods.
type Rect struct{ W, H float64 }

// Area returns the rectangle's area.
func (r *Rect) Area() float64 { return r.W * r.H }

// Perimeter returns the rectangle's perimeter.
func (r *Rect) Perimeter() float64 { return 2 * (r.W + r.H) }

// TotalArea sums any collection of shapes — the caller never learns the
// concrete types.
func TotalArea(shapes ...Shape) float64 {
	var total float64
	for _, s := range shapes {
		total += s.Area()
	}
	return total
}

// AsShape reports whether x's dynamic type satisfies Shape. For a
// pointer-receiver method set this is where the distinction becomes
// visible at runtime instead of at compile time.
func AsShape(x any) (Shape, bool) {
	s, ok := x.(Shape)
	return s, ok
}

// Describe switches on the dynamic type: a type switch replaces the
// assertion-plus-cast dance when several types are expected.
func Describe(x any) string {
	switch v := x.(type) {
	case Circle:
		return "circle r=" + ftoa(v.R)
	case *Rect:
		return "rect " + ftoa(v.W) + "x" + ftoa(v.H)
	case Shape:
		return "other shape"
	default:
		return "not a shape"
	}
}

// IsMeasurable reports whether x satisfies the composed (embedded) type:
// a value must provide Area *and* Perimeter.
func IsMeasurable(x any) bool {
	_, ok := x.(Measurable)
	return ok
}

func ftoa(f float64) string {
	// One decimal place, enough for the test assertions.
	i := int(f*10 + 0.5)
	if i%10 == 0 {
		return itoa(i / 10)
	}
	return itoa(i/10) + "." + itoa(i%10)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	n := i
	if n < 0 {
		n = -n
	}
	for n > 0 {
		b = append([]byte{'0' + byte(n%10)}, b...)
		n /= 10
	}
	if i < 0 {
		return "-" + string(b)
	}
	return string(b)
}

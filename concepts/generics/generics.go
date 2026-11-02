// Package generics demonstrates type parameters: generic functions,
// constraints as type sets, generic types, and type inference.
//
// Key ideas:
//   - A **type parameter** `[T any]` makes one function work for every
//     type that satisfies the constraint.
//   - A **constraint** is an interface. `any` accepts everything;
//     `comparable` accepts types usable as map keys; a union of types
//     (`~int | ~float64`) is a *type set* — including the underlying type
//     with `~` so named types (like `type Celsius float64`) qualify.
//   - **Inference** lets you omit type arguments when the compiler can
//     derive them from ordinary arguments.
//   - Generic types (`Stack[T]`) replace `interface{}`-based containers:
//     no type assertion at the use site, no risk of a wrong cast.
//
// Domain: small collection utilities and a stack — the textbook uses,
// where generics remove boilerplate rather than add it.
package generics

import "fmt"

// Map applies f to every element, returning a new slice of type U.
func Map[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// Filter keeps elements matching pred.
func Filter[T any](in []T, pred func(T) bool) []T {
	var out []T
	for _, v := range in {
		if pred(v) {
			out = append(out, v)
		}
	}
	return out
}

// Sum adds up a slice of any numeric type in the type set.
// The `~` means "underlying type int", so named types qualify.
func Sum[T ~int | ~float64](in []T) T {
	var total T
	for _, v := range in {
		total += v
	}
	return total
}

// IndexOf returns the position of v, or -1. Requires comparable so == is
// legal for every T.
func IndexOf[T comparable](in []T, v T) int {
	for i, x := range in {
		if x == v {
			return i
		}
	}
	return -1
}

// CountBy tallies elements by a string key derived from each element.
func CountBy[T any](in []T, key func(T) string) map[string]int {
	m := make(map[string]int)
	for _, v := range in {
		m[key(v)]++
	}
	return m
}

// Stack is a generic container: the element type is fixed at
// instantiation, so misuse is a compile error rather than a runtime
// type assertion.
type Stack[T any] struct {
	items []T
}

// Push appends an element.
func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

// Pop removes and returns the top element.
func (s *Stack[T]) Pop() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

// Len reports the number of elements.
func (s *Stack[T]) Len() int { return len(s.items) }

// Join concatenates string representations with a separator.
func Join[T fmt.Stringer](in []T, sep string) string {
	out := ""
	for i, v := range in {
		if i > 0 {
			out += sep
		}
		out += v.String()
	}
	return out
}

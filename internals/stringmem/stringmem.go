// Package stringmem shows how strings sit in memory: the two-word header,
// what the conversions actually do, and the zero-copy trick that unsafe
// makes possible (read-only).
//
//   - A string is (pointer, length): immutable bytes owned by whoever
//     created them. There is no capacity because nothing can grow it.
//   - `string(b)` and `[]byte(s)` **copy** — that is what makes strings
//     safe to share and truly immutable. You pay for it in allocation.
//   - unsafe.String / unsafe.StringData / unsafe.Slice can *share* the
//     bytes instead: no copy, no allocation, but the contract flips —
//     the data must be treated as immutable even though the underlying
//     bytes live in a []byte. This package uses unsafe only to prove the
//     sharing in tests; production code should prefer the copying
//     conversions unless profiling says otherwise.
//   - Building a string with `+` in a chain allocates once per
//     intermediate result; strings.Builder allocates geometrically and
//     hands back the buffer at the end.
//
// Domain: assembling a log line out of several fields.
package stringmem

import (
	"strings"
	"unsafe"
)

// JoinNaive accumulates fields with `+=` in a loop. Each append copies
// the whole line again, so k fields cost k allocations and quadratic
// copying.
func JoinNaive(fields []string) string {
	s := ""
	for _, f := range fields {
		s += f
	}
	return s
}

// JoinBuilder writes the same fields into a pre-grown strings.Builder:
// one allocation total, linear copying.
func JoinBuilder(fields []string) string {
	size := 0
	for _, f := range fields {
		size += len(f)
	}
	var b strings.Builder
	b.Grow(size)
	for _, f := range fields {
		b.WriteString(f)
	}
	return b.String()
}

// SharedString exposes the zero-copy view: a string header pointing at
// the caller's existing bytes. The result **must be treated as
// immutable** — writing into b afterwards would change a value the rest
// of the program believes frozen (and would be a data race).
func SharedString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// StringBytes exposes the other direction: a read-only slice over the
// string's bytes without copying. Callers must not write to it.
func StringBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// FromBytes is the ordinary, safe conversion: a fresh copy.
func FromBytes(b []byte) string { return string(b) }

// ToBytes is the ordinary, safe conversion in the other direction: also a
// fresh copy, so the caller can mutate freely.
func ToBytes(s string) []byte { return []byte(s) }

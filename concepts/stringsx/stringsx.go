// Package stringsx demonstrates how Go handles text: strings as immutable
// byte sequences, UTF-8 as the encoding, and the conversions between
// string and []byte.
//
// Key facts, each with a test:
//
//   - `len(s)` counts **bytes**; ranging over a string (or
//     utf8.RuneCountInString) counts **runes** (code points).
//   - `range` yields the byte index where each rune starts, so positions
//     map back onto the original bytes.
//   - Invalid UTF-8 bytes decode as utf8.RuneError with width 1 — Go
//     never fails a decode, it substitutes U+FFFD.
//   - `string(b)` **copies**: mutating b afterwards cannot change the
//     string (and vice versa). The copy is what lets strings be immutable
//     and safe to share.
//   - Building a string by `+=` in a loop reallocates every step;
//     strings.Builder (or bytes.Buffer) grows geometrically instead.
//
// Domain: parsing and cleaning log lines.
package stringsx

import (
	"strings"
	"unicode/utf8"
)

// Stats reports the byte length and the rune count of s.
func Stats(s string) (bytes, runes int) {
	return len(s), utf8.RuneCountInString(s)
}

// RunePos pairs each rune with the byte index where it starts.
type RunePos struct {
	Index int
	R     rune
}

// Runes returns every rune in s with its byte offset.
func Runes(s string) []RunePos {
	var out []RunePos
	for i, r := range s {
		out = append(out, RunePos{Index: i, R: r})
	}
	return out
}

// Clean replaces invalid UTF-8 bytes with '?' so downstream processing
// can assume valid text.
func Clean(s string) string {
	return strings.ToValidUTF8(s, "?")
}

// Cut splits s around the first occurrence of sep, exactly like
// strings.Cut: before, sep, after. If sep is absent, after is empty.
func Cut(s, sep string) (before, after string, found bool) {
	return strings.Cut(s, sep)
}

// ConcatRepeat builds a repeated string with plain concatenation: each
// `+=` copies the whole result, so the work is quadratic.
func ConcatRepeat(part string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += part
	}
	return out
}

// BuilderRepeat builds the same string with strings.Builder: growth is
// geometric, so the work is linear (and allocation-free once warmed up).
func BuilderRepeat(part string, n int) string {
	var b strings.Builder
	b.Grow(len(part) * n)
	for i := 0; i < n; i++ {
		b.WriteString(part)
	}
	return b.String()
}

// FromBytes converts bytes to a string, copying the data so the result
// is immutable and independent of the slice.
func FromBytes(b []byte) string { return string(b) }

// ToBytes converts a string to bytes, also copying: the caller may
// mutate the result freely.
func ToBytes(s string) []byte { return []byte(s) }

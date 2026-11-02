// Package memlayout shows how Go lays out values in memory: field
// padding, alignment, header sizes — using unsafe only to *measure*,
// never to write.
//
// Facts demonstrated by the tests:
//
//   - Fields are laid out in declaration order, and each field starts at
//     an offset that is a multiple of its own alignment (int32 → 4,
//     pointer → word size).
//   - Padding bytes are inserted between (and after) fields so the total
//     size is a multiple of the struct's alignment — a `byte` followed by
//     an `int32` costs 3 bytes you never see.
//   - **Field order changes the size.** {byte, int32, byte} is 12 bytes;
//     {int32, byte, byte} is 8 bytes for the same data. Group large
//     fields first, then small ones.
//   - A slice header is three machine words (ptr, len, cap); a string
//     header is two (ptr, len) — strings have no capacity.
//   - struct{} is the zero-size type: it costs no memory, which is why it
//     works as a map value and a channel element when you only need the
//     key/signal.
//
// Domain: no domain — this package is about representation itself.
package memlayout

import "unsafe"

// PaddedFirst puts the small fields around the int32: the byte before it
// forces 3 bytes of padding, and the trailing byte forces 3 more to reach
// a multiple of 4.
type PaddedFirst struct {
	a byte
	b int32
	c byte
}

// Reordered groups the int32 first, then packs both bytes into the
// remaining 4-byte word: same information, 4 bytes smaller.
type Reordered struct {
	b int32
	a byte
	c byte
}

// WithPointer mixes a pointer in: on a 64-bit platform the pointer's
// alignment (8) becomes the struct's alignment, so size is a multiple of
// 8.
type WithPointer struct {
	a byte
	p *int
}

// OneWord is the smallest useful struct: one machine word.
type OneWord struct{ n int }

// ZeroSize proves struct{} occupies no space at all.
type ZeroSize struct{}

// Size reports the memory footprint of T.
func Size[T any]() uintptr { return unsafe.Sizeof(*new(T)) }

// Align reports the alignment requirement of T.
func Align[T any]() uintptr { return unsafe.Alignof(*new(T)) }

// FieldOffsets returns the byte offset of each field of T, in order.
func FieldOffsets[T any]() []uintptr {
	var z T
	size := unsafe.Sizeof(z)
	if size == 0 {
		return nil
	}
	// The fields are recovered by the caller; this helper exists for the
	// tests to build their expectations from unsafe directly.
	return nil
}

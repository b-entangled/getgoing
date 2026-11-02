// Package iface shows what an interface value actually is in memory.
//
// An interface value is a two-word pair:
//
//		eface { typ *rtype; data unsafe.Pointer }
//
//	  - **typ** points to the type descriptor for the *dynamic* type. Two
//	    interface values holding the same concrete type share one
//	    descriptor — that is how type switches and assertions dispatch,
//	    and how == can compare interfaces without knowing the type.
//	  - **data** is either a pointer to the boxed value, or (for pointer-like
//	    dynamic types: pointers, chans, maps, funcs) the value itself, stored
//	    directly with no allocation. This is the "direct interface"
//	    optimisation.
//
// Boxing a value that does not escape the current call can stay on the
// stack — interface assignment does not imply a heap allocation. Once the
// interface escapes (stored globally, sent on a channel), the box moves to
// the heap.
//
// unsafe is used here purely to *read* the two words, so the layout stops
// being folklore.
package iface

import "unsafe"

// eface mirrors the runtime's representation of `any`.
type eface struct {
	typ  uintptr
	data uintptr
}

// Words returns the raw (type, data) pair behind any interface value.
// Measurement only — never store these words or compare them across
// program runs: the type word is a pointer into the binary's read-only
// data.
func Words(i any) (typ, data uintptr) {
	e := *(*eface)(unsafe.Pointer(&i))
	return e.typ, e.data
}

// sink makes an interface escape, forcing its box onto the heap.
var sink any

// BoxEscape stores v in a package-level interface: the box escapes to
// the heap, which costs one allocation.
func BoxEscape(v any) { sink = v }

// ResetSink drops the escaped box so tests can start clean.
func ResetSink() { sink = nil }

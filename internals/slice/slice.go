// Package slice shows how slices are represented and grown:
//
//   - A slice is a three-word header: (pointer to the backing array,
//     length, capacity). Slicing creates a new *header* over the same
//     array — no data moves.
//   - Because headers share the array, `a[i:j]` aliases `a`. The third
//     index (`a[i:j:k]`) pins capacity so later appends cannot write
//     through the view.
//   - `append` reuses spare capacity in place (see the slicecap mistake)
//     and allocates geometrically when it must grow: capacity roughly
//     doubles for small slices, then eases toward 1.25×, so n appends
//     cost O(n) copies, not O(n²).
//   - A nil slice and an empty slice are different values (nil == nil,
//     empty != nil) but behave the same for len, range and append.
//
// Domain: a growing numeric buffer.
package slice

// Grow appends values one at a time and records every capacity change,
// so the growth policy is observable.
func Grow(values ...int) (out []int, growths []int) {
	out = make([]int, 0, 1)
	prev := cap(out)
	for _, v := range values {
		out = append(out, v)
		if cap(out) != prev {
			growths = append(growths, cap(out))
			prev = cap(out)
		}
	}
	return out, growths
}

// View returns a sub-slice: a new header over the same backing array.
func View(values []int, from, to int) []int {
	return values[from:to]
}

// PinnedView returns a sub-slice with capacity pinned to its length, so
// an append through it must allocate rather than write into the shared
// array.
func PinnedView(values []int, from, to int) []int {
	return values[from:to:to]
}

// CopyInto returns a fully independent copy: fresh header, fresh array.
func CopyInto(values []int) []int {
	out := make([]int, len(values))
	copy(out, values)
	return out
}

// Sum adds every element — range over a nil slice is simply zero
// iterations.
func Sum(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}

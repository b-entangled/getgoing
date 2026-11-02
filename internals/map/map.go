// Package map_ documents the Go map: an unordered hash table with these
// observable properties:
//
//   - A nil map is readable but not writable. Reading (indexing, len,
//     range) yields zero values; writing panics. This makes passing a nil
//     map as a read-only view safe by construction.
//   - Iteration order is deliberately randomised: every range starts at a
//     different position (and randomises probing), so code cannot become
//     dependent on an order the implementation never promised. Programs
//     that need an order must sort the keys.
//   - range visits every present key exactly once, even across growth
//     from a few entries to thousands.
//   - An entry removed before it is reached during iteration is never
//     produced (guaranteed by the language spec, not an accident).
//
// The zero value is a ready-to-read empty map; use make (or a literal)
// before the first write.
package map_

// New returns an empty map with capacity hint n.
func New(n int) map[string]int {
	return make(map[string]int, n)
}

// Fill inserts n distinct keys ("k0".."k{n-1}") with value i.
func Fill(m map[string]int, n int) {
	for i := 0; i < n; i++ {
		m["k"+itoa(i)] = i
	}
}

// KeysSorted returns the keys in lexical order — the only sanctioned way
// to turn map iteration into a deterministic sequence.
func KeysSorted(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// sortStrings is insertion sort — fine for the small key sets in tests.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

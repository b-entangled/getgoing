package stringsx

import (
	"reflect"
	"testing"
)

// len counts bytes; range counts runes. "héllo" is 6 bytes, 5 runes.
func TestByteLengthDiffersFromRuneCount(t *testing.T) {
	b, r := Stats("héllo")
	if b != 6 || r != 5 {
		t.Fatalf("bytes=%d runes=%d, want 6 and 5", b, r)
	}
	if b == r {
		t.Fatal("test premise: multi-byte rune must differ")
	}
}

// range yields the byte offset of each rune, which maps runes back onto
// the byte string.
func TestRangeYieldsByteOffsets(t *testing.T) {
	got := Runes("héllo")
	want := []RunePos{{0, 'h'}, {1, 'é'}, {3, 'l'}, {4, 'l'}, {5, 'o'}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Decoding never fails: invalid bytes become U+FFFD (RuneError).
func TestInvalidUTF8BecomesRuneError(t *testing.T) {
	s := string([]byte{'a', 0xff, 'b'})
	got := Runes(s)
	want := []RunePos{{0, 'a'}, {1, '�'}, {2, 'b'}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if out := Clean(s); out != "a?b" {
		t.Fatalf("Clean = %q, want %q", out, "a?b")
	}
}

// string([]byte) copies: later mutation of the slice is invisible.
func TestConversionFromBytesCopies(t *testing.T) {
	b := []byte("hello")
	s := FromBytes(b)
	b[0] = 'J'
	if s != "hello" {
		t.Fatalf("string changed to %q — conversion must copy", s)
	}
	// And the other direction: strings are immutable, so []byte must be a
	// fresh buffer.
	b2 := ToBytes("hello")
	b2[0] = 'J'
	if ToBytes("hello")[0] != 'h' {
		t.Fatal("mutating one []byte affected the source")
	}
}

// Concatenation in a loop reallocates every iteration; the builder does
// not. Same output, far fewer allocations.
func TestBuilderAvoidsRepeatedAllocation(t *testing.T) {
	const part, n = "log-line-", 200
	if ConcatRepeat(part, n) != BuilderRepeat(part, n) {
		t.Fatal("both builders must produce identical output")
	}
	concatAllocs := testing.AllocsPerRun(10, func() {
		_ = ConcatRepeat(part, n)
	})
	builderAllocs := testing.AllocsPerRun(10, func() {
		_ = BuilderRepeat(part, n)
	})
	if concatAllocs <= builderAllocs {
		t.Fatalf("concat allocs (%v) should exceed builder allocs (%v)", concatAllocs, builderAllocs)
	}
	if builderAllocs > 1 {
		t.Fatalf("builder should allocate at most once, got %v", builderAllocs)
	}
}

func TestCutSplitsOnFirstSeparatorOnly(t *testing.T) {
	before, after, found := Cut("level=info:msg=hello world", ":")
	if before != "level=info" || !found || after != "msg=hello world" {
		t.Fatalf("got (%q, %q, %v)", before, after, found)
	}
	before, after, found = Cut("no-separator", ":")
	if before != "no-separator" || found || after != "" {
		t.Fatalf("absent separator: got (%q, %q, %v)", before, after, found)
	}
}

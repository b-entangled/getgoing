package testingx

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// slugCase is one row of the table: input, want, and a name for t.Run.
type slugCase struct {
	name string
	in   string
	want string
}

var slugCases = []slugCase{
	{"simple", "Hello World", "hello-world"},
	{"already-slugged", "hello-world", "hello-world"},
	{"punctuation-collapsed", "Say,  what?! ", "say-what"},
	{"leading-and-trailing-junk", "---Go Rules---", "go-rules"},
	{"empty-becomes-untitled", "!!!", "untitled"},
	{"digits-kept", "HTTP 200 OK", "http-200-ok"},
	{"unicode-letters-kept", "Élan Vital", "élan-vital"},
}

func TestSlug(t *testing.T) {
	for _, tc := range slugCases {
		t.Run(tc.name, func(t *testing.T) { // named subtests, one per row
			if got := Slug(tc.in); got != tc.want {
				t.Fatalf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// t.Helper marks assertion helpers so a failure points at the *caller's*
// line. The helper below reports its caller — proving the frame exists
// — and the assertion itself is written the way you would use it.
func mustSlug(t *testing.T, in, want string) {
	t.Helper()
	if got := Slug(in); got != want {
		t.Fatalf("Slug(%q) = %q, want %q", in, got, want)
	}
}

func TestHelperAssertionReportsCallerContext(t *testing.T) {
	// If this fails, the message names in/want — and with t.Helper the
	// reported line is the line of this call, not of mustSlug.
	mustSlug(t, "A B", "a-b")
}

// t.Setenv restores the previous value when the *subtest* ends, so
// environment changes never leak between tests.
func TestSetenvIsScopedToTheSubtest(t *testing.T) {
	const key = "TESTINGX_MODE"
	t.Setenv(key, "outer")

	t.Run("inner", func(t *testing.T) {
		t.Setenv(key, "inner")
		if os.Getenv(key) != "inner" {
			t.Fatalf("inner subtest sees %q", os.Getenv(key))
		}
	})

	// The subtest's cleanup already ran: the outer value is back.
	if got := os.Getenv(key); got != "outer" {
		t.Fatalf("after subtest: %q, want %q", got, "outer")
	}
}

// t.Cleanup runs in LIFO order: the last registration runs first.
func TestCleanupRunsInLIFOOrder(t *testing.T) {
	var order []string
	t.Run("scoped", func(t *testing.T) {
		t.Cleanup(func() { order = append(order, "first-registered") })
		t.Cleanup(func() { order = append(order, "second-registered") })
	})
	want := []string{"second-registered", "first-registered"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("cleanup order = %v, want %v", order, want)
	}
}

// t.TempDir returns a directory that exists for this test and is removed
// afterwards — no defer os.RemoveAll.
func TestTempDirIsUsableAndIsolated(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	if dirA == dirB {
		t.Fatal("each TempDir must be unique")
	}
	if !strings.HasPrefix(dirA, os.TempDir()) {
		t.Fatalf("%q not under the system temp dir", dirA)
	}
	if err := os.WriteFile(filepath.Join(dirA, "x.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// FuzzXxx: every seed runs on normal `go test`; `go test -fuzz=FuzzSlug`
// also mutates them. The seed corpus pins the invariants we care about:
// output is lower-case, has no leading/trailing dash, and never panics.
func FuzzSlug(f *testing.F) {
	for _, tc := range slugCases {
		f.Add(tc.in)
	}
	f.Add("")     // the degenerate case
	f.Add("   ")  // whitespace only
	f.Add("\x00") // NUL byte
	f.Fuzz(func(t *testing.T, in string) {
		got := Slug(in)
		if got == "" {
			t.Fatalf("Slug(%q) returned the empty string", in)
		}
		if got != strings.ToLower(got) {
			t.Fatalf("Slug(%q) = %q is not lower-case", in, got)
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Fatalf("Slug(%q) = %q has an edge dash", in, got)
		}
		if strings.Contains(got, "--") {
			t.Fatalf("Slug(%q) = %q has a double dash", in, got)
		}
	})
}

// Example functions are run by the test binary and checked against their
// Output comment.
func ExampleSlug() {
	fmt.Println(Slug("Hello, World!"))
	// Output:
	// hello-world
}

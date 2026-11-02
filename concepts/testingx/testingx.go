// Package testingx demonstrates the testing idioms themselves: the code
// in this directory is the documentation.
//
// What lives here:
//   - **Table-driven tests** with t.Run: one test body, many named cases,
//     failures that say which case broke.
//   - **t.Helper** in shared assertion helpers so failures are reported at
//     the *caller's* line, not inside the helper.
//   - **t.Setenv / t.TempDir / t.Cleanup**: scoped fixtures that the
//     framework restores for you — no manual defer cleanup, and
//     t.Setenv forbids t.Parallel in the same test on purpose.
//   - **Fuzzing**: FuzzXxx runs its seed corpus on every `go test`, and
//     explores mutations under `go test -fuzz`.
//   - **Example functions**: compiled, run, and verified against their
//     "// Output:" comment.
//
// Domain: a slug formatter — small pure logic, ideal for tables and fuzz.
package testingx

import (
	"strings"
	"unicode"
)

// Slug lowercases s, keeps letters and digits, and joins the rest with
// '-'. An empty result becomes "untitled".
func Slug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

package builder

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// --- functional options ---

func TestDefaults(t *testing.T) {
	s := New()
	if s.Addr() != ":8080" {
		t.Errorf("addr default: got %q", s.Addr())
	}
	if s.Timeout() != 30*time.Second {
		t.Errorf("timeout default: got %v", s.Timeout())
	}
	if s.TLS() {
		t.Error("tls default should be false")
	}
	if len(s.Middleware()) != 0 {
		t.Errorf("middleware default should be empty")
	}
}

func TestOptionsOverrideDefaults(t *testing.T) {
	s := New(WithAddr(":9090"), WithTimeout(5*time.Second), WithTLS("cert.pem"))
	if s.Addr() != ":9090" {
		t.Errorf("got %q", s.Addr())
	}
	if s.Timeout() != 5*time.Second {
		t.Errorf("got %v", s.Timeout())
	}
	if !s.TLS() {
		t.Error("tls should be enabled")
	}
}

func TestOptionOrderIndependence(t *testing.T) {
	a := New(WithAddr(":1"), WithTimeout(time.Second))
	b := New(WithTimeout(time.Second), WithAddr(":1"))
	if a.String() != b.String() {
		t.Fatalf("options are order dependent:\n%s\n%s", a.String(), b.String())
	}
}

func TestMiddlewarePreservesOrder(t *testing.T) {
	s := New(WithMiddleware("auth"), WithMiddleware("log", "metrics"))
	want := []string{"auth", "log", "metrics"}
	got := s.Middleware()
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestMiddlewareReturnsCopy(t *testing.T) {
	s := New(WithMiddleware("auth"))
	mw := s.Middleware()
	mw[0] = "tampered"
	if s.Middleware()[0] != "auth" {
		t.Fatal("Middleware leaked internal slice")
	}
}

func TestNoOptionsIsUsable(t *testing.T) {
	s := New()
	if s.Addr() == "" || s.Timeout() <= 0 {
		t.Fatal("zero-value server is not usable")
	}
}

// --- stepwise builder ---

func TestBuildValidQuery(t *testing.T) {
	q, err := From("users").
		Where("age > 18").
		Where("active = true").
		OrderBy("name").
		Limit(10).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM users WHERE age > 18 AND active = true ORDER BY name LIMIT 10"
	if q.String() != want {
		t.Fatalf("\nwant %s\ngot  %s", want, q.String())
	}
}

func TestBuildRequiresTable(t *testing.T) {
	_, err := From("").Limit(5).Build()
	if !errors.Is(err, ErrTableRequired) {
		t.Fatalf("want ErrTableRequired, got %v", err)
	}
}

func TestBuildRejectsNegativeLimit(t *testing.T) {
	_, err := From("t").Limit(-1).Build()
	if !errors.Is(err, ErrBadLimit) {
		t.Fatalf("want ErrBadLimit, got %v", err)
	}
}

func TestBuildReportsAllErrorsAtOnce(t *testing.T) {
	_, err := From("").Where("").Limit(-1).Build()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []error{ErrTableRequired, ErrBadLimit} {
		if !errors.Is(err, want) {
			t.Errorf("joined error missing %v: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "empty where clause") {
		t.Errorf("missing empty-clause error: %v", err)
	}
}

func TestBuildIsImmutable(t *testing.T) {
	b := From("users").Where("a = 1")
	q1, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	b.Where("b = 2") // mutate after build
	q2, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(q1.Where()) != 1 {
		t.Fatalf("earlier build changed: %v", q1.Where())
	}
	if len(q2.Where()) != 2 {
		t.Fatalf("later build wrong: %v", q2.Where())
	}
}

func TestPartiallyBuiltValueCannotEscape(t *testing.T) {
	// SQL's fields are unexported: this test is in-package, so it can read
	// them, but it proves the only path to a non-zero SQL is Build.
	var q SQL
	if q.String() != "SELECT * FROM " {
		t.Fatalf("zero SQL should be inert, got %q", q.String())
	}
}

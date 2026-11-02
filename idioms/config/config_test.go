package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func valid() Settings {
	return Settings{
		Name:     "svc",
		Endpoint: "api.test:443",
		Timeout:  Duration(5 * time.Second),
		Retries:  3,
		TLS:      true,
	}
}

func TestNewValidates(t *testing.T) {
	c, err := New(valid())
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "svc" || c.Endpoint() != "api.test:443" {
		t.Fatalf("%s", c)
	}
	if c.Timeout() != 5*time.Second || c.Retries() != 3 || !c.TLS() {
		t.Fatalf("%s", c)
	}
}

// A struct lets the caller see every problem at once — the headline
// advantage over option-by-option construction.
func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	_, err := New(Settings{Timeout: Duration(-1), Retries: -5})
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"name", "endpoint", "timeout", "retries"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in: %v", want, err)
		}
	}
	if !errors.Is(err, ErrMissingField) || !errors.Is(err, ErrOutOfRange) {
		t.Errorf("both sentinel errors should be reachable: %v", err)
	}
}

func TestMustPanicsOnInvalidInput(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	Must(Settings{})
}

func TestMustReturnsConfigOnValidInput(t *testing.T) {
	c := Must(valid())
	if c.Name() != "svc" {
		t.Fatalf("%s", c)
	}
}

// Config takes a copy: later mutation of the caller's Settings is invisible.
func TestConfigDoesNotAliasCallerSettings(t *testing.T) {
	s := valid()
	s.Headers = map[string]string{"A": "1"}

	c, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Name = "mutated"
	s.Headers["A"] = "tampered"
	s.Headers["B"] = "added"

	if c.Name() != "svc" {
		t.Fatalf("name changed: %q", c.Name())
	}
	h := c.Headers()
	if h["A"] != "1" || len(h) != 1 {
		t.Fatalf("headers changed: %v", h)
	}
}

func TestSettingsReturnsCopy(t *testing.T) {
	c := Must(valid())
	s := c.Settings()
	s.Name = "other"
	s.Headers = map[string]string{"X": "y"}
	if c.Name() != "svc" {
		t.Fatal("Settings leaked internal state")
	}
	if c.Headers() != nil {
		t.Fatal("Headers should be nil when unset")
	}
}

func TestAddressUsesTLSFlag(t *testing.T) {
	plain := Must(valid())
	plain.s.TLS = false // in-package: proves Address reads the flag
	if plain.Address() != "http://api.test:443" {
		t.Fatalf("got %s", plain.Address())
	}
	secure := Must(valid())
	if secure.Address() != "https://api.test:443" {
		t.Fatalf("got %s", secure.Address())
	}
}

// --- JSON round-trip: the reason the struct must be exported ---

func TestFromJSONUsesDefaultsThenOverrides(t *testing.T) {
	c, err := FromJSON([]byte(`{"name":"svc","endpoint":"a:1","timeout":"150ms"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout() != 150*time.Millisecond {
		t.Fatalf("timeout=%s", c.Timeout())
	}
	if c.Retries() != 2 {
		t.Fatalf("default retries should survive: %d", c.Retries())
	}
	if c.TLS() {
		t.Fatal("default tls should survive")
	}
}

func TestFromJSONRejectsBadDuration(t *testing.T) {
	if _, err := FromJSON([]byte(`{"name":"s","endpoint":"a:1","timeout":"soon"}`)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFromJSONRejectsInvalidSettings(t *testing.T) {
	// Missing name → validation error, not a decode error.
	if _, err := FromJSON([]byte(`{"endpoint":"a:1"}`)); !errors.Is(err, ErrMissingField) {
		t.Fatalf("got %v", err)
	}
}

func TestFromJSONRejectsMalformedDocument(t *testing.T) {
	if _, err := FromJSON([]byte(`{not json`)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDurationAcceptsNanoseconds(t *testing.T) {
	c, err := FromJSON([]byte(`{"name":"s","endpoint":"a:1","timeout":1000000000}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout() != time.Second {
		t.Fatalf("timeout=%s", c.Timeout())
	}
}

func TestDurationMarshalIsReadable(t *testing.T) {
	b, err := json.Marshal(Duration(90 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"1m30s"` {
		t.Fatalf("got %s", b)
	}
	if Duration(90*time.Second).String() != "1m30s" {
		t.Fatalf("got %s", Duration(90*time.Second))
	}
}

func TestSettingsRoundTripThroughJSON(t *testing.T) {
	want := valid()
	want.Headers = map[string]string{"X-Trace": "on"}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if diff := Diff(want, got.Settings()); len(diff) != 0 {
		t.Fatalf("round trip changed the value: %v", diff)
	}
}

// --- patching: layered configuration ---

func TestPatchOverlaysOnlyPresentFields(t *testing.T) {
	base := valid()
	base.Headers = map[string]string{"A": "1"}

	patched, err := Patch(base, []byte(`{"timeout":"1s","logging":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if patched.Timeout != Duration(time.Second) {
		t.Fatalf("timeout=%s", patched.Timeout)
	}
	if !patched.Logging {
		t.Fatal("logging not applied")
	}
	// Untouched fields survive.
	if patched.Name != "svc" || patched.Endpoint != "api.test:443" || !patched.TLS {
		t.Fatalf("patch dropped fields: %+v", patched)
	}
	if patched.Retries != 3 {
		t.Fatalf("retries=%d", patched.Retries)
	}
}

func TestPatchRejectsMalformedOverlay(t *testing.T) {
	if _, err := Patch(valid(), []byte(`oops`)); err == nil {
		t.Fatal("expected an error")
	}
}

// --- diffing: a struct can be compared, options cannot ---

func TestDiffListsChangedFields(t *testing.T) {
	a := valid()
	b := valid()
	b.Name = "other"
	b.Retries = 0
	b.Headers = map[string]string{"A": "2"}

	got := Diff(a, b)
	joined := strings.Join(got, "; ")
	for _, want := range []string{"name:", "retries:", "headers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in: %s", want, joined)
		}
	}
	if strings.Contains(joined, "endpoint") {
		t.Errorf("unchanged field reported: %s", joined)
	}
}

func TestDiffOfIdenticalSettingsIsEmpty(t *testing.T) {
	s := valid()
	s.Headers = map[string]string{"A": "1"}
	if got := Diff(s, s); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestDiffDetectsHeaderCountChange(t *testing.T) {
	a := valid()
	b := valid()
	b.Headers = map[string]string{"A": "1"}
	if got := Diff(a, b); len(got) != 1 || !strings.Contains(got[0], "headers") {
		t.Fatalf("got %v", got)
	}
}

// A nil header map is distinct from an empty one only in len, which Diff
// treats the same — an intentional simplification.
func TestNilAndEmptyHeadersDiffCleanly(t *testing.T) {
	a := valid()
	b := valid()
	b.Headers = map[string]string{}
	if got := Diff(a, b); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestStringIsReadable(t *testing.T) {
	c := Must(valid())
	s := c.String()
	for _, want := range []string{"name:svc", "endpoint:api.test:443", "timeout:5s", "tls:true"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

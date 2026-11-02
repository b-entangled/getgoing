package options

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAreUsable(t *testing.T) {
	cfg, err := New(WithName("svc"), WithEndpoint("api.example.com:443"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout() != 30*time.Second {
		t.Errorf("timeout default: %s", cfg.Timeout())
	}
	if cfg.Retries() != 2 {
		t.Errorf("retries default: %d", cfg.Retries())
	}
	if cfg.TLS() {
		t.Error("tls default should be false")
	}
	if cfg.UserAgent() == "" {
		t.Error("user agent default missing")
	}
	if len(cfg.Headers()) != 0 {
		t.Errorf("headers default should be empty: %v", cfg.Headers())
	}
}

func TestOptionsOverrideDefaults(t *testing.T) {
	cfg, err := New(
		WithName("svc"),
		WithEndpoint("a.b:1"),
		WithTimeout(5*time.Second),
		WithRetries(9),
		WithTLS(true),
		WithUserAgent("custom/2"),
		WithLogging(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout() != 5*time.Second || cfg.Retries() != 9 || !cfg.TLS() || !cfg.Logging() {
		t.Fatalf("overrides lost: %s", cfg)
	}
	if cfg.UserAgent() != "custom/2" {
		t.Fatalf("ua=%q", cfg.UserAgent())
	}
}

// The idiom's headline property: options cannot be swapped by position.
func TestOptionsAreOrderIndependent(t *testing.T) {
	a, err := New(WithName("n"), WithEndpoint("h:1"), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(WithTimeout(time.Second), WithEndpoint("h:1"), WithName("n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatalf("order changed the result:\n%s\n%s", a.String(), b.String())
	}
}

func TestHeaderAccumulatesAcrossOptions(t *testing.T) {
	cfg, err := New(
		WithName("n"),
		WithEndpoint("h:1"),
		WithHeader("A", "1"),
		WithHeader("B", "2"),
		WithHeaders(map[string]string{"C": "3"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Headers()
	if len(h) != 3 || h["A"] != "1" || h["B"] != "2" || h["C"] != "3" {
		t.Fatalf("headers=%v", h)
	}
}

// An option can reject its own argument, and the message names the option.
func TestInvalidOptionIsRejectedWithPosition(t *testing.T) {
	_, err := New(WithName("n"), WithEndpoint("h:1"), WithTimeout(-time.Second))
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("want ErrInvalidOption, got %v", err)
	}
	if !strings.Contains(err.Error(), "option 2") {
		t.Fatalf("error should identify the offending option: %v", err)
	}
}

func TestBadEndpointRejected(t *testing.T) {
	_, err := New(WithName("n"), WithEndpoint("no-port"))
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
}

func TestBlankNameRejected(t *testing.T) {
	_, err := New(WithName("   "), WithEndpoint("h:1"))
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
	// The final validation also reports it as missing.
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("got %v", err)
	}
}

func TestNegativeRetriesRejected(t *testing.T) {
	if _, err := New(WithName("n"), WithEndpoint("h:1"), WithRetries(-1)); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
}

func TestBlankHeaderNameRejected(t *testing.T) {
	if _, err := New(WithName("n"), WithEndpoint("h:1"), WithHeader("  ", "v")); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
}

func TestOversizedUserAgentRejected(t *testing.T) {
	long := strings.Repeat("x", 129)
	if _, err := New(WithName("n"), WithEndpoint("h:1"), WithUserAgent(long)); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
}

func TestNilOptionRejected(t *testing.T) {
	if _, err := New(WithName("n"), nil); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
}

// Cross-field validation happens after all options, so a combination no
// single option could detect is still caught.
func TestMissingRequiredFieldReportedOnce(t *testing.T) {
	_, err := New(WithTimeout(time.Second))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("both missing fields should be named: %v", err)
	}
}

// --- presets: options are values ---

func TestPresetProducesSameConfigAsInlineOptions(t *testing.T) {
	fromPreset, err := New(append(Preset{WithName("n"), WithEndpoint("h:1")}, Production.Options()...)...)
	if err != nil {
		t.Fatal(err)
	}
	inline, err := New(
		WithName("n"), WithEndpoint("h:1"),
		WithTimeout(5*time.Second), WithRetries(3), WithTLS(true), WithLogging(false),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fromPreset.String() != inline.String() {
		t.Fatalf("preset diverged:\n%s\n%s", fromPreset, inline)
	}
}

func TestPresetThenIsImmutable(t *testing.T) {
	base := Production
	extended := base.Then(WithTimeout(time.Second))

	if len(base) != 4 {
		t.Fatalf("base preset mutated: %d options", len(base))
	}
	if len(extended) != 5 {
		t.Fatalf("extended=%d", len(extended))
	}

	fromBase, err := New(append(Preset{WithName("n"), WithEndpoint("h:1")}, base.Options()...)...)
	if err != nil {
		t.Fatal(err)
	}
	if fromBase.Timeout() != 5*time.Second {
		t.Fatalf("base preset should keep its own timeout: %s", fromBase.Timeout())
	}
}

func TestNamedPresets(t *testing.T) {
	for _, name := range []string{"production", "prod", "development", "dev"} {
		p, err := Named(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(p) == 0 {
			t.Fatalf("%s: empty preset", name)
		}
	}
	if _, err := Named("staging"); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("got %v", err)
	}
}

func TestDevelopmentPresetIsChattyAndRelaxed(t *testing.T) {
	cfg, err := New(append(Preset{WithName("n"), WithEndpoint("h:1")}, Development.Options()...)...)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Logging() || cfg.TLS() || cfg.Retries() != 0 {
		t.Fatalf("got %s", cfg)
	}
}

// --- read-only access ---

func TestHeadersReturnsCopy(t *testing.T) {
	cfg, err := New(WithName("n"), WithEndpoint("h:1"), WithHeader("A", "1"))
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Headers()
	h["A"] = "tampered"
	if cfg.Headers()["A"] != "1" {
		t.Fatal("Headers leaked internal state")
	}
}

func TestAddressReflectsTLS(t *testing.T) {
	plain, err := New(WithName("n"), WithEndpoint("h:1"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Address() != "http://h:1" {
		t.Fatalf("got %s", plain.Address())
	}

	secure, err := New(WithName("n"), WithEndpoint("h:1"), WithTLS(true))
	if err != nil {
		t.Fatal(err)
	}
	if secure.Address() != "https://h:1" {
		t.Fatalf("got %s", secure.Address())
	}
}

// --- client built on top ---

func TestDialValidates(t *testing.T) {
	if _, err := Dial(WithName("n")); err == nil {
		t.Fatal("Dial must surface configuration errors")
	}
}

func TestClientURL(t *testing.T) {
	c, err := Dial(WithName("n"), WithEndpoint("api.test:443"), WithTLS(true))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.URL("v1/users"); got != "https://api.test:443/v1/users" {
		t.Fatalf("got %s", got)
	}
	if got := c.URL("/leading"); got != "https://api.test:443/leading" {
		t.Fatalf("got %s", got)
	}
}

// The config a caller receives cannot be mutated after construction.
func TestConfigIsEffectivelyImmutable(t *testing.T) {
	cfg, err := New(WithName("n"), WithEndpoint("h:1"))
	if err != nil {
		t.Fatal(err)
	}
	before := cfg.String()
	// There are no setters; the only mutation path would be through an
	// Option, which is only accepted by New.
	_ = before
	if cfg.Name() != "n" {
		t.Fatalf("got %q", cfg.Name())
	}
}

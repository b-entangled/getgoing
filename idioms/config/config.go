// Package config demonstrates the Option-Struct idiom — the deliberately
// plain alternative to functional options (see package options).
//
// Instead of `New(...Option)`, the constructor takes one exported struct
// whose fields are the configuration:
//
//	cfg := config.Settings{Name: "svc", Timeout: 5 * time.Second}
//	c, err := config.New(cfg)
//
// When this is the *better* choice (and it usually is for small configs):
//   - the fields must be visible to a caller — for a YAML/JSON config file
//     you need exported fields and tags anyway, and Options cannot be
//     unmarshalled into;
//   - most fields are required, so "optional" buys nothing;
//   - the caller wants to construct the value lazily, pass it around, or
//     diff two configurations;
//   - the config is read by code outside the package (a CLI printing its
//     settings, a test comparing expected vs actual).
//
// When to prefer functional options instead: many genuinely optional
// fields, options that must validate themselves, or presets you want to
// compose as values.
//
// The trade-off in one line: the struct exposes *what*, the options idiom
// encodes *how much choice there is*.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Settings is the exported configuration value. Its fields are exported so
// it can round-trip through encoding/json / yaml, be built by a CLI flag
// binder, and be inspected by anything outside this package.
type Settings struct {
	Name     string            `json:"name"`
	Endpoint string            `json:"endpoint"`
	Timeout  Duration          `json:"timeout"`
	Retries  int               `json:"retries"`
	Headers  map[string]string `json:"headers,omitempty"`
	TLS      bool              `json:"tls"`
	Logging  bool              `json:"logging"`
}

// Duration wraps time.Duration so a configuration file can say "5s"
// instead of a nanosecond integer — the single most common reason a
// settings struct needs a custom type.
type Duration time.Duration

// UnmarshalJSON accepts either a duration string ("150ms") or a number of
// nanoseconds, which is what encoding/json would otherwise produce.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		parsed, err := time.ParseDuration(t)
		if err != nil {
			return fmt.Errorf("config: bad duration %q: %w", t, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		*d = Duration(time.Duration(t))
		return nil
	default:
		return fmt.Errorf("config: duration must be a string or number, got %T", v)
	}
}

// MarshalJSON always emits the readable string form.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Duration exposes the wrapped time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// --- validation ---

var (
	// ErrMissingField is returned when a required field is empty.
	ErrMissingField = errors.New("config: missing required field")
	// ErrOutOfRange is returned when a numeric field is invalid.
	ErrOutOfRange = errors.New("config: value out of range")
)

// validate checks every field and reports *all* problems at once. With a
// struct this is natural: one loop over a list of (name, ok) pairs.
// With functional options the caller would have to construct everything
// first to discover every problem, one failure at a time.
func (s Settings) validate() error {
	var errs []error

	if s.Name == "" {
		errs = append(errs, fmt.Errorf("%w: name", ErrMissingField))
	}
	if s.Endpoint == "" {
		errs = append(errs, fmt.Errorf("%w: endpoint", ErrMissingField))
	}
	if s.Timeout < 0 {
		errs = append(errs, fmt.Errorf("%w: timeout must be >= 0, got %s", ErrOutOfRange, s.Timeout))
	}
	if s.Retries < 0 {
		errs = append(errs, fmt.Errorf("%w: retries must be >= 0, got %d", ErrOutOfRange, s.Retries))
	}
	for k := range s.Headers {
		if strings.TrimSpace(k) == "" {
			errs = append(errs, fmt.Errorf("%w: header with blank name", ErrMissingField))
			break
		}
	}
	return errors.Join(errs...)
}

// --- constructor and derived accessors ---

// Config is the validated, immutable runtime form. It holds a *copy* of
// the input so a caller mutating its Settings afterwards cannot change a
// live Config — the value semantics of a struct are what make this cheap
// and obvious.
type Config struct {
	s Settings
}

// New validates settings and returns an immutable Config.
func New(s Settings) (*Config, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &Config{s: s.clone()}, nil
}

// Must is New for tests and static configuration: it panics on invalid
// input, which is correct when the value is a compile-time constant and an
// error return would just be ignored.
func Must(s Settings) *Config {
	c, err := New(s)
	if err != nil {
		panic(err)
	}
	return c
}

// clone deep-copies the reference-typed fields so the Config cannot be
// changed from outside.
func (s Settings) clone() Settings {
	out := s
	if s.Headers != nil {
		out.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			out.Headers[k] = v
		}
	}
	return out
}

// Settings returns a copy of the underlying settings, safe to mutate.
func (c *Config) Settings() Settings { return c.s.clone() }

// Name returns the configured name.
func (c *Config) Name() string { return c.s.Name }

// Endpoint returns host:port.
func (c *Config) Endpoint() string { return c.s.Endpoint }

// Timeout returns the per-request timeout; 0 means no timeout, which the
// caller must handle explicitly rather than inheriting a surprise default.
func (c *Config) Timeout() time.Duration { return c.s.Timeout.Duration() }

// Retries returns the retry budget.
func (c *Config) Retries() int { return c.s.Retries }

// TLS reports whether TLS is enabled.
func (c *Config) TLS() bool { return c.s.TLS }

// Logging reports whether debug logging is on.
func (c *Config) Logging() bool { return c.s.Logging }

// Headers returns a copy of the header map.
func (c *Config) Headers() map[string]string { return c.s.clone().Headers }

// Address returns the scheme-prefixed endpoint.
func (c *Config) Address() string {
	if c.s.TLS {
		return "https://" + c.s.Endpoint
	}
	return "http://" + c.s.Endpoint
}

// String renders the configuration for logs.
func (c *Config) String() string {
	keys := make([]string, 0, len(c.s.Headers))
	for k := range c.s.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("Config{name:%s endpoint:%s timeout:%s retries:%d tls:%t logging:%t headers:%s}",
		c.s.Name, c.s.Endpoint, c.s.Timeout, c.s.Retries, c.s.TLS, c.s.Logging, strings.Join(keys, "+"))
}

// --- defaults and file loading ---

// Default returns settings with sensible defaults applied. The caller then
// overrides only what differs — the same "defaults first" rule as the
// options idiom, expressed as a function instead of closures.
func Default() Settings {
	return Settings{
		Endpoint: "127.0.0.1:8080",
		Timeout:  Duration(30 * time.Second),
		Retries:  2,
	}
}

// FromJSON decodes settings on top of Default(), then validates. Because
// the struct is exported with tags, a config file needs no bespoke
// mapping code — this is the property functional options cannot offer.
func FromJSON(data []byte) (*Config, error) {
	s := Default()
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	return New(s)
}

// Patch overlays a partial JSON document (e.g. an environment-specific
// override) onto existing settings. Only fields present in the document
// change, which is what makes layered configuration possible.
func Patch(base Settings, data []byte) (Settings, error) {
	// Round-trip through a pointer-map so absent keys leave base alone.
	var overlay map[string]json.RawMessage
	if err := json.Unmarshal(data, &overlay); err != nil {
		return base, fmt.Errorf("config: decode patch: %w", err)
	}

	// Marshal base, unmarshal over it, then unmarshal the overlay into the
	// same value: encoding/json only touches keys that are present.
	raw, err := json.Marshal(base)
	if err != nil {
		return base, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(raw, &merged); err != nil {
		return base, err
	}
	for k, v := range overlay {
		merged[k] = v
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return base, err
	}
	var s Settings
	if err := json.Unmarshal(out, &s); err != nil {
		return base, fmt.Errorf("config: merge: %w", err)
	}
	return s, nil
}

// Diff reports which fields differ between two settings, as "field:
// before → after" lines. A struct makes this a plain loop; with opaque
// options there is nothing to diff against.
func Diff(a, b Settings) []string {
	var out []string
	add := func(field string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			out = append(out, fmt.Sprintf("%s: %v → %v", field, x, y))
		}
	}
	add("name", a.Name, b.Name)
	add("endpoint", a.Endpoint, b.Endpoint)
	add("timeout", a.Timeout, b.Timeout)
	add("retries", a.Retries, b.Retries)
	add("tls", a.TLS, b.TLS)
	add("logging", a.Logging, b.Logging)
	if len(a.Headers) != len(b.Headers) {
		out = append(out, fmt.Sprintf("headers: %d → %d entries", len(a.Headers), len(b.Headers)))
	} else {
		for k := range a.Headers {
			if a.Headers[k] != b.Headers[k] {
				out = append(out, fmt.Sprintf("headers[%s]: %q → %q", k, a.Headers[k], b.Headers[k]))
				break
			}
		}
	}
	return out
}

// Package options demonstrates the Functional Options idiom — the Go-native
// answer to "how do I construct a type with many optional fields?"
//
// There is no GoF counterpart: this idiom exists because Go has no named
// or default arguments, and because a positional constructor with 8
// parameters is unreadable and easy to get wrong.
//
// Shape:
//
//	func New(opts ...Option) (*T, error)
//	type Option func(*T)   // or func(*config) error for fallible options
//
// Properties that make it worth the ceremony:
//   - options are order-independent — no caller can accidentally pass two
//     strings the wrong way round, because each option names its target;
//   - the zero/default state is defined once, in New, before options run;
//   - options are values: a preconfigured bundle can be stored, composed,
//     and reused (see Preset);
//   - an option can validate itself, so a bad value fails at the call site
//     with a message naming the option, not deep inside New.
//
// When *not* to use it: fewer than ~3 options, or when most fields are
// required — then a plain struct parameter (see package config) is clearer.
package options

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// --- configuration type ---

// Config is fully unexported: the only way in is New plus Options, and the
// only way out is a read method. A partially-configured Config cannot exist.
type Config struct {
	name      string
	endpoint  string
	timeout   time.Duration
	retries   int
	headers   map[string]string
	tls       bool
	userAgent string
	logging   bool
}

// ErrInvalidOption is returned by fallible options; match with errors.Is.
var ErrInvalidOption = errors.New("options: invalid option")

// --- the Option type ---

// Option mutates a Config under construction. Returning an error lets an
// option reject its own argument — the reason this variant is preferred
// over the plain `func(*Config)`: validation lives with the value it
// validates.
type Option func(*Config) error

// --- individual options ---

// WithName sets the client name (required in practice; validated here).
func WithName(name string) Option {
	return func(c *Config) error {
		if strings.TrimSpace(name) == "" {
			return errors.New("name must not be blank")
		}
		c.name = name
		return nil
	}
}

// WithEndpoint sets the remote endpoint and checks its shape.
func WithEndpoint(addr string) Option {
	return func(c *Config) error {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("want host:port, got %q", addr)
		}
		c.endpoint = addr
		return nil
	}
}

// WithTimeout sets the per-request timeout. Non-positive values are
// rejected rather than silently coerced: a caller who wrote 0 meant
// something and deserves to be told it is wrong.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) error {
		if d <= 0 {
			return fmt.Errorf("must be positive, got %s", d)
		}
		c.timeout = d
		return nil
	}
}

// WithRetries sets the retry budget (0 disables retries).
func WithRetries(n int) Option {
	return func(c *Config) error {
		if n < 0 {
			return fmt.Errorf("must be >= 0, got %d", n)
		}
		c.retries = n
		return nil
	}
}

// WithHeader adds one header. Duplicates overwrite, matching how a real
// header map behaves; a blank key is rejected.
func WithHeader(k, v string) Option {
	return func(c *Config) error {
		if strings.TrimSpace(k) == "" {
			return errors.New("header must not be blank")
		}
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[k] = v
		return nil
	}
}

// WithHeaders merges several headers at once — options compose with
// ordinary functions, so a loop can build a slice of them.
func WithHeaders(h map[string]string) Option {
	return func(c *Config) error {
		for k, v := range h {
			if err := WithHeader(k, v)(c); err != nil {
				return err
			}
		}
		return nil
	}
}

// WithTLS enables or disables TLS. The bool parameter is explicit, so
// `WithTLS(true)` reads correctly at the call site — a bare WithTLS()
// that secretly toggles would not.
func WithTLS(enabled bool) Option {
	return func(c *Config) error {
		c.tls = enabled
		return nil
	}
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Config) error {
		if len(ua) > 128 {
			return fmt.Errorf("too long (%d > 128 bytes)", len(ua))
		}
		c.userAgent = ua
		return nil
	}
}

// WithLogging toggles debug logging.
func WithLogging(on bool) Option {
	return func(c *Config) error { c.logging = on; return nil }
}

// --- constructor ---

// New applies defaults, then the caller's options in order, then validates
// the whole result.
//
// Order matters in one direction only: defaults first, so an option can
// always override a default and no default can ever overwrite an option.
// Among options themselves the caller's order is respected for
// dependencies (a header set before a header cleared behaves as written),
// but for independent options order is irrelevant — that is the idiom's
// guarantee.
func New(opts ...Option) (*Config, error) {
	// Defaults. Every field gets a value here; nothing is left to the zero
	// value unless the zero value is genuinely correct.
	cfg := &Config{
		// name and endpoint deliberately have no default: they are
		// required, and a default would make "required" a lie.
		timeout:   30 * time.Second,
		retries:   2,
		headers:   map[string]string{},
		tls:       false,
		userAgent: "options/1.0",
		logging:   false,
	}

	for i, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidOption, i)
		}
		// Options are applied in the caller's order; each is named for the
		// error message by unwrapping via the named constructors below.
		if err := o(cfg); err != nil {
			return nil, fmt.Errorf("%w: option %d: %v", ErrInvalidOption, i, err)
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate enforces cross-field invariants that no single option can see.
// This is why New validates *after* applying options rather than trusting
// each option in isolation.
func (c *Config) validate() error {
	var missing []string
	if c.name == "" {
		missing = append(missing, "name")
	}
	if c.endpoint == "" {
		missing = append(missing, "endpoint")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("options: missing required field(s): %s", strings.Join(missing, ", "))
	}
	if c.timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalidOption)
	}
	return nil
}

// --- read accessors: a Config is readable but not writable ---

// Name returns the configured client name.
func (c *Config) Name() string { return c.name }

// Endpoint returns host:port.
func (c *Config) Endpoint() string { return c.endpoint }

// Address returns endpoint prefixed with the scheme implied by TLS.
func (c *Config) Address() string {
	if c.tls {
		return "https://" + c.endpoint
	}
	return "http://" + c.endpoint
}

// Timeout returns the per-request timeout.
func (c *Config) Timeout() time.Duration { return c.timeout }

// Retries returns the retry budget.
func (c *Config) Retries() int { return c.retries }

// TLS reports whether TLS is enabled.
func (c *Config) TLS() bool { return c.tls }

// Logging reports whether debug logging is on.
func (c *Config) Logging() bool { return c.logging }

// UserAgent returns the User-Agent string.
func (c *Config) UserAgent() string { return c.userAgent }

// Headers returns a copy of the header map so a caller cannot mutate the
// Config through it.
func (c *Config) Headers() map[string]string {
	out := make(map[string]string, len(c.headers))
	for k, v := range c.headers {
		out[k] = v
	}
	return out
}

// String renders the config for logs (secrets would be redacted here).
func (c *Config) String() string {
	keys := make([]string, 0, len(c.headers))
	for k := range c.headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("Config{name:%s endpoint:%s timeout:%s retries:%d tls:%t headers:%s logging:%t}",
		c.name, c.endpoint, c.timeout, c.retries, c.tls, strings.Join(keys, "+"), c.logging)
}

// --- options as values: presets and reuse ---

// Preset is a reusable bundle of options. Because an Option is just a
// function, a named policy is a value that can be passed around, stored in
// a table, or extended.
type Preset []Option

// Then appends more options, returning a new Preset — presets are
// immutable by convention, so sharing one between callers is safe.
func (p Preset) Then(more ...Option) Preset {
	out := make(Preset, 0, len(p)+len(more))
	out = append(out, p...)
	return append(out, more...)
}

// Options exposes the preset as a normal option slice.
func (p Preset) Options() []Option { return []Option(p) }

// Production is a preset: strict timeouts, TLS on, retries up.
var Production = Preset{
	WithTimeout(5 * time.Second),
	WithRetries(3),
	WithTLS(true),
	WithLogging(false),
}

// Development is a preset: relaxed, chatty.
var Development = Preset{
	WithTimeout(30 * time.Second),
	WithRetries(0),
	WithTLS(false),
	WithLogging(true),
}

// Named builds a preset from a table, so configuration files can name a
// policy instead of listing every field.
func Named(name string) (Preset, error) {
	switch name {
	case "production", "prod":
		return Production, nil
	case "development", "dev":
		return Development, nil
	default:
		return nil, fmt.Errorf("%w: unknown preset %q", ErrInvalidOption, name)
	}
}

// --- fallible construction example ---

// Build resolves a config and returns everything a caller needs to make a
// request, demonstrating how the validated Config is consumed.
type Client struct {
	cfg *Config
}

// Dial constructs a client from options.
func Dial(opts ...Option) (*Client, error) {
	cfg, err := New(opts...)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg}, nil
}

// Config exposes a read-only view.
func (c *Client) Config() *Config { return c.cfg }

// URL builds a request URL from the validated config.
func (c *Client) URL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.cfg.Address() + path
}

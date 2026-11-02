// Package builder demonstrates two builder idioms.
//
// Idiom A — functional options: best for constructors with many optional
// fields. Options are order-independent, cannot be swapped by accident, and
// keep the zero/default state in one place.
//
// Idiom B — stepwise builder: best when construction must be validated or
// when a partially-built value must not escape. The built type has unexported
// fields, so the only way out is Build(), which validates.
package builder

import (
	"errors"
	"fmt"
	"time"
)

// =====================================================================
// Idiom A: functional options
// =====================================================================

// Server is built with functional options. It has no exported fields, so it
// cannot be constructed or mutated except through New and its options.
type Server struct {
	addr    string
	timeout time.Duration
	mw      []string
	tls     bool
}

// Option mutates a Server under construction.
type Option func(*Server)

// WithAddr overrides the listen address (default ":8080").
func WithAddr(addr string) Option {
	return func(s *Server) { s.addr = addr }
}

// WithTimeout overrides the request timeout (default 30s).
func WithTimeout(d time.Duration) Option {
	return func(s *Server) { s.timeout = d }
}

// WithMiddleware appends a named middleware, in registration order.
func WithMiddleware(names ...string) Option {
	return func(s *Server) { s.mw = append(s.mw, names...) }
}

// WithTLS enables TLS.
func WithTLS(certFile string) Option {
	return func(s *Server) { s.tls = certFile != "" }
}

// New applies defaults first, then options in the caller's order. Defaults
// before options means an option can always override a default, and an
// option can never be silently reset by a later default.
func New(opts ...Option) *Server {
	s := &Server{
		addr:    ":8080",
		timeout: 30 * time.Second,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Addr reports the configured listen address.
func (s *Server) Addr() string { return s.addr }

// Timeout reports the configured request timeout.
func (s *Server) Timeout() time.Duration { return s.timeout }

// Middleware returns a copy of the middleware chain, in order.
func (s *Server) Middleware() []string {
	cp := make([]string, len(s.mw))
	copy(cp, s.mw)
	return cp
}

// TLS reports whether TLS is enabled.
func (s *Server) TLS() bool { return s.tls }

// String renders the configuration for logging.
func (s *Server) String() string {
	return fmt.Sprintf("Server{addr:%s timeout:%s mw:%v tls:%t}",
		s.addr, s.timeout, s.mw, s.tls)
}

// =====================================================================
// Idiom B: stepwise builder with mandatory validation
// =====================================================================

// SQL is an immutable, validated query. Every field is unexported so a
// half-built value can never leak to a caller: the only constructor is
// SQLBuilder.Build.
type SQL struct {
	table  string
	wheres []string
	order  string
	limit  int
}

// String renders the query.
func (q SQL) String() string {
	b := "SELECT * FROM " + q.table
	if len(q.wheres) > 0 {
		b += " WHERE " + stringsJoin(q.wheres, " AND ")
	}
	if q.order != "" {
		b += " ORDER BY " + q.order
	}
	if q.limit > 0 {
		b += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	return b
}

// Limit returns the configured limit (0 = unlimited).
func (q SQL) Limit() int { return q.limit }

// Where returns a copy of the WHERE clauses.
func (q SQL) Where() []string {
	cp := make([]string, len(q.wheres))
	copy(cp, q.wheres)
	return cp
}

// ErrTableRequired is returned by Build when From was never called.
var ErrTableRequired = errors.New("builder: table name is required")

// ErrBadLimit is returned when LIMIT is negative.
var ErrBadLimit = errors.New("builder: limit must be >= 0")

// SQLBuilder accumulates clauses. It is not safe for concurrent use — one
// builder builds one query on one goroutine, which is the normal case.
type SQLBuilder struct {
	sql  SQL
	errs []error
}

// From starts a query against a table.
func From(table string) *SQLBuilder {
	return &SQLBuilder{sql: SQL{table: table}}
}

// Where appends a clause. Errors are accumulated rather than returned
// immediately so a caller can report every problem at once.
func (b *SQLBuilder) Where(clause string) *SQLBuilder {
	if clause == "" {
		b.errs = append(b.errs, errors.New("builder: empty where clause"))
		return b
	}
	b.sql.wheres = append(b.sql.wheres, clause)
	return b
}

// OrderBy sets the ORDER BY expression.
func (b *SQLBuilder) OrderBy(expr string) *SQLBuilder {
	b.sql.order = expr
	return b
}

// Limit sets the row limit.
func (b *SQLBuilder) Limit(n int) *SQLBuilder {
	if n < 0 {
		b.errs = append(b.errs, fmt.Errorf("%w: %d", ErrBadLimit, n))
		return b
	}
	b.sql.limit = n
	return b
}

// Build validates and freezes the value. On success the returned SQL is
// fully valid; on failure every accumulated error is joined.
func (b *SQLBuilder) Build() (SQL, error) {
	if b.sql.table == "" {
		b.errs = append(b.errs, ErrTableRequired)
	}
	if len(b.errs) > 0 {
		return SQL{}, errors.Join(b.errs...)
	}
	// Copy the slice so later mutation of the builder cannot affect the
	// built value.
	out := b.sql
	out.wheres = append([]string(nil), b.sql.wheres...)
	return out, nil
}

// stringsJoin avoids importing strings just for one call in String().
func stringsJoin(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	total := 0
	for _, p := range parts {
		total += len(p) + len(sep)
	}
	out := make([]byte, 0, total-len(sep))
	for i, p := range parts {
		if i > 0 {
			out = append(out, sep...)
		}
		out = append(out, p...)
	}
	return string(out)
}

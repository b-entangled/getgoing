// Package chain demonstrates the Chain of Responsibility pattern.
//
// A request is passed along a sequence of handlers until one handles it.
// Go's most idiomatic form is a slice of funcs plus a driver function —
// the slice makes ordering inspectable and lets the chain be assembled
// dynamically, unlike the GoF version where each handler holds a `next`
// pointer and the chain shape is buried in wiring code.
//
// Two drivers are provided: Stop (first error wins — validation) and
// Collect (every handler runs — report generation). One line of loop body
// separates them, which is exactly why the slice form beats pointer links.
package chain

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Handler processes a request. Returning a non-nil error stops or is
// collected depending on the driver.
type Handler func(ctx context.Context, r *Request) error

// Request is the value travelling down the chain. Handlers may annotate it.
type Request struct {
	User    string
	Email   string
	Age     int
	Plan    string
	Notes   []string
	Handled bool // set by a terminal handler to signal it consumed the request
}

// Note appends an annotation; handlers use this to explain what they did.
func (r *Request) Note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// --- individual handlers (each one is a distinct rule) ---

// ErrEmailRequired is matched with errors.Is.
var ErrEmailRequired = errors.New("chain: email is required")

// ErrInvalidEmail is matched with errors.Is.
var ErrInvalidEmail = errors.New("chain: email is invalid")

// ErrUnderage is matched with errors.Is.
var ErrUnderage = errors.New("chain: user is under 13")

// ErrDisposableEmail is matched with errors.Is.
var ErrDisposableEmail = errors.New("chain: disposable email domain")

// ErrUnknownPlan is matched with errors.Is.
var ErrUnknownPlan = errors.New("chain: unknown plan")

var disposableDomains = map[string]bool{
	"mailinator.com": true,
	"tempmail.dev":   true,
}

// RequireEmail rejects an empty address.
func RequireEmail(ctx context.Context, r *Request) error {
	if r.Email == "" {
		return ErrEmailRequired
	}
	r.Note("require-email: ok")
	return nil
}

// ValidateEmail does a deliberately simple shape check.
func ValidateEmail(ctx context.Context, r *Request) error {
	at := strings.Index(r.Email, "@")
	if at <= 0 || at == len(r.Email)-1 || strings.Contains(r.Email[:at], "@") {
		return fmt.Errorf("%w: %q", ErrInvalidEmail, r.Email)
	}
	r.Note("validate-email: ok")
	return nil
}

// BanDisposable rejects one-shot email domains.
func BanDisposable(ctx context.Context, r *Request) error {
	parts := strings.Split(r.Email, "@")
	if len(parts) == 2 && disposableDomains[parts[1]] {
		return fmt.Errorf("%w: %s", ErrDisposableEmail, parts[1])
	}
	r.Note("ban-disposable: ok")
	return nil
}

// RequireAge enforces a minimum age.
func RequireAge(min int) Handler {
	return func(ctx context.Context, r *Request) error {
		if r.Age < min {
			return fmt.Errorf("%w: age %d < %d", ErrUnderage, r.Age, min)
		}
		r.Note("require-age(%d): ok", min)
		return nil
	}
}

// ValidatePlan checks the subscription tier.
func ValidatePlan(ctx context.Context, r *Request) error {
	switch r.Plan {
	case "free", "pro", "team":
		r.Note("validate-plan: %s", r.Plan)
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownPlan, r.Plan)
	}
}

// --- drivers ---

// Stop runs handlers in order and stops at the first error, wrapping it
// with the handler index so a failure is easy to locate in a long chain.
func Stop(handlers ...Handler) Handler {
	return func(ctx context.Context, r *Request) error {
		for i, h := range handlers {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := h(ctx, r); err != nil {
				return fmt.Errorf("handler %d: %w", i, err)
			}
		}
		return nil
	}
}

// Collect runs every handler regardless of individual failures and joins
// all errors. Handlers still see the annotations of earlier ones.
func Collect(handlers ...Handler) Handler {
	return func(ctx context.Context, r *Request) error {
		var errs []error
		for i, h := range handlers {
			if err := ctx.Err(); err != nil {
				errs = append(errs, err)
				break
			}
			if err := h(ctx, r); err != nil {
				errs = append(errs, fmt.Errorf("handler %d: %w", i, err))
			}
		}
		return errors.Join(errs...)
	}
}

// Terminal handles the request when nothing earlier rejected it, and marks
// the request handled so a caller can detect that the chain completed.
// This is the chain's "default" branch.
func Terminal(fn func(context.Context, *Request) error) Handler {
	return func(ctx context.Context, r *Request) error {
		if err := fn(ctx, r); err != nil {
			return err
		}
		r.Handled = true
		return nil
	}
}

// Signature returns a compact description of the chain for logging.
func Signature(handlers ...Handler) string {
	return fmt.Sprintf("chain(%d handlers)", len(handlers))
}

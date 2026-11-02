// Package template demonstrates the Template Method pattern — and why Go
// inverts it.
//
// GoF Template Method fixes an algorithm skeleton in a base class and lets
// subclasses override the steps. Go has no inheritance, and shadowing a
// method while embedding produces surprising dispatch, so the base-class
// form is rejected outright.
//
// The Go replacement is the *functional decomposition* inversion: the
// skeleton is an ordinary exported function that takes the varying steps as
// parameters. Advantages over the GoF version:
//   - the skeleton's error wrapping, ordering and cleanup are written once
//     and cannot be forgotten by a subclass;
//   - there are no subclasses to construct or fake in a test;
//   - steps are values, so they can be composed, wrapped, and swapped.
//
// Domain: an ETL job (extract → validate → transform → load → report).
package template

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Row is one record moving through the pipeline.
type Row map[string]string

// Report summarises a run.
type Report struct {
	Read      int
	Accepted  int
	Rejected  int
	Written   int
	Errors    []string
	StepOrder []string
}

// Rejected reports the rows that failed validation.
func (r Report) RejectedRows() int { return r.Rejected }

// String renders the report for logging.
func (r Report) String() string {
	return fmt.Sprintf("read=%d accepted=%d rejected=%d written=%d",
		r.Read, r.Accepted, r.Rejected, r.Written)
}

// --- the varying steps ---

// Extract yields the raw rows. The first step; returning no rows and no
// error means "nothing to do" and the job succeeds with an empty report.
type Extract func(ctx context.Context) ([]Row, error)

// Validate reports whether a row may be processed. Returning an error marks
// the row rejected; the skeleton records it and continues, because one bad
// row must not abort a batch.
type Validate func(ctx context.Context, r Row) error

// Transform converts a validated row into its output form. Returning
// (nil, nil) skips the row deliberately.
type Transform func(ctx context.Context, r Row) (Row, error)

// Load persists a transformed row. Failures are fatal: a partial write is
// worse than no write, so the skeleton stops and returns the error.
type Load func(ctx context.Context, rows []Row) (int, error)

// Steps is the set of overridable operations. Compare with the GoF shape:
// a base class with 4 empty methods. Here the "subclass" is a struct
// literal, and an unconfigured field is caught immediately rather than by
// a nil-pointer panic three levels deep.
type Steps struct {
	Extract   Extract
	Validate  Validate
	Transform Transform
	Load      Load

	// OnRow, if set, is called for every accepted row before transform —
	// an optional hook, showing steps need not be mandatory.
	OnRow func(ctx context.Context, r Row)
}

// ErrMissingStep is returned when a required step is not configured.
var ErrMissingStep = errors.New("template: steps are incomplete")

// validate checks every required step is wired up before any work happens.
func (s Steps) validate() error {
	var missing []string
	if s.Extract == nil {
		missing = append(missing, "Extract")
	}
	if s.Validate == nil {
		missing = append(missing, "Validate")
	}
	if s.Transform == nil {
		missing = append(missing, "Transform")
	}
	if s.Load == nil {
		missing = append(missing, "Load")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingStep, strings.Join(missing, ", "))
	}
	return nil
}

// Run is the *template*: the fixed skeleton. Callers supply only the steps.
//
// Responsibilities the skeleton owns so no caller can forget them:
//  1. validate configuration before touching data;
//  2. respect context cancellation at every stage boundary;
//  3. record step order for auditing;
//  4. separate per-row failures (recorded, continue) from fatal failures
//     (returned immediately);
//  5. return a Report even on partial success, so callers can log detail.
func Run(ctx context.Context, steps Steps) (Report, error) {
	if err := steps.validate(); err != nil {
		return Report{}, err
	}

	rep := Report{}

	// Step 1: extract
	rep.StepOrder = append(rep.StepOrder, "extract")
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	rows, err := steps.Extract(ctx)
	if err != nil {
		return rep, fmt.Errorf("template: extract: %w", err)
	}
	rep.Read = len(rows)

	// Step 2 + 3: validate then transform, row by row.
	var ready []Row
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		rep.StepOrder = append(rep.StepOrder, "validate")

		if verr := steps.Validate(ctx, r); verr != nil {
			rep.Rejected++
			rep.Errors = append(rep.Errors, verr.Error())
			continue
		}
		rep.Accepted++

		if steps.OnRow != nil {
			steps.OnRow(ctx, r)
		}

		rep.StepOrder = append(rep.StepOrder, "transform")
		out, terr := steps.Transform(ctx, r)
		if terr != nil {
			rep.Rejected++
			rep.Errors = append(rep.Errors, terr.Error())
			continue
		}
		if out == nil {
			continue // deliberate skip
		}
		ready = append(ready, out)
	}

	// Step 4: load — fatal on failure.
	rep.StepOrder = append(rep.StepOrder, "load")
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	written, err := steps.Load(ctx, ready)
	rep.Written = written
	if err != nil {
		return rep, fmt.Errorf("template: load: %w", err)
	}

	return rep, nil
}

// =====================================================================
// A second, independent use of the same skeleton idea: file exporters
// =====================================================================

// Encode encodes rows into a byte blob.
type Encode func(ctx context.Context, rows []Row) ([]byte, error)

// Upload stores a blob somewhere.
type Upload func(ctx context.Context, name string, blob []byte) error

// Exporter is a second, smaller template — same inversion, different domain,
// proving the shape generalises rather than being an ETL special case.
type Exporter struct {
	Name    string
	Encode  Encode
	Upload  Upload
	Success func(name string, size int) // optional observability hook
}

// Export runs the export skeleton: encode → upload → notify.
// The skeleton owns ordering and error context; the caller owns only the
// two varying operations.
func (e *Exporter) Export(ctx context.Context, rows []Row) (int, error) {
	if e.Encode == nil || e.Upload == nil {
		return 0, fmt.Errorf("%w: Encode and Upload are required", ErrMissingStep)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	blob, err := e.Encode(ctx, rows)
	if err != nil {
		return 0, fmt.Errorf("export %s: encode: %w", e.Name, err)
	}

	if err := e.Upload(ctx, e.Name, blob); err != nil {
		return 0, fmt.Errorf("export %s: upload: %w", e.Name, err)
	}

	if e.Success != nil {
		e.Success(e.Name, len(blob))
	}
	return len(blob), nil
}

// --- ready-made step implementations, so callers only pick what differs ---

// FromRows is an Extract backed by an in-memory slice.
func FromRows(rows []Row) Extract {
	return func(context.Context) ([]Row, error) {
		out := make([]Row, len(rows))
		copy(out, rows)
		return out, nil
	}
}

// FailExtract is an Extract that always fails — useful for testing the
// skeleton's error path without any I/O.
func FailExtract(err error) Extract {
	return func(context.Context) ([]Row, error) { return nil, err }
}

// RequireFields is a Validate that rejects rows missing any field.
func RequireFields(fields ...string) Validate {
	return func(_ context.Context, r Row) error {
		var missing []string
		for _, f := range fields {
			if r[f] == "" {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("row missing fields: %s", strings.Join(missing, ","))
		}
		return nil
	}
}

// Copy is a Transform that returns the row unchanged.
func Copy(_ context.Context, r Row) (Row, error) {
	out := make(Row, len(r))
	for k, v := range r {
		out[k] = v
	}
	return out, nil
}

// Uppercase is a Transform that upper-cases every value.
func Uppercase(_ context.Context, r Row) (Row, error) {
	out := make(Row, len(r))
	for k, v := range r {
		out[k] = strings.ToUpper(v)
	}
	return out, nil
}

// CountingLoad is a Load that stores nothing and just counts.
func CountingLoad() (Load, *int) {
	written := new(int)
	return func(_ context.Context, rows []Row) (int, error) {
		*written += len(rows)
		return len(rows), nil
	}, written
}

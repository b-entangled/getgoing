package template

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func steps(rows []Row) Steps {
	load, _ := CountingLoad()
	return Steps{
		Extract:   FromRows(rows),
		Validate:  RequireFields("id", "name"),
		Transform: Copy,
		Load:      load,
	}
}

func sampleRows() []Row {
	return []Row{
		{"id": "1", "name": "ada"},
		{"id": "2", "name": ""},     // invalid: missing name
		{"id": "", "name": "grace"}, // invalid: missing id
		{"id": "4", "name": "linus"},
	}
}

func TestHappyPath(t *testing.T) {
	rep, err := Run(context.Background(), steps(sampleRows()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 4 || rep.Accepted != 2 || rep.Rejected != 2 || rep.Written != 2 {
		t.Fatalf("got %+v", rep)
	}
	if len(rep.Errors) != 2 {
		t.Fatalf("errors=%v", rep.Errors)
	}
}

// The skeleton's job: one bad row does not abort the batch.
func TestInvalidRowsAreRecordedNotFatal(t *testing.T) {
	rep, err := Run(context.Background(), steps(sampleRows()))
	if err != nil {
		t.Fatalf("rejected rows must not fail the run: %v", err)
	}
	if rep.Rejected != 2 {
		t.Fatalf("rejected=%d", rep.Rejected)
	}
}

func TestMissingStepIsDetectedBeforeAnyWork(t *testing.T) {
	extracted := false
	s := Steps{
		Extract: func(context.Context) ([]Row, error) {
			extracted = true
			return nil, nil
		},
		Validate:  RequireFields("id"),
		Transform: Copy,
		// Load missing
	}
	_, err := Run(context.Background(), s)
	if !errors.Is(err, ErrMissingStep) {
		t.Fatalf("want ErrMissingStep, got %v", err)
	}
	if !strings.Contains(err.Error(), "Load") {
		t.Fatalf("error should name the missing step: %v", err)
	}
	if extracted {
		t.Fatal("extract ran despite an invalid configuration")
	}
}

func TestExtractFailureIsFatal(t *testing.T) {
	boom := errors.New("disk on fire")
	s := steps(nil)
	s.Extract = FailExtract(boom)

	_, err := Run(context.Background(), s)
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped boom, got %v", err)
	}
	if !strings.Contains(err.Error(), "extract") {
		t.Fatalf("error should name the stage: %v", err)
	}
}

func TestLoadFailureIsFatalAndPropagates(t *testing.T) {
	boom := errors.New("db down")
	s := steps(sampleRows())
	s.Load = func(context.Context, []Row) (int, error) { return 0, boom }

	_, err := Run(context.Background(), s)
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped boom, got %v", err)
	}
	if !strings.Contains(err.Error(), "load") {
		t.Fatalf("error should name the stage: %v", err)
	}
}

func TestCancellationStopsBetweenStages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := steps(sampleRows())
	_, err := Run(ctx, s)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestExtractCanReturnNoRows(t *testing.T) {
	rep, err := Run(context.Background(), steps(nil))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 0 || rep.Written != 0 {
		t.Fatalf("got %+v", rep)
	}
	if rep.String() != "read=0 accepted=0 rejected=0 written=0" {
		t.Fatalf("got %q", rep.String())
	}
}

func TestOptionalHookIsSkippedWhenNil(t *testing.T) {
	s := steps(sampleRows())
	// OnRow deliberately not set.
	rep, err := Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accepted != 2 {
		t.Fatalf("got %+v", rep)
	}
}

func TestOptionalHookRunsForAcceptedRowsOnly(t *testing.T) {
	seen := []string{}
	s := steps(sampleRows())
	s.OnRow = func(_ context.Context, r Row) { seen = append(seen, r["name"]) }

	if _, err := Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// Only the two valid rows reach OnRow.
	if strings.Join(seen, ",") != "ada,linus" {
		t.Fatalf("got %v", seen)
	}
}

func TestTransformErrorRejectsOnlyThatRow(t *testing.T) {
	s := steps(sampleRows())
	s.Transform = func(_ context.Context, r Row) (Row, error) {
		if r["id"] == "4" {
			return nil, errors.New("transform boom")
		}
		return Copy(context.Background(), r)
	}
	rep, err := Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accepted != 2 || rep.Rejected != 3 {
		t.Fatalf("accepted=%d rejected=%d", rep.Accepted, rep.Rejected)
	}
	if rep.Written != 1 {
		t.Fatalf("written=%d", rep.Written)
	}
}

func TestNilTransformRowSkipsWithoutError(t *testing.T) {
	s := steps(sampleRows())
	s.Transform = func(_ context.Context, r Row) (Row, error) {
		if r["id"] == "1" {
			return nil, nil // deliberate skip
		}
		return Copy(context.Background(), r)
	}
	rep, err := Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Written != 1 {
		t.Fatalf("written=%d", rep.Written)
	}
	if rep.Rejected != 2 {
		t.Fatalf("a deliberate skip must not count as a rejection: %+v", rep)
	}
}

func TestStepOrderIsRecorded(t *testing.T) {
	rep, err := Run(context.Background(), steps(sampleRows()))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rep.StepOrder, " ")
	if !strings.HasPrefix(joined, "extract") {
		t.Fatalf("order=%v", rep.StepOrder)
	}
	if rep.StepOrder[len(rep.StepOrder)-1] != "load" {
		t.Fatalf("load must be last: %v", rep.StepOrder)
	}
}

// Swapping one step changes behaviour with no change to the skeleton.
func TestSwappingOneStepChangesBehaviour(t *testing.T) {
	s := steps([]Row{{"id": "1", "name": "ada"}})
	s.Transform = Uppercase

	load, _ := CountingLoad()
	s.Load = load
	var captured []Row
	s.Load = func(_ context.Context, rows []Row) (int, error) {
		captured = rows
		return len(rows), nil
	}

	if _, err := Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || captured[0]["name"] != "ADA" {
		t.Fatalf("got %+v", captured)
	}
}

// --- second template: exporter ---

func TestExporterRunsEncodeThenUpload(t *testing.T) {
	var order []string
	var gotBlob []byte
	e := &Exporter{
		Name: "daily.csv",
		Encode: func(context.Context, []Row) ([]byte, error) {
			order = append(order, "encode")
			return []byte("a,b"), nil
		},
		Upload: func(_ context.Context, name string, blob []byte) error {
			order = append(order, "upload:"+name)
			gotBlob = blob
			return nil
		},
		Success: func(name string, size int) {
			order = append(order, "success")
		},
	}

	n, err := e.Export(context.Background(), []Row{{"a": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("size=%d", n)
	}
	if strings.Join(order, ",") != "encode,upload:daily.csv,success" {
		t.Fatalf("order=%v", order)
	}
	if string(gotBlob) != "a,b" {
		t.Fatalf("blob=%q", gotBlob)
	}
}

func TestExporterDetectsMissingSteps(t *testing.T) {
	e := &Exporter{Name: "x"}
	if _, err := e.Export(context.Background(), nil); !errors.Is(err, ErrMissingStep) {
		t.Fatalf("got %v", err)
	}
}

func TestExporterEncodeFailureStopsBeforeUpload(t *testing.T) {
	boom := errors.New("encode failed")
	uploaded := false
	e := &Exporter{
		Name:   "x",
		Encode: func(context.Context, []Row) ([]byte, error) { return nil, boom },
		Upload: func(context.Context, string, []byte) error { uploaded = true; return nil },
	}
	if _, err := e.Export(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if uploaded {
		t.Fatal("upload ran after encode failed")
	}
}

func TestExporterCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Exporter{
		Name:   "x",
		Encode: func(context.Context, []Row) ([]byte, error) { return []byte("x"), nil },
		Upload: func(context.Context, string, []byte) error { return nil },
	}
	if _, err := e.Export(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

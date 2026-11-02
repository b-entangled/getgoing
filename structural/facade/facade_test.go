package facade

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConvertRunsWholePipeline(t *testing.T) {
	s, err := Convert(context.Background(), "in.mov", Options{
		Target: "mp4", Width: 1280, Height: 720, BitrateKbps: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FrameCount != 10 {
		t.Errorf("frames: got %d", s.FrameCount)
	}
	if s.Target != "mp4" {
		t.Errorf("target: got %s", s.Target)
	}
	// 10 frames * 1024 + 2000
	if s.BytesOut != 12240 {
		t.Errorf("bytes: got %d", s.BytesOut)
	}
}

func TestConvertAppliesScaling(t *testing.T) {
	s, err := Convert(context.Background(), "in.mov", Options{
		Target: "webm", Width: 640, Height: 360,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FrameCount != 10 {
		t.Errorf("scaling must not drop frames, got %d", s.FrameCount)
	}
}

func TestValidateFailsBeforeAnyWork(t *testing.T) {
	_, err := Convert(context.Background(), "in.mov", Options{})
	if err == nil || !strings.Contains(err.Error(), "target format required") {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestNegativeDimensionsRejected(t *testing.T) {
	_, err := Convert(context.Background(), "in.mov", Options{Target: "mp4", Width: -1})
	if err == nil || !strings.Contains(err.Error(), "negative dimensions") {
		t.Fatalf("got %v", err)
	}
}

func TestEmptySourceRejected(t *testing.T) {
	_, err := Convert(context.Background(), "", Options{Target: "mp4"})
	if err == nil || !strings.Contains(err.Error(), "probe") {
		t.Fatalf("want a wrapped probe error, got %v", err)
	}
}

// Errors crossing subsystem boundaries keep their identity: errors.Is works
// through the facade's wrapping.
func TestCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Convert(ctx, "in.mov", Options{Target: "mp4"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestFormatsAreListedByFacade(t *testing.T) {
	got := NewTranscoder().Formats()
	if len(got) != 3 {
		t.Fatalf("want 3 formats, got %v", got)
	}
}

// The payoff: the caller needs one import and one call for five subsystems.
func TestCallerSeesOnlyTheFacade(t *testing.T) {
	ctx := context.Background()
	s, err := Convert(ctx, "in.webm", Options{Target: "gif", FastStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "in.webm" || s.Target != "gif" {
		t.Fatalf("bad summary: %+v", s)
	}
	if s.Duration < 0 {
		t.Fatal("duration must be recorded")
	}
}

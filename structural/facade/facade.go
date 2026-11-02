// Package facade demonstrates the Facade pattern.
//
// A facade is a single, simple entry point over a subsystem of several
// collaborating types. In Go the facade is usually one exported function or
// struct in a package whose other types are unexported — the compiler then
// enforces the boundary, no architecture tests required.
//
// Example: transcoding media looks like one call, but internally it must
// probe the source, decode, transform colour, encode, and report. All four
// subsystem types are unexported; Convert is the whole public API.
package facade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Options configures a transcode.
type Options struct {
	Target      string // "mp4", "webm", "gif"
	Width       int
	Height      int
	BitrateKbps int
	FastStart   bool
}

// Summary reports what the subsystem did.
type Summary struct {
	Source     string
	Target     string
	FrameCount int
	BytesOut   int64
	Duration   time.Duration
}

// Validate checks the options before any expensive work starts.
func (o Options) Validate() error {
	if o.Target == "" {
		return errors.New("facade: target format required")
	}
	if o.Width < 0 || o.Height < 0 {
		return fmt.Errorf("facade: negative dimensions %dx%d", o.Width, o.Height)
	}
	return nil
}

// --- subsystem A: probe/metadata ---

type prober struct{}

// probeResult is subsystem-internal metadata.
type probeResult struct {
	Frames int
	Format string
}

func (prober) probe(ctx context.Context, src string) (probeResult, error) {
	if err := ctx.Err(); err != nil {
		return probeResult{}, err
	}
	if src == "" {
		return probeResult{}, errors.New("facade: empty source")
	}
	// Synthetic metadata: one frame per second of a nominal 10s clip.
	return probeResult{Frames: 10, Format: strings.TrimPrefix(src, "in.")}, nil
}

// --- subsystem B: decoder ---

type decoder struct{}

// decoded is a decoded, in-memory frame sequence.
type decoded struct {
	frames []string
}

func (decoder) decode(ctx context.Context, p probeResult) (*decoded, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frames := make([]string, 0, p.Frames)
	for i := range p.Frames {
		frames = append(frames, fmt.Sprintf("frame-%03d", i))
	}
	return &decoded{frames: frames}, nil
}

// --- subsystem C: colour transform ---

type transformer struct{}

func (transformer) scale(ctx context.Context, d *decoded, w, h int) (*decoded, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w == 0 && h == 0 {
		return d, nil // no scaling requested
	}
	out := make([]string, len(d.frames))
	for i, f := range d.frames {
		out[i] = fmt.Sprintf("%s@%dx%d", f, w, h)
	}
	return &decoded{frames: out}, nil
}

// --- subsystem D: encoder ---

type encoder struct{}

func (encoder) encode(ctx context.Context, d *decoded, o Options) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Synthetic size: 1KB per frame plus a bitrate-derived term.
	return int64(len(d.frames))*1024 + int64(o.BitrateKbps), nil
}

// --- the facade ---

// Transcoder is the facade object. It owns the subsystem instances so a
// caller never constructs them, never sequences them, and never sees them.
type Transcoder struct {
	prober    prober
	decode    decoder
	transform transformer
	encode    encoder
}

// NewTranscoder builds the facade.
func NewTranscoder() *Transcoder { return &Transcoder{} }

// Convert is the single call that runs the whole pipeline in the correct
// order, wraps every subsystem error with context, and guarantees cleanup.
//
// This is the entire public API of the package for the transcoding use case.
func (t *Transcoder) Convert(ctx context.Context, src string, opts Options) (Summary, error) {
	start := time.Now()

	// 1. validate up front — cheap failure beats an expensive failure
	if err := opts.Validate(); err != nil {
		return Summary{}, err
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	// 2. probe
	probe, err := t.prober.probe(ctx, src)
	if err != nil {
		return Summary{}, fmt.Errorf("facade: probe %q: %w", src, err)
	}

	// 3. decode
	raw, err := t.decode.decode(ctx, probe)
	if err != nil {
		return Summary{}, fmt.Errorf("facade: decode: %w", err)
	}

	// 4. transform
	scaled, err := t.transform.scale(ctx, raw, opts.Width, opts.Height)
	if err != nil {
		return Summary{}, fmt.Errorf("facade: scale: %w", err)
	}

	// 5. encode
	size, err := t.encode.encode(ctx, scaled, opts)
	if err != nil {
		return Summary{}, fmt.Errorf("facade: encode: %w", err)
	}

	return Summary{
		Source:     src,
		Target:     opts.Target,
		FrameCount: len(scaled.frames),
		BytesOut:   size,
		Duration:   time.Since(start),
	}, nil
}

// Formats reports the targets the subsystem supports — the facade also
// exists to present a *small* capability list rather than four partial ones.
func (t *Transcoder) Formats() []string {
	return []string{"mp4", "webm", "gif"}
}

// Subsystem types are intentionally unexported. To prove the boundary holds,
// this file is the only place they are named; a sibling package cannot
// reference them at all.

var (
	_ = prober{}
	_ = decoder{}
	_ = transformer{}
	_ = encoder{}
)

// Convert is a package-level shortcut so the common case needs no object.
var defaultT = NewTranscoder()

// Convert runs a conversion with the package-level transcoder.
func Convert(ctx context.Context, src string, opts Options) (Summary, error) {
	return defaultT.Convert(ctx, src, opts)
}

// mu guards defaultT replacement in tests.
var mu sync.Mutex

// setDefault swaps the package-level transcoder (test hook).
func setDefault(t *Transcoder) {
	mu.Lock()
	defer mu.Unlock()
	defaultT = t
}

package adapter

import (
	"context"
	"errors"
	"testing"

	"github.com/b-entangled/projects/learnings/structural/adapter/legacy"
)

func TestLegacyAdapterSatisfiesStore(t *testing.T) {
	ctx := context.Background()
	var s Store = FromLegacy(legacy.New())

	if err := s.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v" {
		t.Fatalf("want v, got %s", got)
	}
}

func TestLegacyAdapterMissingKey(t *testing.T) {
	var s Store = FromLegacy(legacy.New())
	_, err := s.Get(context.Background(), "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLegacyAdapterDoesNotExposeLegacyVersion(t *testing.T) {
	// The adapted value keeps legacy behaviour (version bumping) even though
	// Store cannot see it — proving the wrapper delegates rather than reimplements.
	src := legacy.New()
	var s Store = FromLegacy(src)

	ctx := context.Background()
	_ = s.Put(ctx, "k", []byte("1"))
	_ = s.Put(ctx, "k", []byte("2"))
	if e, _ := src.Load("k"); e.Version != 2 {
		t.Fatalf("legacy version should have advanced to 2, got %d", e.Version)
	}
}

func TestMapAdapterSatisfiesStore(t *testing.T) {
	ctx := context.Background()
	backing := map[string][]byte{"seed": []byte("s")}
	var s Store = FromMap(backing)

	got, err := s.Get(ctx, "seed")
	if err != nil || string(got) != "s" {
		t.Fatalf("seed read failed: %q %v", got, err)
	}

	if err := s.Put(ctx, "new", []byte("n")); err != nil {
		t.Fatal(err)
	}
	if string(backing["new"]) != "n" {
		t.Fatal("map adapter did not write through to the borrowed map")
	}
}

func TestMapAdapterCopiesBuffers(t *testing.T) {
	backing := map[string][]byte{}
	var s Store = FromMap(backing)

	buf := []byte("hello")
	_ = s.Put(context.Background(), "k", buf)
	buf[0] = 'X'

	got, _ := s.Get(context.Background(), "k")
	if string(got) != "hello" {
		t.Fatalf("aliased caller buffer: %q", got)
	}
}

func TestMapAdapterNilMapIsUsable(t *testing.T) {
	var s Store = FromMap(nil)
	ctx := context.Background()
	if err := s.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "k"); err != nil {
		t.Fatal(err)
	}
}

// The payoff: one consumer works with both unrelated adaptees.
func TestConsumerIsAdapteeAgnostic(t *testing.T) {
	ctx := context.Background()
	stores := map[string]Store{
		"legacy": FromLegacy(legacy.New()),
		"map":    FromMap(make(map[string][]byte)),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			c := NewCache(s)
			if err := c.Put(ctx, "a", []byte("1")); err != nil {
				t.Fatal(err)
			}
			got, err := c.Get(ctx, "a")
			if err != nil || string(got) != "1" {
				t.Fatalf("got %q err %v", got, err)
			}
			if _, err := c.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("sentinel lost through consumer: %v", err)
			}
		})
	}
}

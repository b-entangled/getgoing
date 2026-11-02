package factory

import (
	"context"
	"errors"
	"testing"
)

func TestNewStoreSelectsConcreteType(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"memory", "memory"},
		{"", "memory"}, // default
		{"null", "null"},
		{"MEMORY", "memory"}, // case-insensitive
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			s, err := NewStore(tt.driver)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.Name() != tt.want {
				t.Fatalf("want %q, got %q", tt.want, s.Name())
			}
		})
	}
}

func TestNewStoreUnknownDriver(t *testing.T) {
	_, err := NewStore("postgres")
	if !errors.Is(err, ErrUnknownDriver) {
		t.Fatalf("want ErrUnknownDriver, got %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s, err := NewStore("memory")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

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

func TestStoreDoesNotAliasCallerBuffer(t *testing.T) {
	s, _ := NewStore("memory")
	ctx := context.Background()

	buf := []byte("hello")
	if err := s.Put(ctx, "k", buf); err != nil {
		t.Fatal(err)
	}
	buf[0] = 'X' // mutate the caller's slice after Put

	got, _ := s.Get(ctx, "k")
	if string(got) != "hello" {
		t.Fatalf("store aliased caller's buffer: %q", got)
	}
}

func TestPluginRegistryResolvesDriver(t *testing.T) {
	st, err := Open("builtin-memory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Name() != "memory" {
		t.Fatalf("want memory, got %s", st.Name())
	}
}

func TestPluginRegistryUnknown(t *testing.T) {
	if _, err := Open("nope"); !errors.Is(err, ErrUnknownDriver) {
		t.Fatalf("want ErrUnknownDriver, got %v", err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	Register("builtin-memory", func() (Store, error) { return nullStore{}, nil })
}

func TestDriversAreListed(t *testing.T) {
	got := Drivers()
	if len(got) < 2 {
		t.Fatalf("expected at least 2 registered drivers, got %v", got)
	}
}

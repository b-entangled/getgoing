package abstractfactory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBundleIsInternallyConsistent(t *testing.T) {
	tests := []struct {
		provider string
		storePfx string
		queuePfx string
		topicPfx string
	}{
		{"aws", "s3://", "sqs:", "sns:"},
		{"gcp", "gs://", "pubsub-queue:", "pubsub-topic:"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			b, err := Open(tt.provider, "prod")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(b.Store.Location("k"), tt.storePfx) {
				t.Errorf("store from wrong family: %s", b.Store.Location("k"))
			}
			if !strings.HasPrefix(b.Queue.Name(), tt.queuePfx) {
				t.Errorf("queue from wrong family: %s", b.Queue.Name())
			}
			if !strings.HasPrefix(b.Topic.Name(), tt.topicPfx) {
				t.Errorf("topic from wrong family: %s", b.Topic.Name())
			}
		})
	}
}

func TestBundleProductsShareTheProject(t *testing.T) {
	b, err := Open("aws", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Store.Location("obj"); !strings.Contains(got, "team-a") {
		t.Errorf("store missing project: %s", got)
	}
	if got := b.Queue.Name(); !strings.Contains(got, "team-a") {
		t.Errorf("queue missing project: %s", got)
	}
	if got := b.Topic.Name(); !strings.Contains(got, "team-a") {
		t.Errorf("topic missing project: %s", got)
	}
}

func TestUnknownProvider(t *testing.T) {
	if _, err := Open("azure", "p"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("want ErrUnknownProvider, got %v", err)
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	Register("aws", func(string) (Bundle, error) { return Bundle{}, nil })
}

func TestProductsAreUsable(t *testing.T) {
	b, _ := Open("gcp", "p")
	ctx := context.Background()
	if err := b.Store.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := b.Queue.Enqueue(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if err := b.Topic.Publish(ctx, "msg"); err != nil {
		t.Fatal(err)
	}
	if len(Providers()) < 2 {
		t.Fatalf("want >= 2 providers, got %v", Providers())
	}
}

// Package abstractfactory demonstrates the Abstract Factory pattern.
//
// Where the Factory Method (package factory) produces *one* object, an
// abstract factory produces a *family of related objects* that must be used
// together. Here a cloud provider contributes an object store, a queue and a
// topic; mixing an AWS queue with a GCP topic would be a bug, so callers ask
// the provider factory for the whole bundle instead of assembling it.
//
// Go expresses this as a struct of constructors — no AbstractFactory class
// hierarchy required.
package abstractfactory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ObjectStore stores opaque blobs.
type ObjectStore interface {
	Put(ctx context.Context, key string, value []byte) error
	Location(key string) string
}

// Queue is a FIFO work queue.
type Queue interface {
	Enqueue(ctx context.Context, job string) error
	Name() string
}

// Topic is a publish/subscribe channel.
type Topic interface {
	Publish(ctx context.Context, msg string) error
	Name() string
}

// Bundle is one complete, mutually-compatible family.
type Bundle struct {
	Store ObjectStore
	Queue Queue
	Topic Topic
}

// BundleFactory builds a whole family from one configuration. This is the
// abstract factory: a single entry point guarantees the products agree.
type BundleFactory func(project string) (Bundle, error)

// ErrUnknownProvider is matched by errors.Is.
var ErrUnknownProvider = errors.New("abstractfactory: unknown provider")

// --- product implementations ---

type s3Store struct{ project string }

func (s s3Store) Put(context.Context, string, []byte) error { return nil }
func (s s3Store) Location(key string) string {
	return fmt.Sprintf("s3://%s/%s", s.project, key)
}

type sqsQueue struct{ project string }

func (q sqsQueue) Enqueue(context.Context, string) error { return nil }
func (q sqsQueue) Name() string                          { return "sqs:" + q.project }

type snsTopic struct{ project string }

func (t snsTopic) Publish(context.Context, string) error { return nil }
func (t snsTopic) Name() string                          { return "sns:" + t.project }

type gcsStore struct{ project string }

func (s gcsStore) Put(context.Context, string, []byte) error { return nil }
func (s gcsStore) Location(key string) string {
	return fmt.Sprintf("gs://%s/%s", s.project, key)
}

type pubsubQueue struct{ project string }

func (q pubsubQueue) Enqueue(context.Context, string) error { return nil }
func (q pubsubQueue) Name() string                          { return "pubsub-queue:" + q.project }

type pubsubTopic struct{ project string }

func (t pubsubTopic) Publish(context.Context, string) error { return nil }
func (t pubsubTopic) Name() string                          { return "pubsub-topic:" + t.project }

// --- the factories ---

func awsFactory(project string) (Bundle, error) {
	return Bundle{
		Store: s3Store{project: project},
		Queue: sqsQueue{project: project},
		Topic: snsTopic{project: project},
	}, nil
}

func gcpFactory(project string) (Bundle, error) {
	return Bundle{
		Store: gcsStore{project: project},
		Queue: pubsubQueue{project: project},
		Topic: pubsubTopic{project: project},
	}, nil
}

var (
	regMu     sync.RWMutex
	factories = map[string]BundleFactory{}
)

// Register installs a provider factory. Duplicate names panic at start-up.
func Register(provider string, f BundleFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := factories[provider]; dup {
		panic("abstractfactory: duplicate provider: " + provider)
	}
	factories[provider] = f
}

// Open builds a complete family for a provider. Because the whole Bundle
// comes from one factory, the products can never be mixed across providers.
func Open(provider, project string) (Bundle, error) {
	regMu.RLock()
	f, ok := factories[strings.ToLower(provider)]
	regMu.RUnlock()
	if !ok {
		return Bundle{}, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
	return f(project)
}

// Providers lists the registered provider names.
func Providers() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	return out
}

func init() {
	Register("aws", awsFactory)
	Register("gcp", gcpFactory)
}

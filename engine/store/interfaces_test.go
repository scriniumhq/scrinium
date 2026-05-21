package store_test

import (
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/store"
)

// This file is interface conformance smoke for the core package —
// not integration in any operational sense. It used to live as
// integration_test.go, which misled new readers into expecting
// scenario tests; renamed to interfaces_test.go after the P1.13
// audit so the filename matches the file's actual purpose.

// TestEventBusSatisfiesPublisher checks that event.NewEventBus()
// satisfies store.Publisher through structural conformance. This is
// the link between the minimal stack (a Store without Curator) and
// the event bus — it must not be broken.
func TestEventBusSatisfiesPublisher(t *testing.T) {
	bus := event.NewEventBus()

	// Must compile and work at runtime.
	var pub store.Publisher = bus
	pub.Publish(event.Event{Type: "smoke.test", Payload: "hello"})

	// The Store option must accept any Publisher implementation.
	opt := store.WithPublisher(pub)
	if opt == nil {
		t.Fatal("WithPublisher returned nil option")
	}
}

// TestStoreOptionsApply verifies that every With* function returns
// a valid StoreOption. A pure smoke check on the signatures.
func TestStoreOptionsApply(t *testing.T) {
	opts := []store.StoreOption{
		store.WithForceReinit(),
		store.WithPurgeOnReinit(),
		store.WithConfig(domain.StoreConfig{}),
		store.WithStoreIndex(nil),
		store.WithPublisher(nil),
		store.WithHashRegistry(nil),
		store.WithReadRegistry(nil),
		store.WithKeyResolver(store.NewStaticKeyResolver([]byte("k"))),
		store.WithPassphrase(nil),
		store.WithAutoUnlock(),
	}
	for i, opt := range opts {
		if opt == nil {
			t.Errorf("option %d returned nil", i)
		}
	}
}

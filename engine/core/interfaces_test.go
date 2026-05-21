package core_test

import (
	"testing"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/event"
)

// This file is interface conformance smoke for the core package —
// not integration in any operational sense. It used to live as
// integration_test.go, which misled new readers into expecting
// scenario tests; renamed to interfaces_test.go after the P1.13
// audit so the filename matches the file's actual purpose.

// TestEventBusSatisfiesPublisher checks that event.NewEventBus()
// satisfies core.Publisher through structural conformance. This is
// the link between the minimal stack (a Store without Curator) and
// the event bus — it must not be broken.
func TestEventBusSatisfiesPublisher(t *testing.T) {
	bus := event.NewEventBus()

	// Must compile and work at runtime.
	var pub coreapi.Publisher = bus
	pub.Publish(event.Event{Type: "smoke.test", Payload: "hello"})

	// The Store option must accept any Publisher implementation.
	opt := core.WithPublisher(pub)
	if opt == nil {
		t.Fatal("WithPublisher returned nil option")
	}
}

// TestStoreOptionsApply verifies that every With* function returns
// a valid StoreOption. A pure smoke check on the signatures.
func TestStoreOptionsApply(t *testing.T) {
	opts := []core.StoreOption{
		core.WithForceReinit(),
		core.WithPurgeOnReinit(),
		core.WithConfig(domain.StoreConfig{}),
		core.WithStoreIndex(nil),
		core.WithPublisher(nil),
		core.WithHashRegistry(nil),
		core.WithReadRegistry(nil),
		core.WithKeyResolver(core.NewStaticKeyResolver([]byte("k"))),
		core.WithPassphrase(nil),
		core.WithAutoUnlock(),
	}
	for i, opt := range opts {
		if opt == nil {
			t.Errorf("option %d returned nil", i)
		}
	}
}

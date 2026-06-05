package event

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestEventBus_DeliveryOrder(t *testing.T) {
	bus := NewEventBus()
	var got []string
	var mu sync.Mutex

	bus.Subscribe(func(e Event) {
		mu.Lock()
		got = append(got, "first:"+e.Type)
		mu.Unlock()
	})
	bus.Subscribe(func(e Event) {
		mu.Lock()
		got = append(got, "second:"+e.Type)
		mu.Unlock()
	})

	bus.Publish(Event{Type: "test"})

	if len(got) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(got))
	}
	if got[0] != "first:test" || got[1] != "second:test" {
		t.Fatalf("delivery order broken: %v", got)
	}
}

func TestEventBus_PanicSafety(t *testing.T) {
	bus := NewEventBus()
	var afterPanicCalled atomic.Bool

	bus.Subscribe(func(e Event) {
		panic("subscriber panic")
	})
	bus.Subscribe(func(e Event) {
		afterPanicCalled.Store(true)
	})

	// Publish must not propagate the panic.
	bus.Publish(Event{Type: "test"})

	if !afterPanicCalled.Load() {
		t.Fatal("subscriber after panicking one was not called")
	}
}

func TestEventBus_NoPersistence(t *testing.T) {
	bus := NewEventBus()
	bus.Publish(Event{Type: "before-subscribe"})

	var received atomic.Int32
	bus.Subscribe(func(e Event) {
		received.Add(1)
	})

	if received.Load() != 0 {
		t.Fatal("subscriber received an event published before subscription")
	}

	bus.Publish(Event{Type: "after-subscribe"})
	if received.Load() != 1 {
		t.Fatalf("expected 1 received event, got %d", received.Load())
	}
}

func TestEventBus_NilSubscriber(t *testing.T) {
	bus := NewEventBus()
	bus.Subscribe(nil)
	// Must not panic.
	bus.Publish(Event{Type: "test"})
}

func TestEventBus_ConcurrentSubscribeAndPublish(t *testing.T) {
	bus := NewEventBus()
	var counter atomic.Int64

	var wg sync.WaitGroup
	// 10 subscribers registered in parallel with publishing.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Subscribe(func(e Event) { counter.Add(1) })
		}()
	}

	// 100 concurrent publishes.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Publish(Event{Type: "test"})
		}()
	}

	wg.Wait()
	// We do not check the exact value of counter (it depends on which
	// subscribers managed to register before each Publish), but we
	// verify there are no races (under `go test -race`).
	_ = counter.Load()
}
func TestEventBus_UnsubscribeStopsDelivery(t *testing.T) {
	bus := NewEventBus()
	var n int
	unsub := bus.Subscribe(func(Event) { n++ })

	bus.Publish(Event{Type: "x"})
	if n != 1 {
		t.Fatalf("before unsubscribe got %d deliveries, want 1", n)
	}
	unsub()
	bus.Publish(Event{Type: "x"})
	if n != 1 {
		t.Fatalf("after unsubscribe got %d deliveries, want 1 (no further)", n)
	}
}

func TestEventBus_UnsubscribeIdempotent(t *testing.T) {
	bus := NewEventBus()
	unsub := bus.Subscribe(func(Event) {})
	unsub()
	unsub() // second call must be a no-op, not a panic
}

func TestEventBus_NilSubscriberUnsubUsable(t *testing.T) {
	bus := NewEventBus()
	unsub := bus.Subscribe(nil)
	unsub() // unsubscribe of a nil subscription must not panic
}

func TestEventBus_UnsubscribeRemovesOnlyItsOwn(t *testing.T) {
	bus := NewEventBus()
	var a, b int
	unsubA := bus.Subscribe(func(Event) { a++ })
	bus.Subscribe(func(Event) { b++ })

	unsubA()
	bus.Publish(Event{Type: "x"})
	if a != 0 || b != 1 {
		t.Fatalf("after unsubscribing A: a=%d b=%d, want a=0 b=1", a, b)
	}
}

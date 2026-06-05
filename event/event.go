package event

import "sync"

// Event is a single message on the bus. Type identifies the event
// (for example, "store.manifest_saved", "agent.started"); Payload
// carries the concrete payload whose type is determined by Type.
type Event struct {
	Type    string
	Payload any
}

// EventBus is the bus contract. The default implementation
// (returned by NewEventBus) is synchronous, panic-safe, and does not
// persist events: a subscriber registered after a Publish call does
// not receive that event.
type EventBus interface {
	Publish(e Event)
	// Subscribe registers fn and returns a function that removes it.
	// The returned unsubscribe is idempotent and safe to call from any
	// goroutine. A nil fn is ignored; its unsubscribe is a no-op.
	Subscribe(fn func(Event)) func()
}

// NewEventBus returns an event bus with the following guarantees:
//   - Delivery is synchronous: Publish returns only after every
//     subscriber active at the moment of the call has been invoked.
//   - Subscribers are invoked in registration order.
//   - A panic from a subscriber is recovered; delivery to the
//     remaining subscribers continues.
//   - Registering or removing a subscriber concurrently with Publish is
//     race-free; the change takes effect from the next Publish onward.
func NewEventBus() EventBus {
	return &syncBus{}
}

// Publisher is the minimal contract for emitting events; it is passed
// to Store via WithPublisher. It is satisfied by event.EventBus and by
// any custom implementation (asynchronous, persistent, filtering).
type Publisher interface {
	Publish(e Event)
}

// subscription pairs a handler with the id its unsubscribe closure
// captures, so removal is by identity (not by comparing func values,
// which Go does not allow).
type subscription struct {
	id int
	fn func(Event)
}

type syncBus struct {
	mu     sync.RWMutex
	subs   []subscription
	nextID int
}

func (b *syncBus) Publish(e Event) {
	b.mu.RLock()
	subs := b.subs
	b.mu.RUnlock()

	for _, s := range subs {
		safeCall(s.fn, e)
	}
}

func (b *syncBus) Subscribe(fn func(Event)) func() {
	if fn == nil {
		return func() {}
	}
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	// Copy on append, so a concurrent Publish reading the previous
	// snapshot does not race against this registration.
	next := make([]subscription, len(b.subs)+1)
	copy(next, b.subs)
	next[len(b.subs)] = subscription{id: id, fn: fn}
	b.subs = next
	b.mu.Unlock()

	return func() { b.unsubscribe(id) }
}

// unsubscribe removes the subscription with the given id. Idempotent:
// removing an id that is already gone is a no-op.
func (b *syncBus) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := -1
	for i, s := range b.subs {
		if s.id == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	// Copy without the removed entry, preserving order; a concurrent
	// Publish keeps iterating its own prior snapshot safely.
	next := make([]subscription, 0, len(b.subs)-1)
	next = append(next, b.subs[:idx]...)
	next = append(next, b.subs[idx+1:]...)
	b.subs = next
}

func safeCall(fn func(Event), e Event) {
	defer func() {
		// A subscriber's panic is swallowed. Delivery to the remaining
		// subscribers continues. This is the contract of the default
		// implementation.
		_ = recover()
	}()
	fn(e)
}

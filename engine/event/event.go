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
	Subscribe(fn func(Event))
}

// NewEventBus returns an event bus with the following guarantees:
//   - Delivery is synchronous: Publish returns only after every
//     subscriber active at the moment of the call has been invoked.
//   - Subscribers are invoked in registration order.
//   - A panic from a subscriber is recovered; delivery to the
//     remaining subscribers continues.
//   - Registering a new subscriber concurrently with Publish is
//     race-free; the new subscriber starts receiving events from the
//     next Publish onward.
func NewEventBus() EventBus {
	return &syncBus{}
}

// Publisher is the minimal contract for emitting events; it is passed
// to Store via WithPublisher. It is satisfied by event.EventBus and by
// any custom implementation (asynchronous, persistent, filtering).
type Publisher interface {
	Publish(e Event)
}

type syncBus struct {
	mu          sync.RWMutex
	subscribers []func(Event)
}

func (b *syncBus) Publish(e Event) {
	b.mu.RLock()
	subs := b.subscribers
	b.mu.RUnlock()

	for _, fn := range subs {
		safeCall(fn, e)
	}
}

func (b *syncBus) Subscribe(fn func(Event)) {
	if fn == nil {
		return
	}
	b.mu.Lock()
	// Copy on append, so a concurrent Publish reading the previous
	// snapshot of subscribers does not race against this append.
	next := make([]func(Event), len(b.subscribers)+1)
	copy(next, b.subscribers)
	next[len(b.subscribers)] = fn
	b.subscribers = next
	b.mu.Unlock()
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

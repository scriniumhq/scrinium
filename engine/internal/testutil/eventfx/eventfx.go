package eventfx

import (
	"sync"

	"github.com/rkurbatov/scrinium/engine/event"
)

// Recorder is an event.EventBus implementation that captures every
// Publish call for later assertion. Concurrency-safe: tests that
// stress-publish from goroutines can still inspect the slice
// after a sync barrier (e.g., after the producer goroutines have
// joined).
//
// Subscribe is a no-op — the Recorder records, it does not forward.
// If a test needs both recording and forwarding, wrap it in a
// composite bus that publishes to the Recorder and the real bus.
type Recorder struct {
	mu     sync.Mutex
	events []event.Event
}

// New returns an empty Recorder ready to use as event.EventBus.
func New() *Recorder {
	return &Recorder{}
}

// Publish records the event. Always succeeds; never panics.
func (r *Recorder) Publish(e event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Subscribe is a no-op. The Recorder is not a forwarding bus.
func (r *Recorder) Subscribe(fn func(event.Event)) {}

// All returns a snapshot of every recorded event in publish order.
// Modifying the returned slice has no effect on the Recorder.
func (r *Recorder) All() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]event.Event, len(r.events))
	copy(out, r.events)
	return out
}

// ByType returns recorded events whose Type matches t, in publish
// order. Common idiom: assert exactly one event of a given type.
func (r *Recorder) ByType(t string) []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []event.Event
	for _, e := range r.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Count returns the number of recorded events of the given type.
// Equivalent to len(r.ByType(t)) but avoids the slice allocation.
func (r *Recorder) Count(t string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Type == t {
			n++
		}
	}
	return n
}

// Clear discards every recorded event. Useful in long-running
// tests that exercise multiple phases and want to assert the
// events of each phase in isolation.
func (r *Recorder) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// Compile-time guard.
var _ event.EventBus = (*Recorder)(nil)

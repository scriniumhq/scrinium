package store

import (
	"scrinium.dev/engine/event"
)

// events.go — the publish helper. The event-type constants and
// payload structs live in store_payloads.go (portable data); this
// file holds only the *store-bound dispatch, which cannot leave
// store.

// publish emits an event when a Publisher is configured. Cheap when
// nil — the common case for tests and minimal-stack hosts.
func (s *store) publish(typ string, payload any) {
	if s.pub == nil {
		return
	}
	s.pub.Publish(event.Event{Type: typ, Payload: payload})
}

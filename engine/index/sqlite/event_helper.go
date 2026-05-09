package sqlite

import "github.com/rkurbatov/scrinium/engine/event"

// eventOf builds an event.Event. Centralised so other files in the
// package call publish with just (type, payload).
func eventOf(typ string, payload any) event.Event {
	return event.Event{Type: typ, Payload: payload}
}

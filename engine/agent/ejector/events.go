package ejector

import (
	"scrinium.dev/domain"
	"scrinium.dev/event"
)

func (a *ejectorAgent) emitEjected(id domain.ArtifactID, ch, path, method string, start, length int64) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(event.Event{Type: event.EventArtifactEjected, Payload: event.ArtifactEjectedPayload{
		AgentType: "ejector", StoreID: a.storeID, ArtifactID: id,
		ContentHash: ch, Path: path, Method: method, Start: start, Length: length,
	}})
}

func (a *ejectorAgent) emitFailed(id domain.ArtifactID, err error) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(event.Event{Type: event.EventEjectFailed, Payload: event.EjectFailedPayload{
		AgentType: "ejector", StoreID: a.storeID, ArtifactID: id, Err: err,
	}})
}

func (a *ejectorAgent) emitCleanup(counts map[string]int) {
	if a.bus == nil {
		return
	}
	for reason, n := range counts {
		if n == 0 {
			continue
		}
		a.bus.Publish(event.Event{Type: event.EventEjectorCleanup, Payload: event.EjectorCleanupPayload{
			AgentType: "ejector", StoreID: a.storeID, Evicted: int64(n), Reason: reason,
		}})
	}
}

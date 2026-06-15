package wrapper

import (
	"scrinium.dev/engine/store"
	"scrinium.dev/event"
)

// Deps are the dependencies provided by the multistore to a
// decorator at registration time.
type Deps struct {
	Publisher event.Publisher
}

// Factory creates a decorator on top of a Store while
// receiving its dependencies from the multistore. It is applied
// during Target/Backup registration through WithStore/WithBackup,
// giving decorators access to their dependencies through a standard
// contract rather than via public objects.
type Factory interface {
	Wrap(store store.DataStore, deps Deps) (store.DataStore, error)
}

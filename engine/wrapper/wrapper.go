package wrapper

import (
	"fmt"

	"scrinium.dev/engine/store"
	"scrinium.dev/event"
	reg "scrinium.dev/internal/registry"
)

// Deps are the dependencies the assembler/multistore provides to a
// decorator at registration time. Per ADR-88 the index channel was
// removed — a CustomIndex registers itself directly in the StoreIndex —
// so WrapperDeps is just the event Publisher (ADR-75).
type Deps struct {
	Publisher event.Publisher
}

// Class is the single piece of descriptor metadata the Rules Engine
// consults to order and validate a wrapper stack (ADR-75). It does NOT
// determine capabilities: what a wrapper can do is given by the
// sub-interfaces it implements (Projector/Resolver/GCParticipant/
// Compactor), probed by assertion (ADR-84), not by Class.
type Class uint8

const (
	// Structural wrappers change blob physics/structure. The set is
	// CLOSED to {chunker, bundler}: their order is forced
	// chunker → bundler → store, and a new structural wrapper requires a
	// new ADR (physics is an identity/dedup invariant).
	Structural Class = iota
	// Behavioral wrappers decorate without touching physics (AuthGate,
	// namespace, audit-log, metrics, tracing, …). The set is OPEN and
	// order-free; it is the main surface for custom extensions.
	Behavioral
)

// String renders a Class for diagnostics and Rules Engine errors.
func (c Class) String() string {
	switch c {
	case Structural:
		return "structural"
	case Behavioral:
		return "behavioral"
	default:
		return fmt.Sprintf("Class(%d)", uint8(c))
	}
}

// Descriptor names a wrapper and carries its Class. It is the wrapper's
// self-description (Factory.Descriptor), used for registry keying and
// Rules Engine validation.
type Descriptor struct {
	Name  string
	Class Class
}

// Factory creates a decorator on top of a DataStore, receiving its
// dependencies from the assembler through a standard contract rather
// than via public objects. It self-describes through Descriptor so the
// Rules Engine can order and validate a stack without constructing it.
type Factory interface {
	// Wrap decorates inner, returning the outer DataStore.
	Wrap(inner store.DataStore, deps Deps) (store.DataStore, error)
	// Descriptor reports the wrapper's name and class.
	Descriptor() Descriptor
}

var registry = reg.New[Factory]()

// Register installs a Factory under its Descriptor().Name for
// blank-import wiring (ADR-63), the way drivers and agents register. It
// panics on a nil factory, an empty name, or a duplicate — all
// programmer errors at wiring time.
func Register(f Factory) {
	if f == nil {
		panic("wrapper.Register: nil factory")
	}
	name := f.Descriptor().Name
	if name == "" {
		panic("wrapper.Register: empty wrapper name")
	}
	if !registry.SetFirstWins(name, f) {
		panic(fmt.Sprintf("wrapper.Register: duplicate wrapper %q", name))
	}
}

// Lookup returns the Factory registered under name.
func Lookup(name string) (Factory, bool) {
	return registry.Get(name)
}

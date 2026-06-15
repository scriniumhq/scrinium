package assembly

import (
	"context"
	"sync"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/pipeline"
)

// CustomIndex factory signatures. Hosts register implementations through
// the Register* functions (typically in an init()), after which the
// corresponding scheme/kind works in a config document alongside the
// built-ins.
//
// Built-in backends (file://, s3://, sqlite://, postgres://) and the
// built-in pipeline stages (hash/compress/crypto) are NOT registered
// here — they are resolved directly by build through the engine's own
// dialers and stage packages. These registries hold third-party
// custom indexes only; build consults them before falling back to the
// built-in path.
type (
	// DriverFactory builds a Driver from a URI and resolved
	// credentials (SecretRefs already turned into bytes, keyed by
	// credential name).
	DriverFactory func(ctx context.Context, uri string, creds map[string][]byte) (driver.Driver, error)

	// IndexFactory builds a StoreIndex from a URI and resolved
	// credentials.
	IndexFactory func(ctx context.Context, uri string, creds map[string][]byte) (index.StoreIndex, error)

	// PipelineStageFactory builds a transformer factory for an
	// explicit/extra pipeline stage from its config params.
	PipelineStageFactory func(params map[string]any) (pipeline.TransformerFactory, error)

	// AgentFactory builds a user background agent bound to the
	// assembled stack, from its config block.
	AgentFactory func(a Assembly, config map[string]any) (any, error)
)

// registries holds the process-wide custom index tables. A single guard
// covers all four — registration is a startup-time, low-contention
// operation.
type registries struct {
	mu      sync.RWMutex
	drivers map[string]DriverFactory
	indexes map[string]IndexFactory
	stages  map[string]PipelineStageFactory
	agents  map[string]AgentFactory
}

var reg = &registries{
	drivers: map[string]DriverFactory{},
	indexes: map[string]IndexFactory{},
	stages:  map[string]PipelineStageFactory{},
	agents:  map[string]AgentFactory{},
}

// RegisterDriver registers a custom index driver under a URI scheme
// (e.g. "myco-blob"). Panics on empty scheme, nil factory, or
// duplicate — a double import or typo fails at startup.
func RegisterDriver(scheme string, f DriverFactory) {
	mustRegister(scheme, f == nil, "driver", func() {
		reg.drivers[scheme] = f
	})
}

// RegisterIndex registers a custom index index under a URI scheme.
func RegisterIndex(scheme string, f IndexFactory) {
	mustRegister(scheme, f == nil, "index", func() {
		reg.indexes[scheme] = f
	})
}

// RegisterPipelineStage registers a custom index pipeline stage under a
// kind (e.g. "mycompany-watermark").
func RegisterPipelineStage(kind string, f PipelineStageFactory) {
	mustRegister(kind, f == nil, "pipeline stage", func() {
		reg.stages[kind] = f
	})
}

// RegisterAgent registers a user background agent under a kind.
func RegisterAgent(kind string, f AgentFactory) {
	mustRegister(kind, f == nil, "agent", func() {
		reg.agents[kind] = f
	})
}

func mustRegister(key string, nilFactory bool, what string, set func()) {
	if key == "" {
		panic("scrinium: empty " + what + " key")
	}
	if nilFactory {
		panic("scrinium: nil " + what + " factory for " + key)
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	set()
}

func (r *registries) driver(scheme string) (DriverFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.drivers[scheme]
	return f, ok
}

func (r *registries) indexFor(scheme string) (IndexFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.indexes[scheme]
	return f, ok
}

func (r *registries) stage(kind string) (PipelineStageFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.stages[kind]
	return f, ok
}

func (r *registries) agent(kind string) (AgentFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.agents[kind]
	return f, ok
}

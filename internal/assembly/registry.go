package assembly

import (
	"context"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/extension"
	kvreg "scrinium.dev/internal/registry"
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

	// ExtensionFactory builds a fresh extension.Extension. The composition
	// root registers one per built-in/third-party extension (ADR-63); build
	// instantiates a fresh whole per assembly, so each store gets its own
	// index handle. The assembler installs and discovers extensions
	// generically (ADR-88/98) — it special-cases none.
	ExtensionFactory func() extension.Extension
)

// registries holds the process-wide custom index tables. Each is an
// independent concurrency-safe registry.Map; registration is a
// startup-time, low-contention operation.
type registries struct {
	drivers    *kvreg.Map[DriverFactory]
	indexes    *kvreg.Map[IndexFactory]
	stages     *kvreg.Map[PipelineStageFactory]
	agents     *kvreg.Map[AgentFactory]
	extensions *kvreg.Map[ExtensionFactory]
}

var globalRegistry = &registries{
	drivers:    kvreg.New[DriverFactory](),
	indexes:    kvreg.New[IndexFactory](),
	stages:     kvreg.New[PipelineStageFactory](),
	agents:     kvreg.New[AgentFactory](),
	extensions: kvreg.New[ExtensionFactory](),
}

// RegisterDriver registers a custom index driver under a URI scheme
// (e.g. "myco-blob"). Panics on empty scheme, nil factory, or
// duplicate — a double import or typo fails at startup.
func RegisterDriver(scheme string, f DriverFactory) {
	register(globalRegistry.drivers, scheme, f, f == nil, "driver")
}

// RegisterIndex registers a custom index index under a URI scheme.
func RegisterIndex(scheme string, f IndexFactory) {
	register(globalRegistry.indexes, scheme, f, f == nil, "index")
}

// RegisterPipelineStage registers a custom index pipeline stage under a
// kind (e.g. "mycompany-watermark").
func RegisterPipelineStage(kind string, f PipelineStageFactory) {
	register(globalRegistry.stages, kind, f, f == nil, "pipeline stage")
}

// RegisterAgent registers a user background agent under a kind.
func RegisterAgent(kind string, f AgentFactory) {
	register(globalRegistry.agents, kind, f, f == nil, "agent")
}

// RegisterExtension registers an extension factory under its stable name
// (the Descriptor name, e.g. "fs"). The composition root calls this —
// typically from an init() — so the assembler stays extension-agnostic:
// build pulls the registered set, installs each as a whole (ADR-88), and
// discovers its index/view/wrapper/agent parts by assertion (ADR-98).
func RegisterExtension(name string, f ExtensionFactory) {
	register(globalRegistry.extensions, name, f, f == nil, "extension")
}

// register validates the key/factory and installs it first-wins. A
// duplicate key panics (a double import or typo fails at startup), as do
// an empty key or a nil factory. nilFactory is computed at the call site
// because a typed-nil func value can only be compared to nil there.
func register[V any](r *kvreg.Map[V], key string, f V, nilFactory bool, what string) {
	if key == "" {
		panic("scrinium: empty " + what + " key")
	}
	if nilFactory {
		panic("scrinium: nil " + what + " factory for " + key)
	}
	if !r.SetFirstWins(key, f) {
		panic("scrinium: duplicate " + what + " " + key)
	}
}

func (r *registries) driver(scheme string) (DriverFactory, bool) { return r.drivers.Get(scheme) }

func (r *registries) indexFor(scheme string) (IndexFactory, bool) { return r.indexes.Get(scheme) }

func (r *registries) stage(kind string) (PipelineStageFactory, bool) { return r.stages.Get(kind) }

func (r *registries) agent(kind string) (AgentFactory, bool) { return r.agents.Get(kind) }

// extensionList instantiates every registered extension, in stable name
// order, as fresh wholes for one assembly. build installs them through the
// generic extension loop; no extension is special-cased in the assembler.
func (r *registries) extensionList() []extension.Extension {
	names := r.extensions.Keys() // sorted
	out := make([]extension.Extension, 0, len(names))
	for _, name := range names {
		f, _ := r.extensions.Get(name)
		out = append(out, f())
	}
	return out
}

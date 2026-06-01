// Package scrinium is the front door to a Scrinium store. It turns a
// configuration — a YAML/JSON document or a Config built in code — into
// a live, ready-to-use store, and hands it back as a *ScriniumClient.
//
// A *ScriniumClient IS a store: it embeds store.Store, so Put/Get/Walk
// and the Admin() facet are called directly on it (c.Put(...), not
// c.Store().Put(...)). The only behavioural difference from a
// hand-assembled store is Close: c.Close() cascades, releasing the
// store, its index, and the projection together — the wiring the
// assembler owns because it created those pieces.
//
// Projection is non-nil only when the configuration asked for one
// (a `projection:` section); store-only consumers ignore it.
//
//	c, err := scrinium.Open(ctx, "file:///data/app")
//	defer c.Close()
//	c.Put(ctx, scrinium.Artifact{Payload: r}, scrinium.WithNamespace("demo"))
//
// Built-in backends register by blank import, as in database/sql; pull
// in the ones a deployment uses (ADR-63):
//
//	import (
//	    _ "scrinium.dev/engine/driver/localfs"
//	    _ "scrinium.dev/engine/index/sqlite"
//	)
package scrinium

import (
	"context"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/assembly"
	"scrinium.dev/projection"
)

// ScriniumClient is a live, assembled store. Obtain one from Open /
// Build / Load*; release it with Close.
type ScriniumClient struct {
	// Store is embedded: a *ScriniumClient is a store. Put/Get/Walk/
	// Capacity and Admin() are promoted, so callers use c.Put(...)
	// directly.
	store.Store

	// Projection is the read/write filesystem projection over the
	// store. Nil when the configuration had no `projection:` section —
	// store-only consumers never touch it.
	Projection *projection.Projection

	// MountSession is the boot-unique identifier for this assembly,
	// surfaced for daemons that report it in diagnostics.
	MountSession domain.SessionID

	// Info is assembly metadata for diagnostics (store URI, namespace,
	// editing policy, read-only flag, fresh-create flag).
	Info Info

	// asm is retained for its cascading Close, which unwinds the
	// store, index, and projection in the correct order, and for the
	// init-time RecoveryKit handoff. The embedded store.Store.Close
	// (which does NOT close the index, by design) is shadowed by
	// (*ScriniumClient).Close below.
	asm assembly.Assembly
}

// Artifact is the unit a client stores and reads back. Aliased from the
// domain package so a hello-world program can stay in one import.
type Artifact = domain.Artifact

// WithNamespace scopes a Put to a namespace. Re-exported from the store
// package so single-package programs need not import it directly.
var WithNamespace = domain.WithNamespace

// Info is assembly metadata an app may surface in diagnostics.
type Info = assembly.Info

// Config is the programmatic configuration shape. Re-exported so
// callers build it without importing the internal package.
type Config = assembly.Config

// StoreSpec describes a single store (driver + index + policy). Re-exported
// so Open/Build callers can construct a Config without the internal package.
type StoreSpec = assembly.StoreSpec

// Mode selects open/init behaviour for Build.
type Mode = assembly.Mode

const (
	ModeOpenOrInit = assembly.ModeOpenOrInit
	ModeOpen       = assembly.ModeOpen
	ModeInit       = assembly.ModeInit
)

// wrap turns an assembled Assembly into the public *ScriniumClient handle.
func wrap(a assembly.Assembly) *ScriniumClient {
	return &ScriniumClient{
		Store:        a.Store(),
		Projection:   a.Projection(),
		MountSession: a.MountSession(),
		Info:         a.Info(),
		asm:          a,
	}
}

// Close cascades: it releases the projection, the store, and the
// index together. It shadows the embedded store.Store.Close, which
// closes only the store (the index lifetime belongs to the host — here
// the assembler — so the cascade lives at this level).
func (c *ScriniumClient) Close() error {
	if c.asm == nil {
		return nil
	}
	return c.asm.Close()
}

// Extensions lists the index extensions registered on the backing
// store index, for diagnostics (e.g. listing them on a stats page).
// Empty when the index backend exposes none. The raw StoreIndex is
// intentionally not surfaced — it carries mutating methods.
func (c *ScriniumClient) Extensions() []index.ExtensionInfo { return c.asm.Extensions() }

// RecoveryKit returns the recovery-kit bytes produced when this client
// freshly initialised an encrypted store, and true. For a store that
// was opened (not created) or is unencrypted it returns (nil, false).
//
// The host MUST persist a returned kit out of band: it is the only path
// back into the store if the passphrase is lost. The same bytes are
// available later through the Admin facet's ExportRecoveryKit; this
// accessor is the convenience for the create path, paired with
// Info.Created.
func (c *ScriniumClient) RecoveryKit() ([]byte, bool) { return c.asm.RecoveryKit() }

// Open assembles a store from a single driver URI, creating it if
// absent (ModeOpenOrInit). The simplest entry point — no config
// document, no projection.
//
//	c, err := scrinium.Open(ctx, "file:///data/app")
func Open(ctx context.Context, driverURI string, opts ...BuildOption) (*ScriniumClient, error) {
	return Build(ctx, Config{Store: &StoreSpec{Driver: driverURI}}, opts...)
}

// Build assembles a store from a programmatic Config.
func Build(ctx context.Context, cfg Config, opts ...BuildOption) (*ScriniumClient, error) {
	a, err := assembly.Build(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}
	return wrap(a), nil
}

// BuildOption tunes Build/Open (e.g. WithMode).
type BuildOption = assembly.BuildOption

// WithMode sets the open/init behaviour (default ModeOpenOrInit).
func WithMode(m Mode) BuildOption { return assembly.WithMode(m) }

// LoadYAML / LoadInitYAML / LoadOrInitYAML assemble from a YAML
// configuration document. JSON variants mirror them.
func LoadYAML(ctx context.Context, data []byte) (*ScriniumClient, error) {
	return wrapErr(assembly.LoadYAML(ctx, data))
}

func LoadInitYAML(ctx context.Context, data []byte) (*ScriniumClient, error) {
	return wrapErr(assembly.LoadInitYAML(ctx, data))
}

func LoadOrInitYAML(ctx context.Context, data []byte) (*ScriniumClient, error) {
	return wrapErr(assembly.LoadOrInitYAML(ctx, data))
}

func LoadJSON(ctx context.Context, data []byte) (*ScriniumClient, error) {
	return wrapErr(assembly.LoadJSON(ctx, data))
}

func LoadInitJSON(ctx context.Context, data []byte) (*ScriniumClient, error) {
	return wrapErr(assembly.LoadInitJSON(ctx, data))
}

func LoadOrInitJSON(ctx context.Context, data []byte) (*ScriniumClient, error) {
	return wrapErr(assembly.LoadOrInitJSON(ctx, data))
}

func wrapErr(a assembly.Assembly, err error) (*ScriniumClient, error) {
	if err != nil {
		return nil, err
	}
	return wrap(a), nil
}

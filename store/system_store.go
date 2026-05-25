package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/store/driver"
	"scrinium.dev/store/index"
)

// maxSystemPointerSize caps how many bytes are read from a pointer
// file ("<algo>-<hex>\n") before treating it as corrupt.
const maxSystemPointerSize = 256

// systemSessionID is the fixed SessionID engine writers use for
// system artifacts.
const systemSessionID = domain.SessionID("init")

// ArtifactWriter persists a system artifact and returns its ArtifactID.
// skipIndex selects the unindexed write path (see WithoutIndex).
type ArtifactWriter func(
	ctx context.Context,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
	skipIndex bool,
) (domain.ArtifactID, error)

// InlineHandleFactory builds a ReadHandle over an inline manifest's payload.
type InlineHandleFactory func(domain.Manifest) domain.ReadHandle

type systemStore struct {
	drv        driver.Driver
	index      index.StoreIndex
	hashes     domain.HashRegistry
	cfg        domain.StoreConfig
	writeArt   ArtifactWriter
	makeHandle InlineHandleFactory
	log        *slog.Logger
}

// Compile-time check that the concrete type satisfies the contract.
var _ SystemStore = (*systemStore)(nil)

// newSystemStore wires the facade; writeArt and makeHandle inject the two
// store-layer primitives without coupling to *store internals.
func newSystemStore(
	drv driver.Driver,
	idx index.StoreIndex,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
	writeArt ArtifactWriter,
	makeHandle InlineHandleFactory,
	log *slog.Logger,
) *systemStore {
	return &systemStore{
		drv:        drv,
		index:      idx,
		hashes:     hashes,
		cfg:        cfg,
		writeArt:   writeArt,
		makeHandle: makeHandle,
		log:        log,
	}
}

// namespaceForName resolves a system-store name to its physical
// namespace:
//
//	"config/*" → system.config (versioned configuration history)
//	everything else (cursors, snapshots, scrub/, gc/, ingester/,
//	snapshot/, maintenance/, index_snapshot/) → system.state
//
// Pack manifests (system.manifests) and HostStorage transit
// (system.transit) are not addressable through SystemStore — they
// have specialised access paths.
func namespaceForName(name string) (string, error) {
	if err := validateSystemName(name); err != nil {
		return "", err
	}
	if strings.HasPrefix(name, "config/") {
		return domain.NamespaceSystemConfig, nil
	}
	return domain.NamespaceSystemState, nil
}

// validateSystemName enforces the name contract for SystemStore.
// Names are slash-separated path-like strings, must be non-empty,
// must not contain ".", "..", or empty segments, must not start
// or end with "/". The first segment categorises the artifact
// (config/, scrub/, gc/, snapshot/, ...); subsequent segments are
// caller-defined.
func validateSystemName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", errs.ErrInvalidSystemName)
	}
	if name[0] == '/' || name[len(name)-1] == '/' {
		return fmt.Errorf("%w: %q has leading or trailing slash",
			errs.ErrInvalidSystemName, name)
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("%w: %q has empty segment",
			errs.ErrInvalidSystemName, name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q has traversal segment",
				errs.ErrInvalidSystemName, name)
		}
	}
	return nil
}

// pointerPath returns the driver path of the pointer file for the
// given name. Pointers live in a dedicated `pointers/` subtree of the
// physical namespace, kept separate from manifest files and from lease
// files that share the namespace.
//
// Special case: `system.config/current` keeps a flat path — it is the
// single mutable file in the store and its on-disk format predates the
// pointers/ subtree.
func pointerPath(name string) (string, error) {
	ns, err := namespaceForName(name)
	if err != nil {
		return "", err
	}
	if ns == domain.NamespaceSystemConfig && name == "config/current" {
		return domain.NamespaceSystemConfig + "/current", nil
	}
	return ns + "/pointers/" + name, nil
}

func (ss *systemStore) Put(ctx context.Context, a SystemArtifact, opts ...SystemPutOption) error {
	name, payload := a.Name, a.Payload

	o := SystemPutConfig{}
	for _, opt := range opts {
		opt.ApplySystemPut(&o)
	}

	ns, err := namespaceForName(name)
	if err != nil {
		return err
	}
	ptrPath, err := pointerPath(name)
	if err != nil {
		return err
	}

	prevID, prevErr := ss.readPointer(ctx, ptrPath)
	if prevErr != nil && !errors.Is(prevErr, errs.ErrArtifactNotFound) {
		return fmt.Errorf("system store: read pointer for %q: %w", name, prevErr)
	}

	// Inline artifacts need the whole payload in memory; system
	// payloads are small (cursors, configs).
	body, err := io.ReadAll(payload)
	if err != nil {
		return fmt.Errorf("system store: read payload for %q: %w", name, err)
	}

	newID, err := ss.writeArt(
		ctx, ns, systemSessionID, body,
		string(ss.cfg.ContentHasher), o.SkipIndex,
	)
	if err != nil {
		return fmt.Errorf("system store: put %q: %w", name, err)
	}

	// Flip the pointer (driver.Put is atomic).
	if err := ss.writePointer(ctx, ptrPath, newID); err != nil {
		return fmt.Errorf("system store: flip pointer for %q: %w", name, err)
	}

	// Best-effort: a stale predecessor is reclaimed by Orphan Scan;
	// the operation already succeeded once the pointer flipped.
	if prevID != "" && prevID != newID {
		ss.dropPredecessor(ctx, prevID)
	}
	return nil
}

func (ss *systemStore) Get(ctx context.Context, name string) (domain.ReadHandle, error) {
	ptrPath, err := pointerPath(name)
	if err != nil {
		return nil, err
	}
	id, err := ss.readPointer(ctx, ptrPath)
	if err != nil {
		return nil, err
	}
	return ss.readArtifact(ctx, id)
}

// Delete is idempotent.
func (ss *systemStore) Delete(ctx context.Context, name string) error {
	ptrPath, err := pointerPath(name)
	if err != nil {
		return err
	}
	id, err := ss.readPointer(ctx, ptrPath)
	if err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return nil
		}
		return fmt.Errorf("system store: delete %q: %w", name, err)
	}

	// Pointer first: once the pointer is gone, the artifact is
	// unreachable through SystemStore.Get. The manifest file
	// lingers until step 2 finishes (or until Orphan Scan does
	// if we crash).
	if err := ss.drv.Remove(ctx, ptrPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("system store: remove pointer %q: %w", name, err)
	}
	ss.dropPredecessor(ctx, id)
	return nil
}

func (ss *systemStore) Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error {
	// Determine which physical namespaces to scan. config/* lives
	// in system.config; everything else in system.state. An empty
	// prefix means "everything" — scan both.
	scanConfig := prefix == "" || prefix == "config" || strings.HasPrefix(prefix, "config/")
	scanState := prefix == "" || !(prefix == "config" || strings.HasPrefix(prefix, "config/"))

	var names []string
	if scanConfig {
		gathered, err := ss.gatherNames(ctx, domain.NamespaceSystemConfig, prefix)
		if err != nil {
			return err
		}
		names = append(names, gathered...)
	}
	if scanState {
		gathered, err := ss.gatherNames(ctx, domain.NamespaceSystemState, prefix)
		if err != nil {
			return err
		}
		names = append(names, gathered...)
	}

	sort.Strings(names)

	for _, name := range names {
		ptrPath, err := pointerPath(name)
		if err != nil {
			continue
		}
		id, err := ss.readPointerAt(ctx, ptrPath)
		if err != nil {
			continue
		}
		m, err := ss.loadManifest(ctx, id)
		if err != nil {
			continue
		}
		if err := cb(name, m); err != nil {
			return err
		}
	}
	return nil
}

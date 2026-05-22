package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
)

// maxSystemPointerSize caps how many bytes are read from a pointer
// file ("<algo>-<hex>\n") before treating it as corrupt.
const maxSystemPointerSize = 256

// systemSessionID is the fixed SessionID engine writers use for
// system artifacts. Matches the historic value used by the config
// writer so existing on-disk data is compatible.
const systemSessionID = domain.SessionID("init")

// ArtifactWriter persists a system artifact in the given namespace
// and returns its ArtifactID. skipIndex selects the unindexed write
// path (for artifacts whose presence in StoreIndex would be
// redundant or harmful — e.g. index snapshots). Injected by the
// store layer, which owns the low-level inline-artifact primitives
// (shared with the config writer).
type ArtifactWriter func(
	ctx context.Context,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
	skipIndex bool,
) (domain.ArtifactID, error)

// InlineHandleFactory builds a ReadHandle over an inline
// manifest's payload. Injected by the store layer, which owns the
// concrete inlineReadHandle type (shared with the Get path).
type InlineHandleFactory func(domain.Manifest) ReadHandle

// systemStore is the SystemStore implementation bound to a
// store's driver, index, and hash registry.
type systemStore struct {
	drv        driver.Driver
	index      index.StoreIndex
	hashes     domain.HashRegistry
	cfg        domain.StoreConfig
	writeArt   ArtifactWriter
	makeHandle InlineHandleFactory
}

// newSystemStore wires the facade to its dependencies. Called once per Store
// during construction. writeArt and makeHandle inject the two
// store-layer primitives the implementation needs without coupling
// to *store internals.
func newSystemStore(
	drv driver.Driver,
	idx index.StoreIndex,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
	writeArt ArtifactWriter,
	makeHandle InlineHandleFactory,
) *systemStore {
	return &systemStore{
		drv:        drv,
		index:      idx,
		hashes:     hashes,
		cfg:        cfg,
		writeArt:   writeArt,
		makeHandle: makeHandle,
	}
}

// --- name validation and prefix mapping ---

// namespaceForName resolves a system-store name to its physical
// namespace. Per ADR-57:
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
// given name. Per ADR-57 (docs/3 Reference/01 Core §SystemStore.Walk)
// pointers live in a dedicated `pointers/` subtree of the physical
// namespace — keeps the pointer index separate from manifest files
// and from lease files that share the namespace.
//
// Special case: `system.config/current` keeps its historic flat
// path per docs §10.1.4. It is the single mutable file in the
// store and its on-disk format was frozen before ADR-57.
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

// --- Put ---

// Put implements SystemStore.Put.
func (ss *systemStore) Put(ctx context.Context, name string, payload io.Reader, opts ...SystemPutOption) error {
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

	// 1. Record predecessor (if any) for the post-flip cleanup.
	prevID, prevErr := ss.readPointer(ctx, ptrPath)
	if prevErr != nil && !errors.Is(prevErr, errs.ErrArtifactNotFound) {
		return fmt.Errorf("system store: read pointer for %q: %w", name, prevErr)
	}

	// 2. Buffer the payload. Inline artifacts mandate a complete
	//    byte slice; SystemStore consumers always carry small
	//    payloads (cursors, configs), so reading into memory is
	//    appropriate.
	body, err := io.ReadAll(payload)
	if err != nil {
		return fmt.Errorf("system store: read payload for %q: %w", name, err)
	}

	// 3. Write the new artifact through the injected writer. It
	//    writes the manifest file and (unless o.SkipIndex) indexes it.
	newID, err := ss.writeArt(
		ctx, ns, systemSessionID, body,
		string(ss.cfg.ContentHasher), o.SkipIndex,
	)
	if err != nil {
		return fmt.Errorf("system store: put %q: %w", name, err)
	}

	// 4. Flip the pointer. driver.Put is atomic per the driver
	//    contract (LocalFS: rename; S3: PutObject).
	if err := ss.writePointer(ctx, ptrPath, newID); err != nil {
		return fmt.Errorf("system store: flip pointer for %q: %w", name, err)
	}

	// 5. Drop the predecessor manifest. Best-effort: a failure
	//    here leaves an orphan that Orphan Scan reclaims; the
	//    user-visible operation has already succeeded by the time
	//    the pointer flipped.
	if prevID != "" && prevID != newID {
		ss.dropPredecessor(ctx, prevID)
	}
	return nil
}

// --- Get ---

// Get implements SystemStore.Get.
func (ss *systemStore) Get(ctx context.Context, name string) (ReadHandle, error) {
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

// --- Delete ---

// Delete implements SystemStore.Delete. Idempotent.
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

// --- Walk ---

// Walk implements SystemStore.Walk. Iterates over every name
// with the given prefix in alphabetical order, yielding the
// underlying manifest. Empty prefix scans every name.
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

// gatherNames lists pointer paths under namespace/pointers/<prefix>
// and returns the namespace-relative names (post-ADR-57 layout).
//
// For system.config the historic flat pointer "config/current"
// (§10.1.4) is surfaced separately when the prefix admits it,
// since it does not live under pointers/.
func (ss *systemStore) gatherNames(ctx context.Context, namespace, prefix string) ([]string, error) {
	prefixPath := namespace + "/pointers/"
	if prefix != "" {
		prefixPath = namespace + "/pointers/" + prefix
	}
	paths, err := ss.drv.List(ctx, prefixPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("system store: list %q: %w", prefixPath, err)
	}
	out := make([]string, 0, len(paths))
	pointersPrefix := namespace + "/pointers/"
	for _, p := range paths {
		name := strings.TrimPrefix(p, pointersPrefix)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	if namespace == domain.NamespaceSystemConfig &&
		(prefix == "" || prefix == "config" || prefix == "config/" || strings.HasPrefix("config/current", prefix)) {
		if _, err := ss.drv.Stat(ctx, namespace+"/current"); err == nil {
			out = append(out, "config/current")
		}
	}
	return out, nil
}

// --- internal helpers ---

// readPointer reads the pointer file at the given driver path and
// parses its content as an ArtifactID. Returns errs.ErrArtifactNotFound
// when the pointer file is absent.
func (ss *systemStore) readPointer(ctx context.Context, ptrPath string) (domain.ArtifactID, error) {
	id, err := ss.readPointerAt(ctx, ptrPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errs.ErrArtifactNotFound
		}
		return "", err
	}
	return id, nil
}

// readPointerAt is the unwrapped form: surfaces os.ErrNotExist
// directly. Used by Walk where the not-found case is common.
func (ss *systemStore) readPointerAt(ctx context.Context, ptrPath string) (domain.ArtifactID, error) {
	rc, err := ss.drv.Get(ctx, ptrPath)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxSystemPointerSize))
	if err != nil {
		return "", fmt.Errorf("read pointer: %w", err)
	}
	idStr := strings.TrimSpace(string(raw))
	if idStr == "" {
		return "", errs.ErrCorruptedConfigPointer
	}
	if _, _, err := ss.hashes.Parse(idStr); err != nil {
		return "", fmt.Errorf("%w: %v", errs.ErrCorruptedConfigPointer, err)
	}
	return domain.ArtifactID(idStr), nil
}

// writePointer writes the pointer file at ptrPath to contain
// "id\n". Driver.Put is atomic per the driver contract.
func (ss *systemStore) writePointer(ctx context.Context, ptrPath string, id domain.ArtifactID) error {
	body := []byte(string(id) + "\n")
	return ss.drv.Put(ctx, ptrPath, bytes.NewReader(body))
}

// readArtifact opens the manifest file for the given id, returning
// a ReadHandle over the inline payload. SystemStore artifacts are
// always inline (the inline-artifact writer's contract).
func (ss *systemStore) readArtifact(ctx context.Context, id domain.ArtifactID) (ReadHandle, error) {
	m, err := ss.loadManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		return nil, fmt.Errorf("system store: expected inline layout for %s, got %q",
			id, m.LayoutHeader.BlobStorage)
	}
	return ss.makeHandle(m), nil
}

// loadManifest reads and verifies the manifest file for the given
// ArtifactID.
func (ss *systemStore) loadManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, error) {
	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("manifest path: %w", err)
	}
	rc, err := ss.drv.Get(ctx, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Manifest{}, errs.ErrArtifactNotFound
		}
		return domain.Manifest{}, fmt.Errorf("get manifest: %w", err)
	}
	defer rc.Close()
	fileBytes, err := io.ReadAll(rc)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if err := manifestcodec.VerifyArtifactID(id, fileBytes, ss.hashes); err != nil {
		return domain.Manifest{}, fmt.Errorf("verify manifest: %w", err)
	}
	m, err := manifestcodec.DecodeFile(fileBytes)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	m.ArtifactID = id
	return m, nil
}

// dropPredecessor removes the manifest file and the index row (if
// any) for an artifact that has been superseded. Best-effort:
// failures are not propagated, because the user-visible operation
// has already succeeded — Orphan Scan picks up any survivors.
//
// The index row may or may not exist (depending on whether the
// predecessor was written with WithoutIndex). DeleteManifest with
// an unknown ArtifactID returns ErrArtifactNotFound, which we
// silently ignore.
func (ss *systemStore) dropPredecessor(ctx context.Context, id domain.ArtifactID) {
	// Best-effort: read the manifest to know its BlobRef (needed
	// for DeleteManifest's blobRefs argument). On read failure
	// skip indexed cleanup and remove the file anyway.
	m, err := ss.loadManifest(ctx, id)
	if err == nil {
		blobRefs := []string{string(m.BlobRef)}
		_ = ss.index.DeleteManifest(ctx, id, blobRefs)
	}
	if manifestPath, pErr := blobpath.ManifestPath(id); pErr == nil {
		_ = ss.drv.Remove(ctx, manifestPath)
	}
}

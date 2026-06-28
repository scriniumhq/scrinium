package fsops

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
)

// writeFile is a write-side File handle backed by an OS scratch
// file. WriteAt drains into the scratch and bumps the running
// quota; Close turns the scratch into a Store.Put and updates
// the View.
//
// Editing existing artifacts: when replaceArtifactID is non-empty,
// Close treats the operation as a replace — after the new Put it
// also calls Store.Delete(replaceArtifactID) and uses View.Move
// instead of View.Add. inheritedVfsmeta carries the vfsmeta of the
// pre-existing artifact so callers (Setattr, Rename) can inherit
// fields they don't explicitly mutate.
//
// Locks: a single-path Open holds one lock; Rename holds two
// (old + new) acquired in lex order via pathLocks.LockAll.
// The unlock function lives in `unlock` and is called once on
// Close regardless of which path produced the lock.
type writeFile struct {
	fsops       *Ops
	path        string
	scratchPath string
	handle      *os.File
	mode        uint32

	// unlock releases the path-level lock(s) held by this
	// handle. Set by the constructor (Create or open-for-edit
	// helpers); always called exactly once at Close end.
	unlock func()

	// Editing fields.
	replaceArtifactID domain.ArtifactID  // empty for new files
	oldPath           string             // empty for new files
	inheritedVfsmeta  vfsmeta.FileSystem // base for vfsmeta on Close

	// markDirty=true forces Close to perform a Put even when no
	// WriteAt happened. Used by Setattr/Rename where the content
	// is unchanged but metadata has to be re-emitted as a new
	// artifact.
	forceDirty bool

	mu     sync.Mutex
	size   int64 // logical scratch size as the writer sees it
	dirty  bool  // any successful WriteAt
	closed bool
}

// ReadAt reads from the scratch file. Useful for OpenReadWrite
// flows but in 4b primarily exists to satisfy the Handle contract.
func (f *writeFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fmt.Errorf("projection.Ops: file closed")
	}
	return f.handle.ReadAt(p, off)
}

// WriteAt drains data into the scratch at offset off. The quota
// is reserved against the *new* logical size, so a Write that
// would push total scratch usage above the quota returns
// ErrScratchQuota without touching the file.
func (f *writeFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fmt.Errorf("projection.Ops: file closed")
	}
	newEnd := off + int64(len(p))
	delta := newEnd - f.size
	if delta < 0 {
		delta = 0
	}
	if err := f.fsops.quota.Reserve(delta); err != nil {
		return 0, err
	}
	n, err := f.handle.WriteAt(p, off)
	if err != nil {
		// Roll back the reservation; the WriteAt may have
		// partially succeeded — n bytes are on disk, but we
		// account for the full delta because the caller will
		// see the error and likely close.
		f.fsops.quota.Release(delta)
		return n, err
	}
	if newEnd > f.size {
		f.size = newEnd
	}
	if n > 0 {
		f.dirty = true
	}
	return n, nil
}

// Truncate adjusts the scratch size. Stage 4b only allows
// truncating *new* files (the writeFile owns the scratch from
// Create); editing an existing file's size requires AllowTruncate
// and lives in 4c.
func (f *writeFile) Truncate(size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fmt.Errorf("projection.Ops: file closed")
	}
	if size > f.size {
		// Reserve the growth against the quota.
		if err := f.fsops.quota.Reserve(size - f.size); err != nil {
			return err
		}
	} else if size < f.size {
		f.fsops.quota.Release(f.size - size)
	}
	if err := f.handle.Truncate(size); err != nil {
		return err
	}
	f.size = size
	f.dirty = true
	return nil
}

// Sync flushes the scratch to the OS. The scratch is not yet a
// Store artifact; Sync here is purely about durability of the
// in-progress write buffer.
func (f *writeFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fmt.Errorf("projection.Ops: file closed")
	}
	return f.handle.Sync()
}

// Close finalises the handle. Behaviour depends on dirty and on
// whether the handle is editing an existing artifact:
//
//   - Clean (no successful WriteAt and no forceDirty): scratch is
//     deleted, no Put, the path is left untouched.
//   - Dirty + new file: Store.Put -> Store.Get -> View.Add.
//   - Dirty + editing (replaceArtifactID set): Store.Put ->
//     Store.Delete(replaceArtifactID) -> Store.Get -> View.Move.
//
// Quota reserved during writes is released either way. The path
// lock(s) are released last via the unlock closure.
func (f *writeFile) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	dirty := f.dirty || f.forceDirty
	size := f.size
	f.mu.Unlock()

	defer f.unlock()
	defer f.fsops.quota.Release(size)
	defer os.Remove(f.scratchPath)
	defer f.handle.Close()

	if !dirty {
		return nil
	}
	// Rewind the scratch so Store.Put can read from offset 0.
	if _, err := f.handle.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("projection.Ops: seek scratch: %w", err)
	}

	// Build vfsmeta. For editing, start from the inherited vfsmeta
	// (preserves MIME, plus any field not explicitly mutated by
	// the caller) and overlay the new path/mode. For new files,
	// inheritedVfsmeta is the zero value.
	vfsm := f.inheritedVfsmeta
	vfsm.Path = f.path
	if f.mode != 0 {
		vfsm.Mode = f.mode
	}
	// ModTime: for new files (no predecessor), stamp with the
	// current time. For editing, the caller has already placed
	// the desired ModTime into inheritedVfsmeta — Setattr writes
	// the user's value there explicitly, Rename inherits the old
	// artifact's value, and an arbitrary write through
	// openForEdit also keeps the inherited value. Overwriting
	// here would clobber Setattr's intent.
	if f.replaceArtifactID == "" {
		vfsm.ModTime = time.Now().UTC()
	}

	metadata, err := vfsmeta.Embed(nil, vfsm)
	if err != nil {
		return err
	}

	id, err := f.fsops.store.Put(
		context.Background(),
		domain.Artifact{
			Payload: f.handle,
			// vfsmeta is engine-custom index data per ADR-54 — Ext
			// block, not Usr (host-opaque).
			Ext: metadata,
		},
		domain.WithSession(f.fsops.mountSession),
		domain.WithBlobType(domain.BlobTypeRegular),
	)
	if err != nil {
		return err
	}

	// For editing paths, drop the predecessor before refetching
	// the new manifest. If Delete fails the new artifact is
	// already in place; surface the error so the caller can
	// observe the partial state — a subsequent reconciliation
	// (e.g. GC) will eventually drop the orphan.
	if f.replaceArtifactID != "" {
		if err := f.fsops.store.Delete(context.Background(), f.replaceArtifactID); err != nil {
			return fmt.Errorf("projection.Ops: delete predecessor: %w", err)
		}
	}

	// Fetch the resulting manifest to update the View.
	rh, err := f.fsops.store.Get(context.Background(), id)
	if err != nil {
		return fmt.Errorf("projection.Ops: refetch new manifest: %w", err)
	}
	manifest := rh.Manifest()
	rh.Close()

	if f.replaceArtifactID != "" {
		// Editing: Move handles both removal of the old by-path
		// owner (which Store.Delete already enforced separately)
		// and addition of the new manifest in every tree.
		if err := f.fsops.view.Move(f.oldPath, f.path, manifest); err != nil {
			return err
		}
	} else {
		if err := f.fsops.view.Add(manifest); err != nil {
			return err
		}
	}

	// If the new file lives inside a pending directory, the
	// pending entry is now redundant (View.Add/Move ran
	// ensureDirs). Drop the entry to keep state tidy.
	f.fsops.dropParentPendingDirs(f.path)

	return nil
}

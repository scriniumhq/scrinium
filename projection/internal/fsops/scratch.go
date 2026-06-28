package fsops

// scratch.go — Ops-side staging of edit/append operations: allocate a
// scratch file, preload it with the existing artifact's content and
// vfsmeta, and hand back a writeFile ready for mutation. These are Ops
// methods (not writeFile behaviour); they live apart from the writeFile
// handle implementation in writefile.go.

import (
	"context"
	"fmt"
	"io"
	"os"

	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
)

// prepareEditingScratch assembles a writeFile for editing the
// artifact at path: it allocates a scratch file, copies the
// existing content into it, decodes the existing vfsmeta, and
// returns the handle ready for further mutation by the caller.
//
// Caller responsibilities (filled in after the call):
//   - wf.unlock — overwrite if the caller manages locks externally.
//   - wf.path / wf.mode / wf.inheritedVfsmeta — mutate as the
//     editing operation requires.
//   - wf.forceDirty — set to true when no WriteAt will follow
//     (Setattr, Rename) so Close still performs a Put.
//
// On error the scratch is fully cleaned up.
func (o *Ops) prepareEditingScratch(ctx context.Context, path string) (*writeFile, error) {
	n, err := o.lookupInRoot(path)
	if err != nil {
		return nil, err
	}
	if n.FS.IsDir {
		return nil, fmt.Errorf("%w: %q", errs.ErrIsADirectory, path)
	}

	// Decode old vfsmeta to inherit non-mutated fields. A clean
	// failure here (no vfsmeta on the artifact) is acceptable —
	// the inherited struct stays zero, and Close re-encodes from
	// scratch; the artifact gains a fresh vfsmeta with only the
	// mutated fields plus path.
	var inherited vfsmeta.FileSystem
	if n.Artifact != nil {
		if fs, ok, _ := vfsmeta.Decode(n.Artifact.Ext); ok {
			inherited = fs
		}
	}

	scratchPath, scratchFile, err := o.newScratchFile()
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		scratchFile.Close()
		os.Remove(scratchPath)
	}

	// Copy content from the existing artifact into the scratch.
	rh, err := o.openInRoot(ctx, path)
	if err != nil {
		cleanup()
		return nil, err
	}
	written, err := io.Copy(scratchFile, rh)
	rh.Close()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("projection.Ops: stage scratch: %w", err)
	}
	// Reserve quota for the staged bytes. If the quota is
	// exceeded we fail before the caller has a chance to mutate.
	if err := o.quota.Reserve(written); err != nil {
		cleanup()
		return nil, err
	}

	return &writeFile{
		fsops:             o,
		path:              path,
		scratchPath:       scratchPath,
		handle:            scratchFile,
		mode:              inherited.Mode,
		unlock:            func() {}, // caller-managed by default
		replaceArtifactID: n.Artifact.ArtifactID,
		oldPath:           path,
		inheritedVfsmeta:  inherited,
		size:              written,
	}, nil
}

// newScratchFile creates a fresh scratch file in the configured
// directory. Returns the absolute path and the open *os.File.
func (o *Ops) newScratchFile() (string, *os.File, error) {
	dir := o.scratchDir
	if dir == "" {
		// Without an explicit scratch dir, use the OS temp dir.
		// Production callers always set this; tests may rely on
		// the default.
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("projection.Ops: mkdir scratch: %w", err)
	}
	f, err := os.CreateTemp(dir, "scratch-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("projection.Ops: create scratch: %w", err)
	}
	return f.Name(), f, nil
}

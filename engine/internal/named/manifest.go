package named

// manifest.go — building and loading the inline manifest that backs a system
// artifact. System payloads are always Inline + Plain (ContentHash == BlobRef
// == hash(payload)); the address is name+seq, so the manifest's own digest is
// only a byproduct of encoding, and integrity on read comes from re-hashing
// the inline payload against the manifest's embedded ContentHash.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

// sessionID is the fixed SessionID stamped on system inline manifests. System
// artifacts are addressed by name+seq, not by a user write session, and are
// never RollbackSession targets; the sentinel value is retained only because
// the manifest body carries a session field.
const sessionID = domain.SessionID("init")

// BuildInlineManifest constructs the encoded inline manifest for a system
// payload: an Inline blob manifest with an empty Pipeline (ContentHash ==
// BlobRef == hash(payload)), serialised to the bytes that ClaimVersion writes.
// name fills the identity slot (Manifest.Name) — this is what makes the
// manifest IsSystem() and lets validateSlot recognise it (ADR-104); the path
// (named/<name>.<seq>) remains the seq authority, so name is the only slot
// component carried in the manifest. It returns the encoded file bytes and the
// in-memory manifest. No disk write and no indexing happen here — the caller
// writes the bytes through ClaimVersion (or WriteCell).
//
// The serialised manifest's own digest is not the address (the address is
// name+seq), so it is computed only as a byproduct of encoding and discarded.
func BuildInlineManifest(name string, payload []byte, hashAlgo string, hashes domain.HashRegistry) ([]byte, domain.Manifest, error) {
	hasher, err := hashes.NewHasher(hashAlgo)
	if err != nil {
		return nil, domain.Manifest{}, fmt.Errorf("system artifact: content hasher: %w", err)
	}
	if _, err := hasher.Write(payload); err != nil {
		return nil, domain.Manifest{}, fmt.Errorf("system artifact: hash payload: %w", err)
	}
	contentHash := domain.ContentHash(hex.EncodeToString(hasher.Sum(nil)))

	m := domain.Manifest{
		Name:         name,
		SessionID:    sessionID,
		ContentHash:  contentHash,
		BlobRefs:     []domain.BlobRef{domain.BlobRef(contentHash)},
		OriginalSize: int64(len(payload)),
		InlineBlob:   payload,
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutInline},
		CreatedAt:    time.Now().UTC(),
	}

	_, fileBytes, m, err := artifact.ComputeManifestDigest(
		m, hashAlgo, hashes,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain,
		nil, "")
	if err != nil {
		return nil, domain.Manifest{}, fmt.Errorf("system artifact: encode: %w", err)
	}
	return fileBytes, m, nil
}

// Load reads, decodes, and verifies the manifest at a known version path.
// Verification re-hashes the inline payload against the manifest's embedded
// ContentHash (verify-on-read): there is no external digest to check against —
// the path is name+seq, not a content hash — so the manifest's self-described
// content hash is the integrity anchor. A missing file maps to
// errs.ErrArtifactNotFound.
func Load(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, path string) (domain.Manifest, error) {
	rc, err := drv.Get(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Manifest{}, errs.ErrArtifactNotFound
		}
		return domain.Manifest{}, fmt.Errorf("system artifact: get %q: %w", path, err)
	}
	defer rc.Close()

	fileBytes, err := io.ReadAll(io.LimitReader(rc, domain.MaxManifestSize+1))
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("system artifact: read %q: %w", path, err)
	}
	if len(fileBytes) > domain.MaxManifestSize {
		return domain.Manifest{}, fmt.Errorf("system artifact %q: %w", path, errs.ErrManifestTooLarge)
	}
	m, err := artifact.Decode(fileBytes)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("system artifact: decode %q: %w", path, err)
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		return domain.Manifest{}, fmt.Errorf("system artifact %q: expected inline layout, got %q",
			path, m.LayoutHeader.BlobStorage)
	}
	if err := verifyInlinePayload(m, hashes); err != nil {
		return domain.Manifest{}, fmt.Errorf("system artifact %q: %w", path, err)
	}
	return m, nil
}

// verifyInlinePayload recomputes the content hash of an inline manifest's
// payload and checks it against the manifest's declared ContentHash. For a
// system artifact the Pipeline is empty, so the content hash is just
// hash(InlineBlob).
func verifyInlinePayload(m domain.Manifest, hashes domain.HashRegistry) error {
	// ADR-93: ContentHash is bare hex; the algorithm is the manifest's
	// recorded hash_algo (populated on decode, which precedes this check).
	h, err := hashes.NewHasher(m.HashAlgo)
	if err != nil {
		return fmt.Errorf("%w: content hasher: %v", errs.ErrCorruptedContent, err)
	}
	if _, err := h.Write(m.InlineBlob); err != nil {
		return fmt.Errorf("%w: hash payload: %v", errs.ErrCorruptedContent, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != string(m.ContentHash) {
		return fmt.Errorf("%w: payload hash %s != declared %s", errs.ErrCorruptedContent, got, m.ContentHash)
	}
	return nil
}

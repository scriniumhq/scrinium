package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index/extension"
	"scrinium.dev/engine/internal/timefmt"
)

// IndexManifest registers an artifact in the index. Branches on
// manifest.Type:
//   - blob: upserts the blob row, increments ref_count, inserts the
//     manifest row, links manifest -> blob.
//   - toc:  same as blob plus increments ref_count for each chunkRef
//     and links manifest -> chunks (positional).
//   - pack: registers the pack itself as one blob and inserts a row
//     into packed_blobs for each entry; manifests of packed
//     artifacts are NOT inserted into the manifests table — packed
//     artifacts are reachable through LookupPacked, not through Walk.
//     (TRANSITIONAL: ADR-86 replaces packed_blobs with the bundler
//     Resolver map; the pack branch goes when the bundler is rebuilt.)
//
// Regardless of type, the manifest's HandleRefs (artifact→artifact
// edges, ADR-92) are linked into manifest_handles after the per-type
// step. россыпь and composite carry none; pack containers carry their
// members. The manifests table is keyed by ManifestDigest (the file
// hash); the floating handle and the system name are nullable slot
// columns (ADR-83/85/92).
//
// All work happens inside a single transaction; partial registration
// is impossible.
func (i *Index) IndexManifest(
	ctx context.Context,
	m domain.Manifest,
	addr domain.PhysicalAddress,
	chunkRefs []string,
	packedEntries []domain.PackedEntry,
) error {
	return i.observe("IndexManifest", func() error {
		return i.indexManifestTx(ctx, m, addr, chunkRefs, packedEntries)
	})
}

// indexManifestTx runs the actual transactional work. Split out
// for readability — IndexManifest stays a thin observability
// wrapper, while the transactional flow lives here at one level
// of nesting.
func (i *Index) indexManifestTx(
	ctx context.Context,
	m domain.Manifest,
	addr domain.PhysicalAddress,
	chunkRefs []string,
	packedEntries []domain.PackedEntry,
) error {
	return i.inTx(ctx, func(tx *sql.Tx) error {
		switch m.Type {
		case domain.ManifestTypeBlob:
			if err := indexBlobManifest(ctx, tx, m, addr); err != nil {
				return err
			}
		case domain.ManifestTypeTOC:
			if err := indexTOCManifest(ctx, tx, m, addr, chunkRefs); err != nil {
				return err
			}
		case domain.ManifestTypePack:
			if err := indexPackManifest(ctx, tx, m, addr, packedEntries); err != nil {
				return err
			}
		default:
			return fmt.Errorf("sqlite: unknown manifest type: %q", m.Type)
		}

		// Link the artifact→artifact edges (HandleRefs, ADR-92). Empty
		// for россыпь and composite; populated for pack containers
		// (placement of members). Pack volumes in the transitional
		// packed_blobs model insert no manifests row, so they also carry
		// no HandleRefs yet — the loop is a no-op until the bundler is
		// rebuilt to the container model.
		for pos, hr := range m.HandleRefs {
			if err := linkManifestToHandle(ctx, tx, m.Digest, hr, pos); err != nil {
				return err
			}
		}

		// Dispatch to subscribed extensions BEFORE commit. An error
		// from any extension rolls back the entire transaction —
		// strict-consistency guarantee per ADR-49. Pack manifests
		// are an internal type (not surfaced through Walk) so we
		// skip dispatch for them; extensions index user-visible
		// artifacts only.
		if m.Type != domain.ManifestTypePack {
			args := extension.EventArgs{Manifest: m, ArtifactID: m.ArtifactID}
			if err := i.dispatchExtensions(ctx, tx, extension.EventKindManifestIndexed, args); err != nil {
				return err
			}
		}
		return nil
	})
}

// upsertBlob inserts the blob row if missing. ref_count is bumped
// in a separate step so the insert/conflict semantics stay simple.
//
// We use ON CONFLICT(blob_ref) DO NOTHING to keep the call
// idempotent: re-running IndexManifest after a partial crash
// (where the blob row already exists but the manifest row does
// not) leaves the blob untouched and proceeds with the manifest.
func upsertBlob(
	ctx context.Context,
	tx *sql.Tx,
	blobRef string,
	contentHash domain.ContentHash,
	originalSize int64,
	crypto domain.CryptoIdentity,
	addr domain.PhysicalAddress,
) error {
	// last_verified_at is NULL on insert — the blob has never been
	// scrubbed yet. Scrub Agent (M3) updates it via MarkVerified.
	// crypto_identity is the ADR-58 third component of the dedup
	// key: empty for Plain blobs, "<algorithm>/<KeyID>" for
	// encrypted ones.
	const stmt = `
		INSERT INTO blobs (
			blob_ref, content_hash, original_size, 
		    crypto_identity, physical_path,
			pack_ref, pack_offset, pack_size,
			ref_count, last_verified_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, ?)
		ON CONFLICT(blob_ref) DO NOTHING`
	_, err := tx.ExecContext(ctx, stmt,
		blobRef,
		string(contentHash),
		originalSize,
		string(crypto),
		addr.Path,
		addr.PackRef,
		addr.Offset,
		addr.Size,
		timefmt.Format(time.Now()),
	)
	return err
}

// bumpRefCount increments the ref_count of an existing blob.
// Returns an error if the blob row is missing — this would mean a
// caller-side bug (linking a manifest to a blob that was not
// upserted in the same transaction).
func bumpRefCount(ctx context.Context, tx *sql.Tx, blobRef string) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE blobs SET ref_count = ref_count + 1 WHERE blob_ref = ?`,
		blobRef,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("sqlite: bumpRefCount: blob %q not present", blobRef)
	}
	return nil
}

// registerBlob is the upsert+bump pair every blob-bearing manifest
// path (Blob, TOC, Pack-chunks) needs at index time. The two SQL
// operations are idempotent individually and idempotent as a pair:
// re-running on a partially-applied transaction is safe. Pack
// manifest registration in indexPackManifest deliberately does NOT
// go through here — it upserts the pack-blob row but never bumps
// its ref_count, since the bump happens once per packed-blob entry
// rather than once per pack file (see manifest.go indexPackManifest
// for the rationale).
func registerBlob(
	ctx context.Context,
	tx *sql.Tx,
	blobRef string,
	contentHash domain.ContentHash,
	originalSize int64,
	crypto domain.CryptoIdentity,
	addr domain.PhysicalAddress,
) error {
	if err := upsertBlob(ctx, tx, blobRef, contentHash, originalSize, crypto, addr); err != nil {
		return err
	}
	return bumpRefCount(ctx, tx, blobRef)
}

// insertManifestRow writes a row into the manifests table.
// Idempotency: ON CONFLICT(manifest_digest) DO NOTHING. The same
// manifest file registered twice (after a crash, for instance) is a
// no-op rather than an error — re-indexing identical content lands the
// identical row.
//
// Primary key: ManifestDigest (the hash of the file bytes, always
// present). Identity lives in the single nullable slot column
// artifact_id (ADR-83/84/92):
//   - a user artifact's floating handle (m.ArtifactID);
//   - NULL for a pack container — the empty slot; the container is
//     addressed by its volume hash, not a handle.
//
// System artifacts are NOT indexed (ADR-85: identity is a name,
// addressed name→path bypassing the index), so they never reach this
// row and the table has no name column.
func insertManifestRow(ctx context.Context, tx *sql.Tx, m domain.Manifest) error {
	const stmt = `
		INSERT INTO manifests (
			manifest_digest, artifact_id, type, namespace, session_id,
			blob_ref, created_at, retention_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(manifest_digest) DO NOTHING`

	// Identity slot. A user artifact fills artifact_id with its floating
	// handle; a pack container has an empty slot (artifact_id NULL — it is
	// addressed by its volume hash). System artifacts are NOT indexed
	// (ADR-85: name→path bypasses the index), so they never reach this
	// row and the table has no name column.
	var artifactIDArg any
	if m.ArtifactID != "" {
		artifactIDArg = string(m.ArtifactID)
	}

	// blob_ref is NULL for Inline manifests per §9.1.2 — Inline blobs do
	// not have a row in `blobs`, and the routing layer uses the absence
	// of a JOIN partner as the "this is inline, read the file" signal.
	// It is a transitional single-blob cache; the authoritative blob
	// list is manifest_blobs (ADR-92).
	var blobRefArg any
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		blobRefArg = string(m.BlobRef)
	}

	// retention_until is NULL when no retention was set. Stored alongside
	// the manifest row (rather than read from the file at Delete-time) so
	// RollbackSession can do its atomic retention check in one indexed
	// query instead of N file reads.
	var retentionArg any
	if !m.RetentionUntil.IsZero() {
		retentionArg = timefmt.Format(m.RetentionUntil)
	}

	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	_, err := tx.ExecContext(ctx, stmt,
		string(m.Digest),
		artifactIDArg,
		string(m.Type),
		m.Namespace,
		m.SessionID,
		blobRefArg,
		timefmt.Format(createdAt),
		retentionArg,
	)
	return err
}

// linkManifestToBlob inserts an edge row into manifest_blobs, keyed by
// the manifest digest. Position is the chunk index for TOC manifests
// and 0 for blob manifests. Idempotency is provided by the PRIMARY KEY
// (manifest_digest, position) and ON CONFLICT DO NOTHING.
func linkManifestToBlob(
	ctx context.Context,
	tx *sql.Tx,
	digest domain.ManifestDigest,
	blobRef string,
	position int,
) error {
	const stmt = `
		INSERT INTO manifest_blobs (manifest_digest, blob_ref, position)
		VALUES (?, ?, ?)
		ON CONFLICT(manifest_digest, position) DO NOTHING`
	_, err := tx.ExecContext(ctx, stmt,
		string(digest), blobRef, position,
	)
	return err
}

// linkManifestToHandle inserts an edge row into manifest_handles, keyed
// by the manifest digest (ADR-92). Position preserves array order.
// Idempotency is provided by the PRIMARY KEY (manifest_digest, position)
// and ON CONFLICT DO NOTHING.
//
// NOTE: this stores the artifact→artifact edge only. Reference-count
// accounting for HandleRefs (the filled-slot consumption direction,
// ADR-92) is deferred until a producer exists — no россыпь or composite
// carries handle edges, and pack containers arrive with the rebuilt
// bundler.
func linkManifestToHandle(
	ctx context.Context,
	tx *sql.Tx,
	digest domain.ManifestDigest,
	handleRef domain.HandleRef,
	position int,
) error {
	const stmt = `
		INSERT INTO manifest_handles (manifest_digest, handle_ref, position)
		VALUES (?, ?, ?)
		ON CONFLICT(manifest_digest, position) DO NOTHING`
	_, err := tx.ExecContext(ctx, stmt,
		string(digest), string(handleRef), position,
	)
	return err
}

// --- Per-type registration paths ---

func indexBlobManifest(
	ctx context.Context,
	tx *sql.Tx,
	m domain.Manifest,
	addr domain.PhysicalAddress,
) error {
	// Inline manifests carry their bytes inside the manifest
	// itself and do not have a separate blob record. The manifest
	// is still indexed so Walk and GetBySession find it; the
	// blobs table is left alone (deduplication is disabled for
	// inline blobs by design — docs §… ).
	//
	// We dispatch on LayoutHeader.BlobStorage rather than on
	// emptiness of BlobRef because §7.2 mandates that BlobRef be
	// populated even on inline manifests (it carries the hash of
	// the embedded bytes).
	if m.LayoutHeader.BlobStorage == domain.LayoutInline {
		return insertManifestRow(ctx, tx, m)
	}
	if m.BlobRef == "" {
		return fmt.Errorf("sqlite: blob manifest %q has empty BlobRef", m.ArtifactID)
	}
	if err := registerBlob(ctx, tx, string(m.BlobRef), m.ContentHash, m.OriginalSize, domain.CryptoIdentityOf(m.Pipeline), addr); err != nil {
		return err
	}
	if err := insertManifestRow(ctx, tx, m); err != nil {
		return err
	}
	return linkManifestToBlob(ctx, tx, m.Digest, string(m.BlobRef), 0)
}

func indexTOCManifest(
	ctx context.Context,
	tx *sql.Tx,
	m domain.Manifest,
	addr domain.PhysicalAddress,
	chunkRefs []string,
) error {
	if m.BlobRef == "" {
		return fmt.Errorf("sqlite: TOC manifest %q has empty BlobRef", m.ArtifactID)
	}
	// Step 1: register the TOC blob itself.
	if err := registerBlob(ctx, tx, string(m.BlobRef), m.ContentHash, m.OriginalSize, domain.CryptoIdentityOf(m.Pipeline), addr); err != nil {
		return err
	}

	// Step 2: bump ref_count for each chunk and link the manifest
	// to it positionally. The chunk blobs are expected to exist —
	// the chunker.Wrapper writes them via PutBlob before the TOC
	// manifest is finalised (otherwise the TOC would point to
	// nothing). A missing chunk row is therefore an upstream bug
	// and surfaces as the bumpRefCount "not present" error.
	for pos, chunkRef := range chunkRefs {
		if err := bumpRefCount(ctx, tx, chunkRef); err != nil {
			return fmt.Errorf("toc chunk[%d] %q: %w", pos, chunkRef, err)
		}
		// Position starts at 1 for chunks because position 0 is
		// reserved for the TOC blob itself. This keeps DeleteManifest
		// logic uniform across blob and toc types.
		if err := linkManifestToBlob(ctx, tx, m.Digest, chunkRef, pos+1); err != nil {
			return err
		}
	}

	// Step 3: write the manifest row itself, link to the TOC blob
	// at position 0.
	if err := insertManifestRow(ctx, tx, m); err != nil {
		return err
	}
	return linkManifestToBlob(ctx, tx, m.Digest, string(m.BlobRef), 0)
}

// indexPackManifest registers a .pack volume. The pack file itself
// becomes one blob; every packed artifact is registered in
// packed_blobs but NOT in manifests — packed artifacts are not
// reachable through Walk (they live transparently inside the pack)
// and exist for LookupPacked only.
//
// ref_count semantics for the pack blob: incremented once per
// packed artifact. When all packed artifacts are deleted, the pack
// blob's ref_count drops to 0 and it becomes a GC candidate.
//
// TRANSITIONAL (ADR-86): the pack volume becomes a headless container
// manifest whose placement map lives in the bundler Resolver
// substrate (ext_data), not in packed_blobs. This path is retained
// until the bundler is rebuilt.
func indexPackManifest(
	ctx context.Context,
	tx *sql.Tx,
	m domain.Manifest,
	addr domain.PhysicalAddress,
	entries []domain.PackedEntry,
) error {
	if m.BlobRef == "" {
		return fmt.Errorf("sqlite: pack manifest %q has empty BlobRef", m.ArtifactID)
	}
	if err := upsertBlob(ctx, tx, string(m.BlobRef), m.ContentHash, m.OriginalSize, domain.CryptoIdentityOf(m.Pipeline), addr); err != nil {
		return err
	}

	const stmt = `
		INSERT INTO packed_blobs (
			artifact_id, pack_blob_ref, blob_ref,
			manifest_offset, manifest_size,
			blob_offset, blob_size,
			content_hash, crypto_identity, namespace, session_id, pipeline_params
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(artifact_id) DO NOTHING`
	for _, e := range entries {
		// crypto_identity (ADR-58) is transferred from the source blob
		// at pack time; the bundler does not re-encrypt, so it carries
		// the identity verbatim. Empty for a Plain packed blob.
		if _, err := tx.ExecContext(ctx, stmt,
			string(e.ArtifactID),
			string(m.BlobRef),
			e.BlobRef,
			e.ManifestOffset, e.ManifestSize,
			e.BlobOffset, e.BlobSize,
			string(e.ContentHash),
			string(e.CryptoIdentity),
			e.Namespace, e.SessionID,
			e.PipelineParams,
		); err != nil {
			return err
		}
		// Each packed artifact contributes one reference to the
		// pack blob. Not bumping ref_count would break GC.
		if err := bumpRefCount(ctx, tx, string(m.BlobRef)); err != nil {
			return err
		}
	}
	return nil
}

// DeleteManifest performs the logical deletion of an artifact.
// Single transaction:
//  1. Resolve the handle to the manifest digest (the table's PK).
//  2. Read the (digest, blob_ref) edges from manifest_blobs.
//  3. Decrement ref_count for each referenced blob.
//  4. Delete the manifest_blobs and manifest_handles edges.
//  5. Delete the manifests row.
//
// blobRefs argument: the caller passes the same set it intends to
// be decremented. Mismatches between manifest_blobs and blobRefs
// surface as a fatal error: the index has diverged from the
// caller's view, and continuing would corrupt ref_counts. RebuildIndex
// is the recovery tool.
//
// Idempotency: deleting an already-deleted artifact is a no-op
// (returns nil) — an unresolved handle means the manifest is gone.
// Source-of-truth for "already deleted" is the manifests table, not
// manifest_blobs: Inline manifests have no edges in manifest_blobs by
// design (§9.2.1), so checking that table for "exists" gives the wrong
// answer for them.
func (i *Index) DeleteManifest(ctx context.Context, artifactID domain.ArtifactID, blobRefs []string) error {
	return i.observe("DeleteManifest", func() error {
		return i.deleteManifestTx(ctx, artifactID, blobRefs)
	})
}

func (i *Index) deleteManifestTx(
	ctx context.Context,
	artifactID domain.ArtifactID,
	blobRefs []string,
) error {
	return i.inTx(ctx, func(tx *sql.Tx) error {
		// Resolve handle -> digest (the manifests PK). A missing handle
		// means the artifact is already gone: no-op.
		var digest string
		err := tx.QueryRowContext(ctx,
			`SELECT manifest_digest FROM manifests WHERE artifact_id = ? LIMIT 1`,
			string(artifactID),
		).Scan(&digest)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		}

		// Read the actual blob edges from the index. Source of truth is
		// manifest_blobs, not the caller — but if the caller's set
		// disagrees we want to know.
		rows, err := tx.QueryContext(ctx,
			`SELECT blob_ref FROM manifest_blobs WHERE manifest_digest = ?`,
			digest,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		var actual []string
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				return err
			}
			actual = append(actual, ref)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		if !sameSet(actual, blobRefs) {
			return fmt.Errorf("sqlite: DeleteManifest: blobRefs mismatch for %q "+
				"(index has %d edges, caller passed %d)",
				artifactID, len(actual), len(blobRefs))
		}

		for _, ref := range actual {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET ref_count = ref_count - 1 WHERE blob_ref = ? AND ref_count > 0`,
				ref,
			); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM manifest_blobs WHERE manifest_digest = ?`,
			digest,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM manifest_handles WHERE manifest_digest = ?`,
			digest,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM manifests WHERE manifest_digest = ?`,
			digest,
		); err != nil {
			return err
		}

		// Dispatch to extensions before commit. EventArgs carries
		// the actual blob refs we read from manifest_blobs (the
		// authoritative set), not whatever the caller passed in.
		args := extension.EventArgs{
			ArtifactID: artifactID,
			BlobRefs:   actual,
		}
		return i.dispatchExtensions(ctx, tx, extension.EventKindManifestDeleted, args)
	})
}

// sameSet returns true iff a and b contain the same elements
// (multiset equality). O(n+m) via map count; n is small in
// practice — manifests reference at most a few dozen blobs.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
		if count[s] < 0 {
			return false
		}
	}
	return true
}

// ManifestExists reports whether a manifest row with the given
// ArtifactID exists. Cheap point-lookup against the manifests_artifact
// index. Returns (false, nil) when the row is absent — the caller
// distinguishes "not present" from "infrastructure error" via the
// boolean.
func (i *Index) ManifestExists(ctx context.Context, id domain.ArtifactID) (bool, error) {
	const stmt = `SELECT 1 FROM manifests WHERE artifact_id = ? LIMIT 1`
	var one int
	err := i.db.QueryRowContext(ctx, stmt, string(id)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, classifyError(err)
	}
	return true, nil
}

// ResolveManifestDigest returns the current on-disk digest for a handle.
func (i *Index) ResolveManifestDigest(ctx context.Context, id domain.ArtifactID) (domain.ManifestDigest, bool, error) {
	const stmt = `SELECT manifest_digest FROM manifests WHERE artifact_id = ? LIMIT 1`
	var d string
	err := i.db.QueryRowContext(ctx, stmt, string(id)).Scan(&d)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, classifyError(err)
	}
	return domain.ManifestDigest(d), true, nil
}

// ManifestExistsByDigest reports whether a manifest row carries digest.
// The digest is the primary key, so this is a direct point-lookup.
func (i *Index) ManifestExistsByDigest(ctx context.Context, digest domain.ManifestDigest) (bool, error) {
	const stmt = `SELECT 1 FROM manifests WHERE manifest_digest = ? LIMIT 1`
	var one int
	err := i.db.QueryRowContext(ctx, stmt, string(digest)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, classifyError(err)
	}
	return true, nil
}

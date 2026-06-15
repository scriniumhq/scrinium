package sqlite

// CurrentSchemaVersion is the schema version this build of the
// package writes and expects to read. Bumped whenever a migration is
// appended to migrations[].
const CurrentSchemaVersion = 1

// migrations is the ordered list of forward-only schema migrations.
// Each migration is applied inside its own transaction; if any step
// fails the entire migration rolls back and Open returns an error.
//
// Pre-v1 the schema went through five incremental migrations. With no
// real installations yet, that history is collapsed into a single
// baseline at version 1 (schemaBaseline). The forward-migration
// mechanism (migrate.go) is retained for future versions: append a new
// entry here and bump CurrentSchemaVersion. Migrations are append-only
// — never edit one that has already shipped.
var migrations = []migration{
	{
		Version: 1,
		Description: "baseline: blobs, manifests (digest-PK + identity slots), " +
			"manifest_blobs, manifest_handles, ext_meta, " +
			"ext_data, store_meta",
		Statements: []string{schemaBaseline},
	},
}

// migration is a single forward-only schema change. Statements are
// executed in order inside one transaction.
type migration struct {
	Version     int
	Description string
	Statements  []string
}

// schemaBaseline is the full DDL of the collapsed v1 baseline. All
// identifiers stay lowercase and snake_case for SQLite ergonomics.
// Tables:
//
//   - blobs:           the deduplication index, one row per unique blob
//   - manifests:       one row per manifest FILE, keyed by its
//     ManifestDigest (the hash of the file bytes). Identity is carried
//     by the nullable artifact_id column (ADR-83/84/92): a user
//     artifact's floating handle, or NULL for a pack container (the
//     empty slot — the container is addressed by its volume hash). The
//     handle→digest lookup is the manifests_artifact index, not the
//     primary key, because the handle is stable while the digest changes
//     on repack. System artifacts are NOT indexed (ADR-85: identity is a
//     name, addressed name→path bypassing the index), so they have no
//     row here and the table carries no name column; user-Walk
//     invisibility of containers is the predicate artifact_id IS NULL.
//   - manifest_blobs:  ordered M:N edges from a manifest to the blobs it
//     references (BlobRefs — ADR-92), keyed by manifest_digest. A
//     composite references its chunk list; a pack container references
//     [toc_blob, pack_blob]; a regular manifest references one blob.
//   - manifest_handles: ordered edges from a manifest to OTHER artifacts
//     (HandleRefs, the content-addressed DAG — ADR-92), keyed by
//     manifest_digest. Populated for pack containers (placement of
//     members) and any artifact carrying artifact→artifact edges; empty
//     for a plain blob and a composite.
//   - ext_meta:        per-extension state; stores the schema_version
//     persisted from the last successful Setup so later registrations
//     can migrate forward
//   - ext_data:        the universal K/V store index extensions write
//     into; the composite PK (extension, table_name, key) gives each
//     extension a private namespace plus per-table grouping, and
//     WITHOUT ROWID keeps entries in PK order for prefix range scans
//   - store_meta:      singleton key/value table for engine metadata
//     (schema version, descriptor cache, scan timestamps, etc.)
//   - schema_version:  the running schema version, one row per applied
//     migration
//
// Notes on types:
//   - All hashes and refs are TEXT (the project format is
//     "<algo>-<hex>", typically <100 chars).
//   - Sizes and offsets are INTEGER (SQLite INTEGER is 64-bit).
//   - Timestamps are TEXT in RFC 3339 second precision (UTC) — the
//     same format the manifest codec writes on disk, so
//     RebuildIndexAgent can copy strings without reformatting. NULL
//     means "absent" (last_verified_at NULL = never scrubbed;
//     retention_until NULL = no retention).
//   - PRIMARY KEY columns are NOT NULL by SQLite definition; the
//     constraint is repeated for clarity on non-PK NOT NULL columns.
//   - manifests.artifact_id is NULLABLE — set to the floating handle
//     for a user artifact, NULL for a pack container (the empty slot,
//     ADR-92). NULL keeps the index sparse and is the user-Walk
//     invisibility predicate. System artifacts are not indexed (ADR-85),
//     so no name column exists.
//
// The blobs_content index is intentionally NON-UNIQUE. Physical blob
// identity is blob_ref (the PRIMARY KEY) — for an encrypted blob that
// is the hash of the ciphertext, so under EncryptedDedup=Disabled
// three writes of the same plaintext are three distinct rows that
// share (content_hash, original_size, crypto_identity): the identity
// does not include the random IV, and must not, or convergent dedup
// would break. A UNIQUE constraint would reject the second write.
// blobs_content is therefore a plain lookup index supporting the
// ExistsByContent probe (LIMIT 1); duplicate-row prevention is the
// blob_ref PK plus ON CONFLICT(blob_ref) DO NOTHING. The
// crypto_identity dedup component follows ADR-58 (the bundler mirrors
// it in its own placement map, where it stores finished ciphertext
// verbatim).
const schemaBaseline = `
CREATE TABLE blobs (
    blob_ref          TEXT    PRIMARY KEY,
    content_hash      TEXT    NOT NULL,
    original_size     INTEGER NOT NULL,
    physical_path     TEXT    NOT NULL,
    crypto_identity   TEXT    NOT NULL DEFAULT '',
    ref_count         INTEGER NOT NULL DEFAULT 0,
    last_verified_at  TEXT,
    created_at        TEXT    NOT NULL
) WITHOUT ROWID;

CREATE INDEX blobs_content ON blobs(content_hash, original_size, crypto_identity);
CREATE INDEX blobs_orphan  ON blobs(ref_count) WHERE ref_count = 0;
CREATE INDEX blobs_scrub   ON blobs(last_verified_at);

CREATE TABLE manifests (
    manifest_digest  TEXT    PRIMARY KEY,
    artifact_id      TEXT,
    namespace        TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    blob_ref         TEXT,
    created_at       TEXT    NOT NULL,
    retention_until  TEXT,
    last_verified_at TEXT
) WITHOUT ROWID;

CREATE INDEX manifests_artifact  ON manifests(artifact_id);
CREATE INDEX manifests_namespace ON manifests(namespace);
CREATE INDEX manifests_session   ON manifests(session_id);
CREATE INDEX manifests_scrub     ON manifests(last_verified_at);

CREATE TABLE manifest_blobs (
    manifest_digest TEXT    NOT NULL,
    blob_ref        TEXT    NOT NULL,
    position        INTEGER NOT NULL,
    PRIMARY KEY (manifest_digest, position)
) WITHOUT ROWID;

CREATE INDEX manifest_blobs_blob ON manifest_blobs(blob_ref);

CREATE TABLE manifest_handles (
    manifest_digest TEXT    NOT NULL,
    handle_ref      TEXT    NOT NULL,
    position        INTEGER NOT NULL,
    PRIMARY KEY (manifest_digest, position)
) WITHOUT ROWID;

CREATE INDEX manifest_handles_target ON manifest_handles(handle_ref);

CREATE TABLE ext_meta (
    extension      TEXT    PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    registered_at  TEXT    NOT NULL
) WITHOUT ROWID;

CREATE TABLE ext_data (
    extension  TEXT NOT NULL,
    table_name TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      BLOB NOT NULL,
    PRIMARY KEY (extension, table_name, key)
) WITHOUT ROWID;

CREATE TABLE store_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT    NOT NULL
);
`

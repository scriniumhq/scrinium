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
		Description: "baseline: blobs, manifests, manifest_blobs, " +
			"packed_blobs, ext_meta, ext_data, store_meta",
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
//   - blobs:          the deduplication index, one row per unique blob
//   - manifests:      one row per artifact (manifest file on disk)
//   - manifest_blobs: M:N edges from a manifest to the blobs it
//     references (a TOC manifest references many chunk blobs; a
//     regular manifest references one)
//   - packed_blobs:   range-read information for blobs inside a .pack
//     volume; one row per packed artifact
//   - ext_meta:       per-extension state; stores the schema_version
//     persisted from the last successful Setup so later registrations
//     can migrate forward
//   - ext_data:       the universal K/V store index extensions write
//     into; the composite PK (extension, table_name, key) gives each
//     extension a private namespace plus per-table grouping, and
//     WITHOUT ROWID keeps entries in PK order for prefix range scans
//   - store_meta:     singleton key/value table for engine metadata
//     (schema version, descriptor cache, scan timestamps, etc.)
//   - schema_version: the running schema version, one row per applied
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
// crypto_identity dedup component (mirrored on packed_blobs, where the
// bundler stores finished ciphertext verbatim) follows ADR-58.
const schemaBaseline = `
CREATE TABLE blobs (
    blob_ref          TEXT    PRIMARY KEY,
    content_hash      TEXT    NOT NULL,
    original_size     INTEGER NOT NULL,
    physical_path     TEXT    NOT NULL,
    pack_ref          TEXT    NOT NULL DEFAULT '',
    pack_offset       INTEGER NOT NULL DEFAULT 0,
    pack_size         INTEGER NOT NULL DEFAULT 0,
    crypto_identity   TEXT    NOT NULL DEFAULT '',
    ref_count         INTEGER NOT NULL DEFAULT 0,
    last_verified_at  TEXT,
    created_at        TEXT    NOT NULL
) WITHOUT ROWID;

CREATE INDEX blobs_content ON blobs(content_hash, original_size, crypto_identity);
CREATE INDEX blobs_orphan  ON blobs(ref_count) WHERE ref_count = 0;
CREATE INDEX blobs_scrub   ON blobs(last_verified_at);

CREATE TABLE manifests (
    artifact_id      TEXT    PRIMARY KEY,
    manifest_digest  TEXT    NOT NULL DEFAULT '',
    type             TEXT    NOT NULL,
    namespace        TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    blob_ref         TEXT,
    created_at       TEXT    NOT NULL,
    retention_until  TEXT,
    last_verified_at TEXT
) WITHOUT ROWID;

CREATE INDEX manifests_digest    ON manifests(manifest_digest);
CREATE INDEX manifests_namespace ON manifests(namespace);
CREATE INDEX manifests_session   ON manifests(session_id);
CREATE INDEX manifests_scrub     ON manifests(last_verified_at);

CREATE TABLE manifest_blobs (
    artifact_id  TEXT    NOT NULL,
    blob_ref     TEXT    NOT NULL,
    position     INTEGER NOT NULL,
    PRIMARY KEY (artifact_id, position)
) WITHOUT ROWID;

CREATE INDEX manifest_blobs_blob ON manifest_blobs(blob_ref);

CREATE TABLE packed_blobs (
    artifact_id      TEXT    PRIMARY KEY,
    pack_blob_ref    TEXT    NOT NULL,
    blob_ref         TEXT    NOT NULL,
    manifest_offset  INTEGER NOT NULL,
    manifest_size    INTEGER NOT NULL,
    blob_offset      INTEGER NOT NULL,
    blob_size        INTEGER NOT NULL,
    content_hash     TEXT    NOT NULL,
    crypto_identity  TEXT    NOT NULL DEFAULT '',
    namespace        TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    pipeline_params  BLOB
) WITHOUT ROWID;

CREATE INDEX packed_blobs_pack ON packed_blobs(pack_blob_ref);

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

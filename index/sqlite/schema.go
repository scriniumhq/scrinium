package sqlite

// CurrentSchemaVersion is the schema version this build of the
// package writes and expects to read. Bumped whenever a migration
// is added to migrations[].
const CurrentSchemaVersion = 1

// migrations is the ordered list of forward-only schema migrations.
// Each migration is applied inside its own transaction; if any step
// fails the entire migration rolls back and Open returns an error.
//
// The first migration creates the full v1 schema in one shot. Later
// migrations are append-only — never edit a migration that has
// already shipped.
var migrations = []migration{
	{
		Version: 1,
		Description: "initial schema: blobs, manifests, packed_blobs, " +
			"store_meta",
		Statements: []string{schemaV1},
	},
}

// migration is a single forward-only schema change. Statements are
// executed in order inside one transaction.
type migration struct {
	Version     int
	Description string
	Statements  []string
}

// schemaV1 is the initial DDL. All identifiers stay lowercase and
// snake_case for SQLite ergonomics. The schema covers:
//
//   - blobs:         the deduplication index, one row per unique blob
//   - manifests:     one row per artifact (manifest file on disk)
//   - manifest_blobs: M:N edges from a manifest to the blobs it
//     references (a TOC manifest references many
//     chunk blobs; a regular manifest references one)
//   - packed_blobs:  range-read information for blobs inside a .pack
//     volume; one row per packed artifact
//   - store_meta:    singleton key/value table for engine metadata
//     (schema version, descriptor cache, scan
//     timestamps, etc.)
//   - schema_version: the running schema version, one row
//
// Indices:
//   - blobs(content_hash, original_size) is the dedup key for
//     ExistsByContent. Unique to enforce dedup invariants.
//   - blobs(last_verified_at) supports Scrub Agent batched fetches
//     of stale entries.
//   - blobs(ref_count) supports GC ListOrphanBlobs.
//   - manifests(namespace) supports Walk and Scrub iteration.
//   - manifests(session_id) supports RollbackSession.
//   - packed_blobs(pack_blob_ref) supports DeletePacked.
//
// Notes on types:
//   - All hashes and refs are TEXT (the project format is
//     "<algo>-<hex>", typically <100 chars).
//   - Sizes and offsets are INTEGER (SQLite INTEGER is 64-bit).
//   - Timestamps are INTEGER UNIX nanoseconds; we deliberately do
//     not use SQLite's TEXT-based date format because it ignores
//     time zones and rounds to seconds.
//   - PRIMARY KEY columns are NOT NULL by SQLite definition; the
//     constraint is repeated for clarity in non-PK NOT NULL columns.
const schemaV1 = `
CREATE TABLE blobs (
    blob_ref          TEXT    PRIMARY KEY,
    content_hash      TEXT    NOT NULL,
    original_size     INTEGER NOT NULL,
    physical_workspace INTEGER NOT NULL,
    physical_path     TEXT    NOT NULL,
    pack_ref          TEXT    NOT NULL DEFAULT '',
    pack_offset       INTEGER NOT NULL DEFAULT 0,
    pack_size         INTEGER NOT NULL DEFAULT 0,
    ref_count         INTEGER NOT NULL DEFAULT 0,
    last_verified_at  INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL
) WITHOUT ROWID;

CREATE UNIQUE INDEX blobs_content ON blobs(content_hash, original_size);
CREATE INDEX        blobs_orphan  ON blobs(ref_count) WHERE ref_count = 0;
CREATE INDEX        blobs_scrub   ON blobs(last_verified_at);

CREATE TABLE manifests (
    artifact_id      TEXT    PRIMARY KEY,
    type             TEXT    NOT NULL,
    namespace        TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    blob_ref         TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    retention_until  INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE INDEX manifests_namespace ON manifests(namespace);
CREATE INDEX manifests_session   ON manifests(session_id);

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
    namespace        TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    pipeline_params  BLOB
) WITHOUT ROWID;

CREATE INDEX packed_blobs_pack ON packed_blobs(pack_blob_ref);

CREATE TABLE store_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
`

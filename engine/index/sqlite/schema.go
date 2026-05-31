package sqlite

// CurrentSchemaVersion is the schema version this build of the
// package writes and expects to read. Bumped whenever a migration
// is added to migrations[].
const CurrentSchemaVersion = 5

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
	{
		Version:     2,
		Description: "index extensions: ext_meta, ext_data",
		Statements:  []string{schemaV2},
	},
	{
		Version:     3,
		Description: "ADR-58: blobs.crypto_identity in the dedup key",
		Statements:  schemaV3,
	},
	{
		Version:     4,
		Description: "ADR-58: packed_blobs.crypto_identity (pack-layer dedup key)",
		Statements:  schemaV4,
	},
	{
		Version:     5,
		Description: "Scrub: manifests.last_verified_at (manifest-level scrub stamp)",
		Statements:  schemaV5,
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
//     ExistsByContent. Unique to enforce dedup invariants. NOTE:
//     migration v3 replaces this index with a three-column one that
//     adds crypto_identity (ADR-58). The v1 DDL below is left
//     verbatim — migrations are append-only history, not the live
//     shape.
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
//   - Timestamps are TEXT in RFC 3339 second precision (UTC). The
//     same format the manifest writes on disk per
//     internal/manifestcodec §7.5 — RebuildIndexAgent can copy
//     strings without reformatting. NULL means "absent" (e.g.
//     last_verified_at NULL = never scrubbed; retention_until
//     NULL = no retention).
//   - PRIMARY KEY columns are NOT NULL by SQLite definition; the
//     constraint is repeated for clarity in non-PK NOT NULL columns.
const schemaV1 = `
CREATE TABLE blobs (
    blob_ref          TEXT    PRIMARY KEY,
    content_hash      TEXT    NOT NULL,
    original_size     INTEGER NOT NULL,
    physical_path     TEXT    NOT NULL,
    pack_ref          TEXT    NOT NULL DEFAULT '',
    pack_offset       INTEGER NOT NULL DEFAULT 0,
    pack_size         INTEGER NOT NULL DEFAULT 0,
    ref_count         INTEGER NOT NULL DEFAULT 0,
    last_verified_at  TEXT,
    created_at        TEXT    NOT NULL
) WITHOUT ROWID;

CREATE UNIQUE INDEX blobs_content ON blobs(content_hash, original_size);
CREATE INDEX        blobs_orphan  ON blobs(ref_count) WHERE ref_count = 0;
CREATE INDEX        blobs_scrub   ON blobs(last_verified_at);

CREATE TABLE manifests (
    artifact_id      TEXT    PRIMARY KEY,
    type             TEXT    NOT NULL,
    namespace        TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    blob_ref         TEXT,
    created_at       TEXT    NOT NULL,
    retention_until  TEXT
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
    applied_at TEXT    NOT NULL
);
`

// schemaV2 introduces the index-extensions surface. Two tables:
//
//   - ext_meta:  per-extension state. Stores schema_version
//     persisted from the last successful Setup so
//     later registrations can migrate forward.
//   - ext_data:  the universal K/V store extensions write into.
//     Composite PK (extension, table_name, key) gives
//     each extension a private namespace plus
//     per-table grouping inside that namespace.
//
// The single shared ext_data was chosen over generated per-extension
// tables — see 4. API Reference/16 §16.6.1 for the trade-off
// discussion. WITHOUT ROWID keeps the composite PK row-aligned;
// SQLite stores entries in PK order, which is precisely what range
// scans (DeletePrefix, Scan with prefix) want.
const schemaV2 = `
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
`

// schemaV3 adds the crypto-identity component of the blob dedup
// lookup (ADR-58). The column is NOT NULL DEFAULT ” so the
// migration is a pure add for existing rows: Plain blobs keep an
// empty identity and their historical (content_hash, original_size)
// lookup is unchanged.
//
// Crucially the index is NON-UNIQUE. Physical blob identity is
// blob_ref (the PRIMARY KEY) — for an encrypted blob that is the
// hash of the ciphertext, so under Disabled three writes of the
// same plaintext are three distinct rows that share
// (content_hash, original_size, crypto_identity): the identity does
// NOT include the random IV, and must not, or Convergent dedup
// would break. A UNIQUE constraint here would reject the 2nd write.
// blobs_content is therefore a plain lookup index supporting the
// ExistsByContent probe (which already uses LIMIT 1); duplicate-row
// prevention is the blob_ref PK + ON CONFLICT(blob_ref) DO NOTHING.
//
// Append-only: never edit this once shipped.
var schemaV3 = []string{
	`ALTER TABLE blobs ADD COLUMN crypto_identity TEXT NOT NULL DEFAULT ''`,
	`DROP INDEX blobs_content`,
	`CREATE INDEX blobs_content ON blobs(content_hash, original_size, crypto_identity)`,
}

// schemaV4 adds the crypto-identity component to packed_blobs, the
// pack-layer mirror of the blobs.crypto_identity column from v3
// (ADR-58). Packing transfers a blob's identity verbatim — the
// bundler stores the finished ciphertext bytes without re-encrypting
// — so the dedup key (content_hash, original_size, crypto_identity)
// is reproducible inside a pack just as it is for a standalone blob.
// NOT NULL DEFAULT ” makes the migration a pure add: existing rows
// (Plain packed blobs) keep an empty identity.
//
// Pack-layer dedup that consumes this column ships with the bundler
// in M4/S4; the column and the PackedEntry.CryptoIdentity field are
// frozen here so that layer builds on the final shape.
//
// Append-only: never edit this once shipped.
var schemaV4 = []string{
	`ALTER TABLE packed_blobs ADD COLUMN crypto_identity TEXT NOT NULL DEFAULT ''`,
}

// schemaV5 adds a manifest-level scrub stamp. Until v5 last_verified_at
// lived only on blobs, so the Scrub Agent could record that a physical
// blob had been re-hashed but had nowhere to record that an artifact's
// manifest had been checked. Two cases need the manifest-level stamp:
//
//   - Inline artifacts carry their payload inside the manifest file and
//     have no blobs row at all (blob_ref is NULL, §9.1.2). Without a
//     manifest stamp they never appear in any scrub work list and a
//     cold inline artifact is never re-verified against bit rot.
//   - Multi-blob (TOC) artifacts are only fully verified once every
//     referenced blob is fresh; the stamp records that the whole
//     artifact reached that state, distinct from any single blob.
//
// The Scrub Agent's blob pass cascades into manifests via
// manifest_blobs and stamps the consumers it fully covers; a separate
// manifest pass (ListUnverifiedManifests) covers the inline artifacts
// the blob pass cannot reach. NULL = never scrubbed, matching
// blobs.last_verified_at semantics.
//
// Append-only: never edit this once shipped.
var schemaV5 = []string{
	`ALTER TABLE manifests ADD COLUMN last_verified_at TEXT`,
	`CREATE INDEX manifests_scrub ON manifests(last_verified_at)`,
}

// Package postgres will provide a PostgreSQL-backed
// StoreIndex implementation. Status: NOT IMPLEMENTED.
//
// This package is reserved as the home of the future Postgres
// index. When implementing:
//
//  1. Mirror the schema in index/sqlite (same tables,
//     compatible columns) — migration tooling can copy data.
//     PostgreSQL-specific niceties: JSONB for metadata,
//     LISTEN/NOTIFY for change feed, partial indexes.
//  2. Implement all methods of core.StoreIndex against PG.
//     Most translate one-to-one from the sqlite versions; a
//     few will benefit from PG-specific syntax (UPSERT via
//     ON CONFLICT, RETURNING clauses).
//  3. Implement schema migration — same forward-only model
//     as sqlite (versioned migration scripts applied in
//     order).
//  4. Add register.go calling
//     index.RegisterDialer("postgres", openPostgresURI).
//  5. URI form: postgres://user:pass@host:port/dbname?sslmode=...
//     Credentials in the URI is the standard PG convention.
//     Empty password expects ~/.pgpass or PGPASSWORD env.
//  6. Optional: implement event.RemoteBus via LISTEN/NOTIFY,
//     enabling real-time view sync across processes — the
//     headline reason to choose Postgres over SQLite for a
//     multi-process deployment.
//
// Until the implementation lands, attempting to dial a
// postgres:// URI returns "scheme postgres not registered".
//
// See index/sqlite for the reference StoreIndex implementation.
package postgres

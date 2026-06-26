package errs

import "errors"

// ErrInvalidSystemName — a SystemStore name violated the validation
// contract: empty, leading or trailing slash, empty segment ("//"),
// or a "." / ".." traversal segment.
var ErrInvalidSystemName = errors.New("scrinium: invalid system name")

// System-artifact read categories (ADR-104). A system-artifact read is
// fail-closed and surfaces one typed category; the consumer decides the
// reaction by its role. Two more categories reuse existing sentinels:
// integrity is errs.ErrCorruptedContent (verify-on-read), absent is
// errs.ErrArtifactNotFound (no version / cell).

// ErrSystemArtifactForeign — the envelope's store_id does not match the
// reading store's authoritative store_id: the artifact belongs to a
// different store (the identity category). Strict everywhere — there is
// no bypass.
var ErrSystemArtifactForeign = errors.New("scrinium: system artifact belongs to another store")

// ErrSystemArtifactMalformed — the system-artifact envelope could not be
// parsed or is missing a required field (e.g. no store_id): the malformed
// category.
var ErrSystemArtifactMalformed = errors.New("scrinium: malformed system artifact envelope")

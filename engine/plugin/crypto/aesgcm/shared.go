package aesgcm

import (
	"errors"
	"io"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/segaead"
)

// shared.go — small helpers common to the pinned-DEK and
// resolver-backed encoder/decoder pairs: the EncryptedDedup → IV-mode
// mapping, and the io.Reader wrappers that carry a construction-time
// error or fold a segment-authentication failure into the public
// errs.ErrDecryptionFailed sentinel.

// ivModeFor maps the store's EncryptedDedup policy to the blob
// header's IV mode (ADR-58/59). The default (empty) and Disabled both
// mean random per-segment IVs; only Convergent asks for deterministic
// derivation.
func ivModeFor(d domain.EncryptedDedup) segaead.IVMode {
	if d == domain.EncryptedDedupConvergent {
		return segaead.IVModeConvergent
	}
	return segaead.IVModeRandom
}

// errReader is an io.Reader that fails every Read with a fixed error.
// Used when Transform cannot even start (nil resolver, no keys, bad
// AEAD construction): the contract requires Transform to return a
// reader, so the error surfaces on first Read like any other stream
// failure.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// decryptErrReader wraps the segaead read side and translates a
// per-segment authentication failure into errs.ErrDecryptionFailed.
// Structural failures (truncation, bad magic) and resolver problems
// surface verbatim — callers distinguish "could not decrypt" from
// "the bytes on disk are malformed".
type decryptErrReader struct{ r io.Reader }

func (d decryptErrReader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if err != nil && errors.Is(err, segaead.ErrSegmentAuth) {
		return n, errors.Join(errs.ErrDecryptionFailed, err)
	}
	return n, err
}

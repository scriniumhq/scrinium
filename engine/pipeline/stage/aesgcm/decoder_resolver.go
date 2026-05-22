package aesgcm

import (
	"io"

	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/pipeline/internal/segaead"
)

// resolverDecoder is the per-operation Decoder for the
// resolver-backed factory. On Transform it asks the resolver for
// every candidate DEK matching the recorded KeyID and hands the
// ordered AEAD list to segaead, which tries each per segment until one
// authenticates — supporting key rotation and multi-tenant lookups
// out of the box.
//
// Resolver-side problems (missing wiring, no keys for the recorded
// KeyID) surface verbatim so the caller can tell them apart from a
// genuine decryption failure. A segment that no candidate can open
// surfaces errs.ErrDecryptionFailed (via decryptErrReader).
type resolverDecoder struct {
	resolver pipeline.KeyResolver
	keyID    string
}

func (d *resolverDecoder) Transform(r io.Reader) io.Reader {
	aeads, err := resolveAEADs(d.resolver, d.keyID)
	if err != nil {
		return errReader{err: err}
	}
	or, err := segaead.Open(r, aeads)
	if err != nil {
		return errReader{err: err}
	}
	return decryptErrReader{r: or}
}

var _ pipeline.Decoder = (*resolverDecoder)(nil)

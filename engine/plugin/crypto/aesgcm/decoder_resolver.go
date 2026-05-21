package aesgcm

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/errs"
)

// resolverDecoder is the per-operation Decoder for the
// resolver-backed factory. On Transform it asks the resolver for
// every candidate DEK matching the recorded KeyID and tries each
// AEAD.Open call in turn — the same pattern as
// manifestcodec.DecodeFileEncrypted, supporting key rotation and
// multi-tenant lookups out of the box.
//
// Any GCM Open failure (wrong key, tampered ciphertext, wrong IV)
// is indistinguishable at the AEAD layer; we always report the
// public sentinel ErrDecryptionFailed if no candidate succeeds.
type resolverDecoder struct {
	resolver coreapi.KeyResolver
	keyID    string
	iv       []byte
}

func (d *resolverDecoder) Transform(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()

		if len(d.iv) != ivBytes {
			_ = pw.CloseWithError(fmt.Errorf(
				"aesgcm: expected %d-byte IV from manifest stage, got %d",
				ivBytes, len(d.iv)))
			return
		}

		aeads, err := resolveAEADs(d.resolver, d.keyID)
		if err != nil {
			// Resolver-side problems (missing wiring, no keys for
			// the recorded KeyID) surface verbatim; the caller
			// can distinguish them from decryption failures.
			_ = pw.CloseWithError(err)
			return
		}

		ct, err := io.ReadAll(r)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		// Walk candidates in resolver order. First success wins;
		// every Open is constant-time-ish in practice (one AES
		// schedule + one GCM verify), and the list is short
		// (1 today, 2 during rotation windows).
		var lastErr error
		for _, aead := range aeads {
			pt, openErr := aead.Open(nil, d.iv, ct, nil)
			if openErr == nil {
				if _, err := io.Copy(pw, bytes.NewReader(pt)); err != nil {
					_ = pw.CloseWithError(err)
				}
				return
			}
			lastErr = openErr
		}
		_ = pw.CloseWithError(errors.Join(errs.ErrDecryptionFailed, lastErr))
	}()
	return pr
}

// Compile-time assertion: resolverDecoder is a core.Decoder.
var _ coreapi.Decoder = (*resolverDecoder)(nil)

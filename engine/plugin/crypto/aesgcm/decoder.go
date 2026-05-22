package aesgcm

import (
	"crypto/cipher"
	"io"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/internal/segaead"
)

// decoder is the per-operation pinned-DEK Decoder. It reads the blob
// header and then each segment frame in turn, taking the IV from the
// frame (never from the manifest stage) and verifying each segment's
// GCM tag as it goes — per-segment integrity, with corruption
// localised to a single segment (ADR-59 / §03). A tag mismatch
// surfaces as errs.ErrDecryptionFailed.
type decoder struct {
	aead cipher.AEAD
}

func (d *decoder) Transform(r io.Reader) io.Reader {
	or, err := segaead.Open(r, []cipher.AEAD{d.aead})
	if err != nil {
		return errReader{err: err}
	}
	return decryptErrReader{r: or}
}

var _ coreapi.Decoder = (*decoder)(nil)

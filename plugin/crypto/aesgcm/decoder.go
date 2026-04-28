package aesgcm

import (
	"bytes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

	"github.com/rkurbatov/scrinium/errs"
)

// decoder is the per-operation Decoder. It consumes the entire
// ciphertext, calls Open (which verifies the GCM tag), and exposes
// the plaintext as an io.Reader. A tag mismatch surfaces as
// errs.ErrDecryptionFailed at the very first Read of the wrapped
// reader.
type decoder struct {
	aead cipher.AEAD
	iv   []byte
}

func (d *decoder) Transform(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()

		if len(d.iv) != ivBytes {
			_ = pw.CloseWithError(fmt.Errorf(
				"aesgcm: expected %d-byte IV from manifest stage, got %d",
				ivBytes, len(d.iv)))
			return
		}

		ct, err := io.ReadAll(r)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		pt, err := d.aead.Open(nil, d.iv, ct, nil)
		if err != nil {
			// The GCM tag did not validate — either the wrong DEK
			// or a corrupted/tampered ciphertext. Surface as the
			// public sentinel.
			_ = pw.CloseWithError(errors.Join(errs.ErrDecryptionFailed, err))
			return
		}
		if _, err := io.Copy(pw, bytes.NewReader(pt)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()
	return pr
}

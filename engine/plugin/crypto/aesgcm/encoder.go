package aesgcm

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"sync/atomic"

	"scrinium.dev/engine/core"
)

// encoder is the per-operation Encoder. AES-GCM cannot stream the
// authentication tag — it is appended after the entire plaintext
// has been processed — so we buffer the input, seal it once, and
// expose the resulting ciphertext+tag through an io.Reader. This
// is consistent with the docs §3.2 description of "single-pass
// AEAD".
type encoder struct {
	aead cipher.AEAD

	iv         []byte
	outputSize atomic.Int64
	started    bool
}

func (e *encoder) Transform(r io.Reader) io.Reader {
	if e.started {
		pr, pw := io.Pipe()
		_ = pw.CloseWithError(errors.New("aesgcm encoder reused"))
		return pr
	}
	e.started = true

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()

		plaintext, err := io.ReadAll(r)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		iv := make([]byte, ivBytes)
		if _, err := rand.Read(iv); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		e.iv = iv

		// Seal returns ciphertext || tag. We do not bind any
		// additional data — the manifest binds context implicitly
		// (its bytes are hashed into the ArtifactID).
		sealed := e.aead.Seal(nil, iv, plaintext, nil)
		e.outputSize.Store(int64(len(sealed)))

		if _, err := io.Copy(pw, bytes.NewReader(sealed)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()

	return pr
}

// Result returns OutputSize and the IV chosen at Transform time.
// Called by the runner after EOF.
func (e *encoder) Result() core.TransformResult {
	return core.TransformResult{
		OutputSize: e.outputSize.Load(),
		IV:         e.iv,
	}
}

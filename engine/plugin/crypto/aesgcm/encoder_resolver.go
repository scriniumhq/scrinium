package aesgcm

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"sync/atomic"

	"scrinium.dev/engine/core"
)

// resolverEncoder is the per-operation Encoder for the
// resolver-backed factory. It resolves the DEK lazily on first
// Transform; the KeyID surfaced in Result is the resolver's
// DefaultKeyID() at that moment.
//
// AES-GCM cannot stream its tag — it is appended after the entire
// plaintext has been processed — so the implementation mirrors
// the pinned-DEK encoder: buffer input, Seal once, expose
// ciphertext through io.Pipe.
type resolverEncoder struct {
	resolver core.KeyResolver

	iv         []byte
	keyID      string
	outputSize atomic.Int64
	started    bool
}

func (e *resolverEncoder) Transform(r io.Reader) io.Reader {
	if e.started {
		pr, pw := io.Pipe()
		_ = pw.CloseWithError(errors.New("aesgcm resolver encoder reused"))
		return pr
	}
	e.started = true

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()

		// Snapshot the default KeyID and resolve a single AEAD
		// primitive. We take only the first candidate: the
		// write side has no notion of "try alternatives" —
		// DefaultKeyID() is by definition the one to use.
		keyID := ""
		if e.resolver != nil {
			keyID = e.resolver.DefaultKeyID()
		}
		aeads, err := resolveAEADs(e.resolver, keyID)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		aead := aeads[0]
		e.keyID = keyID

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
		sealed := aead.Seal(nil, iv, plaintext, nil)
		e.outputSize.Store(int64(len(sealed)))

		if _, err := io.Copy(pw, bytes.NewReader(sealed)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()

	return pr
}

// Result returns OutputSize, IV, and the KeyID resolved at
// Transform time. The Pipeline runner copies KeyID into
// manifest.Pipeline[i].KeyID so the Decoder can look up the same
// key on read.
func (e *resolverEncoder) Result() core.TransformResult {
	return core.TransformResult{
		OutputSize: e.outputSize.Load(),
		IV:         e.iv,
		KeyID:      e.keyID,
	}
}

// Compile-time assertion: resolverEncoder is a core.Encoder.
var _ core.Encoder = (*resolverEncoder)(nil)

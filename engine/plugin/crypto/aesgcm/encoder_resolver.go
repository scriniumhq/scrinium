package aesgcm

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"sync/atomic"

	"scrinium.dev/engine/coreapi"
)

// resolverEncoder is the per-operation Encoder for the
// resolver-backed factory. The engine chooses the write KeyID once
// (KeyResolver.ResolveWriteKey) and passes it at construction via
// EncodeContext; the encoder resolves the DEK for that KeyID on
// first Transform and surfaces the same KeyID in Result.
//
// AES-GCM cannot stream its tag — it is appended after the entire
// plaintext has been processed — so the implementation mirrors the
// pinned-DEK encoder: buffer input, Seal once, expose ciphertext
// through io.Pipe.
type resolverEncoder struct {
	resolver coreapi.KeyResolver
	keyID    string // chosen by the engine, fixed at construction

	iv         []byte
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

		// The engine already picked the write KeyID and handed it
		// to us via EncodeContext. We fetch the DEK candidates for
		// it and use the first — the write side never tries
		// alternatives (that is a read-path concern).
		aeads, err := resolveAEADs(e.resolver, e.keyID)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		aead := aeads[0]

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

// Result returns OutputSize, IV, and the KeyID this encoder was
// constructed with. The Pipeline runner copies KeyID into
// manifest.Pipeline[i].KeyID so the Decoder can look up the same
// key on read.
func (e *resolverEncoder) Result() coreapi.TransformResult {
	return coreapi.TransformResult{
		OutputSize: e.outputSize.Load(),
		IV:         e.iv,
		KeyID:      e.keyID,
	}
}

// Compile-time assertion: resolverEncoder is a store.Encoder.
var _ coreapi.Encoder = (*resolverEncoder)(nil)

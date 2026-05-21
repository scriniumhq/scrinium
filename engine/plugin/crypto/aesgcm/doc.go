// Package aesgcm provides a Scrinium TransformerFactory for the
// AES-256-GCM AEAD cipher via the Go standard library.
//
// Wiring (typical host setup):
//
//	reg := store.NewTransformerRegistry().
//	    Register("aes-gcm", aesgcm.New(dek))
//	store, _, _ := store.InitStore(ctx, drv,
//	    store.WithReadRegistry(reg), /* ... */)
//
// The factory holds the DEK; per-operation instances generate or
// receive an IV without touching the key directly. The key is
// expected to be 32 bytes (AES-256). Other lengths fail at
// factory-construction time.
//
// Streaming model.
// AES-GCM is a single-message AEAD: the tag covers the entire
// ciphertext and is verified at the very end. The Decoder
// therefore detects tampering only after the last byte has been
// pulled. Callers that read partial bytes and act on them before
// EOF are violating the AEAD contract; the engine consumes the
// stream to EOF before surfacing it to clients on the read path.
//
// IV handling.
// The Encoder generates a fresh 12-byte IV via crypto/rand on its
// first Transform call and exposes it via Result().IV — the
// Pipeline runner records it in manifest.Pipeline[i].IV. The
// Decoder receives the IV via stage.IV at construction.
package aesgcm

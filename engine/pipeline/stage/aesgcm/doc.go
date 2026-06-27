// Package aesgcm provides a Scrinium TransformerFactory for the
// AES-256-GCM AEAD cipher via the Go standard library.
//
// Wiring (typical host setup):
//
//	reg := pipeline.NewTransformerRegistry().
//	    Register("aes-gcm", aesgcm.New(dek))
//	store, _, _ := store.InitStore(ctx, drv,
//	    store.WithReadRegistry(reg), /* ... */)
//
// The factory holds the DEK; per-operation instances seal or open a
// blob without the caller touching the key directly. The key must be
// 32 bytes (AES-256). Other lengths fail at factory-construction time.
//
// # Streaming model (segmented AEAD)
//
// A blob is written as a header followed by a sequence of independent
// AES-GCM segments of fixed plaintext size (ADR-59); the on-disk
// framing lives in internal/segaead, this package is the adapter. The
// encoder reads one segment (≈1 MiB by default) at a time, seals it
// with its own IV and tag, and emits the frame — O(SegmentSize)
// memory and a single pass, regardless of blob size. There is no
// whole-blob buffering. The decoder reads frames one by one, taking
// each IV from its frame and verifying that segment's GCM tag, so
// corruption is localised to a single segment.
//
// Because integrity is checked per segment on every read, the factory
// implements pipeline.AEADCapable: the engine can skip a redundant
// ContentHash recomputation under VerifyOnRead=Auto.
//
// # IV handling
//
// There is no per-blob IV. The IV mode is taken from
// EncodeContext.EncryptedDedup: Disabled draws a fresh random IV per
// segment; Convergent derives it deterministically as
// HMAC-SHA256(DEK, SHA-256(segment) ‖ KeyID ‖ index)[:12], making the
// ciphertext (and therefore the BlobRef) reproducible for dedup. In
// both modes the IV lives in the segment frame, so
// manifest.Pipeline[i].IV stays empty and the Decoder ignores
// stage.IV.
//
// # Pinned vs resolver
//
// New(key) pins a single DEK and records an empty KeyID. NewWithResolver
// resolves the DEK per operation through a pipeline.KeyResolver — the
// engine picks the write KeyID via ResolveWriteKey and threads it
// through EncodeContext; on read the Decoder enumerates the resolver's
// candidate keys per segment to support rotation and multi-tenant
// stores.
package aesgcm

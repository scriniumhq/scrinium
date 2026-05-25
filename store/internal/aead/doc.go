// Package aead is the shared low-level AEAD primitive for Scrinium:
// the AES-256-GCM constructor with the project's invariant checks,
// the key-material constants, and the Wipe hygiene helper. It is the
// single home for "build the cipher / zero a key" so neither the
// manifest-body codec (internal/manifestcodec) nor the blob pipeline
// (pipeline/stage/aesgcm, pipeline/internal/segaead) re-implements the
// AES-GCM construction.
//
// It is deliberately pure stdlib and dependency-free: every layer that
// touches a DEK can import it without pulling in manifest or pipeline
// machinery.
package aead

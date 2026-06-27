// Package recoverykit encodes and decodes the Scrinium Recovery Kit:
// the disaster-recovery payload of last resort — a plain-text
// artefact carrying StoreID, KDF parameters, and the wrapped DEK.
// With it the rebuild agent can reconstruct a Store whose on-disk
// descriptor replicas are gone, provided the operator still
// knows the passphrase and still has the encrypted blobs.
//
// The format is plain text with named sections rather than JSON
// because recovery is human-driven: an operator reads the file in a
// text editor on a fresh machine after a disk failure. Section
// boundaries and key=value lines survive transcription, copy-paste,
// and OCR off paper backups in ways JSON does not.
//
// Depends only on errs and stdlib — no store, descriptor, or consumer
// imports.
package recoverykit

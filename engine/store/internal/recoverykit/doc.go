// Package recoverykit encodes and decodes the Scrinium Recovery
// Kit per §10.3.
//
// The kit is the disaster-recovery payload of last resort: a
// plain-text artefact carrying StoreID, KDF parameters, and the
// wrapped DEK. With it RebuildIndexAgent can reconstruct a Store
// whose every on-disk replica (L0, L1, L2) has been destroyed
// — provided the operator still knows the passphrase and still
// has the encrypted blobs.
//
// The format is plain text with named sections rather than JSON
// because the recovery scenario is human-driven: an operator
// reading the file in a text editor on a fresh machine after a
// disk failure. Section boundaries and key=value lines survive
// transcription, copy-paste through chat, and OCR off paper
// backups in ways JSON does not.
//
// DAG: recoverykit depends on errs and stdlib. It does not
// import core, descriptor, or any consumer package.
package recoverykit

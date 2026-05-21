// Package segaead implements the segmented (framed) AEAD on-disk
// blob format defined by ADR-59 and specified normatively in
// docs/2. Internals/07 §7.6 and §03 §3.2.1.
//
// It is the blob-level counterpart of internal/manifestcrypto and
// internal/manifestcodec: where those frame and protect the manifest
// file, segaead frames and protects the blob body. An encrypted blob
// is a small header followed by a sequence of independent AEAD
// segments of fixed plaintext size:
//
//	header: magic ‖ version ‖ iv_mode ‖ segment_size ‖ key_id
//	frame:  iv(12) ‖ ct_len(4) ‖ ciphertext+tag
//
// The header is symmetric to the manifest (§7.1) and pack (§8.1)
// headers so the read path and Recovery can read the decryption
// parameters from a small prefix.
//
// # Why segmented
//
// The previous monolithic encoder buffered the whole plaintext
// (io.ReadAll) and sealed it once, so peak memory equalled the blob
// size — a multi-gigabyte blob held entirely in RAM per concurrent
// Put. Segmenting bounds memory at one segment (≈1 MiB by default),
// independent of blob size, in a single streaming pass. It also makes
// the Convergent IV mode single-pass: the IV of a segment is derived
// from that segment's plaintext, known as soon as the segment is read,
// rather than from a whole-blob hash that would force a second pass.
//
// # Generic over cipher.AEAD
//
// Seal and Open take a cipher.AEAD (or a list of candidates for key
// rotation), so the format is independent of the concrete cipher. The
// AES-GCM plugin (engine/plugin/crypto/aesgcm) is a thin adapter; a
// future ChaCha20-Poly1305 plugin can reuse this package unchanged.
// Both use a 12-byte nonce (IVLen), which the format assumes.
//
// # Memory and pooling
//
// A SealReader allocates one plaintext segment buffer per operation,
// which satisfies the ADR-59 O(SegmentSize) invariant. Pooling those
// buffers across operations (ADR-09, sync.Pool) is a future
// optimisation, not a correctness requirement, and is intentionally
// omitted here to keep the package dependency-free.
//
// # Errors
//
// The read side reports a per-segment authentication failure as
// ErrSegmentAuth and structural damage (bad magic, truncation) as
// ErrBadMagic / ErrUnsupportedVersion / ErrTruncated. The aesgcm
// adapter folds ErrSegmentAuth into the public errs.ErrDecryptionFailed
// while letting structural errors surface verbatim, so callers can
// tell "could not decrypt" from "the bytes on disk are malformed".
package segaead

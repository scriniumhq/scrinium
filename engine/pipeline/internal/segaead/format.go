package segaead

// format.go — the on-disk layout of a segmented-AEAD blob
// (ADR-59). The header is symmetric to the manifest header and the
// pack-TOC header: magic + version + mode flag + KeyID, so the read
// path and Recovery can read the decryption parameters from a small
// prefix without touching the body.
//
//	[header]
//	  magic         4 bytes   blob magic (\x00 S B 1)
//	  version       1 byte
//	  iv_mode       1 byte    0x00 random (Disabled) / 0x01 convergent
//	  segment_size  4 bytes   uint32, plaintext segment size
//	  key_id_len    2 bytes   uint16
//	  key_id        N bytes
//	[segment k]
//	  iv            12 bytes
//	  ct_len        4 bytes   uint32, len(ciphertext+tag)
//	  ciphertext    ct_len bytes (ciphertext ‖ AEAD tag)
//
// The frame stream ends at the underlying reader's EOF; the last
// segment is simply the final frame (shorter than segment_size for
// a tail). No explicit terminator is needed because every frame is
// self-describing by ct_len — see the package doc for the "frames
// until EOF" rationale.

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Magic is the 4-byte blob signature. The leading NUL marks the
// file as non-text (same convention as \x00SC1 manifests and
// \x00PK1 packs).
var Magic = [4]byte{0x00, 'S', 'B', '1'}

// Version is the only on-disk format version this package writes
// and reads. A higher version on disk surfaces ErrUnsupportedVersion.
const Version byte = 1

// IVMode is the per-blob IV-derivation mode recorded in the header.
// It mirrors StoreConfig.EncryptedDedup (ADR-58): Random is the
// Disabled default, Convergent enables deterministic addresses.
type IVMode byte

const (
	// IVModeRandom draws a fresh random IV per segment. The same
	// plaintext yields different ciphertext and a different BlobRef:
	// encrypted blobs never deduplicate. Maps to EncryptedDedup
	// Disabled.
	IVModeRandom IVMode = 0x00
	// IVModeConvergent derives the IV deterministically from the
	// segment plaintext, the KeyID and the segment index (see iv.go).
	// One plaintext under one key yields one ciphertext: encrypted
	// blobs deduplicate. Maps to EncryptedDedup Convergent.
	IVModeConvergent IVMode = 0x01
)

func (m IVMode) valid() bool { return m == IVModeRandom || m == IVModeConvergent }

// IVLen is the AES-GCM / ChaCha20-Poly1305 nonce size — 12 bytes,
// fixed by both standards. The format stores one IV per segment
// frame, never a single per-blob IV.
const IVLen = 12

// fixedHeaderLen is the size of the header before the variable
// KeyID: magic(4) + version(1) + iv_mode(1) + segment_size(4) +
// key_id_len(2).
const fixedHeaderLen = 4 + 1 + 1 + 4 + 2

// frameHeaderLen is the per-segment prefix: iv(12) + ct_len(4).
const frameHeaderLen = IVLen + 4

// MaxKeyIDLen bounds the KeyID written into the header. The
// key_id_len field is a uint16, but we cap well below that to match
// the manifest-header limit (one octet there); keeping blobs and
// manifests aligned avoids a KeyID that fits one header and not the
// other.
const MaxKeyIDLen = 255

// MaxSegmentSize is the hard upper bound on a blob's plaintext segment size.
// It is independent of the on-disk header — which an attacker controls — and
// caps the per-frame read allocation: a forged segment_size or ct_len above
// this is rejected rather than honoured, so a tampered blob cannot drive an
// unbounded allocation (Out-Of-Memory DoS). Generous over DefaultSegmentSize
// (1 MiB) to leave room for larger configured segments while keeping a single
// frame bounded.
const MaxSegmentSize = 64 << 20 // 64 MiB

// maxAEADOverhead is a generous upper bound on an AEAD's per-segment expansion
// (tag plus any framing). Real primitives (AES-GCM, ChaCha20-Poly1305) add a
// 16-byte tag; 64 leaves headroom without weakening the ct_len bound.
const maxAEADOverhead = 64

// maxCiphertextLen is the largest legitimate ct_len: a full plaintext segment
// plus AEAD overhead. The read path rejects any frame claiming more before
// allocating its body buffer.
const maxCiphertextLen = MaxSegmentSize + maxAEADOverhead

// Format sentinels. Open failures (tag mismatch, wrong key,
// tampered ciphertext) are reported as ErrSegmentAuth; the aesgcm
// adapter folds that into the public errs.ErrDecryptionFailed.
var (
	ErrBadMagic           = errors.New("segaead: bad blob magic")
	ErrUnsupportedVersion = errors.New("segaead: unsupported blob version")
	ErrBadMode            = errors.New("segaead: unknown iv_mode")
	ErrTruncated          = errors.New("segaead: truncated blob")
	ErrSegmentTooLarge    = errors.New("segaead: segment exceeds maximum size")
	ErrSegmentAuth        = errors.New("segaead: segment authentication failed")
	ErrKeyIDTooLong       = fmt.Errorf("segaead: KeyID exceeds %d bytes", MaxKeyIDLen)
)

// header is the parsed/produced header. SegmentSize is the
// plaintext segment size; KeyID identifies the DEK for the read
// path; Mode selects IV derivation.
type header struct {
	Mode        IVMode
	SegmentSize uint32
	KeyID       string
}

// encodeHeader serialises h to its on-disk bytes.
func encodeHeader(h header) ([]byte, error) {
	if !h.Mode.valid() {
		return nil, ErrBadMode
	}
	if len(h.KeyID) > MaxKeyIDLen {
		return nil, ErrKeyIDTooLong
	}
	out := make([]byte, 0, fixedHeaderLen+len(h.KeyID))
	out = append(out, Magic[:]...)
	out = append(out, Version)
	out = append(out, byte(h.Mode))
	out = binary.BigEndian.AppendUint32(out, h.SegmentSize)
	out = binary.BigEndian.AppendUint16(out, uint16(len(h.KeyID)))
	out = append(out, h.KeyID...)
	return out, nil
}

// decodeFixedHeader parses the fixed-length prefix and returns the
// declared KeyID length so the caller can read the remaining bytes.
func decodeFixedHeader(b []byte) (mode IVMode, segSize uint32, keyIDLen int, err error) {
	if len(b) < fixedHeaderLen {
		return 0, 0, 0, ErrTruncated
	}
	if [4]byte(b[0:4]) != Magic {
		return 0, 0, 0, ErrBadMagic
	}
	if b[4] != Version {
		return 0, 0, 0, fmt.Errorf("%w: %d", ErrUnsupportedVersion, b[4])
	}
	mode = IVMode(b[5])
	if !mode.valid() {
		return 0, 0, 0, ErrBadMode
	}
	segSize = binary.BigEndian.Uint32(b[6:10])
	if segSize > MaxSegmentSize {
		return 0, 0, 0, fmt.Errorf("%w: header segment_size %d", ErrSegmentTooLarge, segSize)
	}
	keyIDLen = int(binary.BigEndian.Uint16(b[10:12]))
	return mode, segSize, keyIDLen, nil
}

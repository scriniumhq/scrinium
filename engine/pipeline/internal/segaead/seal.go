package segaead

// seal.go — the write side: a streaming io.Reader that frames its
// source into segmented-AEAD blobs with O(SegmentSize) memory and a
// single pass over the input (ADR-59). No goroutine, no io.Pipe: the
// reader pulls one plaintext segment from the source on demand,
// seals it, and hands back the frame bytes.

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
)

// DefaultSegmentSize is the plaintext segment size used when a caller
// passes 0 (≈1 MiB, the docs/3 §11 default). At this size the format
// overhead (12-byte IV + 16-byte tag + 4-byte length per segment) is
// ~0.003%.
const DefaultSegmentSize = 1 << 20

// SealParams configures the framed encoder.
type SealParams struct {
	// AEAD seals each segment. Its NonceSize must equal IVLen.
	AEAD cipher.AEAD
	// Mode selects IV derivation. IVModeConvergent requires DEK.
	Mode IVMode
	// DEK is the raw key, used only as the HMAC key for convergent
	// IV derivation. Ignored (may be nil) under IVModeRandom.
	DEK []byte
	// KeyID is recorded in the header and folded into the convergent
	// IV. Empty is valid (pinned-DEK single-key wiring).
	KeyID string
	// SegmentSize is the plaintext segment size. 0 → DefaultSegmentSize.
	SegmentSize int
}

// Seal returns an io.Reader that yields the on-disk blob (header
// followed by segment frames) for the plaintext in src. The
// returned *SealReader also reports the number of bytes produced via
// BytesWritten once it has been drained — the aesgcm Encoder surfaces
// that as TransformResult.OutputSize.
func Seal(src io.Reader, p SealParams) (*SealReader, error) {
	if p.AEAD == nil {
		return nil, fmt.Errorf("segaead: nil AEAD")
	}
	if p.AEAD.NonceSize() != IVLen {
		return nil, fmt.Errorf("segaead: AEAD nonce size %d, want %d",
			p.AEAD.NonceSize(), IVLen)
	}
	if !p.Mode.valid() {
		return nil, ErrBadMode
	}
	if p.Mode == IVModeConvergent && len(p.DEK) == 0 {
		return nil, fmt.Errorf("segaead: convergent mode requires a DEK")
	}
	segSize := p.SegmentSize
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	if segSize > MaxSegmentSize {
		return nil, fmt.Errorf("%w: requested %d", ErrSegmentTooLarge, segSize)
	}

	hdr, err := encodeHeader(header{
		Mode:        p.Mode,
		SegmentSize: uint32(segSize),
		KeyID:       p.KeyID,
	})
	if err != nil {
		return nil, err
	}

	return &SealReader{
		src:     src,
		aead:    p.AEAD,
		mode:    p.Mode,
		dek:     p.DEK,
		keyID:   []byte(p.KeyID),
		segSize: segSize,
		// Pending output starts with the header; segments are appended
		// lazily as the reader is drained. A single per-operation
		// segment buffer keeps memory at O(SegmentSize); pooling across
		// operations (ADR-09) is a future optimisation, not required by
		// the constant-memory invariant.
		pending:    hdr,
		segPlain:   make([]byte, segSize),
		headerDone: false,
	}, nil
}

// SealReader is the framed-encoder reader returned by Seal.
type SealReader struct {
	src     io.Reader
	aead    cipher.AEAD
	mode    IVMode
	dek     []byte
	keyID   []byte
	segSize int

	pending    []byte // bytes not yet handed to Read (header, then one frame)
	segPlain   []byte // reusable plaintext read buffer, len == segSize
	index      uint64
	headerDone bool
	srcDone    bool
	written    int64
	err        error
}

// BytesWritten returns the total number of blob bytes produced so
// far. Meaningful after the reader has been drained to EOF.
func (e *SealReader) BytesWritten() int64 { return e.written }

func (e *SealReader) Read(p []byte) (int, error) {
	for len(e.pending) == 0 {
		if e.err != nil {
			return 0, e.err
		}
		if e.srcDone {
			return 0, io.EOF
		}
		if err := e.fill(); err != nil {
			e.err = err
			if len(e.pending) == 0 {
				return 0, e.err
			}
		}
	}
	n := copy(p, e.pending)
	e.pending = e.pending[n:]
	e.written += int64(n)
	return n, nil
}

// fill appends the next frame to e.pending. The header is emitted as
// the initial pending bytes by Seal, so fill only ever produces
// segment frames. Reaching EOF on the source sets srcDone.
func (e *SealReader) fill() error {
	// The header was placed in pending by Seal; the first fill call
	// already starts on segments.
	e.headerDone = true

	n, err := io.ReadFull(e.src, e.segPlain)
	switch {
	case err == nil || err == io.ErrUnexpectedEOF:
		// nil  → a full segment; more may follow.
		// EOFx → a short tail segment; this is the last one.
		if err == io.ErrUnexpectedEOF {
			e.srcDone = true
		}
		if n == 0 {
			// Only possible via ErrUnexpectedEOF with n==0, which
			// io.ReadFull does not return; defensive.
			return nil
		}
		return e.sealSegment(e.segPlain[:n])
	case err == io.EOF:
		// Clean end with no further bytes: a blob whose length is an
		// exact multiple of segSize, or an empty blob (header only).
		e.srcDone = true
		return nil
	default:
		return fmt.Errorf("segaead: read segment: %w", err)
	}
}

// sealSegment encrypts one plaintext segment and appends its frame
// (iv ‖ ct_len ‖ ciphertext+tag) to pending.
func (e *SealReader) sealSegment(plain []byte) error {
	var iv []byte
	var err error
	switch e.mode {
	case IVModeRandom:
		iv, err = randomIV()
		if err != nil {
			return err
		}
	case IVModeConvergent:
		iv = convergentIV(e.dek, e.keyID, plain, e.index)
	default:
		return ErrBadMode
	}

	ct := e.aead.Seal(nil, iv, plain, nil)

	frame := make([]byte, 0, frameHeaderLen+len(ct))
	frame = append(frame, iv...)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(ct)))
	frame = append(frame, ct...)

	e.pending = append(e.pending, frame...)
	e.index++
	return nil
}

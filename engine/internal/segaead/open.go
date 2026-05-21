package segaead

// open.go — the read side: a streaming io.Reader that consumes a
// segmented-AEAD blob frame by frame, verifying each segment's AEAD
// tag (per-segment integrity, ADR-59 / §03), with O(SegmentSize)
// memory. The IV is read from each frame, never from the manifest
// stage. Multiple candidate AEADs are tried per segment to support
// key rotation (the first that authenticates wins).

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Open returns an io.Reader that yields the plaintext of the blob in
// src. aeads are the candidate primitives (one for pinned-DEK, the
// resolver's ordered list for rotation); each segment is opened with
// the first candidate that authenticates. A segment that no candidate
// can open surfaces ErrSegmentAuth.
func Open(src io.Reader, aeads []cipher.AEAD) (io.Reader, error) {
	if len(aeads) == 0 {
		return nil, fmt.Errorf("segaead: no AEAD candidates")
	}
	for _, a := range aeads {
		if a == nil {
			return nil, fmt.Errorf("segaead: nil AEAD candidate")
		}
		if a.NonceSize() != IVLen {
			return nil, fmt.Errorf("segaead: AEAD nonce size %d, want %d",
				a.NonceSize(), IVLen)
		}
	}
	return &openReader{src: src, aeads: aeads}, nil
}

type openReader struct {
	src   io.Reader
	aeads []cipher.AEAD

	pending    []byte // decrypted plaintext not yet handed to Read
	headerDone bool
	srcDone    bool
	err        error
}

func (d *openReader) Read(p []byte) (int, error) {
	for len(d.pending) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.srcDone {
			return 0, io.EOF
		}
		if err := d.fill(); err != nil {
			d.err = err
			if len(d.pending) == 0 {
				return 0, d.err
			}
		}
	}
	n := copy(p, d.pending)
	d.pending = d.pending[n:]
	return n, nil
}

func (d *openReader) fill() error {
	if !d.headerDone {
		if err := d.readHeader(); err != nil {
			return err
		}
		d.headerDone = true
		return nil
	}
	return d.readSegment()
}

// readHeader consumes the variable-length blob header. It reads the
// fixed prefix first to learn the KeyID length, then the KeyID. The
// IV mode and segment size are read for completeness; the decoder
// does not need them (each frame is self-describing), but a future
// range-read path will.
func (d *openReader) readHeader() error {
	fixed := make([]byte, fixedHeaderLen)
	if _, err := io.ReadFull(d.src, fixed); err != nil {
		return headerReadErr(err)
	}
	_, _, keyIDLen, err := decodeFixedHeader(fixed)
	if err != nil {
		return err
	}
	if keyIDLen > 0 {
		if _, err := io.ReadFull(d.src, make([]byte, keyIDLen)); err != nil {
			return headerReadErr(err)
		}
	}
	return nil
}

// readSegment consumes one frame (iv ‖ ct_len ‖ ciphertext+tag),
// authenticates it against the candidate keys, and appends the
// plaintext to pending. A clean EOF on the frame boundary ends the
// stream.
func (d *openReader) readSegment() error {
	fh := make([]byte, frameHeaderLen)
	n, err := io.ReadFull(d.src, fh)
	switch {
	case err == nil:
		// proceed
	case err == io.EOF && n == 0:
		// Clean end exactly on a frame boundary.
		d.srcDone = true
		return nil
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		return fmt.Errorf("%w: frame header (%d/%d bytes)", ErrTruncated, n, frameHeaderLen)
	default:
		return fmt.Errorf("segaead: read frame header: %w", err)
	}

	iv := fh[:IVLen]
	ctLen := binary.BigEndian.Uint32(fh[IVLen:])

	ct := make([]byte, ctLen)
	if _, err := io.ReadFull(d.src, ct); err != nil {
		return fmt.Errorf("%w: segment body (%w)", ErrTruncated, err)
	}

	var lastErr error
	for _, aead := range d.aeads {
		pt, openErr := aead.Open(nil, iv, ct, nil)
		if openErr == nil {
			d.pending = append(d.pending, pt...)
			return nil
		}
		lastErr = openErr
	}
	return fmt.Errorf("%w: %v", ErrSegmentAuth, lastErr)
}

// headerReadErr maps a short read of the header to a truncation
// error rather than a bare io.EOF, which the caller would mistake
// for a clean end-of-stream.
func headerReadErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: header (%w)", ErrTruncated, err)
	}
	return fmt.Errorf("segaead: read header: %w", err)
}

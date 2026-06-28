package segaead

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// A frame whose ct_len is forged above the format maximum must be rejected
// before the body buffer is allocated — otherwise a tampered blob drives an
// attacker-chosen allocation (OOM DoS).
func TestOpen_RejectsOversizedCtLen(t *testing.T) {
	hdr, err := encodeHeader(header{Mode: IVModeRandom, SegmentSize: DefaultSegmentSize})
	if err != nil {
		t.Fatalf("encodeHeader: %v", err)
	}
	frame := make([]byte, frameHeaderLen)
	binary.BigEndian.PutUint32(frame[IVLen:], maxCiphertextLen+1)
	blob := append(append([]byte{}, hdr...), frame...)

	r, err := Open(bytes.NewReader(blob), []cipher.AEAD{mustAEAD(t, make([]byte, 32))})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrSegmentTooLarge) {
		t.Fatalf("want ErrSegmentTooLarge, got %v", err)
	}
}

// A header whose segment_size is forged above the format maximum is rejected
// at decode, before any frame is read.
func TestOpen_RejectsOversizedSegmentSize(t *testing.T) {
	hdr := make([]byte, fixedHeaderLen)
	copy(hdr[0:4], Magic[:])
	hdr[4] = Version
	hdr[5] = byte(IVModeRandom)
	binary.BigEndian.PutUint32(hdr[6:10], MaxSegmentSize+1)
	// key_id_len (bytes 10:12) left zero.

	r, err := Open(bytes.NewReader(hdr), []cipher.AEAD{mustAEAD(t, make([]byte, 32))})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrSegmentTooLarge) {
		t.Fatalf("want ErrSegmentTooLarge, got %v", err)
	}
}

// Seal refuses a segment size above the format maximum, so a blob that cannot
// be read back is never written.
func TestSeal_RejectsOversizedSegmentSize(t *testing.T) {
	_, err := Seal(bytes.NewReader(nil), SealParams{
		AEAD:        mustAEAD(t, make([]byte, 32)),
		Mode:        IVModeRandom,
		SegmentSize: MaxSegmentSize + 1,
	})
	if !errors.Is(err, ErrSegmentTooLarge) {
		t.Fatalf("want ErrSegmentTooLarge, got %v", err)
	}
}

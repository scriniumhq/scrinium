package segaead

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func mustAEAD(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}
	return g
}

func key32(seed byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed + byte(i)
	}
	return k
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func seal(t *testing.T, p SealParams, plain []byte) []byte {
	t.Helper()
	r, err := Seal(bytes.NewReader(plain), p)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("seal read: %v", err)
	}
	if r.BytesWritten() != int64(len(out)) {
		t.Fatalf("BytesWritten=%d, want %d", r.BytesWritten(), len(out))
	}
	return out
}

func open(t *testing.T, aeads []cipher.AEAD, blob []byte) ([]byte, error) {
	t.Helper()
	r, err := Open(bytes.NewReader(blob), aeads)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestRoundTrip_VariousSizes(t *testing.T) {
	key := key32(1)
	aead := mustAEAD(t, key)
	const seg = 1024

	sizes := []int{0, 1, seg - 1, seg, seg + 1, 3 * seg, 3*seg + 7}
	for _, mode := range []IVMode{IVModeRandom, IVModeConvergent} {
		for _, n := range sizes {
			plain := randBytes(t, n)
			blob := seal(t, SealParams{
				AEAD: aead, Mode: mode, DEK: key, KeyID: "k", SegmentSize: seg,
			}, plain)

			// Header magic present.
			if [4]byte(blob[0:4]) != Magic {
				t.Fatalf("mode=%d n=%d: bad magic", mode, n)
			}
			got, err := open(t, []cipher.AEAD{aead}, blob)
			if err != nil {
				t.Fatalf("mode=%d n=%d: open: %v", mode, n, err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("mode=%d n=%d: round-trip mismatch (got %d bytes)", mode, n, len(got))
			}
		}
	}
}

// Convergent: identical plaintext under identical key → byte-identical
// blob (the property that makes BlobRef deterministic and dedup work).
func TestConvergent_Deterministic(t *testing.T) {
	key := key32(7)
	aead := mustAEAD(t, key)
	plain := randBytes(t, 5000)
	p := SealParams{AEAD: aead, Mode: IVModeConvergent, DEK: key, KeyID: "tenant", SegmentSize: 1024}

	a := seal(t, p, plain)
	b := seal(t, p, plain)
	if !bytes.Equal(a, b) {
		t.Fatal("convergent: same key+plaintext must produce identical bytes")
	}
}

// Different KeyID → different ciphertext, even with the same DEK and
// plaintext (KeyID enters the convergent IV).
func TestConvergent_KeyIDSplitsCiphertext(t *testing.T) {
	key := key32(7)
	aead := mustAEAD(t, key)
	plain := randBytes(t, 5000)
	base := SealParams{AEAD: aead, Mode: IVModeConvergent, DEK: key, SegmentSize: 1024}

	p1 := base
	p1.KeyID = "a"
	p2 := base
	p2.KeyID = "b"
	if bytes.Equal(seal(t, p1, plain), seal(t, p2, plain)) {
		t.Fatal("convergent: distinct KeyID must change ciphertext")
	}
}

// Random mode: identical plaintext → different blobs (no dedup).
func TestRandom_NonDeterministic(t *testing.T) {
	key := key32(3)
	aead := mustAEAD(t, key)
	plain := randBytes(t, 4096)
	p := SealParams{AEAD: aead, Mode: IVModeRandom, KeyID: "k", SegmentSize: 1024}
	if bytes.Equal(seal(t, p, plain), seal(t, p, plain)) {
		t.Fatal("random: identical plaintext must NOT produce identical bytes")
	}
}

func TestWrongKeyFails(t *testing.T) {
	aead1 := mustAEAD(t, key32(1))
	aead2 := mustAEAD(t, key32(2))
	blob := seal(t, SealParams{AEAD: aead1, Mode: IVModeRandom, SegmentSize: 256}, randBytes(t, 1000))
	_, err := open(t, []cipher.AEAD{aead2}, blob)
	if !errors.Is(err, ErrSegmentAuth) {
		t.Fatalf("got %v, want ErrSegmentAuth", err)
	}
}

// Rotation: the survivor blob opens with a candidate that is not the
// first in the list.
func TestRotation_SecondCandidateOpens(t *testing.T) {
	oldKey := key32(10)
	newKey := key32(20)
	blob := seal(t, SealParams{AEAD: mustAEAD(t, oldKey), Mode: IVModeRandom, SegmentSize: 256}, randBytes(t, 800))
	got, err := open(t, []cipher.AEAD{mustAEAD(t, newKey), mustAEAD(t, oldKey)}, blob)
	if err != nil {
		t.Fatalf("rotation open: %v", err)
	}
	if len(got) != 800 {
		t.Fatalf("rotation: got %d bytes", len(got))
	}
}

// Per-segment localisation: corrupting a byte in segment 2 still
// surfaces an auth failure (and would not corrupt other segments in a
// range-read path).
func TestTamperedSegmentFails(t *testing.T) {
	key := key32(5)
	aead := mustAEAD(t, key)
	const seg = 256
	plain := randBytes(t, seg*4) // four full segments
	blob := seal(t, SealParams{AEAD: aead, Mode: IVModeRandom, SegmentSize: seg}, plain)

	// Flip a byte inside the second frame's ciphertext. Header is
	// fixedHeaderLen (no KeyID). Frame 0 = frameHeaderLen + (seg+16).
	frame0 := frameHeaderLen + seg + 16
	target := fixedHeaderLen + frame0 + frameHeaderLen + 4 // a few bytes into frame 1's ct
	blob[target] ^= 0x01
	_, err := open(t, []cipher.AEAD{aead}, blob)
	if !errors.Is(err, ErrSegmentAuth) {
		t.Fatalf("got %v, want ErrSegmentAuth", err)
	}
}

func TestTruncatedBlobFails(t *testing.T) {
	key := key32(9)
	aead := mustAEAD(t, key)
	blob := seal(t, SealParams{AEAD: aead, Mode: IVModeRandom, SegmentSize: 256}, randBytes(t, 1000))
	// Chop the last 10 bytes — a partial final frame body.
	_, err := open(t, []cipher.AEAD{aead}, blob[:len(blob)-10])
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v, want ErrTruncated", err)
	}
}

func TestBadMagicFails(t *testing.T) {
	key := key32(9)
	aead := mustAEAD(t, key)
	blob := seal(t, SealParams{AEAD: aead, Mode: IVModeRandom, SegmentSize: 256}, randBytes(t, 100))
	blob[1] = 'X'
	_, err := open(t, []cipher.AEAD{aead}, blob)
	if !errors.Is(err, ErrBadMagic) {
		t.Fatalf("got %v, want ErrBadMagic", err)
	}
}

// A large blob round-trips while reading in small chunks — exercises
// the lazy refill loop and confirms no whole-blob buffering is needed.
func TestLargeBlobStreaming(t *testing.T) {
	key := key32(42)
	aead := mustAEAD(t, key)
	const seg = 1 << 12
	plain := randBytes(t, seg*17+123)

	r, err := Seal(bytes.NewReader(plain), SealParams{AEAD: aead, Mode: IVModeConvergent, DEK: key, KeyID: "k", SegmentSize: seg})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Drain through a tiny buffer to force many refills.
	var blob bytes.Buffer
	if _, err := io.CopyBuffer(&blob, r, make([]byte, 64)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := open(t, []cipher.AEAD{aead}, blob.Bytes())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("large streaming round-trip mismatch")
	}
}

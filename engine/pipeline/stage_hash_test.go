package pipeline

import (
	"bytes"
	"encoding/hex"
	"io"
	"testing"

	"scrinium.dev/domain"
)

// Coverage for decision R2: per-stage hashes are recorded
// unconditionally, and the LAST stage's hasher is the blobRefHasher
// itself (its output IS the final stream) — one hash pass saved, no
// double-feeding of the final tee.

// notFactory: bitwise-NOT each byte — a second self-inverse stage so a
// two-stage pipeline has two DISTINCT intermediate streams.
type notFactory struct{}

func (notFactory) NewEncoder(EncodeContext) Encoder        { return &notCoder{} }
func (notFactory) NewDecoder(domain.PipelineStage) Decoder { return &notCoder{} }

type notCoder struct{}

func (n *notCoder) Transform(r io.Reader) io.Reader { return &notReader{r: r} }
func (n *notCoder) Result() TransformResult         { return TransformResult{KeyID: "k-not"} }

type notReader struct{ r io.Reader }

func (n *notReader) Read(p []byte) (int, error) {
	c, err := n.r.Read(p)
	for i := 0; i < c; i++ {
		p[i] = ^p[i]
	}
	return c, err
}

func xorBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c ^ 0x5A
	}
	return out
}

func notBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = ^c
	}
	return out
}

// TestRunner_PerStageHashes_TwoStages pins the per-stage hash values of
// a two-stage pipeline: the intermediate stage hashes ITS OWN output
// (own hasher), the last stage's hash equals the digest of the on-disk
// bytes — i.e. the blobRefHasher it reuses (decision R2).
func TestRunner_PerStageHashes_TwoStages(t *testing.T) {
	payload := []byte("per-stage hash pinning payload for decision R2")

	reg := NewTransformerRegistry().
		Register("xor", xorFactory{}).
		Register("not", notFactory{})
	r := NewRunner(stubHashes{}, reg)

	stream, pp, err := r.BuildPut("sha256", bytes.NewReader(payload), []string{"xor", "not"}, EncodeContext{})
	if err != nil {
		t.Fatalf("BuildPut: %v", err)
	}
	onDisk, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ch, br, stages := pp.Finalize()

	mid := xorBytes(payload) // after stage 1 (xor)
	final := notBytes(mid)   // after stage 2 (not) = on-disk
	if !bytes.Equal(onDisk, final) {
		t.Fatalf("on-disk bytes are not not(xor(payload)) — stage order broken")
	}

	wantCH := hex.EncodeToString(sha256Sum(payload))
	wantBR := hex.EncodeToString(sha256Sum(onDisk))
	if string(ch) != wantCH {
		t.Errorf("ContentHash: got %s, want %s", ch, wantCH)
	}
	// The single most important R2 assertion: BlobRef is correct, i.e.
	// the reused hasher was fed the final stream exactly once (a
	// double-feed via a leftover final tee would corrupt it).
	if string(br) != wantBR {
		t.Errorf("BlobRef: got %s, want %s", br, wantBR)
	}

	if len(stages) != 2 {
		t.Fatalf("stages: got %d, want 2", len(stages))
	}
	// Intermediate stage: hash of its own output (xored payload).
	wantMid := "sha256-" + hex.EncodeToString(sha256Sum(mid))
	if stages[0].Hash != wantMid {
		t.Errorf("stage[0].Hash: got %s, want %s", stages[0].Hash, wantMid)
	}
	// Last stage: hash of the final stream — must carry the same digest
	// bytes as BlobRef (one hasher serves both, Sum(nil) is
	// non-destructive).
	wantLast := "sha256-" + wantBR
	if stages[1].Hash != wantLast {
		t.Errorf("stage[1].Hash: got %s, want %s (must equal BlobRef digest)", stages[1].Hash, wantLast)
	}
}

// TestRunner_PerStageHashes_SingleStage: with one stage the stage hash
// and BlobRef are the same digest (the reuse case in its purest form).
func TestRunner_PerStageHashes_SingleStage(t *testing.T) {
	payload := []byte("single-stage reuse")

	r := newTestRunner()
	stream, pp, err := r.BuildPut("sha256", bytes.NewReader(payload), []string{"xor"}, EncodeContext{})
	if err != nil {
		t.Fatalf("BuildPut: %v", err)
	}
	onDisk, _ := io.ReadAll(stream)
	_, br, stages := pp.Finalize()

	if string(br) != hex.EncodeToString(sha256Sum(onDisk)) {
		t.Fatalf("BlobRef mismatch")
	}
	if len(stages) != 1 {
		t.Fatalf("stages: got %d, want 1", len(stages))
	}
	if stages[0].Hash != "sha256-"+string(br) {
		t.Errorf("stage[0].Hash: got %s, want sha256-%s", stages[0].Hash, br)
	}
}

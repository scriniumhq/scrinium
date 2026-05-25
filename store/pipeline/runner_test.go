package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"testing"

	"scrinium.dev/domain"
)

// --- stub hash registry ---

type stubHashes struct{}

func (stubHashes) NewHasher(string) (hash.Hash, error)                   { return sha256.New(), nil }
func (stubHashes) Format(_ string, raw []byte) string                    { return "sha256-" + hex.EncodeToString(raw) }
func (stubHashes) Parse(string) (string, []byte, error)                  { return "", nil, nil }
func (stubHashes) Register(string, func() hash.Hash) domain.HashRegistry { return stubHashes{} }

// --- stub stage: XOR each byte with 0x5A (its own inverse) ---

type xorFactory struct{}

func (xorFactory) NewEncoder(EncodeContext) Encoder        { return &xorCoder{} }
func (xorFactory) NewDecoder(domain.PipelineStage) Decoder { return &xorCoder{} }

type xorCoder struct{}

func (x *xorCoder) Transform(r io.Reader) io.Reader { return &xorReader{r: r} }
func (x *xorCoder) Result() TransformResult         { return TransformResult{KeyID: "k-xor"} }

type xorReader struct{ r io.Reader }

func (x *xorReader) Read(p []byte) (int, error) {
	n, err := x.r.Read(p)
	for i := 0; i < n; i++ {
		p[i] ^= 0x5A
	}
	return n, err
}

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

func newTestRunner() *Runner {
	reg := NewTransformerRegistry().Register("xor", xorFactory{})
	return NewRunner(stubHashes{}, reg)
}

func TestRunner_PutRoundTrip(t *testing.T) {
	payload := []byte("scrinium pipeline runner round-trip payload")

	r := newTestRunner()
	stream, pp, err := r.BuildPut("sha256", bytes.NewReader(payload), []string{"xor"}, EncodeContext{})
	if err != nil {
		t.Fatalf("BuildPut: %v", err)
	}
	onDisk, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ch, br, stages := pp.Finalize()

	// ContentHash = sha256(original); BlobRef = sha256(xored on-disk bytes).
	wantCH := stubHashes{}.Format("sha256", sha256Sum(payload))
	wantBR := stubHashes{}.Format("sha256", sha256Sum(onDisk))
	if string(ch) != wantCH {
		t.Errorf("ContentHash mismatch")
	}
	if string(br) != wantBR {
		t.Errorf("BlobRef mismatch")
	}
	if ch == domain.ContentHash(br) {
		t.Errorf("ContentHash and BlobRef must differ when a stage transforms bytes")
	}
	if pp.ContentBytesRead() != int64(len(payload)) {
		t.Errorf("ContentBytesRead = %d, want %d", pp.ContentBytesRead(), len(payload))
	}
	if len(stages) != 1 || stages[0].Algorithm != "xor" || stages[0].KeyID != "k-xor" {
		t.Errorf("stages wrong: %+v", stages)
	}

	// Read path: decode the on-disk bytes back to the original.
	rc, err := r.BuildGet(stages, nopCloser{bytes.NewReader(onDisk)})
	if err != nil {
		t.Fatalf("BuildGet: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, payload) {
		t.Errorf("round-trip mismatch: got %q", got)
	}
}

func TestRunner_PassthroughZeroStages(t *testing.T) {
	payload := []byte("plain bytes, no stages")
	r := newTestRunner()

	stream, pp, err := r.BuildPut("sha256", bytes.NewReader(payload), nil, EncodeContext{})
	if err != nil {
		t.Fatalf("BuildPut: %v", err)
	}
	onDisk, _ := io.ReadAll(stream)
	ch, br, stages := pp.Finalize()

	if !bytes.Equal(onDisk, payload) {
		t.Errorf("zero-stage must pass bytes through unchanged")
	}
	if string(ch) != string(br) {
		t.Errorf("zero-stage: ContentHash and BlobRef must be equal, got %s vs %s", ch, br)
	}
	if len(stages) != 0 {
		t.Errorf("expected no stages, got %d", len(stages))
	}

	// BuildGet with empty stages returns underlying unchanged.
	under := nopCloser{bytes.NewReader(onDisk)}
	rc, err := r.BuildGet(nil, under)
	if err != nil {
		t.Fatalf("BuildGet: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, payload) {
		t.Errorf("passthrough read mismatch")
	}
}

func TestRunner_ValidateAlgos(t *testing.T) {
	r := newTestRunner()
	if err := r.ValidateAlgos([]string{"xor"}); err != nil {
		t.Errorf("known algo must validate: %v", err)
	}
	if err := r.ValidateAlgos([]string{"xor", "ghost"}); err == nil {
		t.Errorf("unknown algo must fail validation")
	}
}

func sha256Sum(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}

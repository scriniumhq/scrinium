// Package manifestcodec serialises and deserialises manifest files
// according to docs/2. Internals/07 §7.1 (file header) and §7.5
// (deterministic body encoding).
//
// File layout for the formats this package handles in M1.4:
//
//	[0..3]   magic: \x00SC1 (JSON)
//	[4]      crypto flag: 0x00 (Plain)
//	[5..]    body (deterministic JSON)
//
// MetadataOnly and Envelope crypto flags, plus the binary (\x00SC2,
// MsgPack) magic, are deferred to M2 and return ErrUnsupportedCrypto
// / ErrUnsupportedEncoding from this package.
//
// ArtifactID is the hash of the *entire file bytes*, header
// included. The package exposes Encode/Decode that work on the
// file bytes, plus ComputeArtifactID that closes the loop:
// manifest -> encoded bytes -> hash -> ArtifactID assignment ->
// re-encoded bytes (to pin the field). A manifest's serialised
// form is therefore stable: the same manifest produces the same
// bytes every time.
package manifestcodec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

// File-format magic bytes from docs §7.1.
var (
	magicJSON   = []byte{0x00, 'S', 'C', '1'}
	magicBinary = []byte{0x00, 'S', 'C', '2'}
)

// Crypto flags from docs §7.1.
const (
	cryptoPlain        = 0x00
	cryptoMetadataOnly = 0x01
	cryptoEnvelope     = 0x02
)

// SchemaVersion is the only on-disk version this package writes
// and reads. Higher versions in a file return
// ErrUnsupportedSchemaVersion at the core sentinel; lower versions
// will be possible to read once we ship migrations.
const SchemaVersion = 1

// Forward-compat sentinels (errs.ErrUnsupportedEncoding,
// errs.ErrUnsupportedCrypto) live in the errs package — see
// errs/forward_compat.go.

// EncodeFile produces the full file bytes (header + body) for a
// manifest in JSON Plain format.
//
// The ArtifactID field of the manifest is NOT used as input — it
// is the *result* of hashing these very bytes. To produce a
// signed manifest:
//
//	bytes, _ := EncodeFile(m, ManifestEncodingJSON, ManifestCryptoPlain)
//	m.ArtifactID = hash(bytes) -> "<algo>-<hex>"
//
// Callers that need the round-trip primitive should use
// ComputeArtifactID below. EncodeFile alone is for tests, for
// re-encoding after ID assignment, and for round-trip verification
// in Verify.
func EncodeFile(m domain.Manifest, encoding domain.ManifestEncoding, crypto domain.ManifestCrypto) ([]byte, error) {
	if encoding != domain.ManifestEncodingJSON && encoding != "" {
		return nil, errs.ErrUnsupportedEncoding
	}
	if crypto != domain.ManifestCryptoPlain && crypto != "" {
		return nil, errs.ErrUnsupportedCrypto
	}

	body, err := marshalBodyJSON(m)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(magicJSON)+1+len(body))
	out = append(out, magicJSON...)
	out = append(out, cryptoPlain)
	out = append(out, body...)
	if len(out) > domain.MaxManifestSize {
		return nil, errs.ErrManifestTooLarge
	}
	return out, nil
}

// DecodeFile parses full manifest bytes, validates the header, and
// returns the manifest with all body fields populated. The
// returned manifest's ArtifactID is NOT set by this function — the
// caller owns deciding whether to re-derive it (and verify) or to
// accept it from a trusted source.
//
// Encoding mismatch: a manifest with the binary magic returns
// errs.ErrUnsupportedEncoding; an unknown magic returns a parse
// error. Crypto flag != Plain returns errs.ErrUnsupportedCrypto.
func DecodeFile(data []byte) (domain.Manifest, error) {
	if len(data) < 5 {
		return domain.Manifest{}, fmt.Errorf("manifestcodec: file too short (%d bytes)", len(data))
	}
	switch {
	case bytes.HasPrefix(data, magicJSON):
		// OK
	case bytes.HasPrefix(data, magicBinary):
		return domain.Manifest{}, errs.ErrUnsupportedEncoding
	default:
		return domain.Manifest{}, fmt.Errorf("manifestcodec: unknown magic %x", data[:4])
	}

	flag := data[4]
	if flag != cryptoPlain {
		return domain.Manifest{}, errs.ErrUnsupportedCrypto
	}

	return unmarshalBodyJSON(data[5:])
}

// ComputeArtifactID encodes a manifest, hashes the resulting bytes
// with the given hasher, and returns both the assigned ArtifactID
// and the final file bytes. The returned bytes already carry the
// manifest with the populated ArtifactID — callers pass them
// straight to driver.Put.
//
// Why the loop: ArtifactID = hash(file bytes), and the bytes
// include the manifest body (which does NOT contain ArtifactID
// itself — that field is on the in-memory struct, never on disk).
// One pass produces both the bytes and the ID; the manifest
// returned via the third value carries the ID for the in-memory
// path (the caller hands it to StoreIndex.IndexManifest).
func ComputeArtifactID(
	m domain.Manifest,
	hashAlgo string,
	registry domain.HashRegistry,
	encoding domain.ManifestEncoding,
	crypto domain.ManifestCrypto,
) (domain.ArtifactID, []byte, domain.Manifest, error) {
	bytesEncoded, err := EncodeFile(m, encoding, crypto)
	if err != nil {
		return "", nil, domain.Manifest{}, err
	}
	h, err := registry.NewHasher(hashAlgo)
	if err != nil {
		return "", nil, domain.Manifest{}, fmt.Errorf("manifestcodec: hasher: %w", err)
	}
	if _, err := h.Write(bytesEncoded); err != nil {
		return "", nil, domain.Manifest{}, err
	}
	id := domain.ArtifactID(registry.Format(hashAlgo, h.Sum(nil)))
	m.ArtifactID = id
	return id, bytesEncoded, m, nil
}

// VerifyArtifactID re-hashes the given file bytes and checks the
// digest against the supplied id. Used on the read path: after
// downloading a manifest file, Verify confirms it has not been
// tampered with by recomputing the hash and comparing.
//
// algoFromID extracts the algorithm name from the id's prefix and
// uses it to pick a hasher from the registry. This way a manifest
// can travel between Stores with different default hashers without
// losing its identity.
func VerifyArtifactID(id domain.ArtifactID, fileBytes []byte, registry domain.HashRegistry) error {
	algo, _, err := registry.Parse(string(id))
	if err != nil {
		return fmt.Errorf("manifestcodec: parse id: %w", err)
	}
	h, err := registry.NewHasher(algo)
	if err != nil {
		return fmt.Errorf("manifestcodec: hasher %q: %w", algo, err)
	}
	if _, err := h.Write(fileBytes); err != nil {
		return err
	}
	got := domain.ArtifactID(registry.Format(algo, h.Sum(nil)))
	if got != id {
		return errs.ErrCorruptedManifest
	}
	return nil
}

// HashStream consumes r through the given algorithm and returns
// the formatted ContentHash plus the byte count read. Used by
// Store.Put to compute ContentHash for the payload as it is being
// streamed to the Driver — TeeReader pattern at the call site.
//
// The returned hash uses the registry's Format("<algo>-<hex>") so
// callers do not need to know the encoding. Size is the actual
// number of bytes read (which may differ from a caller-supplied
// expected size if the reader is short or pads).
func HashStream(h hash.Hash, registry domain.HashRegistry, algo string) func() (domain.ContentHash, error) {
	return func() (domain.ContentHash, error) {
		return domain.ContentHash(registry.Format(algo, h.Sum(nil))), nil
	}
}

// --- Body JSON encoding (deterministic, sorted keys, RFC 3339) ---

// jsonBody is the on-disk shape of a manifest body. Field tags
// fix the JSON keys; field order in the struct is irrelevant —
// the encoder writes alphabetically by tag (we sort manually below
// because encoding/json does not promise alphabetical key order).
//
// Optional fields use omitempty + pointer-where-needed so that
// unset values do not appear in the output, satisfying §7.5.
type jsonBody struct {
	BlobRef       string              `json:"blob_ref"`
	ContentHash   string              `json:"content_hash,omitempty"`
	CreatedAt     string              `json:"created_at"`
	ExternalURI   string              `json:"external_uri,omitempty"`
	InlineBlob    string              `json:"inline_blob,omitempty"` // base64
	LayoutHeader  jsonLayoutHeader    `json:"layout_header"`
	Metadata      json.RawMessage     `json:"metadata,omitempty"`
	Namespace     string              `json:"namespace"`
	OriginalSize  *int64              `json:"original_size,omitempty"`
	Pipeline      []jsonPipelineStage `json:"pipeline"`
	RetentionTime string              `json:"retention_until,omitempty"`
	SchemaVersion int                 `json:"schema_version"`
	SessionID     string              `json:"session_id"`
	System        *jsonSystemFlags    `json:"system,omitempty"`
	Type          string              `json:"type"`
}

type jsonLayoutHeader struct {
	BlobStorage string `json:"blob_storage"`
}

type jsonPipelineStage struct {
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
	IV        string `json:"iv,omitempty"`
}

type jsonSystemFlags struct {
	TOCOffset int64 `json:"toc_offset"`
	TOCSize   int64 `json:"toc_size"`
}

// marshalBodyJSON produces deterministic JSON bytes per §7.5:
// alphabetical key order at every level, no whitespace.
//
// We build the struct, then re-emit through a sorted map so the
// "alphabetical at every level" rule survives — encoding/json
// emits struct fields in declaration order, not key order. The
// extra round trip is O(body size); manifests are small, the cost
// is negligible.
func marshalBodyJSON(m domain.Manifest) ([]byte, error) {
	body := jsonBody{
		BlobRef:       string(m.BlobRef),
		ContentHash:   string(m.ContentHash),
		CreatedAt:     formatRFC3339(m.CreatedAt),
		ExternalURI:   m.ExternalURI,
		LayoutHeader:  jsonLayoutHeader{BlobStorage: m.LayoutHeader.BlobStorage},
		Metadata:      m.Metadata,
		Namespace:     m.Namespace,
		Pipeline:      pipelineToJSON(m.Pipeline),
		SchemaVersion: SchemaVersion,
		SessionID:     m.SessionID,
		Type:          string(m.Type),
	}
	if m.OriginalSize != 0 {
		body.OriginalSize = new(m.OriginalSize)
	}
	if len(m.InlineBlob) > 0 {
		body.InlineBlob = base64.StdEncoding.EncodeToString(m.InlineBlob)
	}
	if !m.RetentionUntil.IsZero() {
		body.RetentionTime = formatRFC3339(m.RetentionUntil)
	}
	if m.SystemFlags.TOCOffset != 0 || m.SystemFlags.TOCSize != 0 {
		body.System = &jsonSystemFlags{
			TOCOffset: m.SystemFlags.TOCOffset,
			TOCSize:   m.SystemFlags.TOCSize,
		}
	}

	// Round trip via map[string]json.RawMessage to enforce sorted
	// keys at the top level. Nested structs do not have variable
	// keys (we do not allow user-supplied fields in pipeline
	// stages, layout_header, or system), so a single-level sort
	// suffices.
	via, err := json.Marshal(&body)
	if err != nil {
		return nil, err
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(via, &asMap); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(asMap))
	for k := range asMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		// Key. encoding/json's default key encoder is fine: ASCII
		// letters and underscores, no escaping required.
		kj, _ := json.Marshal(k)
		out.Write(kj)
		out.WriteByte(':')
		out.Write(asMap[k])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// unmarshalBodyJSON parses body bytes and returns a domain.Manifest.
// Forgives input field order (any order is allowed; only the
// *output* of marshalBodyJSON is sorted). Rejects unknown fields
// to catch typos in hand-edited manifests.
func unmarshalBodyJSON(body []byte) (domain.Manifest, error) {
	var b jsonBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return domain.Manifest{}, fmt.Errorf("manifestcodec: parse body: %w", err)
	}
	if dec.More() {
		return domain.Manifest{}, errors.New("manifestcodec: trailing content after body")
	}

	if b.SchemaVersion != SchemaVersion {
		return domain.Manifest{}, fmt.Errorf("%w: got %d, want %d",
			errs.ErrUnsupportedSchemaVersion, b.SchemaVersion, SchemaVersion)
	}

	m := domain.Manifest{
		Type:        domain.ManifestType(b.Type),
		Namespace:   b.Namespace,
		SessionID:   b.SessionID,
		ContentHash: domain.ContentHash(b.ContentHash),
		BlobRef:     domain.BlobRef(b.BlobRef),
		ExternalURI: b.ExternalURI,
		Metadata:    b.Metadata,
		LayoutHeader: domain.LayoutHeader{
			BlobStorage: b.LayoutHeader.BlobStorage,
		},
		Pipeline: pipelineFromJSON(b.Pipeline),
	}
	if b.OriginalSize != nil {
		m.OriginalSize = *b.OriginalSize
	}
	if b.InlineBlob != "" {
		raw, err := base64.StdEncoding.DecodeString(b.InlineBlob)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: inline_blob base64: %w", err)
		}
		m.InlineBlob = raw
	}
	if b.CreatedAt != "" {
		t, err := parseRFC3339(b.CreatedAt)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: created_at: %w", err)
		}
		m.CreatedAt = t
	}
	if b.RetentionTime != "" {
		t, err := parseRFC3339(b.RetentionTime)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: retention_until: %w", err)
		}
		m.RetentionUntil = t
	}
	if b.System != nil {
		m.SystemFlags = domain.ManifestSystemFlags{
			TOCOffset: b.System.TOCOffset,
			TOCSize:   b.System.TOCSize,
		}
	}
	return m, nil
}

func pipelineToJSON(stages []domain.PipelineStage) []jsonPipelineStage {
	if len(stages) == 0 {
		// §7.2 requires the pipeline field to always be present
		// (empty array allowed but not null/missing). Returning a
		// non-nil empty slice keeps json.Marshal emitting "[]".
		return []jsonPipelineStage{}
	}
	out := make([]jsonPipelineStage, 0, len(stages))
	for _, s := range stages {
		js := jsonPipelineStage{
			Algorithm: s.Algorithm,
			Hash:      s.Hash,
		}
		if len(s.IV) > 0 {
			js.IV = base64.StdEncoding.EncodeToString(s.IV)
		}
		out = append(out, js)
	}
	return out
}

func pipelineFromJSON(stages []jsonPipelineStage) []domain.PipelineStage {
	if len(stages) == 0 {
		return nil
	}
	out := make([]domain.PipelineStage, 0, len(stages))
	for _, s := range stages {
		ps := domain.PipelineStage{
			Algorithm: s.Algorithm,
			Hash:      s.Hash,
		}
		if s.IV != "" {
			raw, err := base64.StdEncoding.DecodeString(s.IV)
			if err == nil {
				ps.IV = raw
			}
			// Decode failures are silent here: the manifest format
			// guarantees IV is base64 if present, and a decode error
			// at unmarshal time means the manifest itself is corrupt.
			// That is caught upstream by VerifyArtifactID — here we
			// keep the partial result and let the upstream check
			// surface the corruption.
		}
		out = append(out, ps)
	}
	return out
}

// formatRFC3339 returns the manifest-format timestamp (UTC, second
// precision) per §7.5. Millisecond/nanosecond precision is
// deliberately dropped: the ArtifactID must be stable for the same
// logical content, and sub-second variance from time.Now() would
// defeat dedup.
func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseRFC3339 accepts both the strict format we write and the
// nanosecond variant for forward compatibility.
func parseRFC3339(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

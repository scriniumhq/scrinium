package manifestcodec

// body_json.go — deterministic JSON body encoding per docs/2.
// Internals/07 §7.5. The struct types here pin the on-disk
// JSON shape; field declaration order in jsonBody mirrors the
// alphabetical JSON-tag order so encoding/json's
// declaration-order emission already produces sorted output
// (TestEncodeFile_DeterministicOrder guards the contract).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/timefmt"
)

// --- Body JSON encoding (deterministic, sorted keys, RFC 3339) ---

// jsonBody is the on-disk shape of a manifest body. Field tags
// fix the JSON keys; field order in the struct mirrors the
// alphabetical order of those tags so encoding/json's
// declaration-order emission already produces the deterministic
// output §7.5 requires — no round-trip-through-map needed.
//
// Optional fields use omitempty + pointer-where-needed so that
// unset values do not appear in the output.
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
	KeyID     string `json:"key_id,omitempty"`
}

type jsonSystemFlags struct {
	TOCOffset int64 `json:"toc_offset"`
	TOCSize   int64 `json:"toc_size"`
}

// marshalBodyJSON produces deterministic JSON bytes per §7.5:
// alphabetical key order, no whitespace. Determinism is achieved
// without a round-trip through map[string]json.RawMessage because
// jsonBody declares its fields in JSON-tag-alphabetical order;
// encoding/json emits struct fields in declaration order, so the
// output is already sorted at the top level. Nested structs
// (jsonLayoutHeader, jsonPipelineStage, jsonSystemFlags) likewise
// declare their fields in order. TestEncodeFile_DeterministicOrder
// guards the contract.
func marshalBodyJSON(m domain.Manifest) ([]byte, error) {
	body := jsonBody{
		BlobRef:       string(m.BlobRef),
		ContentHash:   string(m.ContentHash),
		CreatedAt:     timefmt.Format(m.CreatedAt),
		ExternalURI:   m.ExternalURI,
		LayoutHeader:  jsonLayoutHeader{BlobStorage: m.LayoutHeader.BlobStorage},
		Metadata:      m.Metadata,
		Namespace:     m.Namespace,
		Pipeline:      pipelineToJSON(m.Pipeline),
		SchemaVersion: SchemaVersion,
		SessionID:     string(m.SessionID),
		Type:          string(m.Type),
	}
	if m.OriginalSize != 0 {
		body.OriginalSize = new(m.OriginalSize)
	}
	if len(m.InlineBlob) > 0 {
		body.InlineBlob = base64.StdEncoding.EncodeToString(m.InlineBlob)
	}
	if !m.RetentionUntil.IsZero() {
		body.RetentionTime = timefmt.Format(m.RetentionUntil)
	}
	if m.SystemFlags.TOCOffset != 0 || m.SystemFlags.TOCSize != 0 {
		body.System = &jsonSystemFlags{
			TOCOffset: m.SystemFlags.TOCOffset,
			TOCSize:   m.SystemFlags.TOCSize,
		}
	}

	return json.Marshal(&body)
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
		SessionID:   domain.SessionID(b.SessionID),
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
		t, err := timefmt.Parse(b.CreatedAt)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: created_at: %w", err)
		}
		m.CreatedAt = t
	}
	if b.RetentionTime != "" {
		t, err := timefmt.Parse(b.RetentionTime)
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
			KeyID:     s.KeyID,
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
			KeyID:     s.KeyID,
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

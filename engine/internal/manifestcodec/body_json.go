package manifestcodec

// body_json.go — deterministic JSON body encoding per docs/2.
// Internals/07 §7.2 + §7.5. The body has top-level fields
// (sys, ext, usr, inline_blob) with sys carrying every system
// field defined by §7.2. Determinism: top-level and sys-level
// fields are declared in alphabetical JSON-tag order so
// encoding/json's declaration-order emission produces sorted
// output without round-tripping through a map.

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

// jsonBody is the on-disk top-level shape of a manifest body.
// Per ADR-54 the body has three named blocks (sys, ext, usr)
// plus an optional inline_blob. Field declaration order matches
// alphabetical JSON-tag order so the output is deterministic
// without map-round-tripping.
//
// Sealed/Paranoid bridge: the Metadata field stays in jsonBody
// during the ADR-54 migration so the encrypted modes (which
// still encrypt through Manifest.Metadata) continue to
// round-trip. Removed in R2b-iii together with the legacy
// Manifest.Metadata field.
type jsonBody struct {
	Ext        json.RawMessage `json:"ext,omitempty"`
	InlineBlob string          `json:"inline_blob,omitempty"` // base64
	Metadata   json.RawMessage `json:"metadata,omitempty"`    // Deprecated: bridge for Sealed/Paranoid; removed in R2b-iii.
	Sys        jsonSys         `json:"sys"`
	Usr        json.RawMessage `json:"usr,omitempty"`
}

// jsonSys is the on-disk shape of the sys block. Holds every
// system-level field. Optional fields use omitempty +
// pointer-where-needed so unset values do not appear in output.
type jsonSys struct {
	BlobRef       string              `json:"blob_ref"`
	ContentHash   string              `json:"content_hash,omitempty"`
	CreatedAt     string              `json:"created_at"`
	ExternalURI   string              `json:"external_uri,omitempty"`
	LayoutHeader  jsonLayoutHeader    `json:"layout_header"`
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
// alphabetical key order, no whitespace. Determinism comes from
// declaring fields in alphabetical-by-JSON-tag order at both
// the top level and within jsonSys; encoding/json emits in
// declaration order. TestEncodeFile_KeysAreAlphabetical guards
// the contract.
func marshalBodyJSON(m domain.Manifest) ([]byte, error) {
	body := jsonBody{
		Ext:      m.Ext,
		Metadata: m.Metadata,
		Usr:      m.Usr,
		Sys: jsonSys{
			BlobRef:       string(m.BlobRef),
			ContentHash:   string(m.ContentHash),
			CreatedAt:     timefmt.Format(m.CreatedAt),
			ExternalURI:   m.ExternalURI,
			LayoutHeader:  jsonLayoutHeader{BlobStorage: m.LayoutHeader.BlobStorage},
			Namespace:     m.Namespace,
			Pipeline:      pipelineToJSON(m.Pipeline),
			SchemaVersion: SchemaVersion,
			SessionID:     string(m.SessionID),
			Type:          string(m.Type),
		},
	}
	if m.OriginalSize != 0 {
		body.Sys.OriginalSize = new(m.OriginalSize)
	}
	if len(m.InlineBlob) > 0 {
		body.InlineBlob = base64.StdEncoding.EncodeToString(m.InlineBlob)
	}
	if !m.RetentionUntil.IsZero() {
		body.Sys.RetentionTime = timefmt.Format(m.RetentionUntil)
	}
	if m.SystemFlags.TOCOffset != 0 || m.SystemFlags.TOCSize != 0 {
		body.Sys.System = &jsonSystemFlags{
			TOCOffset: m.SystemFlags.TOCOffset,
			TOCSize:   m.SystemFlags.TOCSize,
		}
	}

	return json.Marshal(&body)
}

// unmarshalBodyJSON parses body bytes and returns a
// domain.Manifest. Forgives input field order (any order is
// allowed; only the *output* of marshalBodyJSON is sorted).
// Rejects unknown fields to catch typos in hand-edited
// manifests.
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

	if b.Sys.SchemaVersion != SchemaVersion {
		return domain.Manifest{}, fmt.Errorf("%w: got %d, want %d",
			errs.ErrUnsupportedSchemaVersion, b.Sys.SchemaVersion, SchemaVersion)
	}

	m := domain.Manifest{
		Type:        domain.ManifestType(b.Sys.Type),
		Namespace:   b.Sys.Namespace,
		SessionID:   domain.SessionID(b.Sys.SessionID),
		ContentHash: domain.ContentHash(b.Sys.ContentHash),
		BlobRef:     domain.BlobRef(b.Sys.BlobRef),
		ExternalURI: b.Sys.ExternalURI,
		Ext:         b.Ext,
		Usr:         b.Usr,
		Metadata:    b.Metadata,
		LayoutHeader: domain.LayoutHeader{
			BlobStorage: b.Sys.LayoutHeader.BlobStorage,
		},
		Pipeline: pipelineFromJSON(b.Sys.Pipeline),
	}
	if b.Sys.OriginalSize != nil {
		m.OriginalSize = *b.Sys.OriginalSize
	}
	if b.InlineBlob != "" {
		raw, err := base64.StdEncoding.DecodeString(b.InlineBlob)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: inline_blob base64: %w", err)
		}
		m.InlineBlob = raw
	}
	if b.Sys.CreatedAt != "" {
		t, err := timefmt.Parse(b.Sys.CreatedAt)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: created_at: %w", err)
		}
		m.CreatedAt = t
	}
	if b.Sys.RetentionTime != "" {
		t, err := timefmt.Parse(b.Sys.RetentionTime)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: retention_until: %w", err)
		}
		m.RetentionUntil = t
	}
	if b.Sys.System != nil {
		m.SystemFlags = domain.ManifestSystemFlags{
			TOCOffset: b.Sys.System.TOCOffset,
			TOCSize:   b.Sys.System.TOCSize,
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

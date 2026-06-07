package artifact

// body.go — deterministic JSON body encoding per docs/2 Internals/07
// §7.2 + §7.5. The body has top-level fields (sys, ext, usr, inline_blob)
// with sys carrying every system field. Determinism: top-level and
// sys-level fields are declared in alphabetical JSON-tag order so
// encoding/json's declaration-order emission produces sorted output
// without round-tripping through a map.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/timefmt"
	"scrinium.dev/errs"
)

// jsonBody is the on-disk top-level shape of a manifest body (ADR-54:
// three named blocks sys/ext/usr plus an optional inline_blob). Field
// declaration order matches alphabetical JSON-tag order for determinism.
type jsonBody struct {
	Ext        json.RawMessage `json:"ext,omitempty"`
	InlineBlob string          `json:"inline_blob,omitempty"` // base64
	Sys        jsonSys         `json:"sys"`
	Usr        json.RawMessage `json:"usr,omitempty"`
}

// jsonSys is the on-disk shape of the sys block — every system-level
// field. Optional fields use omitempty (+ pointer where a zero value is
// meaningful) so unset values do not appear in output.
//
// artifact_id is the floating handle (the external identity); it IS
// serialised here (unlike the manifest digest, which is the hash of these
// bytes and lives only in-memory / as the filename). identity_meta_hash
// (md) and identity_nonce are the other handle inputs and are serialised
// so the handle stays reproducible and survives index loss.
type jsonSys struct {
	ArtifactID       string              `json:"artifact_id,omitempty"`
	BlobRef          string              `json:"blob_ref"`
	ContentHash      string              `json:"content_hash,omitempty"`
	CreatedAt        string              `json:"created_at"`
	IdentityMetaHash string              `json:"identity_meta_hash,omitempty"`
	IdentityNonce    string              `json:"identity_nonce,omitempty"` // base64
	LayoutHeader     jsonLayoutHeader    `json:"layout_header"`
	Namespace        string              `json:"namespace"`
	OriginalSize     *int64              `json:"original_size,omitempty"`
	Pipeline         []jsonPipelineStage `json:"pipeline"`
	RetentionTime    string              `json:"retention_until,omitempty"`
	SchemaVersion    int                 `json:"schema_version"`
	SessionID        string              `json:"session_id"`
	System           *jsonSystemFlags    `json:"system,omitempty"`
	Type             string              `json:"type"`
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

// marshalBodyJSON produces deterministic JSON bytes per §7.5: alphabetical
// key order, no whitespace. Determinism comes from declaring fields in
// alphabetical-by-JSON-tag order at both levels; encoding/json emits in
// declaration order.
func marshalBodyJSON(m domain.Manifest) ([]byte, error) {
	body := jsonBody{
		Ext: m.Ext,
		Usr: m.Usr,
		Sys: jsonSys{
			ArtifactID:       string(m.ArtifactID),
			BlobRef:          string(m.BlobRef),
			ContentHash:      string(m.ContentHash),
			CreatedAt:        timefmt.Format(m.CreatedAt),
			IdentityMetaHash: m.IdentityMetaHash,
			LayoutHeader:     jsonLayoutHeader{BlobStorage: m.LayoutHeader.BlobStorage},
			Namespace:        m.Namespace,
			Pipeline:         pipelineToJSON(m.Pipeline),
			SchemaVersion:    SchemaVersion,
			SessionID:        string(m.SessionID),
			Type:             string(m.Type),
		},
	}
	if len(m.IdentityNonce) > 0 {
		body.Sys.IdentityNonce = base64.StdEncoding.EncodeToString(m.IdentityNonce)
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

// unmarshalBodyJSON parses body bytes into a domain.Manifest. Input field
// order is forgiven (only marshal output is sorted); unknown fields are
// rejected to catch typos in hand-edited manifests.
func unmarshalBodyJSON(body []byte) (domain.Manifest, error) {
	var b jsonBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return domain.Manifest{}, fmt.Errorf("artifact: parse body: %w", err)
	}
	if dec.More() {
		return domain.Manifest{}, errors.New("artifact: trailing content after body")
	}

	if b.Sys.SchemaVersion != SchemaVersion {
		return domain.Manifest{}, fmt.Errorf("%w: got %d, want %d",
			errs.ErrUnsupportedSchemaVersion, b.Sys.SchemaVersion, SchemaVersion)
	}

	m := domain.Manifest{
		ArtifactID:       domain.ArtifactID(b.Sys.ArtifactID),
		IdentityMetaHash: b.Sys.IdentityMetaHash,
		Type:             domain.ManifestType(b.Sys.Type),
		Namespace:        b.Sys.Namespace,
		SessionID:        domain.SessionID(b.Sys.SessionID),
		ContentHash:      domain.ContentHash(b.Sys.ContentHash),
		BlobRef:          domain.BlobRef(b.Sys.BlobRef),
		Ext:              b.Ext,
		Usr:              b.Usr,
		LayoutHeader: domain.LayoutHeader{
			BlobStorage: b.Sys.LayoutHeader.BlobStorage,
		},
		Pipeline: pipelineFromJSON(b.Sys.Pipeline),
	}
	if b.Sys.IdentityNonce != "" {
		raw, err := base64.StdEncoding.DecodeString(b.Sys.IdentityNonce)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: identity_nonce base64: %w", err)
		}
		m.IdentityNonce = raw
	}
	if b.Sys.OriginalSize != nil {
		m.OriginalSize = *b.Sys.OriginalSize
	}
	if b.InlineBlob != "" {
		raw, err := base64.StdEncoding.DecodeString(b.InlineBlob)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: inline_blob base64: %w", err)
		}
		m.InlineBlob = raw
	}
	if b.Sys.CreatedAt != "" {
		t, err := timefmt.Parse(b.Sys.CreatedAt)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: created_at: %w", err)
		}
		m.CreatedAt = t
	}
	if b.Sys.RetentionTime != "" {
		t, err := timefmt.Parse(b.Sys.RetentionTime)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: retention_until: %w", err)
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
		// §7.2 requires the pipeline field to always be present (empty
		// array allowed, but not null/missing). A non-nil empty slice
		// keeps json.Marshal emitting "[]".
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
			// Decode failures are silent here: the format guarantees IV is
			// base64 if present, so a decode error means the manifest is
			// corrupt — caught upstream by VerifyManifestDigest. We keep the
			// partial result and let that check surface the corruption.
		}
		out = append(out, ps)
	}
	return out
}

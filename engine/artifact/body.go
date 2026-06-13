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
//
// Reference model (ADR-92/93): blob_refs is the ordered array of blob
// references (россыпь — one, composite — chunks, pack — [toc, pack]) and
// handle_refs the ordered array of artifact→artifact edges (the DAG).
// MIGRATION: type/namespace/system are still written transitionally —
// type until the core dispatch is removed, namespace until its readers
// move to ext, system until the pack model is rewritten.
type jsonSys struct {
	ArtifactID       string              `json:"artifact_id,omitempty"`
	BlobRefs         []string            `json:"blob_refs"`
	ContentHash      string              `json:"content_hash,omitempty"`
	CreatedAt        string              `json:"created_at"`
	HandleRefs       []string            `json:"handle_refs,omitempty"`
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
			BlobRefs:         blobRefsToJSON(m),
			ContentHash:      string(m.ContentHash),
			CreatedAt:        timefmt.Format(m.CreatedAt),
			HandleRefs:       handleRefsToJSON(m.HandleRefs),
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

// blobRefsToJSON renders the manifest's blob references as the on-disk
// blob_refs array. Bridge (ADR-92 migration): if BlobRefs is unset it
// falls back to the single legacy BlobRef (россыпь), so callers not yet
// migrated to the array keep producing correct output. Always non-nil so
// the field is emitted (§7.5 requires blob_refs present).
func blobRefsToJSON(m domain.Manifest) []string {
	if len(m.BlobRefs) > 0 {
		out := make([]string, len(m.BlobRefs))
		for i, r := range m.BlobRefs {
			out[i] = string(r)
		}
		return out
	}
	if m.BlobRef != "" {
		return []string{string(m.BlobRef)}
	}
	return []string{}
}

// handleRefsToJSON renders HandleRefs as the on-disk handle_refs array, or
// nil to omit it (omitempty) when there are no artifact→artifact edges.
func handleRefsToJSON(refs []domain.HandleRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, len(refs))
	for i, h := range refs {
		out[i] = string(h)
	}
	return out
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
		BlobRefs:         blobRefsFromJSON(b.Sys.BlobRefs),
		HandleRefs:       handleRefsFromJSON(b.Sys.HandleRefs),
		Ext:              b.Ext,
		Usr:              b.Usr,
		LayoutHeader: domain.LayoutHeader{
			BlobStorage: b.Sys.LayoutHeader.BlobStorage,
		},
		Pipeline: pipelineFromJSON(b.Sys.Pipeline),
	}
	// Bridge (ADR-92 migration): keep the single legacy BlobRef populated
	// from the first element so россыпь readers not yet migrated to
	// BlobRefs keep working.
	if len(m.BlobRefs) > 0 {
		m.BlobRef = m.BlobRefs[0]
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

// blobRefsFromJSON converts the on-disk blob_refs array to domain.BlobRef
// values; nil for an empty/absent array.
func blobRefsFromJSON(refs []string) []domain.BlobRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]domain.BlobRef, len(refs))
	for i, r := range refs {
		out[i] = domain.BlobRef(r)
	}
	return out
}

// handleRefsFromJSON converts the on-disk handle_refs array to
// domain.HandleRef values; nil for an empty/absent array.
func handleRefsFromJSON(refs []string) []domain.HandleRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]domain.HandleRef, len(refs))
	for i, r := range refs {
		out[i] = domain.HandleRef(r)
	}
	return out
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

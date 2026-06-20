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
// MIGRATION: namespace is still written transitionally — until its
// readers move to ext (ADR-79).
type jsonSys struct {
	ArtifactID       string              `json:"artifact_id,omitempty"`
	BlobRefs         []string            `json:"blob_refs"`
	ContentHash      string              `json:"content_hash,omitempty"`
	CreatedAt        string              `json:"created_at"`
	HandleRefs       []string            `json:"handle_refs,omitempty"`
	HashAlgo         string              `json:"hash_algo,omitempty"`
	IdentityMetaHash string              `json:"identity_meta_hash,omitempty"`
	IdentityNonce    string              `json:"identity_nonce,omitempty"` // base64
	LayoutHeader     jsonLayoutHeader    `json:"layout_header"`
	OriginalSize     *int64              `json:"original_size,omitempty"`
	Pipeline         []jsonPipelineStage `json:"pipeline"`
	RetentionUntil   string              `json:"retention_until,omitempty"`
	SchemaVersion    int                 `json:"schema_version"`
	SessionID        string              `json:"session_id"`
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
			HashAlgo:         m.HashAlgo,
			IdentityMetaHash: m.IdentityMetaHash,
			LayoutHeader:     jsonLayoutHeader{BlobStorage: m.LayoutHeader.BlobStorage},
			Pipeline:         pipelineToJSON(m.Pipeline),
			SchemaVersion:    SchemaVersion,
			SessionID:        string(m.SessionID),
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
		body.Sys.RetentionUntil = timefmt.Format(m.RetentionUntil)
	}

	return json.Marshal(&body)
}

// blobRefsToJSON renders the manifest's blob references as the on-disk
// blob_refs array (ADR-92). Always non-nil so the field is emitted
// (§7.5 requires blob_refs present); empty for the Inline strategy.
func blobRefsToJSON(m domain.Manifest) []string {
	out := make([]string, len(m.BlobRefs))
	for i, r := range m.BlobRefs {
		out[i] = string(r)
	}
	return out
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

	pipeline, err := pipelineFromJSON(b.Sys.Pipeline)
	if err != nil {
		return domain.Manifest{}, err
	}
	m := domain.Manifest{
		ArtifactID:       domain.ArtifactID(b.Sys.ArtifactID),
		IdentityMetaHash: b.Sys.IdentityMetaHash,
		SessionID:        domain.SessionID(b.Sys.SessionID),
		ContentHash:      domain.ContentHash(b.Sys.ContentHash),
		HashAlgo:         b.Sys.HashAlgo,
		BlobRefs:         blobRefsFromJSON(b.Sys.BlobRefs),
		HandleRefs:       handleRefsFromJSON(b.Sys.HandleRefs),
		Ext:              b.Ext,
		Usr:              b.Usr,
		LayoutHeader: domain.LayoutHeader{
			BlobStorage: b.Sys.LayoutHeader.BlobStorage,
		},
		Pipeline: pipeline,
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
	if b.Sys.RetentionUntil != "" {
		t, err := timefmt.Parse(b.Sys.RetentionUntil)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: retention_until: %w", err)
		}
		m.RetentionUntil = t
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

func pipelineFromJSON(stages []jsonPipelineStage) ([]domain.PipelineStage, error) {
	if len(stages) == 0 {
		return nil, nil
	}
	out := make([]domain.PipelineStage, 0, len(stages))
	for _, s := range stages {
		ps := domain.PipelineStage{
			Algorithm: s.Algorithm,
			Hash:      s.Hash,
			KeyID:     s.KeyID,
		}
		if s.IV != "" {
			// A non-base64 IV is a hard error, matching identity_nonce and
			// inline_blob. The ManifestDigest is the hash of the raw file
			// bytes, so a VerifyManifestDigest pass does NOT validate field
			// syntax — silently dropping a bad IV would fail open into a
			// later, worse-sited decryption fault.
			raw, err := base64.StdEncoding.DecodeString(s.IV)
			if err != nil {
				return nil, fmt.Errorf("artifact: pipeline iv base64: %w", err)
			}
			ps.IV = raw
		}
		out = append(out, ps)
	}
	return out, nil
}

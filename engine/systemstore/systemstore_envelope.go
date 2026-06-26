package systemstore

import (
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// envelope is the systemstore-owned JSON that rides inside a system
// artifact's InlineBlob (ADR-104). It carries the store identity and the
// consumer payload; the artifact's name lives in the manifest slot
// (Manifest.Name) and its seq in the path, so neither is repeated here — the
// envelope holds only what the manifest structure does not already have. The
// manifest format and schema_version are unchanged: this sub-format is owned
// by systemstore, and the core treats InlineBlob as opaque.
type envelope struct {
	// StoreID is the identity of the store that owns the artifact, checked
	// on read against the descriptor's store_id. Mandatory.
	StoreID string `json:"store_id"`
	// InlinePayload is the consumer's payload, base64 in JSON (so a binary
	// payload — e.g. a checkpoint .db until it moves external, ADR-105 — is
	// carried safely). Optional: absent for a status artifact whose meaning
	// is its mere presence.
	InlinePayload []byte `json:"inline_payload,omitempty"`
	// ExternalPayloadRef is a single ManifestDigest of an external, headless
	// data artifact for large payloads (ADR-105). Optional. Resolution is a
	// later iteration's concern; the field is carried now so the envelope
	// shape is stable.
	ExternalPayloadRef string `json:"external_payload_ref,omitempty"`
}

// wrapEnvelope builds the InlineBlob bytes for a system artifact: the consumer
// payload sealed in an envelope carrying storeID. An empty payload yields a
// status envelope (presence-only).
func wrapEnvelope(storeID string, payload []byte) ([]byte, error) {
	b, err := json.Marshal(envelope{StoreID: storeID, InlinePayload: payload})
	if err != nil {
		return nil, fmt.Errorf("system store: marshal envelope: %w", err)
	}
	return b, nil
}

// openEnvelope parses and validates the envelope in a loaded manifest's
// InlineBlob against the reading store's authoritative storeID. It returns the
// parsed envelope or a typed category error: ErrSystemArtifactMalformed
// (unparseable, or no store_id) or ErrSystemArtifactForeign (an explicit
// store_id mismatch). A match — or an unverifiable check when authoritative is
// empty — passes; the classifier treats an empty side as Unknown, never
// Foreign, so only an explicit mismatch is rejected.
func openEnvelope(blob []byte, authoritativeStoreID string) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return envelope{}, fmt.Errorf("%w: %v", errs.ErrSystemArtifactMalformed, err)
	}
	if env.StoreID == "" {
		return envelope{}, fmt.Errorf("%w: envelope has no store_id", errs.ErrSystemArtifactMalformed)
	}
	if domain.ClassifyStoreOwnership(env.StoreID, authoritativeStoreID) == domain.StoreOwnershipForeign {
		return envelope{}, fmt.Errorf("%w: artifact store %q != %q",
			errs.ErrSystemArtifactForeign, env.StoreID, authoritativeStoreID)
	}
	return env, nil
}

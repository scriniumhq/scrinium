package aesgcm

import (
	"crypto/cipher"
	"errors"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/pipeline"
)

// factoryResolver is the resolver-backed AES-GCM TransformerFactory.
// Unlike the pinned-DEK factory it holds no AEAD primitive: the DEK
// is resolved per operation through a coreapi.KeyResolver, which lets
// a single factory cover key rotation, multi-tenant stores, and
// crypto-shredding.
//
// The engine picks the write KeyID once via
// KeyResolver.ResolveWriteKey and passes it (plus the EncryptedDedup
// mode and SegmentSize) through EncodeContext; the Encoder records
// the KeyID in TransformResult.KeyID, and the Pipeline runner copies
// it into manifest.Pipeline[i].KeyID. On Get the Decoder reads the
// recorded KeyID from the stage and asks the resolver for candidate
// keys, trying each until one segment-AEAD-opens.
type factoryResolver struct {
	resolver pipeline.KeyResolver
}

// errKeyResolverMissing surfaces a nil resolver at the moment of
// Transform — the wiring was incomplete. Distinct from
// errKeyResolverEmpty (resolver present, but no keys for the
// requested KeyID).
var errKeyResolverMissing = errors.New("aesgcm: KeyResolver not provided")

// errKeyResolverEmpty surfaces an empty result from GetKeys.
// Treated as a decryption-side failure on read; treated as a
// configuration error on write (Store opened without a usable DEK).
var errKeyResolverEmpty = errors.New("aesgcm: KeyResolver returned no keys")

// NewWithResolver constructs an AES-256-GCM TransformerFactory
// backed by a KeyResolver. Use this when the Store may operate with
// multiple DEKs over its lifetime — encrypted-Store bootstraps,
// RotateKEK aftermath, multi-tenant routing.
//
// The resolver may be nil at construction time; absence is surfaced
// on the first Transform that needs a key.
func NewWithResolver(resolver pipeline.KeyResolver) pipeline.TransformerFactory {
	return &factoryResolver{resolver: resolver}
}

// NewEncoder returns an Encoder bound to the write KeyID the engine
// resolved for this operation (ec.KeyID), the IV mode derived from
// ec.EncryptedDedup, and ec.SegmentSize. The DEK lookup happens on
// first Transform.
func (f *factoryResolver) NewEncoder(ec pipeline.EncodeContext) pipeline.Encoder {
	return &resolverEncoder{
		resolver: f.resolver,
		keyID:    ec.KeyID,
		mode:     ivModeFor(ec.EncryptedDedup),
		segSize:  ec.SegmentSize,
	}
}

// NewDecoder returns a Decoder bound to the recorded stage KeyID.
// The DEK lookup (and candidate enumeration for rotation) happens on
// Transform; the IV comes from each segment frame, not the stage.
func (f *factoryResolver) NewDecoder(stage domain.PipelineStage) pipeline.Decoder {
	return &resolverDecoder{
		resolver: f.resolver,
		keyID:    stage.KeyID,
	}
}

// AEAD implements pipeline.AEADCapable for the resolver-backed
// factory. Same rationale as the pinned-DEK factory: each segment's
// GCM tag authenticates the read, so VerifyOnRead=Auto can skip the
// explicit ContentHash recomputation.
func (f *factoryResolver) AEAD() {}

// resolveKeys returns the raw candidate DEKs the resolver yields for
// keyID, in resolver order. The write side uses keys[0] (both to
// build the AEAD and as the convergent-IV HMAC key); the read side
// builds an AEAD from each candidate.
func resolveKeys(resolver pipeline.KeyResolver, keyID string) ([][]byte, error) {
	if resolver == nil {
		return nil, errKeyResolverMissing
	}
	keys, err := resolver.GetKeys(keyID)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errKeyResolverEmpty
	}
	return keys, nil
}

// resolveAEADs builds AEAD primitives for every candidate DEK, in
// resolver order. Used by the read path; the write path only needs
// keys[0] and builds its single AEAD directly.
func resolveAEADs(resolver pipeline.KeyResolver, keyID string) ([]cipher.AEAD, error) {
	keys, err := resolveKeys(resolver, keyID)
	if err != nil {
		return nil, err
	}
	out := make([]cipher.AEAD, 0, len(keys))
	for _, k := range keys {
		aead, err := buildAEAD(k)
		if err != nil {
			return nil, err
		}
		out = append(out, aead)
	}
	return out, nil
}

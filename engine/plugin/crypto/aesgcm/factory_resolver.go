package aesgcm

import (
	"crypto/cipher"
	"errors"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
)

// factoryResolver is the resolver-backed AES-GCM
// TransformerFactory. Unlike the pinned-DEK factory it holds no
// AEAD primitive: the DEK is resolved per operation through a
// core.KeyResolver, which lets a single factory cover key
// rotation, multi-tenant stores, and crypto-shredding.
//
// The engine picks the write KeyID once via
// KeyResolver.ResolveWriteKey and passes it through EncodeContext;
// the Encoder records it in TransformResult.KeyID, and the Pipeline
// runner copies the field into manifest.Pipeline[i].KeyID. On Get
// the Decoder reads the recorded KeyID from the stage and asks the
// resolver for candidate keys, trying each until one decrypts.
type factoryResolver struct {
	resolver core.KeyResolver
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
func NewWithResolver(resolver core.KeyResolver) core.TransformerFactory {
	return &factoryResolver{resolver: resolver}
}

// NewEncoder returns an Encoder bound to the write KeyID the engine
// resolved for this operation (ec.KeyID). The DEK lookup happens on
// first Transform.
func (f *factoryResolver) NewEncoder(ec core.EncodeContext) core.Encoder {
	return &resolverEncoder{resolver: f.resolver, keyID: ec.KeyID}
}

// NewDecoder returns a Decoder bound to the recorded stage KeyID
// and IV. The DEK lookup happens on Transform.
func (f *factoryResolver) NewDecoder(stage domain.PipelineStage) core.Decoder {
	return &resolverDecoder{
		resolver: f.resolver,
		keyID:    stage.KeyID,
		iv:       stage.IV,
	}
}

// AEAD implements core.AEADCapable for the resolver-backed factory.
// Same rationale as the pinned-DEK factory: the GCM tag
// authenticates every read, so VerifyOnRead=Auto can skip the
// explicit ContentHash recomputation.
func (f *factoryResolver) AEAD() {}

// resolveAEADs returns AEAD primitives for every candidate DEK the
// resolver yields for keyID, in resolver order. The caller (Decoder)
// tries each in turn; the Encoder always uses the first.
func resolveAEADs(resolver core.KeyResolver, keyID string) ([]cipher.AEAD, error) {
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

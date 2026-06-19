package crypto

import (
	"context"
	"errors"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/keyring"
	"scrinium.dev/engine/store/internal/recoverykit"
	"scrinium.dev/errs"
)

// CallProvider invokes the configured PassphraseProvider with the given
// hint, classifying its error returns. A nil provider surfaces
// ErrPassphraseRequired; a provider that returns an error gets that error
// wrapped with ErrPassphraseProvider so callers can branch with
// errors.Is.
//
// The returned slice is owned by the caller and MUST be wiped with
// aead.Wipe once the KEK has been derived. CallProvider does not retain a
// reference.
func CallProvider(ctx context.Context, p domain.PassphraseProvider, hint domain.PassphraseHint) ([]byte, error) {
	if p == nil {
		return nil, errs.ErrPassphraseRequired
	}
	pass, err := p(ctx, hint)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrPassphraseProvider, err)
	}
	if len(pass) == 0 {
		return nil, errs.ErrPassphraseRequired
	}
	return pass, nil
}

// buildRecoveryKit assembles the kit text for an encrypted descriptor.
// Called at InitStore (and later RotateKEK / SetPassphrase) before disk
// I/O — a kit-build failure aborts the operation rather than producing a
// Store the host cannot recover.
func buildRecoveryKit(desc *descriptor.Descriptor, wrappedDEK []byte) ([]byte, error) {
	if desc.KDFParams == nil {
		return nil, errors.New("crypto: descriptor missing KDFParams")
	}
	return recoverykit.Encode(recoverykit.Kit{
		StoreID:      desc.StoreID,
		CreatedAt:    time.Now().UTC(),
		Algorithm:    desc.KDFParams.Algorithm,
		Salt:         desc.KDFParams.Salt,
		Time:         desc.KDFParams.Time,
		Memory:       desc.KDFParams.Memory,
		Threads:      desc.KDFParams.Threads,
		EncryptedDEK: wrappedDEK,
	})
}

// InitEncryptedDEK is the encrypted-DEK leg of InitStore: prompt for a
// passphrase, derive the KEK via Argon2id, wrap the DEK, and produce the
// Recovery Kit. Both side-effects observable to the caller (descriptor
// mutation and kit emission) are returned by value — the function does
// not touch *desc directly so the Plain path stays a trivial else-branch.
//
// On any failure the caller is responsible for zeroing dek; this function
// does not own its lifetime. The passphrase buffer IS owned here and is
// wiped before return.
//
// Centralising this leg lets future variants (KMS-resolved DEK, hardware
// token) drop in beside it as siblings rather than as a growing if/else
// inside InitStore.
func InitEncryptedDEK(
	ctx context.Context,
	storeID string,
	dek []byte,
	provider domain.PassphraseProvider,
	cfgKDFParams *domain.KDFParams,
) (wrappedDEK []byte, kdfParams descriptor.KDFParams, kit []byte, err error) {
	passphrase, perr := CallProvider(ctx, provider, domain.PassphraseHint{
		StoreID: storeID,
		Reason:  "init",
	})
	if perr != nil {
		return nil, descriptor.KDFParams{}, nil, perr
	}

	// cfgKDFParams is the client-side cost override; nil means
	// "use kdf.Default()". WrapDEK handles the zero value, so we
	// dereference only when present.
	var cost domain.KDFParams
	if cfgKDFParams != nil {
		cost = *cfgKDFParams
	}
	wrapped, params, werr := keyring.WrapDEK(dek, passphrase, cost)
	aead.Wipe(passphrase)
	if werr != nil {
		return nil, descriptor.KDFParams{}, nil, fmt.Errorf("wrap DEK: %w", werr)
	}

	// Build the Recovery Kit against a temporary descriptor view before
	// any disk I/O so a kit-generation failure aborts the Store creation.
	probe := &descriptor.Descriptor{
		StoreID:       storeID,
		SchemaVersion: descriptor.CurrentSchemaVersion,
		KDFParams:     &params,
	}
	kitBytes, kerr := buildRecoveryKit(probe, wrapped)
	if kerr != nil {
		return nil, descriptor.KDFParams{}, nil, fmt.Errorf("build recovery kit: %w", kerr)
	}
	return wrapped, params, kitBytes, nil
}

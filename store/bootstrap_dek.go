package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/store/internal/aead"
	"scrinium.dev/store/internal/descriptor"
	"scrinium.dev/store/internal/keyring"
	"scrinium.dev/store/internal/recoverykit"
)

// buildRecoveryKit assembles the kit text for a freshly-encrypted
// descriptor. Called at InitStore (and later RotateKEK /
// SetPassphrase) before disk I/O — a kit-build failure aborts
// the Store creation rather than producing a Store the host
// cannot recover.
func buildRecoveryKit(desc *descriptor.Descriptor, wrappedDEK []byte) ([]byte, error) {
	if desc.KDFParams == nil {
		return nil, errors.New("buildRecoveryKit: descriptor missing KDFParams")
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

// initEncryptedDEK is the encrypted-DEK leg of InitStore: prompt
// for a passphrase, derive the KEK via Argon2id, wrap the DEK,
// and produce the Recovery Kit. Both side-effects observable to
// the caller (descriptor mutation and kit emission) are returned
// by value — the function does not touch *desc directly so the
// Plain path stays a trivial else-branch.
//
// On any failure the caller is responsible for zeroing dek; this
// function does not own its lifetime. The passphrase buffer IS
// owned here and is wiped before return.
//
// Centralising this leg lets future variants (KMS-resolved DEK,
// hardware token) drop in beside it as siblings rather than as a
// growing if/else inside InitStore.
func initEncryptedDEK(
	ctx context.Context,
	storeID string,
	dek []byte,
	provider PassphraseProvider,
	cfgKDFParams *domain.KDFParams,
) (wrappedDEK []byte, kdfParams descriptor.KDFParams, kit []byte, err error) {
	passphrase, perr := callProvider(ctx, provider, PassphraseHint{
		StoreID: storeID,
		Reason:  "init",
	})
	if perr != nil {
		return nil, descriptor.KDFParams{}, nil, perr
	}

	// cfgKDFParams is the client-side cost override; nil means
	// "use kdf.Default()". wrapDEK handles the zero value, so we
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

	// Build the Recovery Kit against a temporary descriptor view
	// before any disk I/O so a kit-generation failure aborts the
	// Store creation.
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

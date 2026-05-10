package scrinium

import (
	"context"
	"errors"

	"scrinium.dev/engine/errs"
)

// OpenOrInit opens the store if it already exists, or initialises
// a fresh one if it does not. Convenient for examples, single-
// binary tools, and tests; production daemons typically separate
// "init" and "serve" subcommands so an operator can audit when a
// brand-new store is being created.
//
// Return values:
//
//   - *Scrinium     — ready-to-use runtime, identical in shape
//     whether it came from Open or Init.
//   - []byte        — recovery kit. Non-nil ONLY when this call
//     just initialised an encrypted store (i.e.
//     created == true AND cfg.PassphraseFile is
//     set). Host MUST persist this out of band.
//   - bool created  — true if this call initialised the store,
//     false if it opened an existing one.
//   - error         — any failure other than "store does not
//     exist". A genuine error (bad URI, no
//     permission, disk fault) does NOT silently
//     fall through to Init.
//
// The "store not found" detection uses errors.Is against
// errs.ErrStoreNotFound, which bridges to fs.ErrNotExist — so the
// branch fires only on the specific not-initialised case, not on
// every Open failure.
//
// Example:
//
//	s, kit, created, err := scrinium.OpenOrInit(ctx, cfg)
//	if err != nil {
//	    return err
//	}
//	defer s.Close()
//	if created && kit != nil {
//	    persistRecoveryKit(kit)  // host responsibility
//	}
func OpenOrInit(ctx context.Context, cfg Config) (*Scrinium, []byte, bool, error) {
	s, err := Open(ctx, cfg)
	if err == nil {
		return s, nil, false, nil
	}
	if !errors.Is(err, errs.ErrStoreNotFound) {
		// Genuine error — bad URI, no permission, corrupted
		// descriptor, etc. Surface it; do NOT silently
		// reinitialise on top.
		return nil, nil, false, err
	}

	s, kit, err := Init(ctx, cfg)
	if err != nil {
		return nil, nil, false, err
	}
	return s, kit, true, nil
}

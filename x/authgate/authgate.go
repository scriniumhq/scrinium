// Package authgate is the AuthGate Extension (ADR-80): a Behavioral gate
// over a Store plus a pluggable rights source (PolicyProvider). One
// authorization mechanism, identical locally and (later) over the network
// — only the provider changes.
//
// LOCAL AUTHGATE IS A SAFETY-GUARD, NOT A SECURITY BOUNDARY. It protects
// against mistakes (your own program won't accidentally wipe a store), not
// against intent: an owner holding the files and key reopens the store
// with other rights. The real access boundary is the network arbiter
// (M7, not yet built). See 1. Concepts/09 Security Model.
//
// STATUS: architectural skeleton (M7). The contract (WrapAuth,
// PolicyProvider, GrantedRights) matches "3. Reference/05 Extensions/05
// AuthGate". Gating logic on the wrapped Store is present; LocalPolicy
// derivation from open parameters and RBAC/namespace filtering are stubs.
//
// DESIGN NOTE (sync): AuthGate gates UpdateConfig (an AdminStore op), so it
// wraps the full store.Store, not just the store.DataStore data plane that
// the standard wrapper.Factory axis decorates (00 Contract §0.3). It is
// therefore applied as a full-store decorator via WrapAuth rather than
// through the per-store wrapper.Factory axis. How (and whether) AuthGate
// also presents as an extension.Extension is left open until M7.
package authgate

import (
	"context"
	"errors"
	"fmt"
	"io"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
)

// ErrPermissionDenied is returned when an operation lacks the required
// right. Callers that do not need to distinguish it from a locked store
// may treat it like errs.ErrLocked (05 AuthGate §5.3).
var ErrPermissionDenied = errors.New("scrinium: permission denied")

// PolicyProvider is the swappable source of rights (Local now;
// Remote/RBAC — M7). Rights are resolved once at store open / client
// connect and enforced on every operation.
type PolicyProvider interface {
	Resolve(ctx context.Context) (GrantedRights, error)
}

// GrantedRights is the resolved permission set for a wrapped Store.
type GrantedRights struct {
	CanWrite   bool
	CanDelete  bool
	CanAdmin   bool     // UpdateConfig and other administrative operations
	Namespaces []string // visibility limit (RBAC — M7); empty = no limit
	Source     string   // provenance of the rights, for audit/diagnostics
}

// WrapAuth wraps inner with an authorization gate driven by p. Rights are
// resolved once here; each operation is checked before delegating to inner
// (05 AuthGate §5.1).
func WrapAuth(inner store.Store, p PolicyProvider) (store.Store, error) {
	if inner == nil {
		return nil, errors.New("authgate: nil inner store")
	}
	if p == nil {
		return nil, errors.New("authgate: nil policy provider")
	}
	// The doc signature carries no ctx; the one-time resolve uses a
	// background context. TODO(authgate): thread the open ctx if needed.
	rights, err := p.Resolve(context.Background())
	if err != nil {
		return nil, fmt.Errorf("authgate: resolve rights: %w", err)
	}
	return &authStore{Store: inner, rights: rights}, nil
}

// authStore embeds store.Store so read/admin-read methods delegate
// transparently; it overrides only the gated mutating operations.
type authStore struct {
	store.Store
	rights GrantedRights
}

func (s *authStore) Put(ctx context.Context, a domain.Artifact, opts ...domain.PutOption) (domain.ArtifactID, error) {
	if !s.rights.CanWrite {
		return "", ErrPermissionDenied
	}
	return s.Store.Put(ctx, a, opts...)
}

func (s *authStore) PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error) {
	if !s.rights.CanWrite {
		return "", ErrPermissionDenied
	}
	return s.Store.PutBlob(ctx, r, blobType)
}

func (s *authStore) Delete(ctx context.Context, id domain.ArtifactID) error {
	if !s.rights.CanDelete {
		return ErrPermissionDenied
	}
	return s.Store.Delete(ctx, id)
}

func (s *authStore) RollbackSession(ctx context.Context, sessionID domain.SessionID) error {
	if !s.rights.CanDelete {
		return ErrPermissionDenied
	}
	return s.Store.RollbackSession(ctx, sessionID)
}

func (s *authStore) UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error {
	if !s.rights.CanAdmin {
		return ErrPermissionDenied
	}
	return s.Store.UpdateConfig(ctx, cfg)
}

// TODO(authgate, M7/RBAC): filter Get/Walk visibility by rights.Namespaces
// in composition with the namespace Extension (04 Namespace); reads are
// currently ungated (delegated via the embedded Store).

// LocalPolicy is the safety-guard provider: it derives GrantedRights from
// the store's open parameters (OpenReadOnly / OpenWithoutAdmin / default).
type LocalPolicy struct {
	// TODO(authgate): carry the open-parameter limiters
	// (read-only / without-admin) the store was opened with.
}

func (LocalPolicy) Resolve(ctx context.Context) (GrantedRights, error) {
	// TODO(authgate): map open parameters onto rights per §5.2:
	//   OpenReadOnly     → CanWrite=false, CanDelete=false, CanAdmin=false
	//   OpenWithoutAdmin → CanWrite=true,  CanDelete=true,  CanAdmin=false
	//   default          → all rights
	// Skeleton grants all rights (no limiter wired yet).
	return GrantedRights{
		CanWrite:  true,
		CanDelete: true,
		CanAdmin:  true,
		Source:    "local-skeleton",
	}, nil
}

// Guards.
var (
	_ store.Store    = (*authStore)(nil)
	_ PolicyProvider = LocalPolicy{}
)

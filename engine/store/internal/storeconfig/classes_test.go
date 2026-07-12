package storeconfig

import (
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Coverage for the ADR-110 class map: PlanConnection is the
// OpenStore-side check — class I → ErrConfigMismatch, class II →
// ErrGovernanceMismatch, class III → the session overlay (or a
// governance refusal under SessionOverrides=Deny). Zero values mean
// "not asked" and pass in every class (there is no silent-ignore
// outcome for populated fields, INV-110-5).

func activeForConnTests() domain.StoreConfig {
	a := ApplyDefaults(domain.StoreConfig{})
	a.RetentionPeriod = 90 * 24 * time.Hour
	return a
}

func TestPlanConnection_EmptyRequestPasses(t *testing.T) {
	overlay, err := PlanConnection(domain.StoreConfig{}, activeForConnTests())
	if err != nil {
		t.Fatalf("empty client config must pass, got %v", err)
	}
	if overlay != (domain.StoreConfig{}) {
		t.Errorf("empty request must yield a zero overlay, got %+v", overlay)
	}
}

func TestValidateConnection_MatchingFieldsPass(t *testing.T) {
	active := activeForConnTests()
	req := domain.StoreConfig{
		DeletionPolicy:  active.DeletionPolicy,  // class II, matching
		RetentionPeriod: active.RetentionPeriod, // class II, matching
		VerifyOnRead:    active.VerifyOnRead,    // class III, matching
		ContentHasher:   active.ContentHasher,   // class I, matching
	}
	if _, err := PlanConnection(req, active); err != nil {
		t.Fatalf("matching fields must pass, got %v", err)
	}
}

func TestValidateConnection_ClassI_Refused(t *testing.T) {
	active := activeForConnTests()
	req := domain.StoreConfig{ContentHasher: domain.HashBLAKE3}
	if active.ContentHasher == domain.HashBLAKE3 {
		req.ContentHasher = domain.HashSHA256
	}
	if _, err := PlanConnection(req, active); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("class I divergence: want ErrConfigMismatch, got %v", err)
	}
}

func TestValidateConnection_ClassII_Refused(t *testing.T) {
	active := activeForConnTests()
	cases := map[string]domain.StoreConfig{
		"DeletionPolicy":       {DeletionPolicy: domain.DeletionPolicyNoDelete},
		"RetentionPeriod":      {RetentionPeriod: active.RetentionPeriod + time.Hour},
		"TombstoneGracePeriod": {TombstoneGracePeriod: active.TombstoneGracePeriod + time.Hour},
		"GCLeasePolicy":        {GCLeasePolicy: domain.GCLeaseLeaderElection},
		"SessionOverrides":     {SessionOverrides: domain.SessionOverridesDeny},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PlanConnection(req, active)
			if !errors.Is(err, errs.ErrGovernanceMismatch) {
				t.Errorf("want ErrGovernanceMismatch, got %v", err)
			}
			// The refusal must name the admin path — the error is the
			// operator's map out of the situation.
			if err != nil && !containsAll(err.Error(), name, "UpdateConfig") {
				t.Errorf("refusal must name the field and the admin path, got %q", err.Error())
			}
		})
	}
}

func TestPlanConnection_ClassIII_BecomesOverlay(t *testing.T) {
	active := activeForConnTests()
	req := domain.StoreConfig{
		BlobStorage:     domain.BlobStorageInline,
		VerifyOnRead:    domain.VerifyOnReadForceEnabled,
		InlineBlobLimit: 4096,
		PackAlignment:   domain.PackAlignment4096,
		EagerFetchLimit: 1 << 20,
	}
	overlay, err := PlanConnection(req, active)
	if err != nil {
		t.Fatalf("class III under Allow must yield an overlay, got %v", err)
	}
	if overlay.BlobStorage != domain.BlobStorageInline ||
		overlay.VerifyOnRead != domain.VerifyOnReadForceEnabled ||
		overlay.InlineBlobLimit != 4096 ||
		overlay.PackAlignment != domain.PackAlignment4096 ||
		overlay.EagerFetchLimit != 1<<20 {
		t.Errorf("overlay lost fields: %+v", overlay)
	}
	// The overlay carries class III ONLY — governance and identity
	// stay zero even if the caller populated them with matching values.
	if overlay.DeletionPolicy != "" || overlay.ContentHasher != "" {
		t.Errorf("overlay must carry class III only, got %+v", overlay)
	}

	// Merge semantics: populated overlay fields win, the rest are the
	// store defaults.
	eff := MergeSession(active, overlay)
	if eff.BlobStorage != domain.BlobStorageInline || eff.InlineBlobLimit != 4096 {
		t.Errorf("merge lost overlay values: %+v", eff)
	}
	if eff.DeletionPolicy != active.DeletionPolicy || eff.RetentionPeriod != active.RetentionPeriod {
		t.Errorf("merge must not touch governance: %+v", eff)
	}
}

func TestPlanConnection_DenyRefusesClassIII(t *testing.T) {
	active := activeForConnTests()
	active.SessionOverrides = domain.SessionOverridesDeny
	req := domain.StoreConfig{BlobStorage: domain.BlobStorageInline}
	if _, err := PlanConnection(req, active); !errors.Is(err, errs.ErrGovernanceMismatch) {
		t.Errorf("Deny must refuse class III like class II, got %v", err)
	}
	// A MATCHING class-III value is not a divergence — passes even
	// under Deny.
	req = domain.StoreConfig{VerifyOnRead: active.VerifyOnRead}
	if _, err := PlanConnection(req, active); err != nil {
		t.Errorf("matching class III under Deny must pass, got %v", err)
	}
}

func TestPlanConnection_CryptoTail(t *testing.T) {
	active := activeForConnTests()
	active.Pipeline = []string{"zstd", "aes-gcm"}
	if tail := cryptoTail(active.Pipeline); len(tail) != 1 || tail[0] != "aes-gcm" {
		t.Fatalf("cryptoTail sanity: got %v", tail)
	}

	// Free non-crypto prefix, tail preserved — allowed.
	if _, err := PlanConnection(domain.StoreConfig{Pipeline: []string{"aes-gcm"}}, active); err != nil {
		t.Errorf("dropping only the compression prefix must pass, got %v", err)
	}
	// Dropping the crypto tail — refused.
	if _, err := PlanConnection(domain.StoreConfig{Pipeline: []string{"zstd"}}, active); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("dropping the crypto tail must be refused, got %v", err)
	}
	// Smuggling own crypto before the tail — refused.
	if _, err := PlanConnection(domain.StoreConfig{Pipeline: []string{"aes-gcm", "zstd", "aes-gcm"}}, active); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("extra crypto stage must be refused, got %v", err)
	}
	// Plain store: client pipeline with a crypto stage — refused.
	plain := activeForConnTests()
	if _, err := PlanConnection(domain.StoreConfig{Pipeline: []string{"aes-gcm"}}, plain); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("client crypto on a plain store must be refused, got %v", err)
	}
}

// Class-II refusal wins over class-III pending when both diverge: the
// governance message carries the actionable admin path.
func TestValidateConnection_GovernanceWinsOverSession(t *testing.T) {
	active := activeForConnTests()
	req := domain.StoreConfig{
		DeletionPolicy: domain.DeletionPolicyNoDelete, // class II
		BlobStorage:    domain.BlobStorageInline,      // class III
	}
	if _, err := PlanConnection(req, active); !errors.Is(err, errs.ErrGovernanceMismatch) {
		t.Errorf("want ErrGovernanceMismatch first, got %v", err)
	}
}

// UpdateConfig semantics are untouched by the class map: changing
// class II is its purpose, so ValidateAgainstActive (class I only)
// still passes a governance change.
func TestValidateAgainstActive_GovernanceChangePasses(t *testing.T) {
	active := activeForConnTests()
	req := ApplyDefaults(domain.StoreConfig{
		RetentionPeriod: active.RetentionPeriod / 2,
	})
	if err := ValidateAgainstActive(req, active); err != nil {
		t.Fatalf("governance change through the admin path must pass class-I check, got %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

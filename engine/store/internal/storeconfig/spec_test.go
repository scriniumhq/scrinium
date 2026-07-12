package storeconfig

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Conformance of the field-spec registry (R-g). Two guarantees:
//
//  1. Completeness — every domain.StoreConfig field has exactly one
//     spec row. Adding a field without classifying it fails here.
//  2. Behaviour — each row's declared connection fate matches what
//     PlanConnection actually does when the field diverges. A row
//     whose class disagrees with the validators fails here.
//
// Together they retire the bug class "the field was forgotten in one
// of the validators".

func TestSpec_CoversEveryStoreConfigField(t *testing.T) {
	typ := reflect.TypeOf(domain.StoreConfig{})

	inSpec := map[string]bool{}
	for _, s := range Specs {
		if inSpec[s.Name] {
			t.Errorf("duplicate spec row: %s", s.Name)
		}
		inSpec[s.Name] = true
	}

	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !inSpec[name] {
			t.Errorf("StoreConfig.%s has no spec row — classify it (ADR-110) before shipping", name)
		}
		delete(inSpec, name)
	}
	for name := range inSpec {
		t.Errorf("spec row %q matches no StoreConfig field — stale registry", name)
	}
}

// divergeProbes builds, per field, a client config whose ONLY populated
// field diverges from the given active config.
func divergeProbes(active domain.StoreConfig) map[string]domain.StoreConfig {
	pickTopology := domain.PathTopologyFlat
	if active.PathTopology == domain.PathTopologyFlat {
		pickTopology = domain.PathTopologySharded
	}
	pickHasher := domain.HashBLAKE3
	if active.ContentHasher == domain.HashBLAKE3 {
		pickHasher = domain.HashSHA256
	}
	pickBlob := domain.BlobStorageInline
	if active.BlobStorage == domain.BlobStorageInline {
		pickBlob = domain.BlobStorageTarget
	}
	return map[string]domain.StoreConfig{
		"PathTopology":         {PathTopology: pickTopology},
		"BlobStorage":          {BlobStorage: pickBlob},
		"ManifestEncoding":     {ManifestEncoding: domain.ManifestEncodingBinary},
		"ManifestCrypto":       {ManifestCrypto: domain.ManifestCryptoSealed},
		"EncryptedDedup":       {EncryptedDedup: domain.EncryptedDedupConvergent},
		"PackAlignment":        {PackAlignment: domain.PackAlignment4096},
		"EagerFetchLimit":      {EagerFetchLimit: active.EagerFetchLimit + 1<<20},
		"Pipeline":             {Pipeline: []string{"zstd"}}, // non-crypto prefix: the ConnDerived free half
		"ContentHasher":        {ContentHasher: pickHasher},
		"VerifyOnRead":         {VerifyOnRead: domain.VerifyOnReadForceEnabled},
		"SegmentSize":          {SegmentSize: active.SegmentSize + 4096},
		"IdentityMode":         {IdentityMode: domain.IdentityModeCoalesced},
		"DeletionPolicy":       {DeletionPolicy: domain.DeletionPolicyNoDelete},
		"DeletionPolicyLock":   {DeletionPolicyLock: true},
		"RetentionPeriod":      {RetentionPeriod: active.RetentionPeriod + 48*time.Hour},
		"TombstoneGracePeriod": {TombstoneGracePeriod: active.TombstoneGracePeriod + time.Hour},
		"InlineBlobLimit":      {InlineBlobLimit: active.InlineBlobLimit + 4096},
		"GCLeasePolicy":        {GCLeasePolicy: domain.GCLeaseLeaderElection},
		"SessionOverrides":     {SessionOverrides: domain.SessionOverridesDeny},
		"KDFParams":            {KDFParams: &domain.KDFParams{Time: 9, Memory: 1 << 16, Threads: 2}},
	}
}

func TestSpec_ConnectionBehaviourMatches(t *testing.T) {
	active := ApplyDefaults(domain.StoreConfig{})
	probes := divergeProbes(active)

	for _, s := range Specs {
		s := s
		t.Run(s.Name, func(t *testing.T) {
			req, ok := probes[s.Name]
			if !ok {
				t.Fatalf("no diverge probe for spec row %s — extend divergeProbes", s.Name)
			}
			overlay, err := PlanConnection(req, active)

			switch s.Conn {
			case ConnRefusedImmutable:
				if !errors.Is(err, errs.ErrConfigMismatch) {
					t.Errorf("declared class I, but PlanConnection = %v", err)
				}
			case ConnRefusedGovernance:
				if !errors.Is(err, errs.ErrGovernanceMismatch) {
					t.Errorf("declared class II, but PlanConnection = %v", err)
				}
			case ConnOverlay, ConnDerived:
				if err != nil {
					t.Fatalf("declared session overlay, but PlanConnection refused: %v", err)
				}
				if zeroOverlay(overlay) {
					t.Errorf("declared session overlay, but the overlay came back empty")
				}
			case ConnIgnored:
				if err != nil {
					t.Fatalf("declared ignored-at-connection, but PlanConnection refused: %v", err)
				}
				if !zeroOverlay(overlay) {
					t.Errorf("declared ignored-at-connection, but it leaked into the overlay: %+v", overlay)
				}
			default:
				t.Fatalf("spec row %s has no ConnBehavior", s.Name)
			}
		})
	}

	// The probe table itself must not outgrow the registry.
	if len(probes) != len(Specs) {
		t.Errorf("probe table has %d entries, registry %d — keep them in lockstep", len(probes), len(Specs))
	}
}

// Every session-classified row must actually be merged by MergeSession
// — a class-III field the merge forgets would be accepted at
// connection and then silently dropped, the exact INV-110-5 sin.
func TestSpec_SessionRowsAreMerged(t *testing.T) {
	active := ApplyDefaults(domain.StoreConfig{})
	probes := divergeProbes(active)

	for _, s := range Specs {
		if s.Conn != ConnOverlay && s.Conn != ConnDerived {
			continue
		}
		req := probes[s.Name]
		overlay, err := PlanConnection(req, active)
		if err != nil {
			t.Fatalf("%s: PlanConnection: %v", s.Name, err)
		}
		eff := MergeSession(active, overlay)
		if reflect.DeepEqual(eff, active) {
			t.Errorf("%s: MergeSession dropped the overlay — effective config equals the defaults", s.Name)
		}
	}
}

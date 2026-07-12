package config

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

// fieldsHandledOutsideRegistry are StoreConfig fields deliberately not
// in the registry, with the reason. KDFParams is ConnIgnored —
// input-only at InitStore, never validated or compared as config
// (registry.go documents this). The test allows exactly these.
var fieldsHandledOutsideRegistry = map[string]string{
	"KDFParams": "ConnIgnored: input-only, not config",
}

func TestRegistry_CoversEveryStoreConfigField(t *testing.T) {
	typ := reflect.TypeOf(domain.StoreConfig{})

	inReg := map[string]bool{}
	for _, r := range registryRows() {
		if inReg[r.Name] {
			t.Errorf("duplicate registry row: %s", r.Name)
		}
		inReg[r.Name] = true
	}

	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if inReg[name] {
			delete(inReg, name)
			continue
		}
		if _, ok := fieldsHandledOutsideRegistry[name]; !ok {
			t.Errorf("StoreConfig.%s is in neither the registry nor the out-of-band allowlist — classify it (ADR-110) before shipping", name)
		}
	}
	for name := range inReg {
		t.Errorf("registry row %q matches no StoreConfig field — stale registry", name)
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
		"MaxArtifactSize":      {MaxArtifactSize: active.MaxArtifactSize + (1 << 20)},
		"KDFParams":            {KDFParams: &domain.KDFParams{Time: 9, Memory: 1 << 16, Threads: 2}},
	}
}

func TestRegistry_ConnectionBehaviourMatches(t *testing.T) {
	active := ApplyDefaults(domain.StoreConfig{})
	probes := divergeProbes(active)

	for _, s := range registryRows() {
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
	if len(probes) != len(registryRows())+len(fieldsHandledOutsideRegistry) {
		t.Errorf("probe table has %d entries, registry+allowlist %d — keep them in lockstep", len(probes), len(registryRows())+len(fieldsHandledOutsideRegistry))
	}
}

// Every session-classified row must actually be merged by MergeSession
// — a class-III field the merge forgets would be accepted at
// connection and then silently dropped, the exact INV-110-5 sin.
func TestRegistry_SessionRowsAreMerged(t *testing.T) {
	active := ApplyDefaults(domain.StoreConfig{})
	probes := divergeProbes(active)

	for _, s := range registryRows() {
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

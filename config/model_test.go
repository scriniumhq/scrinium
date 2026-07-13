package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"scrinium.dev/errs"
)

// --- ApplyDefaults ---

func TestApplyDefaults_FillsEmptyConfig(t *testing.T) {
	got := ApplyDefaults(StoreConfig{})

	// String-typed enum fields must all be non-empty after defaulting.
	// Comparing the stringified value avoids naming each field's
	// concrete enum type.
	checks := []struct {
		name string
		val  string
	}{
		{"PathTopology", string(got.PathTopology)},
		{"BlobStorage", string(got.BlobStorage)},
		{"ManifestEncoding", string(got.ManifestEncoding)},
		{"ManifestCrypto", string(got.ManifestCrypto)},
		{"ContentHasher", string(got.ContentHasher)},
		{"VerifyOnRead", string(got.VerifyOnRead)},
		{"DeletionPolicy", string(got.DeletionPolicy)},
		{"GCLeasePolicy", string(got.GCLeasePolicy)},
	}
	for _, c := range checks {
		if c.val == "" {
			t.Errorf("%s: still zero after ApplyDefaults", c.name)
		}
	}
	if got.PackAlignment != PackAlignmentAuto {
		t.Errorf("PackAlignment: got %v, want Auto (zero promoted)", got.PackAlignment)
	}
	if got.TombstoneGracePeriod != 24*time.Hour {
		t.Errorf("TombstoneGracePeriod: got %v, want 24h", got.TombstoneGracePeriod)
	}
}

func TestApplyDefaults_ResultPassesValidate(t *testing.T) {
	if err := ValidateImmutable(ApplyDefaults(StoreConfig{})); err != nil {
		t.Fatalf("defaulted config must validate, got %v", err)
	}
}

func TestApplyDefaults_PreservesExplicitValues(t *testing.T) {
	in := StoreConfig{
		PathTopology:  PathTopologyFlat,
		ContentHasher: HashBLAKE3,
	}
	got := ApplyDefaults(in)
	if got.PathTopology != PathTopologyFlat {
		t.Errorf("PathTopology overwritten: got %q", got.PathTopology)
	}
	if got.ContentHasher != HashBLAKE3 {
		t.Errorf("ContentHasher overwritten: got %q", got.ContentHasher)
	}
}

func TestApplyDefaults_PlainStoreLeavesCryptoFieldsZero(t *testing.T) {
	got := ApplyDefaults(StoreConfig{}) // defaults to ManifestCryptoPlain
	if got.EncryptedDedup != "" {
		t.Errorf("EncryptedDedup should stay empty for a Plain store, got %q", got.EncryptedDedup)
	}
	if got.SegmentSize != 0 {
		t.Errorf("SegmentSize should stay 0 for a Plain store, got %d", got.SegmentSize)
	}
}

func TestApplyDefaults_EncryptingStoreGetsCryptoDefaults(t *testing.T) {
	got := ApplyDefaults(StoreConfig{ManifestCrypto: ManifestCryptoSealed})
	if got.EncryptedDedup != EncryptedDedupDisabled {
		t.Errorf("EncryptedDedup: got %q, want Disabled", got.EncryptedDedup)
	}
	if got.SegmentSize != DefaultSegmentSize {
		t.Errorf("SegmentSize: got %d, want DefaultSegmentSize", got.SegmentSize)
	}
}

func TestApplyDefaults_DoesNotOverrideFeatureOffFields(t *testing.T) {
	got := ApplyDefaults(StoreConfig{})
	if got.InlineBlobLimit != 0 || got.RetentionPeriod != 0 || got.Pipeline != nil || got.KDFParams != nil {
		t.Error("feature-off fields (InlineBlobLimit/RetentionPeriod/Pipeline/KDFParams) must stay zero")
	}
}

// --- ValidateImmutable ---

func TestValidateImmutable_AcceptsDefaulted(t *testing.T) {
	if err := ValidateImmutable(ApplyDefaults(StoreConfig{})); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestValidateImmutable_RejectsUnknownEnums(t *testing.T) {
	base := ApplyDefaults(StoreConfig{})
	cases := map[string]func(*StoreConfig){
		"PathTopology":     func(c *StoreConfig) { c.PathTopology = "bogus" },
		"ManifestCrypto":   func(c *StoreConfig) { c.ManifestCrypto = "bogus" },
		"ContentHasher":    func(c *StoreConfig) { c.ContentHasher = "bogus" },
		"ManifestEncoding": func(c *StoreConfig) { c.ManifestEncoding = "bogus" },
		"EncryptedDedup":   func(c *StoreConfig) { c.EncryptedDedup = "bogus" },
		// R-a (config review): these enums used to pass unvalidated and
		// persist through UpdateConfig.
		"BlobStorage":     func(c *StoreConfig) { c.BlobStorage = "bogus" },
		"IdentityMode":    func(c *StoreConfig) { c.IdentityMode = "bogus" },
		"VerifyOnRead":    func(c *StoreConfig) { c.VerifyOnRead = "bogus" },
		"DeletionPolicy":  func(c *StoreConfig) { c.DeletionPolicy = "bogus" },
		"GCLeasePolicy":   func(c *StoreConfig) { c.GCLeasePolicy = "bogus" },
		"PackAlignment":   func(c *StoreConfig) { c.PackAlignment = 777 },
		"MaxArtifactSize": func(c *StoreConfig) { c.MaxArtifactSize = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidConfig) {
				t.Errorf("want ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestValidateImmutable_RejectsBinaryEncoding(t *testing.T) {
	cfg := ApplyDefaults(StoreConfig{})
	cfg.ManifestEncoding = ManifestEncodingBinary
	if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("Binary encoding is deferred; want ErrInvalidConfig, got %v", err)
	}
}

func TestValidateImmutable_SegmentSizeBounds(t *testing.T) {
	cfg := ApplyDefaults(StoreConfig{})

	cfg.SegmentSize = MinSegmentSize - 1
	if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("below-min SegmentSize must fail; got %v", err)
	}
	cfg.SegmentSize = MaxSegmentSize + 1
	if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("above-max SegmentSize must fail; got %v", err)
	}
	cfg.SegmentSize = 0 // legitimate "not set"
	if err := ValidateImmutable(cfg); err != nil {
		t.Errorf("zero SegmentSize must pass; got %v", err)
	}
}

func TestValidateImmutable_TombstoneGracePeriodMinimum(t *testing.T) {
	cfg := ApplyDefaults(StoreConfig{})
	cfg.TombstoneGracePeriod = MinTombstoneGracePeriod - 1
	if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidTombstoneGracePeriod) {
		t.Errorf("want ErrInvalidTombstoneGracePeriod, got %v", err)
	}
}

func TestValidateImmutable_InlineBlobLimitUpperBound(t *testing.T) {
	cfg := ApplyDefaults(StoreConfig{})
	cfg.InlineBlobLimit = MaxInlineBlobLimit + 1
	if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("over-limit InlineBlobLimit must fail; got %v", err)
	}
}

func TestValidateImmutable_RetentionPeriodLowerBound(t *testing.T) {
	cfg := ApplyDefaults(StoreConfig{})
	cfg.RetentionPeriod = MinRetentionPeriod - 1
	if err := ValidateImmutable(cfg); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("too-short RetentionPeriod must fail; got %v", err)
	}
}

// --- ValidateAgainstActive ---

func TestValidateAgainstActive_EmptyRequestPasses(t *testing.T) {
	active := ApplyDefaults(StoreConfig{})
	if err := ValidateAgainstActive(StoreConfig{}, active); err != nil {
		t.Errorf("empty request must pass through; got %v", err)
	}
}

func TestValidateAgainstActive_MatchingImmutablesPass(t *testing.T) {
	active := ApplyDefaults(StoreConfig{})
	req := StoreConfig{
		PathTopology:  active.PathTopology,
		ContentHasher: active.ContentHasher,
	}
	if err := ValidateAgainstActive(req, active); err != nil {
		t.Errorf("matching immutables must pass; got %v", err)
	}
}

func TestValidateAgainstActive_MismatchedImmutableFails(t *testing.T) {
	active := ApplyDefaults(StoreConfig{}) // Sharded
	req := StoreConfig{PathTopology: PathTopologyFlat}
	if err := ValidateAgainstActive(req, active); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("want ErrConfigMismatch, got %v", err)
	}
}

func TestValidateAgainstActive_MismatchMessageNamesField(t *testing.T) {
	active := ApplyDefaults(StoreConfig{})
	req := StoreConfig{ContentHasher: HashBLAKE3} // active is SHA256
	err := ValidateAgainstActive(req, active)
	if err == nil || !strings.Contains(err.Error(), "ContentHasher") {
		t.Errorf("error should name the mismatched field; got %v", err)
	}
}

func TestValidateAgainstActive_DeletionPolicyLockAsymmetry(t *testing.T) {
	active := ApplyDefaults(StoreConfig{}) // DeletionPolicyLock=false

	// Requesting lock=true against an unlocked active → mismatch.
	if err := ValidateAgainstActive(StoreConfig{DeletionPolicyLock: true}, active); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("lock=true vs active false must fail; got %v", err)
	}
	// Requesting lock=false (the zero value) must pass through.
	if err := ValidateAgainstActive(StoreConfig{DeletionPolicyLock: false}, active); err != nil {
		t.Errorf("lock=false must pass through; got %v", err)
	}
}

func TestValidateAgainstActive_AccumulatesMultipleMismatches(t *testing.T) {
	active := ApplyDefaults(StoreConfig{})
	req := StoreConfig{
		PathTopology:  PathTopologyFlat,
		ContentHasher: HashBLAKE3,
	}
	err := ValidateAgainstActive(req, active)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "PathTopology") || !strings.Contains(err.Error(), "ContentHasher") {
		t.Errorf("error should report both mismatches; got %v", err)
	}
}

// R-a (config review): IdentityMode is immutable (ADR-73) and used to
// be missing from the against-active comparison — a WithConfig with a
// diverging mode passed OpenStore silently.
func TestValidateAgainstActive_IdentityMode(t *testing.T) {
	active := ApplyDefaults(StoreConfig{IdentityMode: IdentityModeUnique})

	req := StoreConfig{IdentityMode: IdentityModeCoalesced}
	if err := ValidateAgainstActive(req, active); !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("diverging IdentityMode: want ErrConfigMismatch, got %v", err)
	}

	// Empty request field = "not asked" — passes, like every immutable.
	if err := ValidateAgainstActive(StoreConfig{}, active); err != nil {
		t.Errorf("empty request must pass, got %v", err)
	}
	// Matching value passes.
	req = StoreConfig{IdentityMode: IdentityModeUnique}
	if err := ValidateAgainstActive(req, active); err != nil {
		t.Errorf("matching IdentityMode must pass, got %v", err)
	}
}

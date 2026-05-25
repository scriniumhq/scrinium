package assembly

import "testing"

func TestPolicyDefaults(t *testing.T) {
	p := &Policy{
		Encryption: &Encryption{Passphrase: "plain:x"},
		Chunking:   &Chunking{},
		Bundling:   &Bundling{},
	}
	applyPolicyDefaults(p)

	if p.Encryption.Mode != "sealed" {
		t.Errorf("encryption mode default = %q, want sealed", p.Encryption.Mode)
	}
	if p.Encryption.Dedup != "disabled" {
		t.Errorf("dedup default = %q, want disabled", p.Encryption.Dedup)
	}
	if p.Chunking.MaxSize != defaultChunkMaxSize {
		t.Errorf("chunk maxSize default = %d", p.Chunking.MaxSize)
	}
	if p.Chunking.DirectWriteThreshold != defaultChunkMaxSize/2 {
		t.Errorf("chunk dwt default = %d", p.Chunking.DirectWriteThreshold)
	}
	if p.Bundling.MaxBundleSize != defaultBundleMaxSize {
		t.Errorf("bundle size default = %d", p.Bundling.MaxBundleSize)
	}
	if p.Bundling.DirectWriteThreshold != defaultBundleMaxSize/2 {
		t.Errorf("bundle dwt default = %d", p.Bundling.DirectWriteThreshold)
	}
}

func TestProjectionDefaults(t *testing.T) {
	p := &Projection{}
	applyProjectionDefaults(p)
	if p.Editing != "off" || p.RootView != "by-path" || p.ByPathFallback != "orphaned" || p.DefaultMode != 0o644 {
		t.Errorf("projection defaults wrong: %+v", p)
	}
	// Explicit values are preserved.
	q := &Projection{Editing: "on", RootView: "by-date", DefaultMode: 0o600}
	applyProjectionDefaults(q)
	if q.Editing != "on" || q.RootView != "by-date" || q.DefaultMode != 0o600 {
		t.Errorf("projection defaults clobbered explicit: %+v", q)
	}
}

func TestNilDefaultsNoPanic(t *testing.T) {
	applyPolicyDefaults(nil)
	applyProjectionDefaults(nil)
	applyDefaults(&Config{Store: &StoreSpec{Driver: "file:///x"}})
}

func TestResolvePolicyRefs(t *testing.T) {
	c := &Config{
		Store: &StoreSpec{Driver: "file:///d", PolicyRef: "secure"},
		Policies: map[string]*Policy{
			"secure": {Encryption: &Encryption{Passphrase: "plain:p"}},
		},
	}
	if err := resolvePolicyRefs(c); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if c.Store.PolicyRef != "" {
		t.Error("PolicyRef should be cleared after resolution")
	}
	if c.Store.Policy == nil || c.Store.Policy.Encryption == nil {
		t.Fatal("policy not copied from ref")
	}
	// The copy must be independent of the template.
	c.Store.Policy.Encryption.Mode = "paranoid"
	if c.Policies["secure"].Encryption.Mode == "paranoid" {
		t.Error("policy copy is not independent of the template")
	}
}

func TestResolvePolicyRefUnknown(t *testing.T) {
	c := &Config{Store: &StoreSpec{Driver: "file:///d", PolicyRef: "missing"}}
	if err := resolvePolicyRefs(c); err == nil {
		t.Error("expected error for unknown policyRef")
	}
}

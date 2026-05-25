package assembly

import (
	"strings"
	"testing"
)

func wantErr(t *testing.T, c *Config, substr string) {
	t.Helper()
	err := validate(c)
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err.Error(), substr)
	}
}

func TestValidateOK(t *testing.T) {
	c := &Config{Store: &StoreSpec{Driver: "file:///d"}}
	if err := validate(c); err != nil {
		t.Fatalf("valid single store rejected: %v", err)
	}
}

func TestValidateNoStore(t *testing.T) {
	wantErr(t, &Config{}, "set either")
}

func TestValidateBothStoreAndStores(t *testing.T) {
	wantErr(t, &Config{
		Store:  &StoreSpec{Driver: "file:///d"},
		Stores: map[string]*StoreSpec{"a": {Driver: "file:///e"}},
	}, "exactly one")
}

func TestValidateEmptyDriver(t *testing.T) {
	wantErr(t, &Config{Store: &StoreSpec{Driver: "  "}}, "driver")
}

func TestValidateMultistoreNeedsRouting(t *testing.T) {
	wantErr(t, &Config{
		Stores: map[string]*StoreSpec{"a": {Driver: "file:///d"}},
	}, "multistore.routing")
}

func TestValidateRoutingUnknownStore(t *testing.T) {
	wantErr(t, &Config{
		Stores: map[string]*StoreSpec{"hot": {Driver: "file:///d"}},
		Multistore: &MultistoreSpec{
			Routing: map[string]string{"*": "nonexistent"},
		},
	}, "unknown store")
}

func TestValidatePolicyRefAndInline(t *testing.T) {
	wantErr(t, &Config{
		Store: &StoreSpec{Driver: "file:///d", PolicyRef: "x", Policy: &Policy{}},
	}, "not both")
}

func TestValidateEncryptionNeedsPassphrase(t *testing.T) {
	wantErr(t, &Config{
		Store: &StoreSpec{Driver: "file:///d", Policy: &Policy{Encryption: &Encryption{}}},
	}, "passphrase")
}

func TestValidateEncryptionBadMode(t *testing.T) {
	wantErr(t, &Config{
		Store: &StoreSpec{Driver: "file:///d", Policy: &Policy{
			Encryption: &Encryption{Passphrase: "plain:p", Mode: "bogus"},
		}},
	}, "encryption.mode")
}

func TestValidateBadDeletionPolicy(t *testing.T) {
	wantErr(t, &Config{
		Store: &StoreSpec{Driver: "file:///d", Policy: &Policy{DeletionPolicy: "weird"}},
	}, "deletionPolicy")
}

func TestValidateProjectionEnums(t *testing.T) {
	wantErr(t, &Config{
		Store:      &StoreSpec{Driver: "file:///d"},
		Projection: &Projection{Editing: "maybe"},
	}, "projection.editing")
	wantErr(t, &Config{
		Store:      &StoreSpec{Driver: "file:///d"},
		Projection: &Projection{RootView: "by-vibes"},
	}, "projection.rootView")
	wantErr(t, &Config{
		Store:      &StoreSpec{Driver: "file:///d"},
		Projection: &Projection{ScratchQuota: -1},
	}, "scratchQuota")
}

func TestValidateAgentKinds(t *testing.T) {
	wantErr(t, &Config{
		Store:  &StoreSpec{Driver: "file:///d"},
		Agents: []ComponentSpec{{Kind: ""}},
	}, "agents[0]")
}

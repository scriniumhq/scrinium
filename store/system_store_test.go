package store

import (
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// --- validateSystemName ---

func TestValidateSystemName_Accepts(t *testing.T) {
	valid := []string{
		"config/current",
		"config/v1",
		"scrub/cursor",
		"gc/lease",
		"snapshot/2024",
		"a",
		"ingester/state/main",
	}
	for _, name := range valid {
		if err := validateSystemName(name); err != nil {
			t.Errorf("validateSystemName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateSystemName_Rejects(t *testing.T) {
	bad := map[string]string{
		"empty":            "",
		"leading slash":    "/config/current",
		"trailing slash":   "config/current/",
		"empty segment":    "config//current",
		"dot segment":      "config/./current",
		"dotdot traversal": "config/../secret",
	}
	for label, name := range bad {
		t.Run(label, func(t *testing.T) {
			err := validateSystemName(name)
			if !errors.Is(err, errs.ErrInvalidSystemName) {
				t.Errorf("validateSystemName(%q) = %v, want ErrInvalidSystemName", name, err)
			}
		})
	}
}

// --- namespaceForName ---

func TestNamespaceForName_ConfigPrefixGoesToSystemConfig(t *testing.T) {
	for _, name := range []string{"config/current", "config/v3", "config/history/2024"} {
		ns, err := namespaceForName(name)
		if err != nil {
			t.Fatalf("namespaceForName(%q): %v", name, err)
		}
		if ns != domain.NamespaceSystemConfig {
			t.Errorf("namespaceForName(%q) = %q, want %q", name, ns, domain.NamespaceSystemConfig)
		}
	}
}

func TestNamespaceForName_EverythingElseGoesToSystemState(t *testing.T) {
	for _, name := range []string{"scrub/cursor", "gc/lease", "snapshot/x", "configish/notconfig"} {
		ns, err := namespaceForName(name)
		if err != nil {
			t.Fatalf("namespaceForName(%q): %v", name, err)
		}
		if ns != domain.NamespaceSystemState {
			t.Errorf("namespaceForName(%q) = %q, want %q", name, ns, domain.NamespaceSystemState)
		}
	}
}

func TestNamespaceForName_PropagatesNameValidation(t *testing.T) {
	if _, err := namespaceForName("config/../escape"); !errors.Is(err, errs.ErrInvalidSystemName) {
		t.Errorf("invalid name must propagate ErrInvalidSystemName, got %v", err)
	}
}

func TestNamespaceForName_ConfigPrefixIsExact(t *testing.T) {
	// "config" alone (no slash) is NOT the config/ prefix → state.
	ns, err := namespaceForName("config")
	if err != nil {
		t.Fatalf("namespaceForName(config): %v", err)
	}
	if ns != domain.NamespaceSystemState {
		t.Errorf("bare \"config\" should map to system.state, got %q", ns)
	}
}

// --- pointerPath ---

func TestPointerPath_ConfigCurrentKeepsFlatPath(t *testing.T) {
	got, err := pointerPath("config/current")
	if err != nil {
		t.Fatalf("pointerPath(config/current): %v", err)
	}
	want := domain.NamespaceSystemConfig + "/current"
	if got != want {
		t.Errorf("pointerPath(config/current) = %q, want %q (historic flat path)", got, want)
	}
}

func TestPointerPath_ConfigOtherUsesPointersSubtree(t *testing.T) {
	got, err := pointerPath("config/v2")
	if err != nil {
		t.Fatalf("pointerPath(config/v2): %v", err)
	}
	want := domain.NamespaceSystemConfig + "/pointers/config/v2"
	if got != want {
		t.Errorf("pointerPath(config/v2) = %q, want %q", got, want)
	}
}

func TestPointerPath_StateUsesPointersSubtree(t *testing.T) {
	got, err := pointerPath("scrub/cursor")
	if err != nil {
		t.Fatalf("pointerPath(scrub/cursor): %v", err)
	}
	want := domain.NamespaceSystemState + "/pointers/scrub/cursor"
	if got != want {
		t.Errorf("pointerPath(scrub/cursor) = %q, want %q", got, want)
	}
}

func TestPointerPath_PropagatesNameValidation(t *testing.T) {
	if _, err := pointerPath("bad//name"); !errors.Is(err, errs.ErrInvalidSystemName) {
		t.Errorf("invalid name must propagate ErrInvalidSystemName, got %v", err)
	}
}

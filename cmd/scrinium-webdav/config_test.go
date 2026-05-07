package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/projection"
)

func TestDefaultConfig_Sane(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Listen != ":8080" {
		t.Errorf("Listen: got %q, want :8080", cfg.Listen)
	}
	if cfg.Daemon.Namespace != "files" {
		t.Errorf("Namespace: got %q, want files", cfg.Daemon.Namespace)
	}
	if cfg.Daemon.RootView != projection.RootByPath {
		t.Errorf("RootView: got %v, want by-path", cfg.Daemon.RootView)
	}
	if cfg.Daemon.ServicePrefix != "_scrinium" {
		t.Errorf("ServicePrefix: got %q", cfg.Daemon.ServicePrefix)
	}
	if cfg.Daemon.Editing != "off" {
		t.Errorf("Editing: got %q, want off", cfg.Daemon.Editing)
	}
	if cfg.Daemon.ScratchQuota != 1<<30 {
		t.Errorf("ScratchQuota: got %d, want 1 GiB", cfg.Daemon.ScratchQuota)
	}
	if cfg.Daemon.DefaultMode != 0o644 {
		t.Errorf("DefaultMode: got %#o, want 0644", cfg.Daemon.DefaultMode)
	}
	if cfg.BrowsePrefix != "/_browse" {
		t.Errorf("BrowsePrefix: got %q", cfg.BrowsePrefix)
	}
}

func TestLoadConfig_FlagsOnly(t *testing.T) {
	args := []string{
		"--store=file:///var/lib/scrinium",
		"--listen=:9090",
		"--namespace=photos",
		"--root-view=by-date",
		"--editing=on",
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Daemon.Store != "file:///var/lib/scrinium" {
		t.Errorf("Store: got %q", cfg.Daemon.Store)
	}
	if cfg.Listen != ":9090" {
		t.Errorf("Listen: got %q", cfg.Listen)
	}
	if cfg.Daemon.Namespace != "photos" {
		t.Errorf("Namespace: got %q", cfg.Daemon.Namespace)
	}
	if cfg.Daemon.RootView != projection.RootByDate {
		t.Errorf("RootView: got %v", cfg.Daemon.RootView)
	}
	if cfg.Daemon.Editing != "on" {
		t.Errorf("Editing: got %q", cfg.Daemon.Editing)
	}
}

func TestLoadConfig_InvalidRootView(t *testing.T) {
	args := []string{
		"--store=file:///x", "--listen=:9090",
		"--root-view=bogus",
	}
	_, _, err := loadConfig(args)
	if err == nil {
		t.Fatal("expected error for invalid root-view")
	}
}

func TestLoadConfig_YAMLLoaded(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "cfg.yaml")
	yaml := `
store: file:///from/yaml
listen: ":9091"
namespace: yaml-ns
rootView: by-session
editing: on
scratchQuota: 524288000
showBySession: true
`
	if err := os.WriteFile(yamlPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := loadConfig([]string{"--config=" + yamlPath})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Daemon.Store != "file:///from/yaml" {
		t.Errorf("Store: got %q", cfg.Daemon.Store)
	}
	if cfg.Daemon.Namespace != "yaml-ns" {
		t.Errorf("Namespace: got %q", cfg.Daemon.Namespace)
	}
	if cfg.Daemon.RootView != projection.RootBySession {
		t.Errorf("RootView: got %v", cfg.Daemon.RootView)
	}
	if cfg.Daemon.Editing != "on" {
		t.Errorf("Editing: got %q", cfg.Daemon.Editing)
	}
	if cfg.Daemon.ScratchQuota != 524288000 {
		t.Errorf("ScratchQuota: got %d", cfg.Daemon.ScratchQuota)
	}
	if !cfg.Daemon.ShowBySession {
		t.Error("ShowBySession should be true from YAML")
	}
}

func TestLoadConfig_FlagsOverrideYAML(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "cfg.yaml")
	yaml := `
store: file:///from/yaml
namespace: yaml-ns
`
	os.WriteFile(yamlPath, []byte(yaml), 0o644)

	args := []string{
		"--config=" + yamlPath,
		"--store=file:///from/cli",
		"--listen=:9090",
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Daemon.Store != "file:///from/cli" {
		t.Errorf("CLI must override YAML: got %q", cfg.Daemon.Store)
	}
	if cfg.Daemon.Namespace != "yaml-ns" {
		t.Errorf("YAML inherited (no CLI override): got %q", cfg.Daemon.Namespace)
	}
}

func TestLoadConfig_Env(t *testing.T) {
	t.Setenv("SCRINIUM_WEBDAV_STORE", "file:///from/env")
	t.Setenv("SCRINIUM_WEBDAV_NAMESPACE", "env-ns")
	args := []string{"--listen=:9090"}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Daemon.Store != "file:///from/env" {
		t.Errorf("env not applied: got %q", cfg.Daemon.Store)
	}
	if cfg.Daemon.Namespace != "env-ns" {
		t.Errorf("env namespace: got %q", cfg.Daemon.Namespace)
	}
}

func TestLoadConfig_FlagOverridesEnv(t *testing.T) {
	t.Setenv("SCRINIUM_WEBDAV_NAMESPACE", "env-ns")
	args := []string{
		"--store=file:///x", "--listen=:9090",
		"--namespace=cli-ns",
	}
	cfg, _, _ := loadConfig(args)
	if cfg.Daemon.Namespace != "cli-ns" {
		t.Errorf("CLI must beat env: got %q", cfg.Daemon.Namespace)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	cfg := DefaultConfig()
	// Defaults already include Listen=":8080"; without Store
	// Validate must still fail on the daemon level.
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "store") {
		t.Errorf("expected store error, got %v", err)
	}
	cfg.Daemon.Store = "file:///x"
	cfg.Listen = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("expected listen error, got %v", err)
	}
	cfg.Listen = ":8080"
	if err := cfg.Validate(); err != nil {
		t.Errorf("with required fields set, Validate must pass: %v", err)
	}
}

func TestValidate_InvalidEditing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Daemon.Store = "file:///x"
	cfg.Daemon.Editing = "maybe"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for editing=maybe")
	}
}

func TestByteSizeFlag_Suffixes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"500", 500},
		{"500K", 500 << 10},
		{"500k", 500 << 10},
		{"1M", 1 << 20},
		{"2G", 2 << 30},
		{"1T", 1 << 40},
	}
	for _, tc := range cases {
		var v int64
		f := byteSizeFlag{&v}
		if err := f.Set(tc.in); err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if v != tc.want {
			t.Errorf("%q: got %d, want %d", tc.in, v, tc.want)
		}
	}
}

func TestByteSizeFlag_Invalid(t *testing.T) {
	var v int64
	f := byteSizeFlag{&v}
	if err := f.Set("not-a-number"); err == nil {
		t.Error("expected error")
	}
}

func TestOctalFlag_Forms(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"644", 0o644},
		{"0644", 0o644},
		{"0o644", 0o644},
		{"755", 0o755},
		{"0", 0},
	}
	for _, tc := range cases {
		var v uint32
		f := octalFlag{&v}
		if err := f.Set(tc.in); err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if v != tc.want {
			t.Errorf("%q: got %#o, want %#o", tc.in, v, tc.want)
		}
	}
}

func TestBoolPtrFlag_NilByDefault(t *testing.T) {
	var p *bool
	f := boolPtrFlag{&p}
	if p != nil {
		t.Error("default must be nil")
	}
	f.Set("true")
	if p == nil || !*p {
		t.Errorf("after Set(true): got %v", p)
	}
}

func TestBoolPtrFlag_IsBoolFlag(t *testing.T) {
	var p *bool
	f := boolPtrFlag{&p}
	if !f.IsBoolFlag() {
		t.Error("IsBoolFlag must return true")
	}
}

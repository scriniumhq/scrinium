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
	if cfg.Namespace != "files" {
		t.Errorf("Namespace: got %q, want files", cfg.Namespace)
	}
	if cfg.RootView != projection.RootByPath {
		t.Errorf("RootView: got %v, want by-path", cfg.RootView)
	}
	if cfg.ServicePrefix != "_scrinium" {
		t.Errorf("ServicePrefix: got %q", cfg.ServicePrefix)
	}
	if cfg.Editing != "off" {
		t.Errorf("Editing: got %q, want off", cfg.Editing)
	}
	if cfg.ScratchQuota != 1<<30 {
		t.Errorf("ScratchQuota: got %d, want 1 GiB", cfg.ScratchQuota)
	}
	if cfg.DefaultMode != 0o644 {
		t.Errorf("DefaultMode: got %#o, want 0644", cfg.DefaultMode)
	}
}

func TestLoadConfig_FlagsOnly(t *testing.T) {
	args := []string{
		"--store-path=/var/lib/scrinium",
		"--listen=:9090",
		"--namespace=photos",
		"--root-view=by-date",
		"--editing=on",
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.StorePath != "/var/lib/scrinium" {
		t.Errorf("StorePath: got %q", cfg.StorePath)
	}
	if cfg.Listen != ":9090" {
		t.Errorf("Listen: got %q", cfg.Listen)
	}
	if cfg.Namespace != "photos" {
		t.Errorf("Namespace: got %q", cfg.Namespace)
	}
	if cfg.RootView != projection.RootByDate {
		t.Errorf("RootView: got %v", cfg.RootView)
	}
	if cfg.Editing != "on" {
		t.Errorf("Editing: got %q", cfg.Editing)
	}
}

func TestLoadConfig_InvalidRootView(t *testing.T) {
	args := []string{
		"--store-path=/x", "--listen=:9090",
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
storePath: /from/yaml
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
	if cfg.StorePath != "/from/yaml" {
		t.Errorf("StorePath: got %q", cfg.StorePath)
	}
	if cfg.Namespace != "yaml-ns" {
		t.Errorf("Namespace: got %q", cfg.Namespace)
	}
	if cfg.RootView != projection.RootBySession {
		t.Errorf("RootView: got %v", cfg.RootView)
	}
	if cfg.Editing != "on" {
		t.Errorf("Editing: got %q", cfg.Editing)
	}
	if cfg.ScratchQuota != 524288000 {
		t.Errorf("ScratchQuota: got %d", cfg.ScratchQuota)
	}
	if !cfg.ShowBySession {
		t.Error("ShowBySession should be true from YAML")
	}
}

func TestLoadConfig_FlagsOverrideYAML(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "cfg.yaml")
	yaml := `
storePath: /from/yaml
namespace: yaml-ns
`
	os.WriteFile(yamlPath, []byte(yaml), 0o644)

	args := []string{
		"--config=" + yamlPath,
		"--store-path=/from/cli",
		"--listen=:9090",
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.StorePath != "/from/cli" {
		t.Errorf("CLI must override YAML: got %q", cfg.StorePath)
	}
	if cfg.Namespace != "yaml-ns" {
		t.Errorf("YAML inherited (no CLI override): got %q", cfg.Namespace)
	}
}

func TestLoadConfig_Env(t *testing.T) {
	t.Setenv("SCRINIUM_WEBDAV_STORE_PATH", "/from/env")
	t.Setenv("SCRINIUM_WEBDAV_NAMESPACE", "env-ns")
	args := []string{"--listen=:9090"}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.StorePath != "/from/env" {
		t.Errorf("env not applied: got %q", cfg.StorePath)
	}
	if cfg.Namespace != "env-ns" {
		t.Errorf("env namespace: got %q", cfg.Namespace)
	}
}

func TestLoadConfig_FlagOverridesEnv(t *testing.T) {
	t.Setenv("SCRINIUM_WEBDAV_NAMESPACE", "env-ns")
	args := []string{
		"--store-path=/x", "--listen=:9090",
		"--namespace=cli-ns",
	}
	cfg, _, _ := loadConfig(args)
	if cfg.Namespace != "cli-ns" {
		t.Errorf("CLI must beat env: got %q", cfg.Namespace)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "" // clear default to test required-field path
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "store-path") {
		t.Errorf("expected store-path error, got %v", err)
	}
	cfg.StorePath = "/x"
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
	cfg.StorePath = "/x"
	cfg.Listen = "/y"
	cfg.Editing = "maybe"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for editing=maybe")
	}
}

func TestEditingPolicy_Modes(t *testing.T) {
	off := DefaultConfig()
	off.Editing = "off"
	p := off.EditingPolicy()
	if p.AllowRename || p.AllowSetattr || p.AllowTruncate || p.AllowAppend {
		t.Errorf("editing=off must zero every bit, got %+v", p)
	}

	on := DefaultConfig()
	on.Editing = "on"
	p = on.EditingPolicy()
	if !(p.AllowRename && p.AllowSetattr && p.AllowTruncate && p.AllowAppend) {
		t.Errorf("editing=on must set every bit, got %+v", p)
	}

	custom := DefaultConfig()
	custom.Editing = "custom"
	tBool := true
	custom.AllowRename = &tBool
	custom.AllowSetattr = nil
	p = custom.EditingPolicy()
	if !p.AllowRename {
		t.Error("custom AllowRename should propagate")
	}
	if p.AllowSetattr {
		t.Error("custom AllowSetattr nil should be false")
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

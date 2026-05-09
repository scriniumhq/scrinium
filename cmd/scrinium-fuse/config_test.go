//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scrinium.dev/engine/projection"
)

func TestDefaultConfig_Sane(t *testing.T) {
	cfg := DefaultConfig()
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
	if cfg.IndexMode != "memory" {
		t.Errorf("IndexMode: got %q, want memory", cfg.IndexMode)
	}
}

func TestLoadConfig_FlagsOnly(t *testing.T) {
	args := []string{
		"--store=file:///var/lib/scrinium",
		"--mount-point=/mnt/x",
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
	if cfg.MountPoint != "/mnt/x" {
		t.Errorf("MountPoint: got %q", cfg.MountPoint)
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

func TestLoadConfig_IndexModeMemory(t *testing.T) {
	// IndexMode=memory translates to Index URI sqlite://:memory:
	// when no explicit --index is given.
	args := []string{
		"--store=file:///x", "--mount-point=/y",
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Daemon.Index != "sqlite://:memory:" {
		t.Errorf("Index: got %q, want sqlite://:memory:", cfg.Daemon.Index)
	}
}

func TestLoadConfig_IndexExplicitWins(t *testing.T) {
	// Explicit --index overrides the IndexMode shortcut.
	args := []string{
		"--store=file:///x", "--mount-point=/y",
		"--index=sqlite:///custom.db",
	}
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Daemon.Index != "sqlite:///custom.db" {
		t.Errorf("Index: got %q, want explicit sqlite:///custom.db", cfg.Daemon.Index)
	}
}

func TestLoadConfig_InvalidRootView(t *testing.T) {
	args := []string{
		"--store=file:///x", "--mount-point=/y",
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
mountPoint: /mnt/yaml
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
	if cfg.MountPoint != "/mnt/yaml" {
		t.Errorf("MountPoint: got %q", cfg.MountPoint)
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
		"--mount-point=/mnt/cli",
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
	t.Setenv("SCRINIUM_FUSE_STORE", "file:///from/env")
	t.Setenv("SCRINIUM_FUSE_NAMESPACE", "env-ns")
	args := []string{"--mount-point=/mnt/x"}
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
	t.Setenv("SCRINIUM_FUSE_NAMESPACE", "env-ns")
	args := []string{
		"--store=file:///x", "--mount-point=/y",
		"--namespace=cli-ns",
	}
	cfg, _, _ := loadConfig(args)
	if cfg.Daemon.Namespace != "cli-ns" {
		t.Errorf("CLI must beat env: got %q", cfg.Daemon.Namespace)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	cfg := DefaultConfig()
	// No Store → scrinium.Validate fails.
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "store") {
		t.Errorf("expected store error, got %v", err)
	}
	cfg.Daemon.Store = "file:///x"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mount-point") {
		t.Errorf("expected mount-point error, got %v", err)
	}
	cfg.MountPoint = "/y"
	if err := cfg.Validate(); err != nil {
		t.Errorf("with required fields set, Validate must pass: %v", err)
	}
}

func TestValidate_InvalidEditing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Daemon.Store = "file:///x"
	cfg.MountPoint = "/y"
	cfg.Daemon.Editing = "maybe"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for editing=maybe")
	}
}

func TestValidate_InvalidIndexMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Daemon.Store = "file:///x"
	cfg.MountPoint = "/y"
	cfg.IndexMode = "ephemeral"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for unknown index-mode")
	}
}

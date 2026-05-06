package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rkurbatov/scrinium/projection"

	"gopkg.in/yaml.v3"
)

// Config holds every tunable for the FUSE mount. It is built up
// from (in increasing priority): defaults → YAML file (--config)
// → environment variables (SCRINIUM_FUSE_*) → CLI flags.
//
// Field-name choices match the spec — kebab-case in CLI, lower
// camelCase in YAML, SCREAMING_SNAKE_CASE in env vars. The
// translation table lives in the per-flag wiring below.
type Config struct {
	// Required.
	StorePath string `yaml:"storePath"`
	Listen    string `yaml:"listen"`

	// Encryption.
	PassphraseFile string `yaml:"passphraseFile"`

	// Default namespace for new artifacts.
	Namespace string `yaml:"namespace"`

	// Root view.
	RootView       projection.RootView     `yaml:"rootView"`
	ByPathFallback projection.PathFallback `yaml:"byPathFallback"`

	// Service tree.
	ServicePrefix   string `yaml:"servicePrefix"`
	ShowStats       bool   `yaml:"showStats"`
	ShowByArtifact  bool   `yaml:"showByArtifact"`
	ShowOrphaned    bool   `yaml:"showOrphaned"`
	ShowByDate      bool   `yaml:"showByDate"`
	ShowBySession   bool   `yaml:"showBySession"`
	ShowByNamespace bool   `yaml:"showByNamespace"`
	ShowRaw         bool   `yaml:"showRaw"`

	// Editing.
	Editing       string `yaml:"editing"` // "off" | "on" | "custom"
	AllowRename   *bool  `yaml:"allowRename,omitempty"`
	AllowSetattr  *bool  `yaml:"allowSetattr,omitempty"`
	AllowTruncate *bool  `yaml:"allowTruncate,omitempty"`
	AllowAppend   *bool  `yaml:"allowAppend,omitempty"`

	// Scratch.
	ScratchDir   string `yaml:"scratchDir"`
	ScratchQuota int64  `yaml:"scratchQuota"` // bytes; 0 = unlimited

	// POSIX defaults.
	DefaultMode uint32 `yaml:"defaultMode"`
	DefaultUID  uint32 `yaml:"defaultUid"`
	DefaultGID  uint32 `yaml:"defaultGid"`

	// Read-only.
	ReadOnly bool `yaml:"readOnly"`

	// AllowOSJunk disables the desktop-junk filter. Off by
	// default — see junkfilter.go for the patterns rejected.
	AllowOSJunk bool `yaml:"allowOsJunk"`

	// BrowsePrefix is the URL prefix under which the daemon
	// serves human-readable HTML directory listings. Default
	// "/_browse"; empty disables the browser entirely.
	//
	// WebDAV stays on the root path regardless of this setting:
	// the browser is a separate, secondary surface for ad-hoc
	// inspection. Clients (Finder, rclone, Office) always
	// connect to "/", get pure WebDAV.
	BrowsePrefix string `yaml:"browsePrefix"`
}

// DefaultConfig returns a Config populated with the spec's
// documented defaults. Only fields with a non-zero default are
// touched; everything else stays at the Go zero value.
func DefaultConfig() Config {
	return Config{
		Listen:          ":8080",
		Namespace:       "files",
		RootView:        projection.RootByPath,
		ByPathFallback:  projection.FallbackOrphaned,
		ServicePrefix:   "_scrinium",
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   false,
		ShowByNamespace: false,
		ShowRaw:         false,
		Editing:         "off",
		ScratchQuota:    1 << 30, // 1 GiB
		DefaultMode:     0o644,
		DefaultUID:      uint32(os.Getuid()),
		DefaultGID:      uint32(os.Getgid()),
		BrowsePrefix:    "/_browse",
	}
}

// loadConfig builds the final Config for "mount" by walking the
// priority chain: defaults → YAML file (if --config given) →
// environment → CLI flags. The returned FlagSet is exposed so the
// caller can decide how to handle parse errors (the args passed
// in have already been consumed).
//
// args is os.Args[2:] — the slice after "mount".
func loadConfig(args []string) (Config, *flag.FlagSet, error) {
	cfg := DefaultConfig()

	// Apply environment first so flags can still override.
	applyEnv(&cfg)

	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	// --config is parsed in a pre-pass: if present, load YAML
	// before binding the rest. CLI flags then override YAML.
	configPath := ""
	fs.StringVar(&configPath, "config", "", "Path to a YAML config file.")

	// Bind every flag.
	bindFlags(fs, &cfg)

	// First-pass parse to find --config, if any.
	if err := fs.Parse(args); err != nil {
		return cfg, fs, err
	}
	if configPath != "" {
		if err := loadYAMLInto(configPath, &cfg); err != nil {
			return cfg, fs, fmt.Errorf("load config %q: %w", configPath, err)
		}
		// Re-apply env (env > YAML).
		applyEnv(&cfg)
		// Re-parse flags so CLI overrides what YAML supplied.
		// We zero the defaults so unset flags don't clobber YAML.
		// Trick: build a fresh FlagSet whose defaults track cfg's
		// current state.
		fs2 := flag.NewFlagSet("mount", flag.ContinueOnError)
		fs2.SetOutput(os.Stderr)
		fs2.StringVar(&configPath, "config", "", "Path to a YAML config file.")
		bindFlags(fs2, &cfg)
		if err := fs2.Parse(args); err != nil {
			return cfg, fs2, err
		}
		fs = fs2
	}

	return cfg, fs, nil
}

// bindFlags installs every flag onto fs, with the *current* values
// of cfg as defaults. Re-binding after a YAML load uses YAML's
// values as defaults, so unset CLI flags inherit them.
func bindFlags(fs *flag.FlagSet, cfg *Config) {
	fs.StringVar(&cfg.StorePath, "store-path", cfg.StorePath, "Path to the Scrinium store (required).")
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address.")
	fs.StringVar(&cfg.PassphraseFile, "passphrase-file", cfg.PassphraseFile, "File holding the store passphrase (encrypted stores).")
	fs.StringVar(&cfg.Namespace, "namespace", cfg.Namespace, "Default namespace for new artifacts.")

	fs.Var(rootViewFlag{&cfg.RootView}, "root-view", "Root tree: by-path|by-session|by-namespace|by-date|by-artifact.")
	fs.Var(fallbackFlag{&cfg.ByPathFallback}, "by-path-fallback", "Behaviour for artifacts without a resolver path: orphaned|synthetic.")

	fs.StringVar(&cfg.ServicePrefix, "service-prefix", cfg.ServicePrefix, "Service tree prefix; empty disables.")
	fs.BoolVar(&cfg.ShowStats, "show-stats", cfg.ShowStats, "Expose _scrinium/stats.")
	fs.BoolVar(&cfg.ShowByArtifact, "show-by-artifact", cfg.ShowByArtifact, "Expose _scrinium/by-artifact/.")
	fs.BoolVar(&cfg.ShowOrphaned, "show-orphaned", cfg.ShowOrphaned, "Expose _scrinium/orphaned/.")
	fs.BoolVar(&cfg.ShowByDate, "show-by-date", cfg.ShowByDate, "Expose _scrinium/by-date/.")
	fs.BoolVar(&cfg.ShowBySession, "show-by-session", cfg.ShowBySession, "Expose _scrinium/by-session/.")
	fs.BoolVar(&cfg.ShowByNamespace, "show-by-namespace", cfg.ShowByNamespace, "Expose _scrinium/by-namespace/.")
	fs.BoolVar(&cfg.ShowRaw, "show-raw", cfg.ShowRaw, "Expose _scrinium/raw/ — physical store mirror.")

	fs.StringVar(&cfg.Editing, "editing", cfg.Editing, "Editing policy: off|on|custom.")
	fs.Var(boolPtrFlag{&cfg.AllowRename}, "allow-rename", "(custom editing) allow rename().")
	fs.Var(boolPtrFlag{&cfg.AllowSetattr}, "allow-setattr", "(custom editing) allow chmod/chown/utimens.")
	fs.Var(boolPtrFlag{&cfg.AllowTruncate}, "allow-truncate", "(custom editing) allow truncate().")
	fs.Var(boolPtrFlag{&cfg.AllowAppend}, "allow-append", "(custom editing) allow O_APPEND.")

	fs.StringVar(&cfg.ScratchDir, "scratch-dir", cfg.ScratchDir, "Scratch directory for buffered writes.")
	fs.Var(byteSizeFlag{&cfg.ScratchQuota}, "scratch-quota", "Total scratch byte cap (e.g. 500M, 1G); 0 = unlimited.")

	fs.Var(octalFlag{&cfg.DefaultMode}, "default-mode", "POSIX mode for artifacts without an explicit fsmeta.Mode.")
	fs.Var(uintFlag{&cfg.DefaultUID}, "default-uid", "POSIX UID for artifacts without an explicit fsmeta.UID.")
	fs.Var(uintFlag{&cfg.DefaultGID}, "default-gid", "POSIX GID for artifacts without an explicit fsmeta.GID.")

	fs.BoolVar(&cfg.ReadOnly, "read-only", cfg.ReadOnly, "Serve read-only.")
	fs.BoolVar(&cfg.AllowOSJunk, "allow-os-junk", cfg.AllowOSJunk,
		"Permit clients to write OS-generated junk files (.DS_Store, Thumbs.db, AppleDouble ._*, etc).")
	fs.StringVar(&cfg.BrowsePrefix, "browse-prefix", cfg.BrowsePrefix,
		"URL prefix for HTML browser listings. Empty disables. Default \"/_browse\".")
}

// applyEnv overlays SCRINIUM_WEBDAV_* environment variables onto
// cfg. Only string-shaped fields are picked — env vars are blunt
// instruments and we do not want to expose every knob there.
func applyEnv(cfg *Config) {
	if v := os.Getenv("SCRINIUM_WEBDAV_STORE_PATH"); v != "" {
		cfg.StorePath = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_NAMESPACE"); v != "" {
		cfg.Namespace = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_PASSPHRASE_FILE"); v != "" {
		cfg.PassphraseFile = v
	}
}

// loadYAMLInto reads a YAML file and overlays its fields onto cfg.
// Missing fields keep their current cfg value.
func loadYAMLInto(path string, cfg *Config) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("YAML parse: %w", err)
	}
	return nil
}

// Validate checks the assembled Config for required fields and
// inter-field consistency. Returns the first violation. The
// caller is expected to surface this as the program exit reason.
func (cfg *Config) Validate() error {
	if cfg.StorePath == "" {
		return fmt.Errorf("--store-path is required")
	}
	if cfg.Listen == "" {
		return fmt.Errorf("--listen is required")
	}
	switch cfg.RootView {
	case projection.RootByPath, projection.RootBySession,
		projection.RootByNamespace, projection.RootByDate, projection.RootByArtifact:
	default:
		return fmt.Errorf("--root-view: invalid value %q", cfg.RootView)
	}
	switch cfg.ByPathFallback {
	case projection.FallbackOrphaned, projection.FallbackSynthetic:
	default:
		return fmt.Errorf("--by-path-fallback: invalid value %q", cfg.ByPathFallback)
	}
	switch cfg.Editing {
	case "off", "on", "custom":
	default:
		return fmt.Errorf("--editing: must be off|on|custom, got %q", cfg.Editing)
	}
	if cfg.ScratchQuota < 0 {
		return fmt.Errorf("--scratch-quota: must be >= 0")
	}
	return nil
}

// EditingPolicy reduces cfg.Editing + per-bit flags to a concrete
// projection.EditingPolicy. The custom mode wins on a per-bit
// basis: "off" forces every bit to false regardless of explicit
// AllowX, "on" forces every bit to true, "custom" inspects the
// pointer flags (nil = false).
func (cfg *Config) EditingPolicy() projection.EditingPolicy {
	switch cfg.Editing {
	case "on":
		return projection.EditingOn()
	case "custom":
		return projection.EditingPolicy{
			AllowRename:   ptrBool(cfg.AllowRename),
			AllowSetattr:  ptrBool(cfg.AllowSetattr),
			AllowTruncate: ptrBool(cfg.AllowTruncate),
			AllowAppend:   ptrBool(cfg.AllowAppend),
		}
	default: // "off" and anything unrecognised falls here
		return projection.EditingOff()
	}
}

func ptrBool(p *bool) bool { return p != nil && *p }

// --- Custom flag types ---

// rootViewFlag binds a CLI flag to *projection.RootView with
// allowed-value validation.
type rootViewFlag struct{ p *projection.RootView }

func (f rootViewFlag) String() string {
	if f.p == nil {
		return ""
	}
	return string(*f.p)
}

func (f rootViewFlag) Set(s string) error {
	rv := projection.RootView(s)
	switch rv {
	case projection.RootByPath, projection.RootBySession,
		projection.RootByNamespace, projection.RootByDate, projection.RootByArtifact:
		*f.p = rv
		return nil
	}
	return fmt.Errorf("invalid root-view %q", s)
}

// fallbackFlag binds a CLI flag to *projection.PathFallback.
type fallbackFlag struct{ p *projection.PathFallback }

func (f fallbackFlag) String() string {
	if f.p == nil {
		return ""
	}
	return string(*f.p)
}

func (f fallbackFlag) Set(s string) error {
	fb := projection.PathFallback(s)
	switch fb {
	case projection.FallbackOrphaned, projection.FallbackSynthetic:
		*f.p = fb
		return nil
	}
	return fmt.Errorf("invalid by-path-fallback %q", s)
}

// boolPtrFlag binds a CLI flag to **bool — nil means "not set",
// allowing the editing-custom logic to distinguish "default" from
// "explicit false".
type boolPtrFlag struct{ p **bool }

func (f boolPtrFlag) String() string {
	if f.p == nil || *f.p == nil {
		return ""
	}
	return strconv.FormatBool(**f.p)
}

func (f boolPtrFlag) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*f.p = &b
	return nil
}

// IsBoolFlag tells the flag package the flag accepts no argument
// when written as -flag (sets to true).
func (f boolPtrFlag) IsBoolFlag() bool { return true }

// byteSizeFlag accepts human-friendly suffixes on integer byte
// counts: "500", "500K", "500M", "1G", "2T". Lower- and
// upper-case suffixes are equivalent. Decimal multipliers (1K =
// 1000) are NOT used; we use binary (1K = 1024) — matches typical
// quota tooling conventions.
type byteSizeFlag struct{ p *int64 }

func (f byteSizeFlag) String() string {
	if f.p == nil {
		return ""
	}
	return strconv.FormatInt(*f.p, 10)
}

func (f byteSizeFlag) Set(s string) error {
	if s == "" {
		return fmt.Errorf("empty size")
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'k', 'K':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1 << 30
		s = s[:len(s)-1]
	case 't', 'T':
		mult = 1 << 40
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return fmt.Errorf("size: %w", err)
	}
	*f.p = n * mult
	return nil
}

// octalFlag accepts octal POSIX-style mode strings like "0644" or
// "644" (leading 0 optional). Decimal not accepted to avoid
// confusion: "644" decimal would be 0o1204 which is nonsense as
// a POSIX mode.
type octalFlag struct{ p *uint32 }

func (f octalFlag) String() string {
	if f.p == nil {
		return ""
	}
	return fmt.Sprintf("0%o", *f.p)
}

func (f octalFlag) Set(s string) error {
	s = strings.TrimPrefix(s, "0o")
	s = strings.TrimPrefix(s, "0")
	if s == "" {
		*f.p = 0
		return nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return fmt.Errorf("octal mode: %w", err)
	}
	*f.p = uint32(n)
	return nil
}

// uintFlag binds a CLI flag to *uint32.
type uintFlag struct{ p *uint32 }

func (f uintFlag) String() string {
	if f.p == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*f.p), 10)
}

func (f uintFlag) Set(s string) error {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return err
	}
	*f.p = uint32(n)
	return nil
}

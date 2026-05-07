package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rkurbatov/scrinium/internal/daemon"
	"github.com/rkurbatov/scrinium/projection"

	"gopkg.in/yaml.v3"
)

// Config is what the scrinium-webdav binary reads. The
// daemon-level fields (Store, Index, Routing, Editing,
// Policy, etc.) are embedded inline so a user's YAML stays
// flat — store and listen sit at the same level.
//
// What's specific to scrinium-webdav stays here: the listen
// address, the browse-prefix for the embedded HTML view, and
// the OS-junk filter toggle.
//
// Field-name choices match the spec — kebab-case in CLI,
// lower camelCase in YAML, SCREAMING_SNAKE_CASE in env vars.
type Config struct {
	// Daemon embeds the shared bootstrap config. yaml:",inline"
	// keeps the user-visible YAML flat — `store: ...` and
	// `listen: ...` live at the same level rather than under
	// a `daemon:` block. CLI flags follow suit.
	Daemon daemon.Config `yaml:",inline"`

	// Listen is the HTTP listen address (e.g. ":8080").
	// Required.
	Listen string `yaml:"listen"`

	// AllowOSJunk disables the desktop-junk filter (.DS_Store,
	// Thumbs.db, AppleDouble ._*, etc.). Off by default — see
	// junkfilter.go for the patterns rejected.
	AllowOSJunk bool `yaml:"allowOsJunk"`
}

// DefaultConfig returns a Config populated with the spec's
// documented defaults. Daemon's defaults come from
// daemon.DefaultConfig; only WebDAV-specific defaults and
// the legacy "files" namespace are set here.
func DefaultConfig() Config {
	cfg := Config{
		Daemon: daemon.DefaultConfig(),
		Listen: ":8080",
	}
	// Backward-compat default for the daemon-level namespace.
	// Old configs left it implicit; we keep "files" so an
	// existing scratch directory stays the source of truth.
	if cfg.Daemon.Namespace == "" {
		cfg.Daemon.Namespace = "files"
	}
	// Old webdav default for scratch quota — keep it at 1 GiB
	// rather than the unlimited 0 for safety.
	if cfg.Daemon.ScratchQuota == 0 {
		cfg.Daemon.ScratchQuota = 1 << 30
	}
	// Service trees default OFF for WebDAV. Diagnostic trees
	// generate Finder/rclone listing noise; admins enable
	// specific trees explicitly with --show-X flags.
	cfg.Daemon.ShowStats = false
	cfg.Daemon.ShowByArtifact = false
	cfg.Daemon.ShowOrphaned = false
	cfg.Daemon.ShowByDate = false
	cfg.Daemon.ShowBySession = false
	cfg.Daemon.ShowByNamespace = false
	cfg.Daemon.ShowRaw = false
	return cfg
}

// loadConfig builds the final Config for "serve" by walking the
// priority chain: defaults → YAML file (if --config given) →
// environment → CLI flags. The returned FlagSet is exposed so
// the caller can decide how to handle parse errors (the args
// passed in have already been consumed).
//
// args is os.Args[2:] — the slice after "serve".
func loadConfig(args []string) (Config, *flag.FlagSet, error) {
	cfg := DefaultConfig()

	// Apply environment first so flags can still override.
	applyEnv(&cfg)

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	// --config is parsed in a pre-pass: if present, load YAML
	// before binding the rest. CLI flags then override YAML.
	configPath := ""
	fs.StringVar(&configPath, "config", "", "Path to a YAML config file.")

	bindFlags(fs, &cfg)

	if err := fs.Parse(args); err != nil {
		return cfg, fs, err
	}
	if configPath != "" {
		if err := loadYAMLInto(configPath, &cfg); err != nil {
			return cfg, fs, fmt.Errorf("load config %q: %w", configPath, err)
		}
		applyEnv(&cfg)
		// Re-parse flags so CLI overrides what YAML supplied.
		fs2 := flag.NewFlagSet("serve", flag.ContinueOnError)
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

// bindFlags installs every flag onto fs, with the *current*
// values of cfg as defaults. Re-binding after a YAML load
// uses YAML's values as defaults, so unset CLI flags inherit
// them.
//
// Daemon fields are bound through &cfg.Daemon.<Field>;
// WebDAV-specific fields through &cfg.<Field>. The flag
// names themselves don't reveal the embedding — this is
// surface-level naming.
func bindFlags(fs *flag.FlagSet, cfg *Config) {
	// Daemon — store and index URIs.
	fs.StringVar(&cfg.Daemon.Store, "store", cfg.Daemon.Store,
		"Store URI (file:///path or bare /path; required).")
	fs.StringVar(&cfg.Daemon.Index, "index", cfg.Daemon.Index,
		"Index URI (sqlite:///path; defaults to <storedir>/index.db when store is local).")

	// WebDAV-specific.
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address.")
	fs.BoolVar(&cfg.AllowOSJunk, "allow-os-junk", cfg.AllowOSJunk,
		"Permit clients to write OS-generated junk files (.DS_Store, Thumbs.db, AppleDouble ._*, etc).")

	// Daemon — encryption / namespace.
	fs.StringVar(&cfg.Daemon.PassphraseFile, "passphrase-file", cfg.Daemon.PassphraseFile,
		"File holding the store passphrase (encrypted stores).")
	fs.StringVar(&cfg.Daemon.Namespace, "namespace", cfg.Daemon.Namespace,
		"Default namespace for new artifacts.")

	// Daemon — routing.
	fs.Var(rootViewFlag{&cfg.Daemon.RootView}, "root-view",
		"Root tree: by-path|by-session|by-namespace|by-date|by-artifact.")
	fs.StringVar(&cfg.Daemon.ByPathFallback, "by-path-fallback", cfg.Daemon.ByPathFallback,
		"Behaviour for artifacts without a resolver path: orphaned|synthetic.")
	fs.StringVar(&cfg.Daemon.ServicePrefix, "service-prefix", cfg.Daemon.ServicePrefix,
		"Service tree prefix; empty disables.")
	fs.BoolVar(&cfg.Daemon.ShowStats, "show-stats", cfg.Daemon.ShowStats, "Expose _scrinium/stats.")
	fs.BoolVar(&cfg.Daemon.ShowByArtifact, "show-by-artifact", cfg.Daemon.ShowByArtifact, "Expose _scrinium/by-artifact/.")
	fs.BoolVar(&cfg.Daemon.ShowOrphaned, "show-orphaned", cfg.Daemon.ShowOrphaned, "Expose _scrinium/orphaned/.")
	fs.BoolVar(&cfg.Daemon.ShowByDate, "show-by-date", cfg.Daemon.ShowByDate, "Expose _scrinium/by-date/.")
	fs.BoolVar(&cfg.Daemon.ShowBySession, "show-by-session", cfg.Daemon.ShowBySession, "Expose _scrinium/by-session/.")
	fs.BoolVar(&cfg.Daemon.ShowByNamespace, "show-by-namespace", cfg.Daemon.ShowByNamespace, "Expose _scrinium/by-namespace/.")
	fs.BoolVar(&cfg.Daemon.ShowRaw, "show-raw", cfg.Daemon.ShowRaw, "Expose _scrinium/raw/ — physical store mirror.")

	// Daemon — editing policy.
	fs.StringVar(&cfg.Daemon.Editing, "editing", cfg.Daemon.Editing, "Editing policy: off|on|custom.")
	fs.Var(boolPtrFlag{&cfg.Daemon.AllowRename}, "allow-rename", "(custom editing) allow rename().")
	fs.Var(boolPtrFlag{&cfg.Daemon.AllowSetattr}, "allow-setattr", "(custom editing) allow chmod/chown/utimens.")
	fs.Var(boolPtrFlag{&cfg.Daemon.AllowTruncate}, "allow-truncate", "(custom editing) allow truncate().")
	fs.Var(boolPtrFlag{&cfg.Daemon.AllowAppend}, "allow-append", "(custom editing) allow O_APPEND.")

	// Daemon — scratch / readonly.
	fs.StringVar(&cfg.Daemon.ScratchDir, "scratch-dir", cfg.Daemon.ScratchDir, "Scratch directory for buffered writes.")
	fs.Var(byteSizeFlag{&cfg.Daemon.ScratchQuota}, "scratch-quota", "Total scratch byte cap (e.g. 500M, 1G); 0 = unlimited.")
	fs.BoolVar(&cfg.Daemon.ReadOnly, "read-only", cfg.Daemon.ReadOnly, "Serve read-only.")

	// Daemon — POSIX defaults.
	fs.Var(octalFlag{&cfg.Daemon.DefaultMode}, "default-mode", "POSIX mode for artifacts without an explicit fsmeta.Mode.")
	fs.Var(uintFlag{&cfg.Daemon.DefaultUID}, "default-uid", "POSIX UID for artifacts without an explicit fsmeta.UID.")
	fs.Var(uintFlag{&cfg.Daemon.DefaultGID}, "default-gid", "POSIX GID for artifacts without an explicit fsmeta.GID.")
}

// applyEnv overlays SCRINIUM_WEBDAV_* environment variables
// onto cfg. Only string-shaped fields are picked — env vars
// are blunt instruments and we do not want to expose every
// knob there.
func applyEnv(cfg *Config) {
	if v := os.Getenv("SCRINIUM_WEBDAV_STORE"); v != "" {
		cfg.Daemon.Store = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_INDEX"); v != "" {
		cfg.Daemon.Index = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_NAMESPACE"); v != "" {
		cfg.Daemon.Namespace = v
	}
	if v := os.Getenv("SCRINIUM_WEBDAV_PASSPHRASE_FILE"); v != "" {
		cfg.Daemon.PassphraseFile = v
	}
}

// loadYAMLInto reads a YAML file and overlays its fields onto
// cfg. Missing fields keep their current cfg value.
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

// Validate checks the assembled Config. Daemon-level checks
// run via cfg.Daemon.Validate(); WebDAV-level checks live
// here.
func (cfg *Config) Validate() error {
	if err := cfg.Daemon.Validate(); err != nil {
		return err
	}
	if cfg.Listen == "" {
		return fmt.Errorf("--listen is required")
	}
	return nil
}

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

// boolPtrFlag binds a CLI flag to **bool — nil means "not set",
// allowing the editing-custom logic to distinguish "default"
// from "explicit false".
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

// IsBoolFlag tells the flag package the flag accepts no
// argument when written as -flag (sets to true).
func (f boolPtrFlag) IsBoolFlag() bool { return true }

// byteSizeFlag accepts human-friendly suffixes on integer
// byte counts: "500", "500K", "500M", "1G", "2T". Lower- and
// upper-case suffixes are equivalent. Binary multipliers
// (1K = 1024).
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

// octalFlag accepts octal POSIX-style mode strings like "0644"
// or "644" (leading 0 optional).
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

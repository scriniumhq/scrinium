package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rkurbatov/scrinium/cmd/internal/daemon"
	"github.com/rkurbatov/scrinium/projection"

	"gopkg.in/yaml.v3"
)

// Config is what scrinium-fuse reads. The daemon-level fields
// (Store, Index, Routing, Editing, Policy, etc.) are embedded
// inline so a user's YAML stays flat. What's specific to FUSE
// — mount point, allow-other, index-mode shortcut — stays
// here.
type Config struct {
	// Daemon embeds the shared bootstrap config. yaml:",inline"
	// keeps the user-visible YAML flat.
	Daemon daemon.Config `yaml:",inline"`

	// MountPoint is the directory the FUSE filesystem mounts
	// onto. Required.
	MountPoint string `yaml:"mountPoint"`

	// AllowOther passes the allow_other FUSE flag — other
	// users on the host can read the mount. Off by default;
	// most distros also require user_allow_other in
	// /etc/fuse.conf.
	AllowOther bool `yaml:"allowOther"`

	// IndexMode is a convenience shortcut: "memory" sets the
	// daemon's Index URI to sqlite://:memory: (volatile, fast,
	// rebuilt at every mount). "persistent" leaves Index empty
	// so daemon defaults to <storedir>/index.db.
	//
	// Setting Daemon.Index explicitly takes precedence over
	// this shortcut.
	IndexMode string `yaml:"indexMode"`
}

// DefaultConfig returns a Config with the documented defaults.
// Daemon's defaults come from daemon.DefaultConfig; only
// FUSE-specific defaults and the "files" namespace fallback
// are set here.
func DefaultConfig() Config {
	cfg := Config{
		Daemon:    daemon.DefaultConfig(),
		IndexMode: "memory",
	}
	if cfg.Daemon.Namespace == "" {
		cfg.Daemon.Namespace = "files"
	}
	if cfg.Daemon.ScratchQuota == 0 {
		cfg.Daemon.ScratchQuota = 1 << 30
	}
	return cfg
}

// loadConfig assembles a Config: defaults → env → YAML → CLI.
//
// args is os.Args[2:] — the slice after "mount".
func loadConfig(args []string) (Config, *flag.FlagSet, error) {
	cfg := DefaultConfig()
	applyEnv(&cfg)

	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

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
		fs2 := flag.NewFlagSet("mount", flag.ContinueOnError)
		fs2.SetOutput(os.Stderr)
		fs2.StringVar(&configPath, "config", "", "Path to a YAML config file.")
		bindFlags(fs2, &cfg)
		if err := fs2.Parse(args); err != nil {
			return cfg, fs2, err
		}
		fs = fs2
	}

	// Translate the IndexMode shortcut into a concrete URI if
	// the user didn't pass --index explicitly. Done after
	// parsing so an explicit --index always wins.
	if cfg.Daemon.Index == "" && cfg.IndexMode == "memory" {
		cfg.Daemon.Index = "sqlite://:memory:"
	}

	return cfg, fs, nil
}

// bindFlags installs every flag onto fs. Daemon fields are
// bound through &cfg.Daemon.<Field>; FUSE-specific fields
// through &cfg.<Field>.
func bindFlags(fs *flag.FlagSet, cfg *Config) {
	// Daemon — store and index URIs.
	fs.StringVar(&cfg.Daemon.Store, "store", cfg.Daemon.Store,
		"Store URI (file:///path or bare /path; required).")
	fs.StringVar(&cfg.Daemon.Index, "index", cfg.Daemon.Index,
		"Index URI (sqlite://...; overrides --index-mode if set).")

	// FUSE-specific.
	fs.StringVar(&cfg.MountPoint, "mount-point", cfg.MountPoint, "Directory to mount onto (required).")
	fs.BoolVar(&cfg.AllowOther, "allow-other", cfg.AllowOther, "Allow other users to access the mount.")
	fs.StringVar(&cfg.IndexMode, "index-mode", cfg.IndexMode,
		"Projection index mode: memory|persistent. Shortcut for --index sqlite://:memory: vs default.")

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
	fs.BoolVar(&cfg.Daemon.ReadOnly, "read-only", cfg.Daemon.ReadOnly, "Mount read-only.")

	// Daemon — POSIX defaults.
	fs.Var(octalFlag{&cfg.Daemon.DefaultMode}, "default-mode", "POSIX mode for artifacts without an explicit fsmeta.Mode.")
	fs.Var(uintFlag{&cfg.Daemon.DefaultUID}, "default-uid", "POSIX UID for artifacts without an explicit fsmeta.UID.")
	fs.Var(uintFlag{&cfg.Daemon.DefaultGID}, "default-gid", "POSIX GID for artifacts without an explicit fsmeta.GID.")
}

// applyEnv overlays SCRINIUM_FUSE_* environment variables.
func applyEnv(cfg *Config) {
	if v := os.Getenv("SCRINIUM_FUSE_STORE"); v != "" {
		cfg.Daemon.Store = v
	}
	if v := os.Getenv("SCRINIUM_FUSE_INDEX"); v != "" {
		cfg.Daemon.Index = v
	}
	if v := os.Getenv("SCRINIUM_FUSE_MOUNT_POINT"); v != "" {
		cfg.MountPoint = v
	}
	if v := os.Getenv("SCRINIUM_FUSE_NAMESPACE"); v != "" {
		cfg.Daemon.Namespace = v
	}
	if v := os.Getenv("SCRINIUM_FUSE_PASSPHRASE_FILE"); v != "" {
		cfg.Daemon.PassphraseFile = v
	}
}

// loadYAMLInto reads a YAML file and overlays its fields onto
// cfg. Missing fields keep their current value.
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
// run via cfg.Daemon.Validate(); FUSE-level checks here.
func (cfg *Config) Validate() error {
	if err := cfg.Daemon.Validate(); err != nil {
		return err
	}
	if cfg.MountPoint == "" {
		return fmt.Errorf("--mount-point is required")
	}
	switch cfg.IndexMode {
	case "", "memory", "persistent":
	default:
		return fmt.Errorf("--index-mode: must be memory|persistent, got %q", cfg.IndexMode)
	}
	return nil
}

// --- Custom flag types ---
//
// These mirror cmd/scrinium-webdav/config.go. Duplication
// between cmd packages is intentional — flag types are
// trivially small and lifting them into a shared internal
// package would create a poor cross-cmd dependency for the
// sake of ~100 lines.

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

func (f boolPtrFlag) IsBoolFlag() bool { return true }

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

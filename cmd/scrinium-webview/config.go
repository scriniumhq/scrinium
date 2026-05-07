package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/rkurbatov/scrinium/internal/daemon"
	"github.com/rkurbatov/scrinium/projection"

	"gopkg.in/yaml.v3"
)

// Config is what scrinium-webview reads. The daemon-level
// fields are embedded inline; only Listen and BrowsePrefix are
// webview-specific.
//
// scrinium-webview always opens the store ReadOnly — even if
// the YAML/CLI says otherwise, runServe forces it. The flag
// is still configurable here because the daemon config's
// ReadOnly field is shared across binaries; making it always-
// true at this layer is webview-cmd's policy choice.
type Config struct {
	Daemon daemon.Config `yaml:",inline"`

	// Listen is the HTTP listen address. Default ":8081" —
	// one above webdav's default so a developer can run both
	// against the same store on adjacent ports.
	Listen string `yaml:"listen"`

	// BrowsePrefix is the URL prefix under which the HTML
	// view serves listings and artifact pages. Default "/" —
	// since there's no WebDAV protocol contending for the
	// root path, the browser owns it.
	BrowsePrefix string `yaml:"browsePrefix"`

	// DefaultTree picks which tree the bare BrowsePrefix
	// (typically "/") redirects to. Allowed values match the
	// service tree names: by-path | by-date | by-session |
	// by-namespace | by-artifact | orphaned. Default
	// "by-path" — the natural starting point for a browser
	// that thinks in filesystem terms.
	DefaultTree string `yaml:"defaultTree"`
}

// DefaultConfig — webview defaults.
func DefaultConfig() Config {
	cfg := Config{
		Daemon:       daemon.DefaultConfig(),
		Listen:       ":8081",
		BrowsePrefix: "/",
		DefaultTree:  "by-path",
	}
	if cfg.Daemon.Namespace == "" {
		cfg.Daemon.Namespace = "files"
	}
	return cfg
}

// loadConfig assembles a Config: defaults → env → YAML → CLI.
func loadConfig(args []string) (Config, *flag.FlagSet, error) {
	cfg := DefaultConfig()
	applyEnv(&cfg)

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
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

// bindFlags installs every flag onto fs.
//
// Webview is read-only by definition, so write-policy flags
// (editing, allow-rename, default-mode, scratch-dir, etc.)
// are NOT bound. Users who want those should use
// scrinium-webdav.
func bindFlags(fs *flag.FlagSet, cfg *Config) {
	// Daemon — store and index URIs.
	fs.StringVar(&cfg.Daemon.Store, "store", cfg.Daemon.Store,
		"Store URI (file:///path or bare /path; required).")
	fs.StringVar(&cfg.Daemon.Index, "index", cfg.Daemon.Index,
		"Index URI (sqlite:///path; defaults to <storedir>/index.db when store is local).")

	// Webview-specific.
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address.")
	fs.StringVar(&cfg.BrowsePrefix, "browse-prefix", cfg.BrowsePrefix,
		"URL prefix for HTML browser listings. Default \"/\".")
	fs.StringVar(&cfg.DefaultTree, "default-tree", cfg.DefaultTree,
		"Tree the root URL redirects to: by-path|by-date|by-session|by-namespace|by-artifact|orphaned.")

	// Daemon — encryption / namespace.
	fs.StringVar(&cfg.Daemon.PassphraseFile, "passphrase-file", cfg.Daemon.PassphraseFile,
		"File holding the store passphrase (encrypted stores).")
	fs.StringVar(&cfg.Daemon.Namespace, "namespace", cfg.Daemon.Namespace,
		"Namespace constraint (visible-only filter for webview).")

	// Daemon — routing (which trees the browser shows).
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
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SCRINIUM_WEBVIEW_STORE"); v != "" {
		cfg.Daemon.Store = v
	}
	if v := os.Getenv("SCRINIUM_WEBVIEW_INDEX"); v != "" {
		cfg.Daemon.Index = v
	}
	if v := os.Getenv("SCRINIUM_WEBVIEW_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("SCRINIUM_WEBVIEW_NAMESPACE"); v != "" {
		cfg.Daemon.Namespace = v
	}
	if v := os.Getenv("SCRINIUM_WEBVIEW_PASSPHRASE_FILE"); v != "" {
		cfg.Daemon.PassphraseFile = v
	}
}

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

func (cfg *Config) Validate() error {
	if err := cfg.Daemon.Validate(); err != nil {
		return err
	}
	if cfg.Listen == "" {
		return fmt.Errorf("--listen is required")
	}
	if cfg.BrowsePrefix == "" {
		return fmt.Errorf("--browse-prefix is required (use \"/\" for root)")
	}
	switch cfg.DefaultTree {
	case "", "by-path", "by-date", "by-session", "by-namespace", "by-artifact", "orphaned":
		// OK; "" handled by runServe falling back to "by-path".
	default:
		return fmt.Errorf("--default-tree: %q is not one of {by-path, by-date, by-session, by-namespace, by-artifact, orphaned}", cfg.DefaultTree)
	}
	return nil
}

// --- flag types (mirror webdav's; deliberate duplication) ---

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

// (no boolPtrFlag, byteSizeFlag, octalFlag, uintFlag — webview
// doesn't bind any of those flags. If we add admin commands
// later we'll reach for the same definitions.)

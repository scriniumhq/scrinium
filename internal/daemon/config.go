package daemon

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rkurbatov/scrinium/projection"
)

// Config is the shared configuration consumed by every
// scrinium binary. Surface-specific configs (webdav listen
// address, fuse mount point, etc.) live in the respective
// cmd packages and reference this Config.
//
// Config is meant to be loaded from YAML or built up
// programmatically; CLI flag binding is the cmd's job.
//
// Two URIs identify the backend:
//   - Store points at the artifact storage (file://, s3://...).
//   - Index points at the metadata index (sqlite://, postgres://).
//
// Bare paths in Store are accepted for backward compatibility
// (treated as file://). Index always requires an explicit
// scheme.
type Config struct {
	// Store URI of the artifact store.
	Store string `yaml:"store"`

	// Index URI of the metadata index. Empty defaults to
	// sqlite at <storeDir>/index.db when Store is a file://
	// URI; for any other store scheme the Index must be set
	// explicitly.
	Index string `yaml:"index"`

	// PassphraseFile points at a file holding the store's
	// encryption passphrase. Empty means unencrypted store
	// (Plain DEK).
	PassphraseFile string `yaml:"passphraseFile"`

	// Namespace constrains writes/visibility to a single
	// namespace. Empty = global.
	Namespace string `yaml:"namespace"`

	// ScratchDir is the staging directory for in-flight
	// writes. Empty = <storeDir>/.scratch when the store is
	// local; required for non-local stores.
	ScratchDir string `yaml:"scratchDir"`

	// ScratchQuota is the maximum bytes the scratch directory
	// may hold across all in-flight writes. 0 = unlimited.
	ScratchQuota int64 `yaml:"scratchQuota"`

	// ReadOnly disables writes through the FSOps facade.
	// Stores opened read-only never publish events for
	// modifications (there are none to publish).
	ReadOnly bool `yaml:"readOnly"`

	// Editing controls draft/transit semantics. Values:
	//   "off"    — reject all in-place edits (strict CAS).
	//   "on"     — allow all editing operations.
	//   "custom" — consult AllowRename/AllowSetattr/...
	//              individually. Each flag defaults to false
	//              (a nil pointer is treated as off).
	// Empty string is treated as "off".
	Editing string `yaml:"editing"`

	// Custom editing flags. Only consulted when Editing is
	// "custom"; unused otherwise. Pointers so the YAML
	// loader can distinguish "unset" from "explicit false"
	// when assembling a policy from layered sources (file +
	// env + CLI).
	AllowRename   *bool `yaml:"allowRename,omitempty"`
	AllowSetattr  *bool `yaml:"allowSetattr,omitempty"`
	AllowTruncate *bool `yaml:"allowTruncate,omitempty"`
	AllowAppend   *bool `yaml:"allowAppend,omitempty"`

	// DefaultMode/UID/GID fill in fsmeta defaults for
	// artifacts written without explicit POSIX bits.
	DefaultMode uint32 `yaml:"defaultMode"`
	DefaultUID  uint32 `yaml:"defaultUid"`
	DefaultGID  uint32 `yaml:"defaultGid"`

	// Routing — service prefix and which trees are visible.
	ServicePrefix   string              `yaml:"servicePrefix"`
	RootView        projection.RootView `yaml:"rootView"`
	ByPathFallback  string              `yaml:"byPathFallback"` // "orphaned" | "drop"
	ShowStats       bool                `yaml:"showStats"`
	ShowByArtifact  bool                `yaml:"showByArtifact"`
	ShowOrphaned    bool                `yaml:"showOrphaned"`
	ShowByDate      bool                `yaml:"showByDate"`
	ShowBySession   bool                `yaml:"showBySession"`
	ShowByNamespace bool                `yaml:"showByNamespace"`
	ShowRaw         bool                `yaml:"showRaw"`
}

// DefaultConfig returns a Config with the recommended
// defaults: Plain-DEK store, ServicePrefix "_scrinium",
// service trees enabled, root view = byPath, fallback =
// orphaned, editing off, default mode 0644.
//
// Callers customise from here rather than building from
// scratch — the field set keeps growing and zero-valued
// configs accumulate footguns.
func DefaultConfig() Config {
	return Config{
		Editing:         "off",
		DefaultMode:     0o644,
		DefaultUID:      uint32(os.Getuid()),
		DefaultGID:      uint32(os.Getgid()),
		ServicePrefix:   "_scrinium",
		RootView:        projection.RootByPath,
		ByPathFallback:  "orphaned",
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         true,
	}
}

// Validate reports configuration mistakes that would otherwise
// surface at Open time as confusing wrappers around lower-level
// errors. Lightweight checks only — full validation happens
// inside Open against the actual filesystem and index.
func (c Config) Validate() error {
	var errs []string

	if c.Store == "" {
		errs = append(errs, "store: empty (e.g. file:///path/to/store)")
	}

	switch c.Editing {
	case "", "off", "on", "custom":
		// OK
	default:
		errs = append(errs, fmt.Sprintf("editing: %q is not one of {off, on, custom}", c.Editing))
	}

	switch c.ByPathFallback {
	case "", "orphaned", "synthetic":
		// OK
	default:
		errs = append(errs, fmt.Sprintf("byPathFallback: %q is not one of {orphaned, synthetic}", c.ByPathFallback))
	}

	switch c.RootView {
	case "", projection.RootByPath, projection.RootByDate,
		projection.RootBySession, projection.RootByNamespace,
		projection.RootByArtifact, projection.RootByOrphaned:
		// OK
	default:
		errs = append(errs, fmt.Sprintf("rootView: %q is not a known tree", c.RootView))
	}

	if c.ScratchQuota < 0 {
		errs = append(errs, "scratchQuota: negative")
	}

	if len(errs) > 0 {
		return errors.New("daemon config: " + strings.Join(errs, "; "))
	}
	return nil
}

// editingPolicy returns the projection-level policy derived
// from the string field. Centralised here so the cmd packages
// don't each duplicate the mapping.
//
// "custom" inspects the per-operation pointer flags; a nil
// pointer is read as false.
func (c Config) editingPolicy() projection.EditingPolicy {
	switch c.Editing {
	case "on":
		return projection.EditingOn()
	case "custom":
		return projection.EditingPolicy{
			AllowRename:   ptrBool(c.AllowRename),
			AllowSetattr:  ptrBool(c.AllowSetattr),
			AllowTruncate: ptrBool(c.AllowTruncate),
			AllowAppend:   ptrBool(c.AllowAppend),
		}
	default:
		return projection.EditingOff()
	}
}

// ptrBool dereferences a *bool, treating nil as false.
func ptrBool(p *bool) bool { return p != nil && *p }

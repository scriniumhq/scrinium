package assembly

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"scrinium.dev/internal/secretref"
)

// Config is the typed in-memory form a composer YAML/JSON document
// parses into, before defaults and validation. It is exported for
// parsers, tests, and Explain, but is NOT the recommended programmatic
// assembly path (ADR-52: declarative via YAML/JSON, programmatic via
// engine primitives). Its shape may change between minor versions.
//
// Exactly one of Store (single) or Stores (multi) must be set. With
// Stores, Multistore is required. See 3. Reference/10 Declarative Configuration.md.
type Config struct {
	Store      *StoreSpec            `yaml:"store,omitempty" json:"store,omitempty"`
	Stores     map[string]*StoreSpec `yaml:"stores,omitempty" json:"stores,omitempty"`
	Multistore *MultistoreSpec       `yaml:"multistore,omitempty" json:"multistore,omitempty"`
	Policies   map[string]*Policy    `yaml:"policies,omitempty" json:"policies,omitempty"`
	Projection *Projection           `yaml:"projection,omitempty" json:"projection,omitempty"`
	Agents     []ComponentSpec       `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// Projection holds the read/write-surface defaults the runtime applies
// when assembling its FSOps/View. Hybrid model (ADR-52 follow-up):
// these are the shared defaults; an individual surface may override
// any field in its own config block. Omitting the whole section
// leaves engine defaults in place (editing off, root view by path).
type Projection struct {
	// RootView selects the default tree presented at the root
	// (byPath, byDate, bySession, byNamespace, byArtifact, byOrphaned).
	RootView string `yaml:"rootView,omitempty" json:"rootView,omitempty"`
	// ByPathFallback is what the byPath tree does with manifests that
	// carry no path: "orphaned" or "synthetic".
	ByPathFallback string `yaml:"byPathFallback,omitempty" json:"byPathFallback,omitempty"`

	// Editing controls in-place edits: "off" (strict CAS), "on", or
	// "custom" (consult the Allow* flags). Empty = off.
	Editing       string `yaml:"editing,omitempty" json:"editing,omitempty"`
	AllowRename   *bool  `yaml:"allowRename,omitempty" json:"allowRename,omitempty"`
	AllowSetattr  *bool  `yaml:"allowSetattr,omitempty" json:"allowSetattr,omitempty"`
	AllowTruncate *bool  `yaml:"allowTruncate,omitempty" json:"allowTruncate,omitempty"`
	AllowAppend   *bool  `yaml:"allowAppend,omitempty" json:"allowAppend,omitempty"`

	// Namespace constrains writes/visibility to a single namespace.
	// Empty = global.
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`

	// ScratchDir / ScratchQuota govern the staging area for in-flight
	// FSOps writes. Empty dir defaults under a local store; 0 quota is
	// unlimited.
	ScratchDir   string `yaml:"scratchDir,omitempty" json:"scratchDir,omitempty"`
	ScratchQuota Size   `yaml:"scratchQuota,omitempty" json:"scratchQuota,omitempty"`

	// ReadOnly disables writes through FSOps.
	ReadOnly bool `yaml:"readOnly,omitempty" json:"readOnly,omitempty"`

	// Default POSIX bits for artifacts written without explicit ones.
	DefaultMode uint32 `yaml:"defaultMode,omitempty" json:"defaultMode,omitempty"`
	DefaultUID  uint32 `yaml:"defaultUid,omitempty" json:"defaultUid,omitempty"`
	DefaultGID  uint32 `yaml:"defaultGid,omitempty" json:"defaultGid,omitempty"`
}

// StoreSpec is one store: a backend URI, optional credentials, and a
// policy (inline or by reference — never both).
type StoreSpec struct {
	Driver      string      `yaml:"driver" json:"driver"`
	Index       string      `yaml:"index,omitempty" json:"index,omitempty"`
	Credentials Credentials `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	Policy      *Policy     `yaml:"policy,omitempty" json:"policy,omitempty"`
	PolicyRef   string      `yaml:"policyRef,omitempty" json:"policyRef,omitempty"`
}

// Credentials are driver/index-specific secret references, keyed by
// name (accessKeyId, secretAccessKey, sessionToken, …). Each value is
// a SecretRef resolved at load time.
type Credentials map[string]secretref.Ref

// Policy is the set of behaviours applied to a store.
type Policy struct {
	Encryption          *Encryption     `yaml:"encryption,omitempty" json:"encryption,omitempty"`
	Chunking            *Chunking       `yaml:"chunking,omitempty" json:"chunking,omitempty"`
	Bundling            *Bundling       `yaml:"bundling,omitempty" json:"bundling,omitempty"`
	Pipeline            []PipelineStage `yaml:"pipeline,omitempty" json:"pipeline,omitempty"`
	PipelineExtra       []PipelineStage `yaml:"pipelineExtra,omitempty" json:"pipelineExtra,omitempty"`
	DeletionPolicy      string          `yaml:"deletionPolicy,omitempty" json:"deletionPolicy,omitempty"`
	DefaultPutNamespace string          `yaml:"defaultPutNamespace,omitempty" json:"defaultPutNamespace,omitempty"`
	Retention           Duration        `yaml:"retention,omitempty" json:"retention,omitempty"`
	MaxArtifactSize     Size            `yaml:"maxArtifactSize,omitempty" json:"maxArtifactSize,omitempty"`
	GC                  *Schedule       `yaml:"gc,omitempty" json:"gc,omitempty"`
	Scrub               *ScrubSchedule  `yaml:"scrub,omitempty" json:"scrub,omitempty"`
}

// Encryption enables manifest+blob encryption. Passphrase is required
// when present; Mode defaults to "sealed".
type Encryption struct {
	Passphrase secretref.Ref `yaml:"passphrase" json:"passphrase"`
	Mode       string        `yaml:"mode,omitempty" json:"mode,omitempty"` // sealed | paranoid
	// Dedup selects encrypted-blob dedup: "disabled" (default) or
	// "convergent" (ADR-58). Optional.
	Dedup string `yaml:"dedup,omitempty" json:"dedup,omitempty"`
	// SegmentSize is the segmented-AEAD plaintext segment size
	// (ADR-59). Optional; engine default applied when zero.
	SegmentSize Size `yaml:"segmentSize,omitempty" json:"segmentSize,omitempty"`
}

// Chunking enables content-defined chunking of large artifacts.
type Chunking struct {
	MaxSize              Size `yaml:"maxSize,omitempty" json:"maxSize,omitempty"`
	DirectWriteThreshold Size `yaml:"directWriteThreshold,omitempty" json:"directWriteThreshold,omitempty"`
}

// Bundling enables packing of small artifacts into .pack volumes.
type Bundling struct {
	MaxBundleSize        Size     `yaml:"maxBundleSize,omitempty" json:"maxBundleSize,omitempty"`
	FlushInterval        Duration `yaml:"flushInterval,omitempty" json:"flushInterval,omitempty"`
	DirectWriteThreshold Size     `yaml:"directWriteThreshold,omitempty" json:"directWriteThreshold,omitempty"`
}

// MultistoreSpec wires several named stores together.
type MultistoreSpec struct {
	Routing     map[string]string   `yaml:"routing,omitempty" json:"routing,omitempty"`
	Replication map[string][]string `yaml:"replication,omitempty" json:"replication,omitempty"`
}

// Schedule is a cron schedule (GC).
type Schedule struct {
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
}

// ScrubSchedule is a cron schedule plus per-stage verification (Scrub).
type ScrubSchedule struct {
	Schedule             string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	PerStageVerification bool   `yaml:"perStageVerification,omitempty" json:"perStageVerification,omitempty"`
}

// ComponentSpec is a generic kind+config block, used for user
// agents. Config is left as a raw map for the registered factory
// to decode into its own typed struct.
type ComponentSpec struct {
	Kind   string         `yaml:"kind" json:"kind"`
	Config map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

// PipelineStage is one entry of an explicit pipeline. In YAML it is
// either a bare string ("hash") or a single-key map carrying the
// stage's params ("compress: {algo: zstd, level: 3}").
type PipelineStage struct {
	Kind   string
	Params map[string]any
}

// UnmarshalYAML accepts a scalar (kind only) or a single-key mapping
// (kind + params).
func (p *PipelineStage) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		p.Kind = node.Value
		return nil
	case yaml.MappingNode:
		if len(node.Content) != 2 {
			return fmt.Errorf("pipeline stage: want a single-key mapping, got %d keys", len(node.Content)/2)
		}
		p.Kind = node.Content[0].Value
		var params map[string]any
		if err := node.Content[1].Decode(&params); err != nil {
			return fmt.Errorf("pipeline stage %q params: %w", p.Kind, err)
		}
		p.Params = params
		return nil
	default:
		return fmt.Errorf("pipeline stage: unexpected YAML node kind %d", node.Kind)
	}
}

// UnmarshalJSON accepts a string ("hash") or a single-key object
// ({"compress": {...}}).
func (p *PipelineStage) UnmarshalJSON(data []byte) error {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		p.Kind = s
		return nil
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("pipeline stage: %w", err)
	}
	if len(m) != 1 {
		return fmt.Errorf("pipeline stage: want a single-key object, got %d keys", len(m))
	}
	for k, v := range m {
		p.Kind, p.Params = k, v
	}
	return nil
}

// --- Size: byte sizes like "64MB", "16KiB", "1048576" ---

// Size is a byte count parsed from a human string. Suffixes: B, KB/MB/GB/TB
// (decimal, ×1000) and KiB/MiB/GiB/TiB (binary, ×1024). A bare number
// is bytes. Zero means "unset / use default".
type Size int64

func (s Size) Int64() int64 { return int64(s) }

func (s *Size) UnmarshalYAML(node *yaml.Node) error {
	return s.parse(node.Value)
}

func (s *Size) UnmarshalJSON(data []byte) error {
	str := strings.Trim(strings.TrimSpace(string(data)), `"`)
	return s.parse(str)
}

func (s *Size) parse(in string) error {
	in = strings.TrimSpace(in)
	if in == "" {
		*s = 0
		return nil
	}
	// Pure number → bytes.
	if n, err := strconv.ParseInt(in, 10, 64); err == nil {
		*s = Size(n)
		return nil
	}
	upper := strings.ToUpper(in)
	type unit struct {
		suffix string
		mult   int64
	}
	// Longest suffixes first so "KIB" is matched before "KB"/"B".
	units := []unit{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"TB", 1000 * 1000 * 1000 * 1000},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			num := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return fmt.Errorf("size %q: %w", in, err)
			}
			*s = Size(int64(f * float64(u.mult)))
			return nil
		}
	}
	return fmt.Errorf("size %q: unrecognised unit", in)
}

// --- Duration: time spans like "5m", "90d", "7y" ---

// Duration extends time.Duration's grammar with day (d) and year (y)
// suffixes, common in retention windows. Zero means "unset".
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	return d.parse(node.Value)
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	str := strings.Trim(strings.TrimSpace(string(data)), `"`)
	return d.parse(str)
}

func (d *Duration) parse(in string) error {
	in = strings.TrimSpace(in)
	if in == "" {
		*d = 0
		return nil
	}
	const (
		day  = 24 * time.Hour
		year = 365 * day
	)
	if strings.HasSuffix(in, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(in, "d"), 64)
		if err != nil {
			return fmt.Errorf("duration %q: %w", in, err)
		}
		*d = Duration(time.Duration(n * float64(day)))
		return nil
	}
	if strings.HasSuffix(in, "y") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(in, "y"), 64)
		if err != nil {
			return fmt.Errorf("duration %q: %w", in, err)
		}
		*d = Duration(time.Duration(n * float64(year)))
		return nil
	}
	std, err := time.ParseDuration(in)
	if err != nil {
		return fmt.Errorf("duration %q: %w", in, err)
	}
	*d = Duration(std)
	return nil
}

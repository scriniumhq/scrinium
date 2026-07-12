package config

import "fmt"

// Default sizes/schedules for declarative configuration.
const (
	defaultChunkMaxSize   = Size(64 << 20) // 64 MiB
	defaultBundleMaxSize  = Size(64 << 20) // 64 MiB
	defaultBundleFlush    = Duration(5 * 60 * 1e9)
	defaultEncryptionMode = "sealed"
	defaultEncryptedDedup = "disabled"
	// Exported: the assembly reads these when materializing the
	// built-in maintenance schedules (ADR-110 config axis lives here).
	DefaultGCEvery         = Duration(24 * 60 * 60 * 1e9)     // 24h
	DefaultScrubEvery      = Duration(7 * 24 * 60 * 60 * 1e9) // 168h (weekly)
	DefaultCheckpointEvery = Duration(24 * 60 * 60 * 1e9)     // 24h
)

// applyDefaults fills in the optional fields the spec defaults, in
// place, after parsing and policyRef resolution. It does not validate
// (see validate); it assumes the document already passed structural
// checks performed there.
func applyDefaults(c *Config) {
	for _, s := range c.allStores() {
		applyPolicyDefaults(s.Policy)
	}
	applyProjectionDefaults(c.Projection)
}

// applyProjectionDefaults fills the shared projection defaults when the
// section is present. Absence leaves the whole thing to engine
// defaults at build time. Kept deterministic (no os.Getuid here) so
// the result is reproducible and testable; the assembler fills
// uid/gid/scratch-path defaults that depend on the environment.
func applyProjectionDefaults(p *Projection) {
	if p == nil {
		return
	}
	if p.Editing == "" {
		p.Editing = "off"
	}
	if p.ByPathFallback == "" {
		p.ByPathFallback = "orphaned"
	}
	if p.DefaultMode == 0 {
		p.DefaultMode = 0o644
	}
}

// applyPolicyDefaults defaults a single (already-resolved, inline)
// policy. nil policy means "all defaults / no features", left as-is.
func applyPolicyDefaults(p *Policy) {
	if p == nil {
		return
	}
	if p.Encryption != nil {
		if p.Encryption.Mode == "" {
			p.Encryption.Mode = defaultEncryptionMode
		}
		if p.Encryption.Dedup == "" {
			p.Encryption.Dedup = defaultEncryptedDedup
		}
	}
	if p.Chunking != nil {
		if p.Chunking.MaxSize == 0 {
			p.Chunking.MaxSize = defaultChunkMaxSize
		}
		if p.Chunking.DirectWriteThreshold == 0 {
			p.Chunking.DirectWriteThreshold = p.Chunking.MaxSize / 2
		}
	}
	if p.Bundling != nil {
		if p.Bundling.MaxBundleSize == 0 {
			p.Bundling.MaxBundleSize = defaultBundleMaxSize
		}
		if p.Bundling.FlushInterval == 0 {
			p.Bundling.FlushInterval = defaultBundleFlush
		}
		if p.Bundling.DirectWriteThreshold == 0 {
			p.Bundling.DirectWriteThreshold = p.Bundling.MaxBundleSize / 2
		}
	}
	// GC, Scrub and Checkpoint are technical hygiene; when a block is present
	// but no cadence is given, default to an interval (no cron adapter
	// needed). An explicit cron Schedule, if set, wins and is left as-is.
	// Absence of the block entirely is handled at agent-wiring time with
	// the same defaults.
	if p.GC != nil && p.GC.Every == 0 && p.GC.Schedule == "" {
		p.GC.Every = DefaultGCEvery
	}
	if p.Scrub != nil && p.Scrub.Every == 0 && p.Scrub.Schedule == "" {
		p.Scrub.Every = DefaultScrubEvery
	}
	if p.Checkpoint != nil && p.Checkpoint.Every == 0 && p.Checkpoint.Schedule == "" {
		p.Checkpoint.Every = DefaultCheckpointEvery
	}
}

// resolvePolicyRefs replaces every StoreSpec.PolicyRef with the
// referenced named policy, copied into Policy. Must run before
// applyDefaults and build. PolicyRef and inline Policy are mutually
// exclusive (validate enforces it); an unknown ref is an error.
func resolvePolicyRefs(c *Config) error {
	for name, s := range c.named() {
		if s.PolicyRef == "" {
			continue
		}
		p, ok := c.Policies[s.PolicyRef]
		if !ok {
			return fmt.Errorf("store %q: policyRef %q not found in policies", name, s.PolicyRef)
		}
		// Deep copy: policies are immutable templates, and defaults are
		// applied to each store's own copy. A shallow copy would alias
		// the template's nested pointers (Encryption, Chunking, …), so
		// defaulting or editing one store's policy would bleed into the
		// template and every other store that references it.
		s.Policy = clonePolicy(p)
		s.PolicyRef = ""
	}
	return nil
}

// clonePolicy returns an independent deep copy of p (nil-safe).
func clonePolicy(p *Policy) *Policy {
	if p == nil {
		return nil
	}
	cp := *p // copies value fields; pointer/slice fields cloned below.
	if p.Encryption != nil {
		e := *p.Encryption
		cp.Encryption = &e
	}
	if p.Chunking != nil {
		c := *p.Chunking
		cp.Chunking = &c
	}
	if p.Bundling != nil {
		b := *p.Bundling
		cp.Bundling = &b
	}
	if p.GC != nil {
		g := *p.GC
		cp.GC = &g
	}
	if p.Scrub != nil {
		s := *p.Scrub
		cp.Scrub = &s
	}
	if p.Checkpoint != nil {
		s := *p.Checkpoint
		cp.Checkpoint = &s
	}
	cp.Pipeline = clonePipeline(p.Pipeline)
	cp.PipelineExtra = clonePipeline(p.PipelineExtra)
	return &cp
}

// clonePipeline deep-copies a pipeline slice, including each stage's
// Params map (and any nested map/slice inside it), so the clone shares no
// reference with the source. nil in, nil out.
//
// The previous append-based copy duplicated the slice but left every
// stage's Params map aliased — editing one store's stage params would
// have bled into the template and every sibling store.
func clonePipeline(in []PipelineStage) []PipelineStage {
	if in == nil {
		return nil
	}
	out := make([]PipelineStage, len(in))
	for i, st := range in {
		st.Params = cloneParams(st.Params)
		out[i] = st
	}
	return out
}

// cloneParams deep-copies a stage's free-form params. Values are the
// shapes a YAML/JSON decode produces (scalars, map[string]any, []any);
// each map and slice is rebuilt so the clone shares none with the source.
func cloneParams(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneParams(t)
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = deepCopyValue(e)
		}
		return s
	default:
		return v
	}
}

// allStores returns every StoreSpec in the config (the single Store or
// each of Stores), for uniform iteration.
func (c *Config) allStores() []*StoreSpec {
	if c.Store != nil {
		return []*StoreSpec{c.Store}
	}
	out := make([]*StoreSpec, 0, len(c.Stores))
	for _, s := range c.Stores {
		out = append(out, s)
	}
	return out
}

// named returns the stores keyed by name. The single Store gets
// the implicit name "default" so error messages are uniform.
func (c *Config) named() map[string]*StoreSpec {
	if c.Store != nil {
		return map[string]*StoreSpec{"default": c.Store}
	}
	return c.Stores
}

// Normalize is the pre-build pipeline over a decoded Config: policy
// references resolved, then the defaults ladder applied. Call Validate
// afterwards. Exported for the assembly (and any other consumer of the
// declarative model).
func (c *Config) Normalize() error {
	if err := resolvePolicyRefs(c); err != nil {
		return err
	}
	applyDefaults(c)
	return nil
}

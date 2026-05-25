package assembly

import "fmt"

// Default sizes/schedules from 3. Reference/10 Composer.md §10.2.
const (
	defaultChunkMaxSize   = Size(64 << 20) // 64 MiB
	defaultBundleMaxSize  = Size(64 << 20) // 64 MiB
	defaultBundleFlush    = Duration(5 * 60 * 1e9)
	defaultEncryptionMode = "sealed"
	defaultEncryptedDedup = "disabled"
	defaultGCSchedule     = "0 3 * * *"
	defaultScrubSchedule  = "0 4 * * 0"
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
// the result is reproducible and testable; the runtime fills
// uid/gid/scratch-path defaults that depend on the environment.
func applyProjectionDefaults(p *Projection) {
	if p == nil {
		return
	}
	if p.Editing == "" {
		p.Editing = "off"
	}
	if p.RootView == "" {
		p.RootView = "by-path"
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
	// GC and Scrub run by default; fill schedules when the block is
	// present but the schedule omitted. Absence of the block entirely
	// is handled at agent-wiring time (M3) with the same defaults.
	if p.GC != nil && p.GC.Schedule == "" {
		p.GC.Schedule = defaultGCSchedule
	}
	if p.Scrub != nil && p.Scrub.Schedule == "" {
		p.Scrub.Schedule = defaultScrubSchedule
	}
}

// resolvePolicyRefs replaces every StoreSpec.PolicyRef with the
// referenced named policy, copied into Policy. Must run before
// applyDefaults and build. PolicyRef and inline Policy are mutually
// exclusive (validate enforces it); an unknown ref is an error.
func resolvePolicyRefs(c *Config) error {
	for name, s := range c.namedStores() {
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
	if p.Pipeline != nil {
		cp.Pipeline = append([]PipelineStage(nil), p.Pipeline...)
	}
	if p.PipelineExtra != nil {
		cp.PipelineExtra = append([]PipelineStage(nil), p.PipelineExtra...)
	}
	return &cp
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

// namedStores returns the stores keyed by name. The single Store gets
// the implicit name "default" so error messages are uniform.
func (c *Config) namedStores() map[string]*StoreSpec {
	if c.Store != nil {
		return map[string]*StoreSpec{"default": c.Store}
	}
	return c.Stores
}

package config

import (
	"errors"
	"fmt"
	"strings"
)

// validate performs structural and cross-component checks before
// build, so a config mistake surfaces as a clear message rather than a
// confusing failure deep inside the engine. It runs after policyRef
// resolution and defaulting.
// Validate checks a normalized Config: structural rules, schedule
// triggers, projection routing — and the policy blocks via the SAME
// vocabulary tables the mapping uses plus the engine validator
// (ValidateImmutable on the mapped StoreConfig), so the file-level
// check can never drift from the domain rules.
func (c *Config) Validate() error {
	return validateConfig(c)
}

func validateConfig(c *Config) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	switch {
	case c.Store == nil && len(c.Stores) == 0:
		add("no store: set either `store:` (single) or `stores:` (multi)")
	case c.Store != nil && len(c.Stores) > 0:
		add("both `store:` and `stores:` set; use exactly one")
	}

	// Multi-store requires a multistore block with routing.
	if len(c.Stores) > 0 {
		if c.Multistore == nil || len(c.Multistore.Routing) == 0 {
			add("`stores:` requires a `multistore.routing:` block")
		}
		for ns, target := range routingOf(c) {
			if _, ok := c.Stores[target]; !ok {
				add("multistore.routing[%q]: unknown store %q", ns, target)
			}
		}
		for ns, targets := range replicationOf(c) {
			for _, t := range targets {
				if _, ok := c.Stores[t]; !ok {
					add("multistore.replication[%q]: unknown store %q", ns, t)
				}
			}
		}
	} else if c.Multistore != nil {
		add("`multistore:` is set but there is only a single `store:`")
	}

	for name, s := range c.named() {
		if s == nil {
			add("store %q: empty", name)
			continue
		}
		if strings.TrimSpace(s.Driver) == "" {
			add("store %q: `driver:` is empty (e.g. file:///path)", name)
		}
		if s.PolicyRef != "" && s.Policy != nil {
			add("store %q: set either `policy:` or `policyRef:`, not both", name)
		}
		validatePolicy(name, s.Policy, add)
	}

	for i, ag := range c.Agents {
		if strings.TrimSpace(ag.Kind) == "" {
			add("agents[%d]: empty `kind:`", i)
		}
		validateTrigger(fmt.Sprintf("agents[%d]", i), ag.Every, ag.Schedule, add)
	}

	validateProjection(c.Projection, add)

	if len(errs) > 0 {
		return fmt.Errorf("scrinium config: %w", errors.Join(errs...))
	}
	return nil
}

func validateProjection(p *Projection, add func(string, ...any)) {
	if p == nil {
		return
	}
	switch p.Editing {
	case "", "off", "on", "custom":
	default:
		add("projection.editing %q is not one of {off, on, custom}", p.Editing)
	}
	// RootView is validated by the View at build time (it must name an
	// active root — intrinsic or extension-provided); assembly does not
	// enumerate the roots, so it does not pre-validate the name here.
	switch p.ByPathFallback {
	case "", "orphaned", "synthetic":
	default:
		add("projection.byPathFallback %q is not one of {orphaned, synthetic}", p.ByPathFallback)
	}
	if p.ScratchQuota < 0 {
		add("projection.scratchQuota is negative")
	}
}

func validatePolicy(store string, p *Policy, add func(string, ...any)) {
	if p == nil {
		return
	}
	if p.Encryption != nil {
		if p.Encryption.Passphrase.IsZero() {
			add("store %q: encryption set but `passphrase:` missing", store)
		}
		if _, ok := encryptionModeVocab[p.Encryption.Mode]; !ok && p.Encryption.Mode != "" {
			add("store %q: encryption.mode %q is not one of %s", store, p.Encryption.Mode, vocabWords(encryptionModeVocab))
		}
		if _, ok := dedupVocab[p.Encryption.Dedup]; !ok && p.Encryption.Dedup != "" {
			add("store %q: encryption.dedup %q is not one of %s", store, p.Encryption.Dedup, vocabWords(dedupVocab))
		}
	}
	if _, ok := deletionPolicyVocab[p.DeletionPolicy]; !ok && p.DeletionPolicy != "" {
		add("store %q: deletionPolicy %q is not one of %s", store, p.DeletionPolicy, vocabWords(deletionPolicyVocab))
	}
	// The words are in the dictionary — now let the engine validator
	// judge the mapped result (bounds like MinRetentionPeriod included),
	// so a file error surfaces at validation time with the store label,
	// not deep inside InitStore.
	if cfg, ok := StoreConfigFromPolicy(p); ok || p.DeletionPolicy != "" || p.Retention != 0 || p.MaxArtifactSize > 0 {
		if err := ValidateImmutable(ApplyDefaults(cfg)); err != nil {
			add("store %q: policy maps to an invalid StoreConfig: %v", store, err)
		}
	}
	if p.GC != nil {
		validateTrigger(fmt.Sprintf("store %q gc", store), p.GC.Every, p.GC.Schedule, add)
	}
	if p.Scrub != nil {
		validateTrigger(fmt.Sprintf("store %q scrub", store), p.Scrub.Every, p.Scrub.Schedule, add)
	}
	if p.Checkpoint != nil {
		validateTrigger(fmt.Sprintf("store %q checkpoint", store), p.Checkpoint.Every, p.Checkpoint.Schedule, add)
	}
}

// validateTrigger enforces the one-trigger rule shared by built-in
// maintenance schedules and user agents: an interval (Every) and a cron
// expression (Schedule) are mutually exclusive, and an interval, if set,
// must be positive.
func validateTrigger(label string, every Duration, schedule string, add func(string, ...any)) {
	if every != 0 && schedule != "" {
		add("%s: set either `every:` or `schedule:`, not both", label)
	}
	if every < 0 {
		add("%s: `every:` must be positive", label)
	}
}

func routingOf(c *Config) map[string]string {
	if c.Multistore == nil {
		return nil
	}
	return c.Multistore.Routing
}

func replicationOf(c *Config) map[string][]string {
	if c.Multistore == nil {
		return nil
	}
	return c.Multistore.Replication
}

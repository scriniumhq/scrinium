package composer

import (
	"fmt"
	"strings"
)

// validate performs structural and cross-component checks before
// build, so a config mistake surfaces as a clear message rather than a
// confusing failure deep inside the engine. It runs after policyRef
// resolution and defaulting.
func validate(c *Config) error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

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

	for name, s := range c.namedStores() {
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
	}

	validateProjection(c.Projection, add)

	if len(errs) > 0 {
		return fmt.Errorf("composer config: %s", strings.Join(errs, "; "))
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
	switch p.RootView {
	case "", "by-path", "by-date", "by-session", "by-namespace", "by-artifact", "by-orphaned":
	default:
		add("projection.rootView %q is not a known tree", p.RootView)
	}
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
		switch p.Encryption.Mode {
		case "", "sealed", "paranoid":
		default:
			add("store %q: encryption.mode %q is not one of {sealed, paranoid}", store, p.Encryption.Mode)
		}
		switch p.Encryption.Dedup {
		case "", "disabled", "convergent":
		default:
			add("store %q: encryption.dedup %q is not one of {disabled, convergent}", store, p.Encryption.Dedup)
		}
	}
	switch p.DeletionPolicy {
	case "", "free", "retention", "noDelete":
	default:
		add("store %q: deletionPolicy %q is not one of {free, retention, noDelete}", store, p.DeletionPolicy)
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

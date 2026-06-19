package assembly

import (
	"fmt"
	"time"

	"scrinium.dev/engine/agent"
)

// standardSchedulerTick is how often the built-in scheduler goroutine
// (WithStandardScheduler) ticks the interval primitive. It bounds the
// latency between an agent becoming due and running, not the configured
// intervals themselves; 1s is ample for maintenance cadences.
const standardSchedulerTick = time.Second

// resolvedSchedule is a fully-resolved schedule for one kind: either an
// interval or a parsed cron Next, plus the config to build the agent with.
type resolvedSchedule struct {
	interval time.Duration
	next     func(time.Time) time.Time
	cfg      any
}

// resolveSchedules collects declared schedules into a kind->resolved map,
// replace-by-kind (later sources override earlier): config policy blocks,
// agents[] triggers, then the WithSchedule option. A cron expression
// requires the cron adapter (cron.Enable); without it, or on a parse error,
// resolveSchedules fails fast (§9.7).
func resolveSchedules(spec *StoreSpec, c *Config, aw agentWiring) (map[string]resolvedSchedule, error) {
	out := make(map[string]resolvedSchedule)
	set := func(kind string, every Duration, cron string, cfg any) error {
		rs := resolvedSchedule{cfg: cfg}
		switch {
		case cron != "":
			if aw.cronParser == nil {
				return fmt.Errorf("scrinium: agent %q declares a cron schedule %q but cron is not enabled (pass cron.Enable())", kind, cron)
			}
			next, err := aw.cronParser(cron)
			if err != nil {
				return fmt.Errorf("scrinium: agent %q cron schedule %q: %w", kind, cron, err)
			}
			rs.next = next
		case every > 0:
			rs.interval = time.Duration(every)
		default:
			return nil // no trigger declared
		}
		out[kind] = rs // replace-by-kind
		return nil
	}

	// Config policy blocks (gc/scrub/checkpoint). applyDefaults has filled the
	// cadence of a present block, so each present block carries a trigger.
	if spec != nil && spec.Policy != nil {
		p := spec.Policy
		if p.GC != nil {
			if err := set("gc", p.GC.Every, p.GC.Schedule, agentCfg(aw, "gc")); err != nil {
				return nil, err
			}
		}
		if p.Scrub != nil {
			if err := set("scrub", p.Scrub.Every, p.Scrub.Schedule, agentCfg(aw, "scrub")); err != nil {
				return nil, err
			}
		}
		if p.Checkpoint != nil {
			if err := set("checkpoint", p.Checkpoint.Every, p.Checkpoint.Schedule, agentCfg(aw, "checkpoint")); err != nil {
				return nil, err
			}
		}
	}

	// Config agents[] triggers. A WithAgentConfig override wins over the
	// inline config map.
	for _, ag := range c.Agents {
		if ag.Every == 0 && ag.Schedule == "" {
			continue
		}
		cfg := agentCfg(aw, ag.Kind)
		if cfg == nil && len(ag.Config) > 0 {
			cfg = ag.Config
		}
		if err := set(ag.Kind, ag.Every, ag.Schedule, cfg); err != nil {
			return nil, err
		}
	}

	// WithSchedule options override. expr is an interval (time.ParseDuration)
	// or, failing that, a cron expression.
	for kind, expr := range aw.schedules {
		if d, derr := time.ParseDuration(expr); derr == nil {
			if err := set(kind, Duration(d), "", agentCfg(aw, kind)); err != nil {
				return nil, err
			}
		} else if err := set(kind, 0, expr, agentCfg(aw, kind)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addHygieneDefaults adds the built-in maintenance schedules (gc/scrub/
// checkpoint) to an active scheduler's set, but only for kinds that are
// registered and not already scheduled. Technical hygiene runs whenever the
// scheduler is active (§10.2.9); a built-in that was not blank-imported (so
// not registered) is skipped rather than failing the build.
func addHygieneDefaults(out map[string]resolvedSchedule, aw agentWiring) {
	defaults := []struct {
		kind  string
		every Duration
	}{
		{"gc", defaultGCEvery},
		{"scrub", defaultScrubEvery},
		{"checkpoint", defaultCheckpointEvery},
	}
	for _, d := range defaults {
		if _, ok := out[d.kind]; ok {
			continue
		}
		if _, registered := agent.Lookup(d.kind); !registered {
			continue
		}
		out[d.kind] = resolvedSchedule{interval: time.Duration(d.every), cfg: agentCfg(aw, d.kind)}
	}
}

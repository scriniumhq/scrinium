package assembly

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
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
func resolveSchedules(spec *StoreSpec, c *Config, opts *Options) (map[string]resolvedSchedule, error) {
	out := make(map[string]resolvedSchedule)
	set := func(kind string, every Duration, cron string, cfg any) error {
		rs := resolvedSchedule{cfg: cfg}
		switch {
		case cron != "":
			if opts.cronParser == nil {
				return fmt.Errorf("scrinium: agent %q declares a cron schedule %q but cron is not enabled (pass cron.Enable())", kind, cron)
			}
			next, err := opts.cronParser(cron)
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
			if err := set("gc", p.GC.Every, p.GC.Schedule, agentCfg(opts, "gc")); err != nil {
				return nil, err
			}
		}
		if p.Scrub != nil {
			if err := set("scrub", p.Scrub.Every, p.Scrub.Schedule, agentCfg(opts, "scrub")); err != nil {
				return nil, err
			}
		}
		if p.Checkpoint != nil {
			if err := set("checkpoint", p.Checkpoint.Every, p.Checkpoint.Schedule, agentCfg(opts, "checkpoint")); err != nil {
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
		cfg := agentCfg(opts, ag.Kind)
		if cfg == nil && len(ag.Config) > 0 {
			cfg = ag.Config
		}
		if err := set(ag.Kind, ag.Every, ag.Schedule, cfg); err != nil {
			return nil, err
		}
	}

	// WithSchedule options override. expr is an interval (time.ParseDuration)
	// or, failing that, a cron expression.
	for kind, expr := range opts.schedules {
		if d, derr := time.ParseDuration(expr); derr == nil {
			if err := set(kind, Duration(d), "", agentCfg(opts, kind)); err != nil {
				return nil, err
			}
		} else if err := set(kind, 0, expr, agentCfg(opts, kind)); err != nil {
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
func addHygieneDefaults(out map[string]resolvedSchedule, opts *Options) {
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
		out[d.kind] = resolvedSchedule{interval: time.Duration(d.every), cfg: agentCfg(opts, d.kind)}
	}
}

// buildScheduler validates configured and extension agent kinds against the
// registry, assembles the agent deps, then resolves schedules (config
// blocks, agents[] triggers, WithSchedule, and scheduled extension agents)
// with replace-by-kind layering. The scheduler is active only when
// WithStandardScheduler was passed or any schedule was declared; when
// active it adds the hygiene defaults, registers every schedule, and starts
// the tick goroutine (registering its stop in cleanups). Inactive leaves
// sched nil and stopTicker a no-op.
func (bs *buildState) buildScheduler() error {
	for _, ag := range bs.c.Agents {
		if _, ok := agent.Lookup(ag.Kind); !ok {
			return fmt.Errorf("scrinium: no agent registered for kind %q (blank-import the agent package, as with drivers)", ag.Kind)
		}
	}
	bs.agentDeps = agent.AgentDeps{
		Publisher: bs.bus,
		Driver:    bs.drv,
		Index:     bs.idx,
		HostID:    uuid.NewString(), // per-process actor id for lease/takeover events
		// StoreID is left empty until the store exposes its descriptor id;
		// it only tags agent events (diagnostics), not lease identity.
	}

	scheds, serr := resolveSchedules(bs.spec, bs.c, bs.opts)
	if serr != nil {
		return serr
	}

	// Paired agents from extensions: the kind must be registered; a declared
	// schedule (interval or cron) joins the set (replace-by-kind) and
	// activates the scheduler. Without a schedule the kind is available via
	// RunMaintenance only.
	for _, ea := range bs.extAgents {
		if _, ok := agent.Lookup(ea.Kind); !ok {
			return fmt.Errorf("scrinium: extension agent %q not registered (blank-import its package, as with drivers)", ea.Kind)
		}
		if ea.Schedule == "" {
			continue
		}
		rs := resolvedSchedule{cfg: ea.Config}
		if d, derr := time.ParseDuration(ea.Schedule); derr == nil {
			rs.interval = d
		} else if bs.opts.cronParser == nil {
			return fmt.Errorf("scrinium: extension agent %q has cron schedule %q but cron is not enabled (cron.Enable())", ea.Kind, ea.Schedule)
		} else {
			next, cerr := bs.opts.cronParser(ea.Schedule)
			if cerr != nil {
				return fmt.Errorf("scrinium: extension agent %q schedule %q: %w", ea.Kind, ea.Schedule, cerr)
			}
			rs.next = next
		}
		scheds[ea.Kind] = rs
	}

	schedActive := bs.opts.standardScheduler || len(scheds) > 0
	if !schedActive {
		return nil
	}
	addHygieneDefaults(scheds, bs.opts)

	s, nerr := agent.NewScheduler(bs.st, bs.agentDeps)
	if nerr != nil {
		return fmt.Errorf("scrinium: scheduler: %w", nerr)
	}
	sched := s
	bs.sched = s
	for kind, rs := range scheds {
		if aerr := sched.Add(agent.Schedule{
			Agent:    kind,
			Interval: rs.interval,
			Next:     rs.next,
			Config:   rs.cfg,
		}); aerr != nil {
			return fmt.Errorf("scrinium: schedule %q: %w", kind, aerr)
		}
	}
	done := make(chan struct{})
	var once sync.Once
	stopTicker := func() { once.Do(func() { close(done) }) }
	bs.stopTicker = stopTicker
	go func() {
		tk := time.NewTicker(standardSchedulerTick)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-tk.C:
				_ = sched.Tick(now)
			}
		}
	}()
	bs.cleanups = append(bs.cleanups, func() {
		stopTicker()
		_ = sched.Stop(context.Background())
	})
	return nil
}

// agentCfg returns the WithAgentConfig override for kind, or nil.
func agentCfg(opts *Options, kind string) any {
	if opts.agentConfigs == nil {
		return nil
	}
	return opts.agentConfigs[kind]
}

package assembly

import (
	"context"

	"scrinium.dev/engine/agent"
	"scrinium.dev/event"
)

// Mode selects what Build does when the described store does or does
// not already exist.
type Mode int

const (
	// ModeOpenOrInit opens the store, creating it if absent. The
	// forgiving default — good for "just give me a working store".
	ModeOpenOrInit Mode = iota
	// ModeOpen opens an existing store and fails if it is absent.
	ModeOpen
	// ModeInit creates a fresh store and fails if one already exists.
	ModeInit
)

func (m Mode) internal() buildMode {
	switch m {
	case ModeOpen:
		return modeOpen
	case ModeInit:
		return modeInit
	default:
		return modeOpenOrInit
	}
}

// Build assembles a store from a programmatically-constructed Config,
// returning a live assembly.Assembly. It is the direct counterpart to
// the Load* functions: those parse bytes into a Config and call Build;
// callers holding a Config in hand call Build without round-tripping
// through YAML.
//
// Build applies defaults and validates before assembling, so a
// half-filled Config (just a Store driver, say) becomes a fully
// defaulted store. The mode defaults to ModeOpenOrInit; pass
// WithMode to change it.
//
//	asm, err := Build(ctx, Config{
//	    Store: &StoreSpec{Driver: "file:///data/app"},
//	})
//
// The Config is treated as owned by Build for the duration of the call
// (defaults are applied in place); do not mutate it concurrently.
func Build(ctx context.Context, cfg Config, opts ...BuildOption) (Assembly, error) {
	o := Options{mode: ModeOpenOrInit}
	for _, opt := range opts {
		opt(&o)
	}
	if err := prepare(&cfg); err != nil {
		return nil, err
	}
	return build(ctx, &cfg, o.mode.internal(), agentWiring{
		handlers:   o.eventHandlers,
		stdSched:   o.standardScheduler,
		cronParser: o.cronParser,
	})
}

// BuildOption tunes a Build call.
type BuildOption func(*Options)

// Options is the opaque set of build-time options a BuildOption mutates.
// The type is exported so feature packages in subtrees (e.g. the cron
// adapter) can define their own BuildOption, but its fields are private:
// applications use the With* helpers, and feature adapters use the
// exported setter SPI (e.g. SetCronParser). This keeps option invariants
// owned by the assembler.
type Options struct {
	mode              Mode
	eventHandlers     []func(event.Event)
	standardScheduler bool
	cronParser        agent.CronParser
}

// SetCronParser installs the cron expression parser used by ScheduleCron.
// It is the SPI for cron adapter packages (scrinium.dev/engine/agent/cron
// calls it from the option it exports); applications do not call it
// directly.
func (o *Options) SetCronParser(p agent.CronParser) { o.cronParser = p }

// WithMode sets the open/init behaviour (default ModeOpenOrInit).
func WithMode(m Mode) BuildOption {
	return func(o *Options) { o.mode = m }
}

// WithEventHandler registers an event handler before assembly begins, so
// it observes events emitted during Build/Init as well as every later
// store and agent event. Pass it more than once to register several. For
// subscriptions added after the client exists, use the client's Subscribe
// (which returns an unsubscribe). A nil handler is ignored.
func WithEventHandler(fn func(event.Event)) BuildOption {
	return func(o *Options) {
		if fn != nil {
			o.eventHandlers = append(o.eventHandlers, fn)
		}
	}
}

// WithStandardScheduler runs the built-in scheduler (ADR-69 level 2): one
// goroutine ticks the interval-based primitive on real time and runs due
// agents, and it is stopped on Close. Without it there is no resident
// goroutine (level 1): agents run only when the host calls RunMaintenance.
// Schedules are added with the client's ScheduleEvery/ScheduleCron. A host
// that wants to own the clock itself drives agent.Scheduler directly on the
// primitives, not through the client (level 3).
func WithStandardScheduler() BuildOption {
	return func(o *Options) { o.standardScheduler = true }
}

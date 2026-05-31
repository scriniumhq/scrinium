package assembly

import (
	"context"
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
	o := buildOptions{mode: ModeOpenOrInit}
	for _, opt := range opts {
		opt(&o)
	}
	if err := prepare(&cfg); err != nil {
		return nil, err
	}
	return build(ctx, &cfg, o.mode.internal())
}

// BuildOption tunes a Build call.
type BuildOption func(*buildOptions)

type buildOptions struct {
	mode Mode
}

// WithMode sets the open/init behaviour (default ModeOpenOrInit).
func WithMode(m Mode) BuildOption {
	return func(o *buildOptions) { o.mode = m }
}

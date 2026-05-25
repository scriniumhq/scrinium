package driver

// PutOption configures a single Driver.Put call. Functional options
// keep the Driver.Put signature stable as the option set grows and
// let existing callers pass no options at all (the zero PutConfig is
// the historical unconditional-overwrite behaviour). See ADR-26 and
// 3. Reference/02 Driver.md.
type PutOption func(*PutConfig)

// PutConfig is the resolved set of per-call Put options. A Driver
// builds it once at the top of Put via NewPutConfig and then honours
// the fields it supports; a Driver that cannot satisfy an option
// (for example a backend without an atomic create-if-absent) must
// return an error rather than silently downgrading to an
// unconditional write.
type PutConfig struct {
	// Exclusive requests a create-if-absent write: the Put must
	// fail with errs.ErrAlreadyExists if path is already populated,
	// instead of overwriting it. LocalFS implements it with an
	// O_EXCL link; S3 with an If-None-Match: * precondition. Used
	// by the engine for concurrent writes into a shared Location
	// to avoid clobbering another writer's blob or resurrecting a
	// tombstone. The default (false) is the historical
	// unconditional overwrite.
	Exclusive bool
}

// NewPutConfig folds opts onto the default PutConfig. Drivers call it
// at the start of Put.
func NewPutConfig(opts ...PutOption) PutConfig {
	var c PutConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithExclusive marks a Put as exclusive: the write succeeds only if
// path does not already exist, otherwise it returns
// errs.ErrAlreadyExists. See PutConfig.Exclusive.
func WithExclusive() PutOption {
	return func(c *PutConfig) { c.Exclusive = true }
}

package store

type withoutIndexOption struct{}

func (withoutIndexOption) ApplySystemPut(c *SystemPutConfig) {
	c.SkipIndex = true
}

// WithoutIndex skips indexing the manifest — for artifacts whose presence
// in the index would be harmful, most notably index snapshots (a snapshot
// row cannot reference a manifest that exists only after the snapshot).
func WithoutIndex() SystemPutOption {
	return withoutIndexOption{}
}

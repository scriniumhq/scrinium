package pipeline

// registry.go — the default TransformerRegistry implementation, beside
// the TransformerFactory/TransformerRegistry contracts it implements.
//
// The concurrency-safe map mechanics are the shared internal/registry
// primitive; this type only adds the contract's shape (chainable
// Register, error-on-missing Get).

import (
	"scrinium.dev/errs"
	reg "scrinium.dev/internal/registry"
)

// NewTransformerRegistry creates an empty transformer registry. The
// host application registers factories through Register.
func NewTransformerRegistry() TransformerRegistry {
	return &transformerRegistry{m: reg.New[TransformerFactory]()}
}

// transformerRegistry implements TransformerRegistry over a
// registry.Map, which carries the lock + map so concurrent
// registration and reads stay safe.
type transformerRegistry struct {
	m *reg.Map[TransformerFactory]
}

var _ TransformerRegistry = (*transformerRegistry)(nil)

func (r *transformerRegistry) Get(id string) (TransformerFactory, error) {
	f, ok := r.m.Get(id)
	if !ok {
		return nil, errs.ErrUnsupportedAlgorithm
	}
	return f, nil
}

func (r *transformerRegistry) Register(id string, f TransformerFactory) TransformerRegistry {
	r.m.Set(id, f)
	return r
}

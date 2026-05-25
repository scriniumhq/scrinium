package pipeline

// registry.go — the default TransformerRegistry implementation,
// moved in from the former plugins grab-bag. It lives next to the
// TransformerFactory/TransformerRegistry contracts it implements.

import (
	"sync"

	"scrinium.dev/errs"
)

// NewTransformerRegistry creates an empty transformer registry. The
// host application registers factories through Register.
func NewTransformerRegistry() TransformerRegistry {
	return &transformerRegistry{factories: make(map[string]TransformerFactory)}
}

// transformerRegistry implements TransformerRegistry with an RWMutex
// so concurrent registration and reads stay safe. In production
// registration usually happens once (when wiring the stack), but the
// protection is cheaper than chasing flaky races in tests.
type transformerRegistry struct {
	mu        sync.RWMutex
	factories map[string]TransformerFactory
}

var _ TransformerRegistry = (*transformerRegistry)(nil)

func (r *transformerRegistry) Get(id string) (TransformerFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[id]
	if !ok {
		return nil, errs.ErrUnsupportedAlgorithm
	}
	return f, nil
}

func (r *transformerRegistry) Register(id string, f TransformerFactory) TransformerRegistry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[id] = f
	return r
}

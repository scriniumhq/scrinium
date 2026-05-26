package hashing

import (
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// registry implements domain.HashRegistry with an RWMutex so
// concurrent registration and reads stay safe. In production
// registration usually happens once (when wiring the stack), but the
// protection is cheaper than chasing flaky races in tests.
type registry struct {
	mu      sync.RWMutex
	hashers map[string]func() hash.Hash
}

var _ domain.HashRegistry = (*registry)(nil)

// NewHashRegistry creates an empty hash-algorithm registry. The host
// application registers factories through Register.
func NewHashRegistry() domain.HashRegistry {
	return &registry{hashers: make(map[string]func() hash.Hash)}
}

// Parse splits an "<algo>-<hex>" identifier into its algorithm name
// and raw bytes.
func (r *registry) Parse(h string) (algo string, raw []byte, err error) {
	dash := strings.IndexByte(h, '-')
	if dash <= 0 || dash == len(h)-1 {
		return "", nil, errors.New("hashing: invalid hash format, expected '<algo>-<hex>'")
	}
	algo = h[:dash]
	hexPart := h[dash+1:]
	raw, err = hex.DecodeString(hexPart)
	if err != nil {
		return "", nil, errors.New("hashing: invalid hash hex part: " + err.Error())
	}
	return algo, raw, nil
}

// NewHasher creates a fresh hash.Hash for the given algorithm.
func (r *registry) NewHasher(algo string) (hash.Hash, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.hashers[algo]
	if !ok {
		return nil, errs.ErrUnsupportedAlgorithm
	}
	return fn(), nil
}

// Format builds an "<algo>-<hex>" identifier from an algorithm name
// and raw digest bytes.
func (r *registry) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

// Register registers a hasher factory under an algorithm name and
// returns the registry for chaining.
func (r *registry) Register(algo string, fn func() hash.Hash) domain.HashRegistry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hashers[algo] = fn
	return r
}

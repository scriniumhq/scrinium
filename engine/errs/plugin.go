package errs

import "errors"

// Plugin registries (hashes, transformers, key resolvers): a lookup
// for a non-registered plugin id surfaces this sentinel. Used by
// HashRegistry.NewHasher and TransformerRegistry.Get.

// ErrUnsupportedAlgorithm — the algorithm has not been registered
// in the corresponding registry.
var ErrUnsupportedAlgorithm = errors.New("scrinium: unsupported algorithm")

// ErrInjected is the sentinel returned by the faulty test driver
// for a deliberately injected fault. Test code asserts
// errors.Is(err, ErrInjected) to confirm a fault landed.
//
// Lives in errs (rather than driver/faulty) so that a test outside
// the faulty package can match the sentinel without importing the
// driver subpackage just for that.
var ErrInjected = errors.New("scrinium: injected fault")

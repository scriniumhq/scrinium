// Package plugin contains plugin implementations for layer L2:
// concrete hashers, compressors, crypto plugins, and key resolvers.
//
// In M0 this package exists as an entry point in the DAG; concrete
// subpackages (plugin/compress/zstd, plugin/crypto/aesgcm,
// plugin/hash/sha256, etc.) appear in M2.1.
//
// DAG: plugin imports core (Encoder, Decoder, TransformerFactory,
// KeyResolver contracts).
package plugin

// placeholder keeps the package non-empty until the first real
// subpackage lands. Remove when actual code arrives in plugin/*.
const placeholder = "scrinium-plugin"

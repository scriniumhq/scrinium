package keyring

import (
	"golang.org/x/crypto/argon2"

	"scrinium.dev/store/internal/aead"
)

// kdfAlgorithm is the only KDF currently supported.
const kdfAlgorithm = "argon2id"

// deriveKEK computes the KEK from a passphrase via Argon2id. The
// output is exactly aead.DEKLen bytes (the AES-256 key size). The
// passphrase is not zeroed here — callers wipe their own buffers.
func deriveKEK(passphrase, salt []byte, time, memory uint32, threads uint8) []byte {
	return argon2.IDKey(passphrase, salt, time, memory, threads, aead.DEKLen)
}

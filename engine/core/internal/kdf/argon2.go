package kdf

import (
	"golang.org/x/crypto/argon2"
)

// Derive computes the KEK from a passphrase using Argon2id with
// the supplied parameters. The output is exactly KEKLen (32) bytes.
//
// This is the algorithmic kernel: it takes raw inputs and runs
// the cipher. Higher layers are responsible for assembling these
// inputs from the client-supplied domain.KDFParams plus a
// freshly-generated or descriptor-restored salt.
//
// The passphrase byte slice is NOT zeroed by this function —
// callers are responsible for wiping their own buffers after the
// derived KEK is no longer needed.
func Derive(passphrase, salt []byte, time, memory uint32, threads uint8) []byte {
	return argon2.IDKey(passphrase, salt, time, memory, threads, KEKLen)
}

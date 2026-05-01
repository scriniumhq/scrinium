// Package keywrap wraps and unwraps the data-encryption key (DEK)
// with the key-encryption key (KEK) using AES-256-GCM.
//
// The wrap format is the canonical AEAD layout: a 12-byte random
// nonce followed by the ciphertext-with-tag produced by AES-256-GCM
// over the DEK as plaintext, with empty additional authenticated
// data. Length:  len(wrapped) == 12 + len(dek) + 16. For a 32-byte
// DEK that is exactly 60 bytes.
//
// Tamper detection is the AEAD's own auth-tag check: any single-bit
// modification of the wrapped bytes, or attempting to unwrap with a
// different KEK, surfaces as errs.ErrDecryptionFailed. Callers do
// not need to layer their own integrity check on top.
//
// DAG: keywrap depends on errs and the standard library's
// crypto/aes, crypto/cipher, crypto/rand. It does not import core,
// kdf, domain, or anything else.
package keywrap

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// AlgAES256GCM names the value-encryption algorithm recorded on an Envelope so a
// future algorithm change can decrypt old records by their stamped alg.
const AlgAES256GCM = "AES-256-GCM"

// dekSize is the data-encryption-key length: 32 bytes → AES-256.
const dekSize = 32

// Envelope is one sealed secret: the value encrypted under a fresh per-secret DEK
// (AES-256-GCM), plus the DEK wrapped under the KEK. It is the unit a store
// persists — every field is ciphertext or metadata, never cleartext. The storage
// layer (S2) maps these fields onto columns.
type Envelope struct {
	Ciphertext []byte // the value, AES-256-GCM sealed under the DEK
	Nonce      []byte // GCM nonce for Ciphertext
	WrappedDEK []byte // the DEK sealed under the KEK (provider-defined framing)
	KEKVersion int    // which KEK generation wrapped the DEK
	Alg        string // value-encryption alg (AlgAES256GCM)
}

// Seal generates a fresh DEK, encrypts plaintext under it with AES-256-GCM, and
// wraps the DEK with kp. aad (optional, may be nil) is bound as GCM additional
// authenticated data — the storage layer passes the secret's stable identity
// (scope/tenant/name) so a ciphertext cannot be silently relabeled onto a
// different handle; Open must be given the same aad. The DEK is zeroed before
// return (best-effort defense-in-depth: Go's GC may already have copied the
// buffer, so the real guarantee is the envelope, not memory hygiene).
func Seal(ctx context.Context, kp KeyProvider, plaintext, aad []byte) (*Envelope, error) {
	dek := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("secrets: generate DEK: %w", err)
	}
	defer zero(dek)

	ciphertext, nonce, err := gcmSeal(dek, plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("secrets: seal value: %w", err)
	}
	wrapped, version, err := kp.Wrap(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("secrets: wrap DEK: %w", err)
	}
	return &Envelope{
		Ciphertext: ciphertext,
		Nonce:      nonce,
		WrappedDEK: wrapped,
		KEKVersion: version,
		Alg:        AlgAES256GCM,
	}, nil
}

// Open unwraps the DEK with kp and decrypts the value. aad must match the value
// passed to Seal (nil if none). It fails closed on an unknown alg, a KEK-version
// the provider cannot unwrap, a tampered wrapped DEK, or a tampered/relabeled
// ciphertext (the GCM auth tag catches all three). The DEK is zeroed before
// return.
func Open(ctx context.Context, kp KeyProvider, e *Envelope, aad []byte) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("secrets: nil envelope")
	}
	if e.Alg != AlgAES256GCM {
		return nil, fmt.Errorf("secrets: unsupported value algorithm %q", e.Alg)
	}
	dek, err := kp.Unwrap(ctx, e.WrappedDEK, e.KEKVersion)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrap DEK: %w", err)
	}
	defer zero(dek)

	plaintext, err := gcmOpen(dek, e.Ciphertext, e.Nonce, aad)
	if err != nil {
		return nil, fmt.Errorf("secrets: open value: %w", err)
	}
	return plaintext, nil
}

// gcmSeal encrypts plaintext under a 32-byte key with AES-256-GCM, returning the
// ciphertext (with the appended auth tag) and the fresh random nonce.
func gcmSeal(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nonce, nil
}

// gcmOpen decrypts ciphertext under a 32-byte key with AES-256-GCM. A tampered
// ciphertext, nonce, or aad fails the auth tag and returns an error.
func gcmOpen(key, ciphertext, nonce, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("nonce is %d bytes, want %d", len(nonce), gcm.NonceSize())
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

// newGCM builds an AES-256-GCM AEAD from a 32-byte key.
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != dekSize {
		return nil, fmt.Errorf("key is %d bytes, want %d (AES-256)", len(key), dekSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// zero overwrites b in place. Best-effort: it clears the live buffer so a DEK
// does not linger after use, but it cannot reach copies the runtime may have
// made, so it is defense-in-depth, not a guarantee.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

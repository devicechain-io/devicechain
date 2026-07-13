// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"fmt"
)

// InstanceKEKProvider is the config name of the default KEK provider: a 256-bit
// root key delivered via the instance K8s Secret. It gives the zero-cloud default
// real encryption-at-rest with no new infrastructure.
const InstanceKEKProvider = "instance"

// instanceKEKVersion is the single KEK generation this v1 provider seals with.
// The version is still recorded on every envelope and checked on unwrap, so a
// future multi-version provider (online KEK rotation) is an additive change with
// no schema or envelope-format change.
const instanceKEKVersion = 1

// instanceKeyProvider wraps DEKs with an instance root key held in memory, using
// AES-256-GCM (the same primitive as the value layer — one algorithm, stdlib
// only, and the auth tag makes a tampered wrapped DEK fail closed on Unwrap).
// The wrapped form is nonce||ciphertext.
type instanceKeyProvider struct {
	kek []byte // 32 bytes (AES-256)
}

// NewInstanceKeyProvider builds the default provider from a 256-bit root key
// (the instance secretsRootKey, wired from the K8s Secret in a later slice). A
// key of the wrong length is rejected fail-closed — a service that cannot form
// its KEK must not start.
func NewInstanceKeyProvider(rootKey []byte) (KeyProvider, error) {
	if len(rootKey) != dekSize {
		return nil, fmt.Errorf("secrets: instance root key is %d bytes, want %d (256-bit)", len(rootKey), dekSize)
	}
	// Copy so a caller mutating/zeroing its buffer cannot corrupt the KEK.
	kek := make([]byte, dekSize)
	copy(kek, rootKey)
	return &instanceKeyProvider{kek: kek}, nil
}

func (p *instanceKeyProvider) Name() string { return InstanceKEKProvider }

// Wrap seals dek under the instance KEK and stamps the current version.
func (p *instanceKeyProvider) Wrap(_ context.Context, dek []byte) (wrapped []byte, version int, err error) {
	ciphertext, nonce, err := gcmSeal(p.kek, dek, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("wrap dek: %w", err)
	}
	// Frame as nonce||ciphertext so Unwrap can split without extra metadata.
	wrapped = make([]byte, 0, len(nonce)+len(ciphertext))
	wrapped = append(wrapped, nonce...)
	wrapped = append(wrapped, ciphertext...)
	return wrapped, instanceKEKVersion, nil
}

// Unwrap recovers a DEK sealed by this provider. It fails closed on an unknown
// version (a different KEK generation this v1 provider has no key for) or on a
// wrapped blob that does not authenticate under the KEK.
func (p *instanceKeyProvider) Unwrap(_ context.Context, wrapped []byte, version int) ([]byte, error) {
	if version != instanceKEKVersion {
		return nil, fmt.Errorf("unknown KEK version %d (instance provider seals version %d)", version, instanceKEKVersion)
	}
	nonceSize, err := gcmNonceSize(p.kek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < nonceSize {
		return nil, fmt.Errorf("wrapped DEK is %d bytes, shorter than the %d-byte nonce", len(wrapped), nonceSize)
	}
	nonce, ciphertext := wrapped[:nonceSize], wrapped[nonceSize:]
	dek, err := gcmOpen(p.kek, ciphertext, nonce, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap dek: %w", err)
	}
	return dek, nil
}

// gcmNonceSize returns the AES-256-GCM nonce size for framing a wrapped DEK.
func gcmNonceSize(key []byte) (int, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return 0, err
	}
	return gcm.NonceSize(), nil
}

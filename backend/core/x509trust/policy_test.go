// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
)

func p521Key(t testing.TB) crypto.Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func p384Key(t testing.TB) crypto.Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCheckPublicKey(t *testing.T) {
	allowed := map[string]any{
		"RSA-2048": rsaKey(t, 2048).Public(),
		"P-256":    ecKey(t).Public(),
		"P-384":    p384Key(t).Public(),
		"Ed25519":  edKey(t).Public(),
	}
	for name, pub := range allowed {
		if err := CheckPublicKey(pub); err != nil {
			t.Errorf("%s must be allowed: %v", name, err)
		}
	}
	refused := map[string]any{
		"RSA-1024":     rsaKey(t, 1024).Public(),
		"RSA 2047 bit": &rsa.PublicKey{N: rsaKey(t, 1024).Public().(*rsa.PublicKey).N, E: 65537},
		"P-521":        p521Key(t).Public(),
		"P-224":        mustP224(t),
		"nil modulus":  &rsa.PublicKey{},
		"unknown type": struct{}{},
		"nil":          nil,
	}
	for name, pub := range refused {
		err := CheckPublicKey(pub)
		if !errors.Is(err, ErrKeyPolicy) {
			t.Errorf("%s must be refused with ErrKeyPolicy, got %v", name, err)
		}
	}
}

func mustP224(t testing.TB) any {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k.Public()
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// KeyBits is the RSA modulus size for the platform signing key. 2048 is the
// floor for RS256 in any serious deployment; the keypair is generated once per
// instance and persisted (ADR-008).
const KeyBits = 2048

const (
	pemTypePrivateKey = "PRIVATE KEY"
	pemTypePublicKey  = "PUBLIC KEY"
)

// GenerateKeyPair creates a fresh RSA signing keypair for the instance.
func GenerateKeyPair() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, KeyBits)
}

// EncodePrivateKeyPEM serializes a private key as PKCS#8 PEM for storage.
func EncodePrivateKeyPEM(key *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: der}), nil
}

// DecodePrivateKeyPEM parses a PKCS#8 PEM private key.
func DecodePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("auth: no PEM block found in private key data")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("auth: private key is not RSA")
	}
	return key, nil
}

// EncodePublicKeyPEM serializes a public key as PKIX PEM for distribution to
// the validating services.
func EncodePublicKeyPEM(pub *rsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePublicKey, Bytes: der}), nil
}

// DecodePublicKeyPEM parses a PKIX PEM public key.
func DecodePublicKeyPEM(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("auth: no PEM block found in public key data")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("auth: public key is not RSA")
	}
	return pub, nil
}

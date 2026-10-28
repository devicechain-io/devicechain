// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
)

const (
	// MinRSABits is the smallest RSA modulus the key policy accepts.
	MinRSABits = 2048
	// MaxRSABits is the largest. A modulus past this costs the verifier far more than it adds
	// to security, and a certificate presenting one is an attempt to make the broker spend CPU
	// on the authentication path.
	MaxRSABits = 8192
)

// ErrKeyPolicy is returned for a certificate whose public key the policy does not accept.
var ErrKeyPolicy = errors.New("x509trust: public key not allowed")

// CheckPublicKey applies the key policy: RSA of 2048 to 8192 bits, ECDSA on P-256 or P-384, or
// Ed25519. Anything else (RSA-1024, P-224, P-521, DSA, an unknown algorithm) is refused.
func CheckPublicKey(pub any) error {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if k.N == nil || k.N.BitLen() < MinRSABits || k.N.BitLen() > MaxRSABits {
			return fmt.Errorf("%w: RSA keys must be between %d and %d bits", ErrKeyPolicy, MinRSABits, MaxRSABits)
		}
	case *ecdsa.PublicKey:
		if c := k.Curve; c != elliptic.P256() && c != elliptic.P384() {
			return fmt.Errorf("%w: ECDSA keys must use P-256 or P-384", ErrKeyPolicy)
		}
	case ed25519.PublicKey:
		// Always 256 bits; nothing to check.
	default:
		return fmt.Errorf("%w: unsupported key type", ErrKeyPolicy)
	}
	return nil
}

// weakSignature is whether the certificate is signed with a hash that must not carry trust.
// Go's verifier already refuses most of these; naming them here makes the refusal a classified
// one and keeps it from depending on the verifier's defaults.
func weakSignature(c *x509.Certificate) bool {
	switch c.SignatureAlgorithm {
	case x509.MD2WithRSA, x509.MD5WithRSA, x509.SHA1WithRSA, x509.DSAWithSHA1, x509.ECDSAWithSHA1:
		return true
	}
	return false
}

// oidNameConstraints is the NameConstraints certificate extension (RFC 5280, 4.2.1.10).
var oidNameConstraints = asn1.ObjectIdentifier{2, 5, 29, 30}

// hasNameConstraints is whether the certificate carries a NameConstraints extension of any
// kind, critical or not and whatever name types it constrains. It looks at the extension
// itself rather than at the fields Go parsed from it, because Go only understands some name
// types and would report a constraint it cannot read as none.
func hasNameConstraints(c *x509.Certificate) bool {
	for _, e := range c.Extensions {
		if e.Id.Equal(oidNameConstraints) {
			return true
		}
	}
	return false
}

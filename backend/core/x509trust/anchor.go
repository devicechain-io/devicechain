// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// ErrAnchorInvalid carries the reason a submitted trust anchor was refused. Like
// ErrBundleInvalid it never quotes the input.
var ErrAnchorInvalid = errors.New("invalid trust anchor")

// MaxAnchorBytes bounds an anchor's PEM as submitted.
const MaxAnchorBytes = 64 << 10

// Anchor is a validated trust anchor: exactly one CA certificate.
type Anchor struct {
	// Certificate is the parsed CA certificate.
	Certificate *x509.Certificate
	// PEM is the canonical PEM encoding, safe to store.
	PEM string
	// Info is the display description.
	Info CaCertificate
}

func anchorInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrAnchorInvalid, fmt.Sprintf(format, a...))
}

// CheckAnchor applies the trust-anchor rules to a parsed certificate: it must assert it is a
// CA, may sign certificates, be unexpired at now, and use an allowed key. A root or an
// intermediate may be an anchor; whatever is trusted is the end of the chain.
func CheckAnchor(cert *x509.Certificate, now time.Time) error {
	if !cert.BasicConstraintsValid || !cert.IsCA {
		return anchorInvalid("the certificate is not a CA certificate")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return anchorInvalid("the certificate may not sign certificates (keyCertSign is missing)")
	}
	if now.After(cert.NotAfter) {
		return anchorInvalid("the certificate has expired")
	}
	if now.Before(cert.NotBefore) {
		return anchorInvalid("the certificate is not valid yet")
	}
	if weakSignature(cert) {
		return anchorInvalid("the certificate is signed with a weak algorithm (SHA-1 or older)")
	}
	if hasNameConstraints(cert) {
		return anchorInvalid("the certificate carries name constraints, which are not supported: " +
			"a trust anchor is the whole identity boundary, so constrain the devices at your CA instead")
	}
	if err := CheckPublicKey(cert.PublicKey); err != nil {
		return anchorInvalid("%v", err)
	}
	return nil
}

// ParseTrustAnchor parses text that must hold exactly one CA certificate and applies
// CheckAnchor at now. It reuses ParseCaBundle's rules (no private key, only CERTIFICATE blocks,
// nothing silently dropped) and then requires a single certificate.
func ParseTrustAnchor(text string, now time.Time) (*Anchor, error) {
	if len(text) > MaxAnchorBytes {
		return nil, anchorInvalid("the certificate is larger than %d KiB", MaxAnchorBytes>>10)
	}
	canonical, certs, err := parseBundle(text)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAnchorInvalid, err)
	}
	switch len(certs) {
	case 0:
		return nil, anchorInvalid("no certificate was supplied")
	case 1:
	default:
		return nil, anchorInvalid("supply exactly one certificate, not %d", len(certs))
	}
	if err := CheckAnchor(certs[0], now); err != nil {
		return nil, err
	}
	return &Anchor{Certificate: certs[0], PEM: canonical, Info: describe(certs[0])}, nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package x509trust is the one place DeviceChain decides whether an X.509 certificate may be
// trusted: parsing the PEM text an administrator supplies, the key policy, the fingerprint, and
// the verification of a device's client-certificate chain against a single trust anchor.
//
// Two callers rely on it and must not drift apart: user-management (a private CA bundle for an
// identity provider) and device-management (tenant trust anchors for mTLS devices). The parser
// is therefore here, once, and neither caller keeps a copy.
package x509trust

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrBundleInvalid carries the reason a PEM bundle was refused. The message never contains the
// submitted text: a private key pasted by mistake must not travel into a log or an error
// response.
var ErrBundleInvalid = errors.New("invalid CA bundle")

const (
	// MaxBundleBytes bounds a bundle as submitted.
	MaxBundleBytes = 64 << 10
	// MaxBundleCerts bounds the certificates in one bundle.
	MaxBundleCerts = 64
)

func bundleInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBundleInvalid, fmt.Sprintf(format, a...))
}

// CaCertificate describes one certificate of a bundle for display.
type CaCertificate struct {
	// FingerprintSHA256 is the hex SHA-256 of the DER certificate.
	FingerprintSHA256 string
	Subject           string
	Issuer            string
	NotBefore         time.Time
	NotAfter          time.Time
	// IsCA is whether the certificate asserts it is a certificate authority. A bundle may carry
	// a non-CA certificate (pinning a provider's own self-signed certificate is legitimate), so
	// this is reported, not required. Trust anchors require it (see ParseTrustAnchor).
	IsCA bool
}

// Fingerprint is the hex SHA-256 of the certificate's DER encoding.
func Fingerprint(cert *x509.Certificate) string { return FingerprintDER(cert.Raw) }

// FingerprintDER is the hex SHA-256 of a DER certificate.
func FingerprintDER(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func describe(cert *x509.Certificate) CaCertificate {
	return CaCertificate{
		FingerprintSHA256: Fingerprint(cert),
		Subject:           cert.Subject.String(),
		Issuer:            cert.Issuer.String(),
		NotBefore:         cert.NotBefore.UTC(),
		NotAfter:          cert.NotAfter.UTC(),
		IsCA:              cert.BasicConstraintsValid && cert.IsCA,
	}
}

// ParseCaBundle validates a PEM bundle and returns its canonical form (the CERTIFICATE blocks
// re-encoded, with anything outside them dropped) and the certificates it holds. An empty
// bundle is valid and means "no private CA": it returns "" and no certificates.
//
// Refused: more than 64 KiB; a private key anywhere in the text; any PEM block that is not a
// CERTIFICATE or that carries headers; a block that does not parse as X.509; no certificate at
// all. Errors name the reason and never quote the input.
func ParseCaBundle(text string) (string, []CaCertificate, error) {
	canonical, certs, err := parseBundle(text)
	if err != nil || certs == nil {
		return canonical, nil, err
	}
	out := make([]CaCertificate, len(certs))
	for i, c := range certs {
		out[i] = describe(c)
	}
	return canonical, out, nil
}

// parseBundle is ParseCaBundle's parsing core, returning the parsed certificates in order,
// de-duplicated.
func parseBundle(text string) (string, []*x509.Certificate, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil, nil
	}
	if len(text) > MaxBundleBytes {
		return "", nil, bundleInvalid("the bundle is larger than %d KiB", MaxBundleBytes>>10)
	}
	// A private key is refused wherever it sits, including in a block too damaged to decode.
	if strings.Contains(strings.ToUpper(text), "PRIVATE KEY") {
		return "", nil, bundleInvalid("the text contains a private key; supply certificates only, and keep the key private")
	}
	var (
		out    []byte
		certs  []*x509.Certificate
		seen   = map[string]bool{}
		rest   = []byte(text)
		blocks int
	)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		blocks++
		if block.Type != "CERTIFICATE" {
			return "", nil, bundleInvalid("only CERTIFICATE blocks are accepted")
		}
		if len(block.Headers) != 0 {
			return "", nil, bundleInvalid("a certificate block must not carry headers")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", nil, bundleInvalid("a certificate block is not a valid X.509 certificate")
		}
		fp := Fingerprint(cert)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		if len(certs) >= MaxBundleCerts {
			return "", nil, bundleInvalid("at most %d certificates", MaxBundleCerts)
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
		certs = append(certs, cert)
	}
	// A block too damaged to decode is skipped by the PEM reader, which would silently drop a
	// trust anchor the administrator meant to supply.
	if markers := strings.Count(text, "-----BEGIN "); markers != blocks {
		return "", nil, bundleInvalid("%d certificate block(s) could not be read; fix or remove them", markers-blocks)
	}
	if len(certs) == 0 {
		return "", nil, bundleInvalid("no PEM certificate was found (expected -----BEGIN CERTIFICATE----- blocks)")
	}
	return string(out), certs, nil
}

// ParseCertificates is ParseCaBundle for a caller that needs the certificates themselves, not
// their description: the same text rules, the certificates in the order they were given, and the
// canonical text. An empty text returns no certificates and no error.
func ParseCertificates(text string) (string, []*x509.Certificate, error) {
	return parseBundle(text)
}

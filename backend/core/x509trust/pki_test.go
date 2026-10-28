// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync/atomic"
	"testing"
	"time"
)

// The tests build their certificates here, from one fixed "now", so every time-based case is
// relative to a clock the test controls and none depends on the wall clock.
var testNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

var serial atomic.Int64

// node is a certificate with its key, able to sign the next one down.
type node struct {
	cert *x509.Certificate
	key  crypto.Signer
}

// spec describes one certificate. The zero value is a valid client-auth leaf; each negative
// control changes exactly one field of a spec a paired test proves succeeds.
type spec struct {
	cn        string
	ca        bool
	maxPath   int // for CAs; -1 (via pathLenZero=false) leaves it unlimited
	ku        x509.KeyUsage
	eku       []x509.ExtKeyUsage
	noEKU     bool
	notBefore time.Time
	notAfter  time.Time
	key       crypto.Signer
	sigAlg    x509.SignatureAlgorithm
	pad       int // bytes of opaque extension, to make a certificate large
	mutate    func(*x509.Certificate)
}

func ecKey(t testing.TB) crypto.Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func rsaKey(t testing.TB, bits int) crypto.Signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func edKey(t testing.TB) crypto.Signer {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (s spec) template() *x509.Certificate {
	t := s.baseTemplate()
	if s.mutate != nil {
		s.mutate(t)
	}
	if s.pad > 0 {
		t.ExtraExtensions = []pkix.Extension{{Id: []int{1, 3, 6, 1, 4, 1, 99999, 1}, Value: make([]byte, s.pad)}}
	}
	return t
}

func (s spec) baseTemplate() *x509.Certificate {
	nb, na := s.notBefore, s.notAfter
	if nb.IsZero() {
		nb = testNow.Add(-time.Hour)
	}
	if na.IsZero() {
		na = testNow.Add(24 * time.Hour)
	}
	cn := s.cn
	if cn == "" {
		cn = "cert"
	}
	t := &x509.Certificate{
		SerialNumber:       big.NewInt(serial.Add(1)),
		Subject:            pkix.Name{CommonName: cn},
		NotBefore:          nb,
		NotAfter:           na,
		SignatureAlgorithm: s.sigAlg,
	}
	if s.ca {
		t.IsCA = true
		t.BasicConstraintsValid = true
		t.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		if s.ku != 0 {
			t.KeyUsage = s.ku
		}
		if s.maxPath > 0 {
			t.MaxPathLen = s.maxPath
		} else {
			t.MaxPathLen = -1
		}
		t.ExtKeyUsage = s.eku
		return t
	}
	t.KeyUsage = x509.KeyUsageDigitalSignature
	if s.ku != 0 {
		t.KeyUsage = s.ku
	}
	switch {
	case s.noEKU:
	case s.eku != nil:
		t.ExtKeyUsage = s.eku
	default:
		t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	return t
}

// issue creates the certificate described by s, signed by parent (nil = self-signed).
func issue(t testing.TB, parent *node, s spec) *node {
	t.Helper()
	if s.key == nil {
		s.key = ecKey(t)
	}
	tmpl := s.template()
	signer, issuer := s.key, tmpl
	if parent != nil {
		signer, issuer = parent.key, parent.cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, s.key.Public(), signer)
	if err != nil {
		t.Fatalf("create certificate %q: %v", s.cn, err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate %q: %v", s.cn, err)
	}
	return &node{cert: c, key: s.key}
}

func (n *node) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: n.cert.Raw}))
}

// chainOf is the leaf-first slice a TLS peer presents.
func chainOf(ns ...*node) []*x509.Certificate {
	out := make([]*x509.Certificate, len(ns))
	for i, n := range ns {
		out[i] = n.cert
	}
	return out
}

// basicPKI is root -> intermediate -> leaf, every certificate valid at testNow.
type basicPKI struct{ root, inter, leaf *node }

func newBasicPKI(t testing.TB) basicPKI {
	root := issue(t, nil, spec{cn: "root", ca: true})
	inter := issue(t, root, spec{cn: "inter", ca: true})
	leaf := issue(t, inter, spec{cn: "device-1"})
	return basicPKI{root, inter, leaf}
}

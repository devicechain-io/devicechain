// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests need certificates that are valid against the wall clock, because the code under
// test reads it: every certificate starts an hour ago and ends a day from now unless a case
// changes exactly one of those.

var pkiSerial atomic.Int64

// pkiNode is a certificate and its key.
type pkiNode struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func (n *pkiNode) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: n.cert.Raw}))
}

type pkiOpts struct {
	cn        string
	ca        bool
	eku       []x509.ExtKeyUsage
	notBefore time.Time
	notAfter  time.Time
	mutate    func(*x509.Certificate)
}

func newPki(t testing.TB, parent *pkiNode, o pkiOpts) *pkiNode {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	if o.notBefore.IsZero() {
		o.notBefore = now.Add(-time.Hour)
	}
	if o.notAfter.IsZero() {
		o.notAfter = now.Add(24 * time.Hour)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(pkiSerial.Add(1) + now.UnixNano()),
		Subject:      pkix.Name{CommonName: o.cn},
		NotBefore:    o.notBefore,
		NotAfter:     o.notAfter,
	}
	if o.ca {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		tmpl.MaxPathLen = -1
	} else {
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		if o.eku != nil {
			tmpl.ExtKeyUsage = o.eku
		}
	}
	if o.mutate != nil {
		o.mutate(tmpl)
	}
	issuer, signer := tmpl, key
	if parent != nil {
		issuer, signer = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, signer)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &pkiNode{cert: cert, key: key}
}

func newRootCA(t testing.TB, cn string) *pkiNode {
	return newPki(t, nil, pkiOpts{cn: cn, ca: true})
}

func (n *pkiNode) leaf(t testing.TB, cn string) *pkiNode {
	return newPki(t, n, pkiOpts{cn: cn})
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// crlRevoked is one entry of a CRL under construction.
type crlRevoked struct {
	serial *big.Int
	reason int
}

// crlSpec is a CRL under construction. Zero fields get usable defaults: number 1, issued an hour
// ago, good for a week.
type crlSpec struct {
	number     *big.Int
	thisUpdate time.Time
	nextUpdate time.Time
	revoked    []crlRevoked
	mutate     func(*x509.RevocationList)
}

func revokedSerials(serials ...*big.Int) []crlRevoked {
	out := make([]crlRevoked, len(serials))
	for i, s := range serials {
		out[i] = crlRevoked{serial: s}
	}
	return out
}

// crl signs a CRL with this CA and returns its DER.
func (n *pkiNode) crl(t testing.TB, s crlSpec) []byte {
	t.Helper()
	if s.number == nil {
		s.number = big.NewInt(1)
	}
	if s.thisUpdate.IsZero() {
		s.thisUpdate = time.Now().Add(-time.Hour)
	}
	if s.nextUpdate.IsZero() {
		s.nextUpdate = time.Now().Add(7 * 24 * time.Hour)
	}
	tmpl := &x509.RevocationList{Number: s.number, ThisUpdate: s.thisUpdate, NextUpdate: s.nextUpdate}
	for _, r := range s.revoked {
		tmpl.RevokedCertificateEntries = append(tmpl.RevokedCertificateEntries, x509.RevocationListEntry{
			SerialNumber: r.serial, RevocationTime: s.thisUpdate, ReasonCode: r.reason})
	}
	if s.mutate != nil {
		s.mutate(tmpl)
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, n.cert, n.key)
	require.NoError(t, err)
	return der
}

// crlPEM is crl as PEM text.
func (n *pkiNode) crlPEM(t testing.TB, s crlSpec) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: n.crl(t, s)})
}

// sameKeyAs re-issues this CA's key under another name, signed by parent: a different issuer
// identity (key and name) that shares the key.
func (n *pkiNode) reissuedAs(t testing.TB, parent *pkiNode, cn string) *pkiNode {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(pkiSerial.Add(1) + time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: -1,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent.cert, &n.key.PublicKey, parent.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &pkiNode{cert: cert, key: n.key}
}

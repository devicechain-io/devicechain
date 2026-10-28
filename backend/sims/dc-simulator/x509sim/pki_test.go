// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509sim

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/x509trust"
)

func TestIssueDeviceShape(t *testing.T) {
	now := time.Now()
	ca, err := NewCA("test ca", now, DefaultCAValidity)
	require.NoError(t, err)
	d, err := ca.IssueDevice("press-07", now, 48*time.Hour)
	require.NoError(t, err)

	require.Equal(t, "press-07", d.Cert.Subject.CommonName)
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, d.Cert.ExtKeyUsage)
	require.False(t, d.Cert.IsCA)
	pub, ok := d.Cert.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok, "device key must be ECDSA")
	require.Equal(t, elliptic.P256(), pub.Curve)
	require.True(t, d.Cert.NotBefore.Before(now), "notBefore is backdated")
	require.WithinDuration(t, now.Add(48*time.Hour), d.Cert.NotAfter, time.Second)

	require.True(t, ca.Cert.IsCA)
	require.NotZero(t, ca.Cert.KeyUsage&x509.KeyUsageCertSign)
	_, err = x509trust.ParseTrustAnchor(string(CertPEM(ca.Cert)), now)
	require.NoError(t, err, "the platform must accept the CA as a trust anchor")
}

func TestDeviceValidityIsCappedByTheCA(t *testing.T) {
	now := time.Now()
	ca, err := NewCA("short ca", now, time.Hour)
	require.NoError(t, err)
	d, err := ca.IssueDevice("x", now, 24*time.Hour)
	require.NoError(t, err)
	require.False(t, d.Cert.NotAfter.After(ca.Cert.NotAfter))
}

func TestMintedChainVerifies(t *testing.T) {
	now := time.Now()
	ca, err := NewCA("test ca", now, DefaultCAValidity)
	require.NoError(t, err)
	d, err := ca.IssueDevice("press-07", now, time.Hour)
	require.NoError(t, err)

	v, err := x509trust.VerifyClientChain(ca.Cert, []*x509.Certificate{d.Cert}, now)
	require.NoError(t, err)
	require.Equal(t, x509trust.Fingerprint(d.Cert), v.Fingerprint)

	// Negative control: the same certificate against another CA is refused, so the check above
	// is able to fail.
	other, err := NewCA("other", now, DefaultCAValidity)
	require.NoError(t, err)
	_, err = x509trust.VerifyClientChain(other.Cert, []*x509.Certificate{d.Cert}, now)
	require.Equal(t, x509trust.ReasonUntrusted, x509trust.ReasonOf(err))

	// And an expired one is refused: validity is real.
	_, err = x509trust.VerifyClientChain(ca.Cert, []*x509.Certificate{d.Cert}, now.Add(2*time.Hour))
	require.Equal(t, x509trust.ReasonExpired, x509trust.ReasonOf(err))
}

func TestIssueRejectsEmptyCN(t *testing.T) {
	ca, err := NewCA("test ca", time.Now(), time.Hour)
	require.NoError(t, err)
	_, err = ca.IssueDevice("", time.Now(), time.Hour)
	require.Error(t, err)
}

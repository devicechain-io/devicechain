// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func mustMint(t *testing.T, replicas int) *natsTLSMaterial {
	t.Helper()
	m, err := mintNATSTLS(natsReleaseName, infraNamespace, replicas, time.Now())
	if err != nil {
		t.Fatalf("minting broker TLS: %v", err)
	}
	return m
}

func parseOne(t *testing.T, pemText string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		t.Fatalf("not PEM:\n%s", pemText)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}
	return c
}

func caPool(t *testing.T, m *natsTLSMaterial) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM([]byte(m.CACertPEM)) {
		t.Fatal("the authority's certificate was not accepted into a trust pool")
	}
	return p
}

// 🔴 THE ONE THAT IS SILENT IF IT IS WRONG. The single leaf plays both roles: a
// server on the client and messaging listeners, and a CLIENT on the route listener,
// where each broker presents this same certificate to its peers with verification on.
// Drop the client role and that handshake is rejected — routes retry, the stream
// layer never elects a leader, and it reads as a broken cluster rather than as a
// certificate two extension bits short. A single-server install never opens a route,
// so it cannot notice.
//
// Verified by asking the standard library to verify for each usage in turn, not by
// reading back the field this code just set. Reading the field would pass for a
// certificate no verifier accepts.
func TestTheBrokerLeafVerifiesAsBothAServerAndAClient(t *testing.T) {
	m := mustMint(t, 3)
	leaf := parseOne(t, m.LeafCertPEM)
	roots := caPool(t, m)

	for name, usage := range map[string]x509.ExtKeyUsage{
		"as a server, to every client that dials it": x509.ExtKeyUsageServerAuth,
		"as a client, to its peers on the route":     x509.ExtKeyUsageClientAuth,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := leaf.Verify(x509.VerifyOptions{
				Roots:     roots,
				DNSName:   natsReleaseName + "." + infraNamespace,
				KeyUsages: []x509.ExtKeyUsage{usage},
			}); err != nil {
				t.Fatalf("the leaf does not verify %s: %v", name, err)
			}
		})
	}
}

// Every name anything dials the broker by has to be on the certificate. The per-pod
// route names only exist above one replica, and only a multi-server install can find
// out the hard way.
func TestTheLeafCoversEveryNameTheBrokerIsDialledBy(t *testing.T) {
	const replicas = 3
	m := mustMint(t, replicas)
	leaf := parseOne(t, m.LeafCertPEM)
	roots := caPool(t, m)

	want := []string{
		"dc-nats",
		"dc-nats.dc-system",
		"dc-nats.dc-system.svc",
		"dc-nats.dc-system.svc.cluster.local",
		"localhost",
		// The route names, at all four resolution depths, for every server.
		"dc-nats-0.dc-nats-headless",
		"dc-nats-0.dc-nats-headless.dc-system",
		"dc-nats-0.dc-nats-headless.dc-system.svc",
		"dc-nats-0.dc-nats-headless.dc-system.svc.cluster.local",
		"dc-nats-2.dc-nats-headless.dc-system.svc.cluster.local",
	}
	for _, host := range want {
		if err := leaf.VerifyHostname(host); err != nil {
			t.Errorf("the leaf is not valid for %q, which something dials the broker by: %v", host, err)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots: roots, DNSName: host,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}); err != nil {
			t.Errorf("peer verification for %q fails: %v", host, err)
		}
	}

	// The counterweight: a certificate valid for everything is not a certificate.
	if err := leaf.VerifyHostname("not-the-broker.dc-system"); err == nil {
		t.Error("the leaf is valid for a name nothing should reach it by")
	}
}

// A single-server install opens no route, so naming per-pod hosts there would be
// certifying names that do not resolve.
func TestASingleServerGetsNoRouteNames(t *testing.T) {
	names := natsServerDNSNames(natsReleaseName, infraNamespace, 1)
	for _, n := range names {
		if strings.Contains(n, "-headless") {
			t.Errorf("a single-server install was given the route name %q", n)
		}
	}
	if len(names) != 5 {
		t.Errorf("want the five service names, got %d: %v", len(names), names)
	}
}

// 🔴 THE SECRET MUST NOT CARRY THE AUTHORITY'S PRIVATE KEY. That key is what signs
// every future certificate for this instance; the broker needs its own key and the
// authority's public certificate, and nothing more. This is the value whose presence
// in a state file is the reason any of this moved.
func TestTheBrokerSecretNeverCarriesTheAuthorityKey(t *testing.T) {
	m := mustMint(t, 1)
	s := natsTLSSecret(natsReleaseName, m)
	for k, v := range s.Data {
		if strings.Contains(v, m.CAKeyPEM) {
			t.Fatalf("the authority's private key is in the Secret under %q", k)
		}
	}
	if s.Data["tls.key"] != m.LeafKeyPEM {
		t.Error("tls.key is not the leaf's key")
	}
	if s.Type != "kubernetes.io/tls" {
		t.Errorf("type is %q; the chart mounts this as a TLS Secret", s.Type)
	}
}

// 🔴 tls.crt IS THE LEAF FOLLOWED BY THE AUTHORITY. A leaf alone verifies for anything
// that already holds the authority and fails for everything else — a failure that
// depends on which client is asking, which is the hardest kind to reproduce.
func TestTheServedChainIsLeafThenAuthority(t *testing.T) {
	m := mustMint(t, 1)
	chain := natsTLSSecret(natsReleaseName, m).Data["tls.crt"]

	var certs []*x509.Certificate
	rest := []byte(chain)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parsing a certificate out of tls.crt: %v", err)
		}
		certs = append(certs, c)
	}
	if len(certs) != 2 {
		t.Fatalf("tls.crt holds %d certificates, want the leaf and the authority", len(certs))
	}
	if certs[0].IsCA {
		t.Error("the authority is presented first; a peer reads the first entry as the leaf")
	}
	if !certs[1].IsCA {
		t.Error("the second entry is not the authority, so the chain is incomplete")
	}
	if err := certs[0].CheckSignatureFrom(certs[1]); err != nil {
		t.Errorf("the presented leaf was not signed by the presented authority: %v", err)
	}
}

// The authority outlives the leaf by an order of magnitude, and neither begins in the
// future — a certificate valid from the instant it is issued is not yet valid on any
// machine whose clock is a moment behind, and the pods that reject it do so at
// startup, minutes after a bootstrap reported success.
func TestTheValidityWindowsTolerateSkewAndTheAuthorityOutlivesTheLeaf(t *testing.T) {
	now := time.Now()
	m, err := mintNATSTLS(natsReleaseName, infraNamespace, 1, now)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	ca, leaf := parseOne(t, m.CACertPEM), parseOne(t, m.LeafCertPEM)

	for name, c := range map[string]*x509.Certificate{"authority": ca, "leaf": leaf} {
		if !c.NotBefore.Before(now) {
			t.Errorf("the %s is not valid until %s, which is not before now", name, c.NotBefore)
		}
	}
	if !leaf.NotAfter.After(now.Add(360 * 24 * time.Hour)) {
		t.Errorf("the leaf expires at %s, sooner than the year it is meant to last", leaf.NotAfter)
	}
	if !ca.NotAfter.After(leaf.NotAfter.Add(5 * 365 * 24 * time.Hour)) {
		t.Errorf("the authority (%s) does not outlive the leaf (%s) by the margin that makes "+
			"re-issuing a leaf routine", ca.NotAfter, leaf.NotAfter)
	}
}

// Two instances must not share an authority, and two runs must not share a serial.
func TestEveryMintIsDistinct(t *testing.T) {
	a, b := mustMint(t, 1), mustMint(t, 1)
	if a.CAKeyPEM == b.CAKeyPEM {
		t.Fatal("two mints produced the same authority key")
	}
	if parseOne(t, a.CACertPEM).SerialNumber.Cmp(parseOne(t, b.CACertPEM).SerialNumber) == 0 {
		t.Fatal("two authorities share a serial number")
	}
	if parseOne(t, a.LeafCertPEM).SerialNumber.Sign() <= 0 {
		t.Fatal("a certificate serial is not positive; some verifiers refuse that")
	}
}

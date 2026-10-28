// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"
)

// A name constraint on a CA would read as fencing it to a set of names, but identity here is the
// subject CN and Go applies constraints to alternative names only. So any chain with a
// constrained CA is refused, whatever the constraint says.
func TestVerifyRefusesNameConstraints(t *testing.T) {
	requireAccepted(t, "control (no constraints)", baseline())

	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	kinds := map[string]func(*x509.Certificate){
		"permitted DNS":      func(c *x509.Certificate) { c.PermittedDNSDomains = []string{".customera.example"} },
		"excluded DNS":       func(c *x509.Certificate) { c.ExcludedDNSDomains = []string{".customerb.example"} },
		"permitted email":    func(c *x509.Certificate) { c.PermittedEmailAddresses = []string{"customera.example"} },
		"excluded email":     func(c *x509.Certificate) { c.ExcludedEmailAddresses = []string{"customerb.example"} },
		"permitted URI":      func(c *x509.Certificate) { c.PermittedURIDomains = []string{".customera.example"} },
		"excluded URI":       func(c *x509.Certificate) { c.ExcludedURIDomains = []string{".customerb.example"} },
		"permitted IP range": func(c *x509.Certificate) { c.PermittedIPRanges = []*net.IPNet{ipnet} },
		"excluded IP range":  func(c *x509.Certificate) { c.ExcludedIPRanges = []*net.IPNet{ipnet} },
		// The extension present but constraining nothing Go understands (a directoryName
		// constraint would look like this to a parser that skips it): the extension itself is
		// the signal, not what Go made of it.
		"bare extension": func(c *x509.Certificate) {
			c.ExtraExtensions = append(c.ExtraExtensions, pkix.Extension{Id: []int{2, 5, 29, 30}, Value: []byte{0x30, 0x08, 0xa0, 0x06, 0x30, 0x04, 0xa4, 0x02, 0x30, 0x00}})
		},
	}
	for name, mutate := range kinds {
		w := baseline()
		w.inter.mutate = mutate
		requireRefused(t, "intermediate: "+name, w, ReasonNameConstraints)

		w = baseline()
		w.root.mutate = mutate
		requireRefused(t, "anchor: "+name, w, ReasonNameConstraints)

		w = baseline()
		w.anchorIsInter = true
		w.inter.mutate = mutate
		requireRefused(t, "intermediate as anchor: "+name, w, ReasonNameConstraints)
	}

	// A constrained anchor cannot be stored either.
	w := baseline()
	w.root.mutate = kinds["permitted DNS"]
	if _, err := ParseTrustAnchor(issue(t, nil, w.root).pem(), testNow); !errors.Is(err, ErrAnchorInvalid) {
		t.Fatalf("a constrained anchor was accepted: %v", err)
	}
}

// The reason names the certificate that failed, at the time it was verified at. A leaf inside
// the skew window is verified at its notBefore, and an intermediate that is already over by
// then has EXPIRED; it must not be reported as the leaf "not yet valid".
func TestVerifyClassifiesTheCertificateThatFailed(t *testing.T) {
	w := baseline()
	w.leaf.notBefore = testNow.Add(3 * time.Minute)
	requireAccepted(t, "control (leaf in the skew window)", w)

	w.inter.notAfter = testNow.Add(time.Minute) // alive now, over by the time the leaf starts
	requireRefused(t, "intermediate over before the leaf starts", w, ReasonExpired)

	// The converse still reads as not yet valid: the failing certificate is the leaf.
	w = baseline()
	w.leaf.notBefore = testNow.Add(time.Hour)
	requireRefused(t, "leaf far in the future", w, ReasonNotYetValid)
}

// With a cross-signed intermediate there are two valid paths. The session may last as long as
// the longest one, whichever the verifier lists first.
func TestVerifyNotAfterIsTheLongestPath(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true, notAfter: testNow.Add(200 * time.Hour)})
	k := ecKey(t)
	short := issue(t, root, spec{cn: "inter", ca: true, key: k, notAfter: testNow.Add(10 * time.Hour)})
	long := issue(t, root, spec{cn: "inter", ca: true, key: k, notAfter: testNow.Add(40 * time.Hour)})
	signer := &node{cert: short.cert, key: k}
	leaf := issue(t, signer, spec{cn: "device-1", notAfter: testNow.Add(100 * time.Hour)})

	for name, c := range map[string]struct {
		presented []*x509.Certificate
		want      time.Duration
	}{
		"short first": {chainOf(leaf, short, long), 40 * time.Hour},
		"long first":  {chainOf(leaf, long, short), 40 * time.Hour},
		"only short":  {chainOf(leaf, short), 10 * time.Hour},
	} {
		v, err := VerifyClientChain(root.cert, c.presented, testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !v.NotAfter.Equal(testNow.Add(c.want)) {
			t.Fatalf("%s: NotAfter %v, want %v", name, v.NotAfter, testNow.Add(c.want))
		}
	}
}

func TestVerifyAnchorSignatureAndValidity(t *testing.T) {
	rsaRoot := func(alg x509.SignatureAlgorithm, nb time.Time) *node {
		return issue(t, nil, spec{cn: "root", ca: true, key: rsaKey(t, 2048), sigAlg: alg, notBefore: nb})
	}
	check := func(root *node) error {
		leaf := issue(t, root, spec{cn: "d"})
		_, err := VerifyClientChain(root.cert, chainOf(leaf), testNow)
		return err
	}
	if err := check(rsaRoot(x509.SHA256WithRSA, time.Time{})); err != nil {
		t.Fatalf("control: %v", err)
	}
	if got := ReasonOf(check(rsaRoot(x509.SHA1WithRSA, time.Time{}))); got != ReasonAnchorInvalid {
		t.Fatalf("SHA-1 anchor: %q", got)
	}
	// An anchor that has not started yet is not an anchor, even by a second.
	if got := ReasonOf(check(rsaRoot(x509.SHA256WithRSA, testNow.Add(time.Second)))); got != ReasonAnchorInvalid {
		t.Fatalf("anchor starting in a second: %q", got)
	}
	if err := check(rsaRoot(x509.SHA256WithRSA, testNow)); err != nil {
		t.Fatalf("anchor starting now: %v", err)
	}
}

// The RSA bounds are on the bit length of the modulus, so the edges are tested on the policy
// directly (a real 8192-bit key takes far too long to generate for a unit test).
func TestCheckPublicKeyRSABounds(t *testing.T) {
	bits := func(n int) *rsa.PublicKey {
		return &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), uint(n-1)), E: 65537}
	}
	for n, want := range map[int]bool{
		2047: false, 2048: true, 4096: true, MaxRSABits: true, MaxRSABits + 1: false, 16384: false,
	} {
		err := CheckPublicKey(bits(n))
		if want && err != nil {
			t.Errorf("%d bits must be allowed: %v", n, err)
		}
		if !want && !errors.Is(err, ErrKeyPolicy) {
			t.Errorf("%d bits must be refused, got %v", n, err)
		}
	}
}

// RSA-3072 is inside the bounds and verifies.
func TestVerifyAcceptsRSA3072(t *testing.T) {
	w := baseline()
	w.leaf = spec{cn: "d", key: rsaKey(t, 3072)}
	requireAccepted(t, "RSA-3072", w)
}

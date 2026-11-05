// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestVerifyCAChain(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	inter := issue(t, root, spec{cn: "inter", ca: true})
	inter2 := issue(t, inter, spec{cn: "inter2", ca: true})

	ok := func(name string, anchor *node, chain ...*node) {
		t.Helper()
		if err := VerifyCAChain(anchor.cert, chainOf(chain...), testNow); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	ok("the anchor itself", root, root)
	ok("one level", root, inter)
	ok("two levels with the linking CA", root, inter2, inter)

	refuse := func(name string, want Reason, anchor *x509.Certificate, chain []*x509.Certificate, now time.Time) {
		t.Helper()
		err := VerifyCAChain(anchor, chain, now)
		if err == nil || ReasonOf(err) != want {
			t.Fatalf("%s: want %q, got %v", name, want, err)
		}
	}
	refuse("empty", ReasonNoCertificate, root.cert, nil, testNow)
	refuse("no anchor", ReasonAnchorInvalid, nil, chainOf(inter), testNow)
	refuse("missing link", ReasonUntrusted, root.cert, chainOf(inter2), testNow)
	other := issue(t, nil, spec{cn: "other", ca: true})
	refuse("foreign anchor", ReasonUntrusted, other.cert, chainOf(inter), testNow)

	leaf := issue(t, inter, spec{cn: "leaf", ku: x509.KeyUsageCRLSign})
	refuse("signer is not a CA", ReasonNotCA, root.cert, chainOf(leaf, inter), testNow)
	noSign := issue(t, root, spec{cn: "ns", ca: true, ku: x509.KeyUsageCertSign})
	refuse("signer lacks cRLSign", ReasonNoCRLSign, root.cert, chainOf(noSign), testNow)
	noKU := issue(t, root, spec{cn: "nku", ca: true, mutate: func(c *x509.Certificate) { c.KeyUsage = 0 }})
	refuse("absent keyUsage is not 'any'", ReasonNoCRLSign, root.cert, chainOf(noKU), testNow)

	// Unlike a device chain, extended key usage on the path is not demanded or enforced.
	eku := issue(t, root, spec{cn: "eku", ca: true, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	ok("a CA with an unrelated EKU", root, eku)

	expired := issue(t, root, spec{cn: "old", ca: true, notBefore: testNow.Add(-48 * time.Hour), notAfter: testNow.Add(-time.Hour)})
	refuse("expired signer", ReasonExpired, root.cert, chainOf(expired), testNow)
	future := issue(t, root, spec{cn: "new", ca: true, notBefore: testNow.Add(time.Hour), notAfter: testNow.Add(48 * time.Hour)})
	refuse("not yet valid beyond the skew", ReasonNotYetValid, root.cert, chainOf(future), testNow)
	soon := issue(t, root, spec{cn: "soon", ca: true, notBefore: testNow.Add(time.Minute), notAfter: testNow.Add(48 * time.Hour)})
	ok("within the not-before skew", root, soon)

	rsaRoot := issue(t, nil, spec{cn: "rsa-root", ca: true, key: rsaKey(t, 2048)})
	weak := issue(t, rsaRoot, spec{cn: "weak", ca: true, sigAlg: x509.SHA1WithRSA})
	refuse("weak signature", ReasonWeakSignature, rsaRoot.cert, chainOf(weak), testNow)
	strong := issue(t, rsaRoot, spec{cn: "strong", ca: true, sigAlg: x509.SHA256WithRSA})
	ok("control for the weak-signature case", rsaRoot, strong)
	small := issue(t, root, spec{cn: "small", ca: true, key: rsaKey(t, 1024)})
	refuse("key policy", ReasonKeyPolicy, root.cert, chainOf(small), testNow)

	constrained := issue(t, root, spec{cn: "nc", ca: true, mutate: func(c *x509.Certificate) { c.PermittedDNSDomains = []string{".a.example"} }})
	refuse("name constraints on the signer", ReasonNameConstraints, root.cert, chainOf(constrained), testNow)
	refuse("name constraints on a linking CA", ReasonNameConstraints, root.cert, chainOf(inter2, constrained), testNow)
	cRoot := issue(t, nil, spec{cn: "cr", ca: true, mutate: func(c *x509.Certificate) { c.PermittedDNSDomains = []string{".a.example"} }})
	refuse("name constraints on the anchor", ReasonNameConstraints, cRoot.cert, chainOf(cRoot), testNow)

	var many []*x509.Certificate
	for i := 0; i < MaxPresentedCerts+1; i++ {
		many = append(many, inter.cert)
	}
	refuse("too many", ReasonTooLong, root.cert, many, testNow)
	big := issue(t, root, spec{cn: "big", ca: true, pad: MaxPresentedDERBytes})
	refuse("too large", ReasonTooLarge, root.cert, chainOf(big), testNow)
	refuse("nil entry", ReasonMalformed, root.cert, []*x509.Certificate{nil}, testNow)
	refuse("anchor expired", ReasonAnchorInvalid, root.cert, chainOf(inter), root.cert.NotAfter.Add(time.Hour))

	// Path length is the verifier's: a CA below a pathlen-0 intermediate is refused.
	p0 := issue(t, root, spec{cn: "p0", ca: true, mutate: func(c *x509.Certificate) { c.MaxPathLen = 0; c.MaxPathLenZero = true }})
	under := issue(t, p0, spec{cn: "under", ca: true})
	ok("the signer itself may sit directly under a pathlen-0 CA", root, under, p0)
	under2 := issue(t, under, spec{cn: "under2", ca: true})
	refuse("path length", ReasonUntrusted, root.cert, chainOf(under2, under, p0), testNow)
}

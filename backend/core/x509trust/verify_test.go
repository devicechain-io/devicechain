// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"
)

// world is a root -> intermediate -> leaf PKI described by three specs. A test changes ONE
// field of the baseline (which TestVerifyBaseline proves succeeds) and names the refusal that
// field must cause: that is the negative control for each refusal below.
type world struct {
	root, inter, leaf spec
	// anchorIsInter makes the intermediate the trust anchor, so the root is not trusted.
	anchorIsInter bool
	// extra intermediates between inter and leaf, when a deep chain is wanted.
	deeper int
}

type built struct {
	anchor    *node
	presented []*x509.Certificate
	root      *node
	inter     *node
	leaf      *node
}

func (w world) build(t *testing.T) built {
	t.Helper()
	root := issue(t, nil, w.root)
	inter := issue(t, root, w.inter)
	parent := inter
	presented := []*node{}
	var mids []*node
	for i := 0; i < w.deeper; i++ {
		m := issue(t, parent, spec{cn: "mid", ca: true})
		mids = append(mids, m)
		parent = m
	}
	leaf := issue(t, parent, w.leaf)
	presented = append(presented, leaf)
	for i := len(mids) - 1; i >= 0; i-- {
		presented = append(presented, mids[i])
	}
	presented = append(presented, inter)
	anchor := root
	if w.anchorIsInter {
		anchor = inter
		presented = presented[:len(presented)-1]
	}
	return built{anchor: anchor, presented: chainOf(presented...), root: root, inter: inter, leaf: leaf}
}

func baseline() world {
	return world{
		root:  spec{cn: "root", ca: true},
		inter: spec{cn: "inter", ca: true},
		leaf:  spec{cn: "device-1"},
	}
}

func (b built) verify() (*Verified, error) {
	return VerifyClientChain(b.anchor.cert, b.presented, testNow)
}

func requireRefused(t *testing.T, name string, w world, want Reason) {
	t.Helper()
	b := w.build(t)
	v, err := b.verify()
	if err == nil {
		t.Fatalf("%s: the chain was accepted; want refusal %q", name, want)
	}
	if v != nil {
		t.Fatalf("%s: a refusal must not return a result", name)
	}
	if !errors.Is(err, ErrChainInvalid) {
		t.Fatalf("%s: error %v does not wrap ErrChainInvalid", name, err)
	}
	if got := ReasonOf(err); got != want {
		t.Fatalf("%s: refused as %q (%v); want %q", name, got, err, want)
	}
}

func requireAccepted(t *testing.T, name string, w world) *Verified {
	t.Helper()
	v, err := w.build(t).verify()
	if err != nil {
		t.Fatalf("%s: the control must succeed: %v", name, err)
	}
	return v
}

// The paired positive: everything below is this world with one thing broken.
func TestVerifyBaseline(t *testing.T) {
	b := baseline().build(t)
	v, err := b.verify()
	if err != nil {
		t.Fatal(err)
	}
	if v.Fingerprint != FingerprintDER(b.leaf.cert.Raw) || v.Fingerprint != Fingerprint(b.leaf.cert) {
		t.Fatalf("fingerprint %q is not the SHA-256 of the leaf DER", v.Fingerprint)
	}
	if len(v.Chain) != 3 || v.Chain[0] != b.leaf.cert || !v.Chain[2].Equal(b.root.cert) {
		t.Fatalf("verified path is wrong: %d certificates", len(v.Chain))
	}
	if !v.NotAfter.Equal(testNow.Add(24 * time.Hour)) {
		t.Fatalf("NotAfter = %v", v.NotAfter)
	}
}

func TestVerifyRefusesExpired(t *testing.T) {
	w := baseline()
	requireAccepted(t, "control", w)

	w.leaf.notAfter = testNow.Add(-time.Minute)
	w.leaf.notBefore = testNow.Add(-time.Hour)
	requireRefused(t, "expired leaf", w, ReasonExpired)

	w = baseline()
	w.inter.notAfter = testNow.Add(-time.Minute)
	requireRefused(t, "expired intermediate", w, ReasonExpired)

	// Expiry by one second: the boundary is inclusive of notAfter, exclusive after.
	w = baseline()
	w.leaf.notAfter = testNow.Add(-time.Second)
	requireRefused(t, "expired a second ago", w, ReasonExpired)
	w.leaf.notAfter = testNow.Add(time.Second)
	requireAccepted(t, "expires in a second", w)
}

func TestVerifyNotBeforeSkew(t *testing.T) {
	w := baseline()
	requireAccepted(t, "control", w)

	// Inside the 5-minute allowance: accepted, verified at notBefore.
	w.leaf.notBefore = testNow.Add(3 * time.Minute)
	w.leaf.notAfter = testNow.Add(24 * time.Hour)
	v := requireAccepted(t, "3 minutes early", w)
	if !v.NotAfter.Equal(testNow.Add(24 * time.Hour)) {
		t.Fatalf("notAfter is still the leaf's, got %v", v.NotAfter)
	}

	// The edge of the allowance is inclusive, one second past it is not.
	w.leaf.notBefore = testNow.Add(NotBeforeSkew)
	requireAccepted(t, "exactly at the skew", w)
	w.leaf.notBefore = testNow.Add(NotBeforeSkew + time.Second)
	requireRefused(t, "just beyond the skew", w, ReasonNotYetValid)

	w.leaf.notBefore = testNow.Add(time.Hour)
	requireRefused(t, "an hour early", w, ReasonNotYetValid)

	// The allowance does not rescue a leaf that has already ended.
	w = baseline()
	w.leaf.notBefore = testNow.Add(-2 * time.Hour)
	w.leaf.notAfter = testNow.Add(-time.Hour)
	requireRefused(t, "skew is not an expiry pardon", w, ReasonExpired)
}

func TestVerifyRefusesTheWrongAnchor(t *testing.T) {
	b := baseline().build(t)
	if _, err := b.verify(); err != nil {
		t.Fatalf("control: %v", err)
	}
	other := issue(t, nil, spec{cn: "other root", ca: true})
	_, err := VerifyClientChain(other.cert, b.presented, testNow)
	if got := ReasonOf(err); got != ReasonUntrusted {
		t.Fatalf("unrelated anchor: %q (%v)", got, err)
	}

	// An impostor with the SAME subject name but a different key must not pass for the anchor:
	// the leaf below chains to the impostor, so the real root refuses it.
	impostorRoot := issue(t, nil, spec{cn: "root", ca: true})
	impostorInter := issue(t, impostorRoot, spec{cn: "inter", ca: true})
	forged := issue(t, impostorInter, spec{cn: "device-1"})
	_, err = VerifyClientChain(b.root.cert, chainOf(forged, impostorInter), testNow)
	if got := ReasonOf(err); got != ReasonUntrusted {
		t.Fatalf("same-name impostor: %q (%v)", got, err)
	}
}

func TestVerifyRequiresAnExplicitClientAuthUsage(t *testing.T) {
	w := baseline()
	requireAccepted(t, "control (clientAuth)", w)

	w.leaf.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	requireRefused(t, "serverAuth only", w, ReasonNotClientAuth)

	w.leaf.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageAny}
	requireRefused(t, "anyExtendedKeyUsage only", w, ReasonNotClientAuth)

	// No EKU extension at all: Go would treat that as "any use"; a client certificate must say
	// it is one.
	w.leaf.eku = nil
	w.leaf.noEKU = true
	requireRefused(t, "no EKU extension", w, ReasonNotClientAuth)

	// clientAuth among others is fine.
	w.leaf.noEKU = false
	w.leaf.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	requireAccepted(t, "clientAuth + serverAuth", w)

	// An intermediate constrained to serverAuth cannot vouch for a client certificate.
	w = baseline()
	w.inter.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	requireRefused(t, "intermediate limited to serverAuth", w, ReasonNotClientAuth)
	w.inter.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	requireAccepted(t, "intermediate limited to clientAuth", w)
}

func TestVerifyRefusesACALeaf(t *testing.T) {
	requireAccepted(t, "control", baseline())
	w := baseline()
	w.leaf = spec{cn: "ca-as-leaf", ca: true, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	requireRefused(t, "CA leaf", w, ReasonLeafIsCA)
}

func TestVerifyRefusesSHA1(t *testing.T) {
	// SHA-1 needs an RSA signer, so the intermediate (which signs the leaf) is RSA here.
	sha1World := func(leafAlg, interAlg x509.SignatureAlgorithm) world {
		w := baseline()
		w.root.key = rsaKey(t, 2048)
		w.inter.key = rsaKey(t, 2048)
		w.inter.sigAlg = interAlg
		w.leaf.sigAlg = leafAlg
		return w
	}
	requireAccepted(t, "control (SHA-256 RSA)", sha1World(x509.SHA256WithRSA, x509.SHA256WithRSA))
	requireRefused(t, "SHA-1 leaf", sha1World(x509.SHA1WithRSA, x509.SHA256WithRSA), ReasonWeakSignature)
	requireRefused(t, "SHA-1 intermediate", sha1World(x509.SHA256WithRSA, x509.SHA1WithRSA), ReasonWeakSignature)
}

func TestVerifyKeyPolicy(t *testing.T) {
	// Accepted: the allowed algorithms.
	for name, k := range map[string]func() spec{
		"RSA-2048": func() spec { return spec{cn: "d", key: rsaKey(t, 2048)} },
		"P-256":    func() spec { return spec{cn: "d", key: ecKey(t)} },
		"Ed25519":  func() spec { return spec{cn: "d", key: edKey(t)} },
	} {
		w := baseline()
		w.leaf = k()
		requireAccepted(t, name, w)
	}

	w := baseline()
	w.leaf = spec{cn: "d", key: rsaKey(t, 1024)}
	requireRefused(t, "RSA-1024 leaf", w, ReasonKeyPolicy)

	w = baseline()
	w.leaf = spec{cn: "d", key: p521Key(t)}
	requireRefused(t, "P-521 leaf", w, ReasonKeyPolicy)

	w = baseline()
	w.inter.key = rsaKey(t, 1024)
	requireRefused(t, "RSA-1024 intermediate", w, ReasonKeyPolicy)

	// The anchor's own key is held to the policy too: it is refused as an anchor.
	w = baseline()
	w.root.key = rsaKey(t, 1024)
	requireRefused(t, "RSA-1024 anchor", w, ReasonAnchorInvalid)
}

// A weak key is named as such even when the chain would not have built anyway: the policy is
// applied to what was presented before the path is searched.
func TestVerifyKeyPolicyPrecedesPathBuilding(t *testing.T) {
	w := baseline()
	w.leaf = spec{cn: "d", key: rsaKey(t, 1024)}
	b := w.build(t)
	other := issue(t, nil, spec{cn: "other root", ca: true})
	_, err := VerifyClientChain(other.cert, b.presented, testNow)
	if got := ReasonOf(err); got != ReasonKeyPolicy {
		t.Fatalf("%q (%v)", got, err)
	}
}

func TestVerifyChainDepth(t *testing.T) {
	// leaf + 4 intermediates (inter + 3 more) = 5 presented: the limit.
	w := baseline()
	w.deeper = 3
	b := w.build(t)
	if len(b.presented) != MaxPresentedCerts {
		t.Fatalf("setup: %d presented", len(b.presented))
	}
	if _, err := b.verify(); err != nil {
		t.Fatalf("control (5 certificates): %v", err)
	}

	// leaf + 5 intermediates = 6 presented (a depth-6 path with the anchor): refused.
	w.deeper = 4
	b = w.build(t)
	if len(b.presented) != MaxPresentedCerts+1 {
		t.Fatalf("setup: %d presented", len(b.presented))
	}
	_, err := b.verify()
	if got := ReasonOf(err); got != ReasonTooLong {
		t.Fatalf("6 certificates: %q (%v)", got, err)
	}
}

func TestVerifyChainSize(t *testing.T) {
	w := baseline()
	w.leaf.pad = 1 << 10
	requireAccepted(t, "control (1 KiB of extension)", w)

	w.leaf.pad = MaxPresentedDERBytes + 1
	requireRefused(t, "over 16 KiB", w, ReasonTooLarge)

	// The bound is on the whole presentation, not on one certificate.
	w = baseline()
	w.inter.pad = MaxPresentedDERBytes/2 + 512
	w.leaf.pad = MaxPresentedDERBytes/2 + 512
	requireRefused(t, "two certificates summing over 16 KiB", w, ReasonTooLarge)
}

func TestVerifyIntermediateAnchor(t *testing.T) {
	// The intermediate as anchor: the leaf alone verifies, and the root is not needed.
	w := baseline()
	w.anchorIsInter = true
	requireAccepted(t, "intermediate as anchor", w)

	// The root as anchor, given only the leaf, cannot: its intermediate is missing. The
	// anchor choice is what trusts a path; a root above an anchor confers nothing.
	b := baseline().build(t)
	_, err := VerifyClientChain(b.root.cert, chainOf(b.leaf), testNow)
	if got := ReasonOf(err); got != ReasonUntrusted {
		t.Fatalf("root without its intermediate: %q (%v)", got, err)
	}
	// With the intermediate presented, the root anchors it.
	if _, err := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter), testNow); err != nil {
		t.Fatalf("control: %v", err)
	}
	// The intermediate does not trust the root's other children: a sibling intermediate's leaf
	// is refused against this intermediate as the anchor.
	sibling := issue(t, b.root, spec{cn: "sibling", ca: true})
	siblingLeaf := issue(t, sibling, spec{cn: "device-2"})
	_, err = VerifyClientChain(b.inter.cert, chainOf(siblingLeaf, sibling), testNow)
	if got := ReasonOf(err); got != ReasonUntrusted {
		t.Fatalf("a sibling's leaf against an intermediate anchor: %q (%v)", got, err)
	}
}

func TestVerifyNotAfterIsTheMinimumOverTheChain(t *testing.T) {
	leafEnd := testNow.Add(72 * time.Hour)
	interEnd := testNow.Add(10 * time.Hour)
	rootEnd := testNow.Add(48 * time.Hour)

	w := baseline()
	w.leaf.notAfter = leafEnd
	w.inter.notAfter = interEnd
	w.root.notAfter = rootEnd
	if v := requireAccepted(t, "intermediate ends first", w); !v.NotAfter.Equal(interEnd) {
		t.Fatalf("NotAfter = %v, want the intermediate's %v", v.NotAfter, interEnd)
	}

	w.inter.notAfter = testNow.Add(60 * time.Hour)
	if v := requireAccepted(t, "anchor ends first", w); !v.NotAfter.Equal(rootEnd) {
		t.Fatalf("NotAfter = %v, want the anchor's %v", v.NotAfter, rootEnd)
	}

	w.root.notAfter = testNow.Add(96 * time.Hour)
	if v := requireAccepted(t, "leaf ends first", w); !v.NotAfter.Equal(testNow.Add(60 * time.Hour)) {
		// inter (60h) < leaf (72h)
		t.Fatalf("NotAfter = %v", v.NotAfter)
	}
	w.inter.notAfter = testNow.Add(96 * time.Hour)
	if v := requireAccepted(t, "leaf ends first", w); !v.NotAfter.Equal(leafEnd) {
		t.Fatalf("NotAfter = %v, want the leaf's %v", v.NotAfter, leafEnd)
	}
}

func TestVerifyAnchorRules(t *testing.T) {
	b := baseline().build(t)
	if _, err := VerifyClientChain(b.root.cert, b.presented, testNow); err != nil {
		t.Fatalf("control: %v", err)
	}
	for name, a := range map[string]*x509.Certificate{
		"nil":              nil,
		"a non-CA":         issue(t, nil, spec{cn: "leafy"}).cert,
		"without certSign": issue(t, nil, spec{cn: "r", ca: true, ku: x509.KeyUsageCRLSign}).cert,
		"expired anchor":   issue(t, nil, spec{cn: "r", ca: true, notBefore: testNow.Add(-48 * time.Hour), notAfter: testNow.Add(-time.Hour)}).cert,
	} {
		_, err := VerifyClientChain(a, b.presented, testNow)
		if got := ReasonOf(err); got != ReasonAnchorInvalid {
			t.Fatalf("%s: %q (%v)", name, got, err)
		}
	}
}

func TestVerifyNothingPresented(t *testing.T) {
	b := baseline().build(t)
	_, err := VerifyClientChain(b.root.cert, nil, testNow)
	if got := ReasonOf(err); got != ReasonNoCertificate {
		t.Fatalf("%q (%v)", got, err)
	}
}

// A refusal names the reason and never the certificate: these errors reach logs.
func TestVerifyErrorsCarryNoCertificateContent(t *testing.T) {
	w := baseline()
	w.leaf.cn = "secret-device-name"
	w.leaf.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	b := w.build(t)
	_, err := b.verify()
	if err == nil || strings.Contains(err.Error(), "secret-device-name") {
		t.Fatalf("error leaks the subject or is nil: %v", err)
	}
}

func TestReasonOfIsEmptyForForeignErrors(t *testing.T) {
	if ReasonOf(errors.New("x")) != "" || ReasonOf(nil) != "" {
		t.Fatal("ReasonOf must be empty for errors that are not chain refusals")
	}
}

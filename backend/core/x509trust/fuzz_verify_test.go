// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// FuzzVerifyClientChain feeds the verifier an anchor, a presentation and a clock, all mutated,
// and checks every ACCEPTED answer against facts that do not depend on how the verifier reached
// it. Most inputs are refused; what matters is that nothing accepted is wrong.
func FuzzVerifyClientChain(f *testing.F) {
	p := newBasicPKI(f)
	f.Add(p.root.pem(), p.leaf.pem()+p.inter.pem(), int64(0))
	f.Add(p.inter.pem(), p.leaf.pem(), int64(0))
	f.Add(p.root.pem(), p.leaf.pem()+p.inter.pem(), int64(3600*30))
	f.Add(p.root.pem(), p.leaf.pem()+p.inter.pem(), int64(-3600*30))
	f.Add(p.root.pem(), p.inter.pem()+p.leaf.pem(), int64(0))
	f.Add(p.leaf.pem(), p.leaf.pem(), int64(0))
	f.Add("", "", int64(0))

	f.Fuzz(func(t *testing.T, anchorPEM, chainPEM string, offsetSeconds int64) {
		a, err := ParseTrustAnchor(anchorPEM, testNow)
		if err != nil {
			return
		}
		presented := decodeAll(chainPEM)
		now := testNow.Add(time.Duration(offsetSeconds%(1<<30)) * time.Second)

		v, err := VerifyClientChain(a.Certificate, presented, now)
		if err != nil {
			if v != nil || ReasonOf(err) == "" {
				t.Fatalf("a refusal must return nothing and carry a reason: %v", err)
			}
			return
		}
		leaf := presented[0]
		if v.Leaf != leaf || v.Fingerprint != Fingerprint(leaf) {
			t.Fatal("the result is not about the presented leaf")
		}
		if leaf.IsCA {
			t.Fatal("accepted a CA as a client certificate")
		}
		if !hasClientAuth(leaf) {
			t.Fatal("accepted a leaf with no explicit clientAuth usage")
		}
		if len(presented) > MaxPresentedCerts {
			t.Fatalf("accepted %d presented certificates", len(presented))
		}
		if len(v.Chain) < 2 || v.Chain[0] != leaf || !v.Chain[len(v.Chain)-1].Equal(a.Certificate) {
			t.Fatal("the verified path does not run from the leaf to the anchor")
		}
		min := v.Chain[0].NotAfter
		for _, c := range v.Chain {
			if hasNameConstraints(c) && c != leaf {
				t.Fatal("accepted a chain with a name-constrained CA")
			}
			if weakSignature(c) && c != a.Certificate {
				t.Fatal("accepted a weak signature")
			}
			if CheckPublicKey(c.PublicKey) != nil {
				t.Fatal("accepted a key outside the policy")
			}
			if c.NotAfter.Before(min) {
				min = c.NotAfter
			}
		}
		if v.NotAfter.After(min.UTC()) {
			t.Fatal("NotAfter is later than a certificate on the path expires")
		}
		if !now.Before(v.NotAfter) {
			t.Fatal("accepted a chain that had already ended")
		}
	})
}

// decodeAll reads every CERTIFICATE block of text that parses, in order.
func decodeAll(text string) []*x509.Certificate {
	var out []*x509.Certificate
	rest := []byte(text)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return out
		}
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			out = append(out, c)
		}
	}
}

// FuzzParseChainPEMOracle states what every ACCEPTED presentation must satisfy, whatever the
// text: one certificate per entry, in order, within the bounds, each of which re-encodes to a
// presentation that is accepted again and unchanged.
func FuzzParseChainPEMOracle(f *testing.F) {
	p := newBasicPKI(f)
	f.Add(p.leaf.pem(), p.inter.pem(), "")
	f.Add(p.leaf.pem(), "", "")
	f.Add("x", p.inter.pem(), p.root.pem())

	f.Fuzz(func(t *testing.T, a, b, c string) {
		entries := []string{a, b, c}
		certs, err := ParseChainPEM(entries)
		if err != nil {
			if certs != nil || ReasonOf(err) == "" {
				t.Fatalf("a refusal must return nothing and carry a reason: %v", err)
			}
			return
		}
		if len(certs) != len(entries) {
			t.Fatalf("%d entries produced %d certificates", len(entries), len(certs))
		}
		again := make([]string, len(certs))
		size := 0
		for i, cert := range certs {
			if cert == nil || len(cert.Raw) == 0 {
				t.Fatal("an accepted entry produced no certificate")
			}
			size += len(cert.Raw)
			again[i] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
			if strings.Count(entries[i], "-----BEGIN ") != 1 {
				t.Fatalf("entry %d did not hold exactly one block", i)
			}
		}
		if size > 2*MaxPresentedDERBytes {
			t.Fatalf("accepted %d bytes of DER", size)
		}
		round, err := ParseChainPEM(again)
		if err != nil || len(round) != len(certs) {
			t.Fatalf("a re-encoded presentation was not accepted: %v", err)
		}
		for i := range round {
			if !round[i].Equal(certs[i]) {
				t.Fatalf("certificate %d changed on re-encoding", i)
			}
		}
	})
}

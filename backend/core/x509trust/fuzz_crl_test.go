// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509/pkix"
	"testing"
	"time"
)

// FuzzParseRevocationList feeds the CRL parser mutated bytes, a clock and a bound, under a fixed
// anchor and one intermediate. A refusal must be classified and return nothing; an ACCEPTED
// list is re-checked against facts that do not depend on how the parser reached it. Seeds are
// valid CRLs, each rule-breaking shape of the table tests, and PEM framing.
func FuzzParseRevocationList(f *testing.F) {
	root := issue(f, nil, spec{cn: "root", ca: true})
	inter := issue(f, root, spec{cn: "inter", ca: true})

	good := craftCRL(f, goodOpts(root))
	many := goodOpts(inter)
	many.entries = []pkix.RevokedCertificate{entry(1, reasonExt(1)), entry(300), entry(70000, reasonExt(6))}
	seeds := [][]byte{
		good,
		pemCRL(good),
		craftCRL(f, many),
		pemCRL(craftCRL(f, many)),
		append(pemCRL(good), pemCRL(good)...),
		good[:len(good)/2],
		append(append([]byte(nil), good...), 0),
		{},
	}
	o := goodOpts(root)
	o.exts = []pkix.Extension{{Id: oidExtIssuingDistPoint, Value: []byte{0x30, 0x03, 0x85, 0x01, 0xff}}}
	seeds = append(seeds, craftCRL(f, o))
	for _, s := range seeds {
		f.Add(s, int64(0), uint8(0))
	}
	f.Add(good, int64(-3600*100), uint8(1))
	f.Add(good, int64(3600*100), uint8(2))

	f.Fuzz(func(t *testing.T, data []byte, offsetSeconds int64, mode uint8) {
		now := testNow.Add(time.Duration(offsetSeconds%(1<<30)) * time.Second)
		var chain []*node
		if mode%2 == 1 {
			chain = []*node{inter}
		}
		lim := CRLLimits{MaxBytes: 1 << 20, MaxEntries: 64}
		var certs = chainOf(chain...)
		rl, err := ParseRevocationListLimits(data, certs, root.cert, now, lim)
		if err != nil {
			if rl != nil || ListRuleOf(err) == "" {
				t.Fatalf("a refusal must return nothing and carry a rule: %v", err)
			}
			return
		}
		if len(data) > lim.MaxBytes {
			t.Fatal("accepted over the byte bound")
		}
		checkAcceptedList(t, rl, now, lim)
		// The accepted signer is the anchor or the supplied intermediate, nothing else.
		if !rl.Signer.Equal(root.cert) && !(len(certs) == 1 && rl.Signer.Equal(inter.cert)) {
			t.Fatal("accepted a signer that was not offered")
		}
	})
}

// FuzzApplyRevocation drives ApplyRevocation over a cross-signed PKI with an arbitrary subset of
// its certificates revoked (by fingerprint or by issuer+serial), an arbitrary subset of issuers
// listed, either mode and a moving clock. The expected survivors are derived here from the two
// paths directly, so the property is "ApplyRevocation agrees with the obvious reading", not
// "ApplyRevocation agrees with itself".
func FuzzApplyRevocation(f *testing.F) {
	p := newCrossPKI(f)
	v := p.verify(f)
	rootID, ixID := IssuerIDOfCA(p.root.cert), IssuerIDOfCA(p.ixNew.cert)
	paths := map[string][]*node{"new": {p.leaf, p.ixNew}, "old": {p.leaf, p.ixOld}}
	issuerOf := func(i int) IssuerID { // issuer of the i-th cert on a path
		if i == 0 {
			return ixID
		}
		return rootID
	}
	f.Add(uint16(0), uint8(0), int64(0), int64(0))
	f.Add(uint16(0b000001), uint8(1), int64(0), int64(0))
	f.Add(uint16(0b001000), uint8(0), int64(0), int64(0))
	f.Add(uint16(0b100100), uint8(3), int64(3600), int64(60))
	f.Add(uint16(0xffff), uint8(2), int64(-3600), int64(0))

	f.Fuzz(func(t *testing.T, bits uint16, flags uint8, offsetSeconds, graceSeconds int64) {
		fp := map[string]bool{}
		ser := map[IssuerSerial]bool{}
		st := RevocationState{
			Now:                 testNow.Add(time.Duration(offsetSeconds%(1<<20)) * time.Second),
			StaleGrace:          time.Duration(uint64(graceSeconds)%(1<<20)) * time.Second,
			RevokedFingerprints: map[string]RevocationReason{},
			RevokedSerials:      map[IssuerSerial]RevocationReason{},
			Lists:               map[IssuerID]ListStatus{},
		}
		st.Mode = ModeEntries
		if flags&1 == 1 {
			st.Mode = ModeRequireCurrent
		}
		certs := []*node{p.leaf, p.ixOld, p.ixNew}
		for i, c := range certs {
			if bits&(1<<i) != 0 {
				fp[Fingerprint(c.cert)] = true
				st.RevokedFingerprints[Fingerprint(c.cert)] = RevocationUnspecified
			}
			if bits&(1<<(i+3)) != 0 {
				// The serial entry is under the issuer the cert really has.
				is := IssuerSerial{Issuer: rootID, Serial: mustSerial(t, c.cert)}
				if i == 0 {
					is.Issuer = ixID
				}
				ser[is] = true
				st.RevokedSerials[is] = RevocationUnspecified
			}
		}
		// Lists: bit 6 root, bit 7 ix; both stale-or-fresh by bit 8.
		next := testNow.Add(time.Hour)
		if bits&(1<<8) != 0 {
			next = testNow.Add(-time.Hour)
		}
		if bits&(1<<6) != 0 {
			st.Lists[rootID] = ListStatus{NextUpdate: next}
		}
		if bits&(1<<7) != 0 {
			st.Lists[ixID] = ListStatus{NextUpdate: next}
		}

		// The oracle.
		type verdict struct{ revoked, unknown bool }
		judge := func(path []*node) verdict {
			var vd verdict
			for i, c := range path {
				if fp[Fingerprint(c.cert)] {
					vd.revoked = true
				}
				s, _ := SerialHex(c.cert.SerialNumber)
				if ser[IssuerSerial{Issuer: issuerOf(i), Serial: s}] {
					vd.revoked = true
				}
			}
			if vd.revoked {
				return vd
			}
			if st.Mode == ModeRequireCurrent {
				for i := range path {
					l, ok := st.Lists[issuerOf(i)]
					if !ok || st.Now.After(l.NextUpdate.Add(st.StaleGrace)) {
						vd.unknown = true
					}
				}
			}
			return vd
		}
		wantClear := map[string]bool{}
		revoked, unknown := 0, 0
		for name, path := range paths {
			switch vd := judge(path); {
			case vd.revoked:
				revoked++
			case vd.unknown:
				unknown++
			default:
				wantClear[name] = true
			}
		}

		out, err := v.ApplyRevocation(st)
		if len(wantClear) == 0 {
			if out != nil || err == nil {
				t.Fatalf("admitted with every path dropped: %v", out)
			}
			want := ReasonRevocationUnknown
			if unknown == 0 && revoked > 0 {
				want = ReasonRevoked
			}
			if ReasonOf(err) != want {
				t.Fatalf("reason %q, want %q", ReasonOf(err), want)
			}
			return
		}
		if err != nil || out == nil {
			t.Fatalf("refused with a clear path: %v", err)
		}
		if len(out.Chains) != len(wantClear) {
			t.Fatalf("%d survivors, want %d", len(out.Chains), len(wantClear))
		}
		for _, c := range out.Chains {
			name := "old"
			if hasCert(c, p.ixNew.cert) {
				name = "new"
			}
			if !wantClear[name] {
				t.Fatalf("path %s survived but should have been dropped", name)
			}
		}
		// Longest-lived survivor: "new" outlives "old" (20h vs 10h).
		wantNew := wantClear["new"]
		if hasCert(out.Chain, p.ixNew.cert) != wantNew {
			t.Fatal("the picked path is not the longest-lived survivor")
		}
		wantNA := testNow.Add(10 * time.Hour)
		if wantNew {
			wantNA = testNow.Add(20 * time.Hour)
		}
		if !out.NotAfter.Equal(wantNA) {
			t.Fatalf("notAfter %v, want %v", out.NotAfter, wantNA)
		}
	})
}

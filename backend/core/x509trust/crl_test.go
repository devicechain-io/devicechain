// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// crlOpts describes a CRL built by hand, byte for byte, so every rule can be broken one field at
// a time (x509.CreateRevocationList refuses to emit most of the malformed shapes).
type crlOpts struct {
	signer    *node
	issuerRaw []byte // CRL issuer; defaults to the signer's subject
	number    *big.Int
	noNumber  bool
	this      time.Time
	next      time.Time // zero = omitted
	entries   []pkix.RevokedCertificate
	exts      []pkix.Extension // extra CRL extensions
	sigAlg    asn1.ObjectIdentifier
	flipSig   bool
	signWith  crypto.Signer               // defaults to signer.key
	rawTBS    func(content []byte) []byte // edits the TBS content before it is signed
}

var (
	oidECDSAWithSHA256 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidECDSAWithSHA1   = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 1}
)

func crlNumberExt(n *big.Int) pkix.Extension {
	v, err := asn1.Marshal(n)
	if err != nil {
		panic(err)
	}
	return pkix.Extension{Id: oidExtCRLNumber, Value: v}
}

func reasonExt(code int) pkix.Extension {
	v, err := asn1.Marshal(asn1.Enumerated(code))
	if err != nil {
		panic(err)
	}
	return pkix.Extension{Id: oidExtReasonCode, Value: v}
}

func entry(serial int64, exts ...pkix.Extension) pkix.RevokedCertificate {
	return pkix.RevokedCertificate{SerialNumber: big.NewInt(serial), RevocationTime: testNow.Add(-time.Hour), Extensions: exts}
}

// craftCRL returns the DER of a CRL; the zero crlOpts (plus a signer) is valid at testNow.
func craftCRL(t testing.TB, o crlOpts) []byte {
	t.Helper()
	if o.this.IsZero() {
		o.this = testNow.Add(-time.Hour)
	}
	if o.number == nil {
		o.number = big.NewInt(1)
	}
	if o.sigAlg == nil {
		o.sigAlg = oidECDSAWithSHA256
	}
	if o.issuerRaw == nil {
		o.issuerRaw = o.signer.cert.RawSubject
	}
	if o.signWith == nil {
		o.signWith = o.signer.key
	}
	var issuer pkix.RDNSequence
	if _, err := asn1.Unmarshal(o.issuerRaw, &issuer); err != nil {
		t.Fatal(err)
	}
	exts := append([]pkix.Extension(nil), o.exts...)
	if !o.noNumber {
		exts = append([]pkix.Extension{crlNumberExt(o.number)}, exts...)
	}
	alg := pkix.AlgorithmIdentifier{Algorithm: o.sigAlg}
	tbs := pkix.TBSCertificateList{
		Version:             1,
		Signature:           alg,
		Issuer:              issuer,
		ThisUpdate:          o.this.UTC(),
		NextUpdate:          o.next.UTC(),
		RevokedCertificates: o.entries,
		Extensions:          exts,
	}
	if o.next.IsZero() {
		tbs.NextUpdate = time.Time{}
	}
	tbsDER, err := asn1.Marshal(tbs)
	if err != nil {
		t.Fatalf("marshal tbs: %v", err)
	}
	if o.rawTBS != nil {
		var rv asn1.RawValue
		if _, err := asn1.Unmarshal(tbsDER, &rv); err != nil {
			t.Fatal(err)
		}
		tbsDER = derOf(asn1.ClassUniversal, asn1.TagSequence, true, o.rawTBS(rv.Bytes))
	}
	sum := sha256.Sum256(tbsDER)
	sig, err := o.signWith.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if o.flipSig {
		sig[len(sig)-1] ^= 0x01
	}
	der, err := asn1.Marshal(pkix.CertificateList{
		TBSCertList:        pkix.TBSCertificateList{Raw: tbsDER},
		SignatureAlgorithm: alg,
		SignatureValue:     asn1.BitString{Bytes: sig, BitLength: 8 * len(sig)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// goodOpts is the control every refusal case below breaks exactly one field of.
func goodOpts(signer *node) crlOpts {
	return crlOpts{signer: signer, next: testNow.Add(24 * time.Hour), entries: []pkix.RevokedCertificate{entry(7)}}
}

func pemCRL(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
}

func wantRule(t *testing.T, err error, rule ListRule) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted; want rule %q", rule)
	}
	if got := ListRuleOf(err); got != rule {
		t.Fatalf("rule %q (%v); want %q", got, err, rule)
	}
	if !errors.Is(err, ErrRevocationListInvalid) {
		t.Fatalf("error does not wrap the sentinel: %v", err)
	}
}

func parseAt(der []byte, chain []*node, anchor *node) (*RevocationList, error) {
	var cs []*x509.Certificate
	for _, n := range chain {
		cs = append(cs, n.cert)
	}
	return ParseRevocationList(der, cs, anchor.cert, testNow)
}

func TestParseRevocationListAccepts(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	inter := issue(t, root, spec{cn: "inter", ca: true})
	inter2 := issue(t, inter, spec{cn: "inter2", ca: true})

	t.Run("anchor signs, DER and PEM", func(t *testing.T) {
		der := craftCRL(t, goodOpts(root))
		for name, in := range map[string][]byte{"der": der, "pem": pemCRL(der), "pem with whitespace": append([]byte("\n  "), append(pemCRL(der), '\n', ' ')...)} {
			rl, err := parseAt(in, nil, root)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if rl.Issuer != IssuerIDOfCA(root.cert) || rl.Signer != root.cert || rl.SignerPEM != root.pem() {
				t.Fatalf("%s: wrong issuer identity", name)
			}
			if len(rl.Entries) != 1 || rl.Entries[0].Serial != "7" || rl.Entries[0].Reason != RevocationUnspecified {
				t.Fatalf("%s: entries %+v", name, rl.Entries)
			}
			if rl.Number.Int64() != 1 || !rl.NextUpdate.After(rl.ThisUpdate) || rl.SHA256 != sha256Hex(der) {
				t.Fatalf("%s: metadata %+v", name, rl)
			}
		}
	})
	t.Run("intermediate one level down", func(t *testing.T) {
		rl, err := parseAt(craftCRL(t, goodOpts(inter)), []*node{inter}, root)
		if err != nil || rl.Issuer != IssuerIDOfCA(inter.cert) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("intermediate two levels down needs its chain", func(t *testing.T) {
		der := craftCRL(t, goodOpts(inter2))
		rl, err := parseAt(der, []*node{inter2, inter}, root)
		if err != nil || rl.Issuer != IssuerIDOfCA(inter2.cert) {
			t.Fatalf("with the chain: %v", err)
		}
		// Without the linking CA the signer cannot be tied to the anchor.
		_, err = parseAt(der, []*node{inter2}, root)
		wantRule(t, err, RuleIssuerChain)
	})
	t.Run("a CRL from the Go standard library", func(t *testing.T) {
		tmpl := &x509.RevocationList{
			Number:     big.NewInt(42),
			ThisUpdate: testNow.Add(-time.Minute),
			NextUpdate: testNow.Add(time.Hour),
			RevokedCertificateEntries: []x509.RevocationListEntry{
				{SerialNumber: big.NewInt(0x1234), RevocationTime: testNow.Add(-time.Hour), ReasonCode: 1},
				{SerialNumber: big.NewInt(5), RevocationTime: testNow.Add(-time.Hour)},
			},
		}
		der, err := x509.CreateRevocationList(rand.Reader, tmpl, inter.cert, inter.key)
		if err != nil {
			t.Fatal(err)
		}
		rl, err := parseAt(der, []*node{inter}, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(rl.Entries) != 2 || rl.Entries[0].Serial != "1234" || rl.Entries[0].Reason != RevocationKeyCompromise || rl.Entries[1].Reason != RevocationUnspecified {
			t.Fatalf("%+v", rl.Entries)
		}
	})
	t.Run("reasons", func(t *testing.T) {
		want := map[int]RevocationReason{
			0: RevocationUnspecified, 1: RevocationKeyCompromise, 2: RevocationCACompromise,
			3: RevocationAffiliationChanged, 4: RevocationSuperseded, 5: RevocationCessationOfOperation,
			6: RevocationCertificateHold, 9: RevocationPrivilegeWithdrawn, 10: RevocationAACompromise,
		}
		for code, name := range want {
			o := goodOpts(root)
			o.entries = []pkix.RevokedCertificate{entry(3, reasonExt(code))}
			rl, err := parseAt(craftCRL(t, o), nil, root)
			if err != nil || rl.Entries[0].Reason != name {
				t.Fatalf("reason %d: %v %+v", code, err, rl)
			}
		}
	})
	t.Run("boundaries that are accepted", func(t *testing.T) {
		for name, mut := range map[string]func(*crlOpts){
			"thisUpdate within the skew":     func(o *crlOpts) { o.this = testNow.Add(NotBeforeSkew - time.Second); o.next = o.this.Add(time.Hour) },
			"already expired (entries real)": func(o *crlOpts) { o.this = testNow.Add(-72 * time.Hour); o.next = testNow.Add(-48 * time.Hour) },
			"CRL number at 160 bits":         func(o *crlOpts) { o.number = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 160), big.NewInt(1)) },
			"CRL number zero":                func(o *crlOpts) { o.number = big.NewInt(0) },
			"no entries":                     func(o *crlOpts) { o.entries = nil },
			"unknown NON-critical CRL ext": func(o *crlOpts) {
				o.exts = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 3, 4}, Value: []byte{5, 0}}}
			},
			"unknown non-critical entry ext": func(o *crlOpts) {
				o.entries = []pkix.RevokedCertificate{entry(3, pkix.Extension{Id: asn1.ObjectIdentifier{1, 2, 3, 5}, Value: []byte{5, 0}})}
			},
			"invalidity date entry extension": func(o *crlOpts) {
				o.entries = []pkix.RevokedCertificate{entry(3, pkix.Extension{Id: oidExtInvalidityDate, Value: []byte{0x18, 0x0f, '2', '0', '2', '6', '1', '0', '2', '7', '0', '0', '0', '0', '0', '0', 'Z'}})}
			},
		} {
			o := goodOpts(root)
			mut(&o)
			if _, err := parseAt(craftCRL(t, o), nil, root); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}

func TestParseRevocationListRefuses(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	inter := issue(t, root, spec{cn: "inter", ca: true})
	otherRoot := issue(t, nil, spec{cn: "other-root", ca: true})
	good := craftCRL(t, goodOpts(root))
	if _, err := parseAt(good, nil, root); err != nil {
		t.Fatalf("control: %v", err)
	}

	crit := func(oid asn1.ObjectIdentifier) pkix.Extension {
		return pkix.Extension{Id: oid, Critical: true, Value: []byte{0x05, 0x00}}
	}
	idp := pkix.Extension{Id: oidExtIssuingDistPoint, Value: []byte{0x30, 0x00}}
	cases := []struct {
		name string
		mut  func(*crlOpts)
		rule ListRule
	}{
		{"bad signature", func(o *crlOpts) { o.flipSig = true }, RuleBadSignature},
		{"signed by a foreign key under the right name", func(o *crlOpts) { o.signWith = ecKey(t) }, RuleBadSignature},
		{"weak signature algorithm", func(o *crlOpts) { o.sigAlg = oidECDSAWithSHA1 }, RuleWeakSignature},
		{"CRL issuer is not the signer", func(o *crlOpts) { o.issuerRaw = inter.cert.RawSubject }, RuleWrongIssuer},
		{"no nextUpdate", func(o *crlOpts) { o.next = time.Time{} }, RuleNoNextUpdate},
		{"nextUpdate == thisUpdate", func(o *crlOpts) { o.this = testNow.Add(-time.Hour); o.next = o.this }, RuleUpdateOrder},
		{"nextUpdate before thisUpdate", func(o *crlOpts) { o.this = testNow.Add(-time.Hour); o.next = o.this.Add(-time.Minute) }, RuleUpdateOrder},
		{"thisUpdate beyond the skew", func(o *crlOpts) {
			o.this = testNow.Add(NotBeforeSkew + time.Second)
			o.next = o.this.Add(time.Hour)
		}, RuleThisUpdateFuture},
		{"no CRL number", func(o *crlOpts) { o.noNumber = true }, RuleNoCRLNumber},
		{"negative CRL number", func(o *crlOpts) { o.number = big.NewInt(-1) }, RuleBadCRLNumber},
		{"CRL number over 20 octets", func(o *crlOpts) { o.number = new(big.Int).Lsh(big.NewInt(1), 160) }, RuleBadCRLNumber},
		{"delta CRL indicator", func(o *crlOpts) {
			o.exts = []pkix.Extension{{Id: oidExtDeltaCRLIndic, Critical: true, Value: []byte{0x02, 0x01, 0x01}}}
		}, RuleDelta},
		{"delta CRL indicator, non-critical", func(o *crlOpts) {
			o.exts = []pkix.Extension{{Id: oidExtDeltaCRLIndic, Value: []byte{0x02, 0x01, 0x01}}}
		}, RuleDelta},
		{"IDP, critical", func(o *crlOpts) { e := idp; e.Critical = true; o.exts = []pkix.Extension{e} }, RuleIssuingDistPoint},
		{"IDP, non-critical", func(o *crlOpts) { o.exts = []pkix.Extension{idp} }, RuleIssuingDistPoint},
		{"IDP onlyContainsAttributeCerts", func(o *crlOpts) {
			// [5] BOOLEAN TRUE inside the IDP sequence: the flag a flag-by-flag check misses.
			o.exts = []pkix.Extension{{Id: oidExtIssuingDistPoint, Value: []byte{0x30, 0x03, 0x85, 0x01, 0xff}}}
		}, RuleIssuingDistPoint},
		{"duplicated extension", func(o *crlOpts) { o.exts = []pkix.Extension{crlNumberExt(big.NewInt(9))} }, RuleDuplicateExt},
		{"duplicated unknown extension", func(o *crlOpts) {
			e := pkix.Extension{Id: asn1.ObjectIdentifier{1, 2, 3, 4}, Value: []byte{5, 0}}
			o.exts = []pkix.Extension{e, e}
		}, RuleDuplicateExt},
		{"unknown critical CRL extension", func(o *crlOpts) { o.exts = []pkix.Extension{crit(asn1.ObjectIdentifier{1, 2, 3, 4})} }, RuleCriticalExt},
		{"entry certificateIssuer", func(o *crlOpts) {
			o.entries = []pkix.RevokedCertificate{entry(3, pkix.Extension{Id: oidExtCertIssuer, Value: []byte{0x30, 0x00}})}
		}, RuleEntryCertIssuer},
		{"entry certificateIssuer, critical", func(o *crlOpts) {
			o.entries = []pkix.RevokedCertificate{entry(3, pkix.Extension{Id: oidExtCertIssuer, Critical: true, Value: []byte{0x30, 0x00}})}
		}, RuleEntryCertIssuer},
		{"entry unknown critical extension", func(o *crlOpts) {
			o.entries = []pkix.RevokedCertificate{entry(3, crit(asn1.ObjectIdentifier{1, 2, 3, 5}))}
		}, RuleEntryCriticalExt},
		{"entry duplicated extension", func(o *crlOpts) {
			o.entries = []pkix.RevokedCertificate{entry(3, reasonExt(1), reasonExt(1))}
		}, RuleDuplicateExt},
		{"entry reason removeFromCRL", func(o *crlOpts) { o.entries = []pkix.RevokedCertificate{entry(3, reasonExt(8))} }, RuleEntryReason},
		{"entry reason 7 (unassigned)", func(o *crlOpts) { o.entries = []pkix.RevokedCertificate{entry(3, reasonExt(7))} }, RuleEntryReason},
		{"entry reason 11", func(o *crlOpts) { o.entries = []pkix.RevokedCertificate{entry(3, reasonExt(11))} }, RuleEntryReason},
		{"entry serial zero", func(o *crlOpts) { o.entries = []pkix.RevokedCertificate{entry(0)} }, RuleEntrySerial},
		{"entry serial negative", func(o *crlOpts) { o.entries = []pkix.RevokedCertificate{entry(-4)} }, RuleEntrySerial},
		{"serial listed twice", func(o *crlOpts) { o.entries = []pkix.RevokedCertificate{entry(3), entry(4), entry(3)} }, RuleDuplicateSerial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := goodOpts(root)
			c.mut(&o)
			_, err := parseAt(craftCRL(t, o), nil, root)
			wantRule(t, err, c.rule)
		})
	}

	t.Run("framing", func(t *testing.T) {
		twoBlocks := append(pemCRL(good), pemCRL(good)...)
		_, err := parseAt(twoBlocks, nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(append(pemCRL(good), []byte("trailing")...), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: good}), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Headers: map[string]string{"A": "b"}, Bytes: good}), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(append(append([]byte(nil), good...), 0x00), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(good[:len(good)-3], nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt([]byte("not a crl"), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(nil, nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt([]byte("-----BEGIN X509 CRL-----\n"), nil, root)
		wantRule(t, err, RuleMalformed)
	})

	t.Run("bounds", func(t *testing.T) {
		_, err := ParseRevocationListLimits(good, nil, root.cert, testNow, CRLLimits{MaxBytes: len(good) - 1, MaxEntries: 10})
		wantRule(t, err, RuleTooLarge)
		if _, err := ParseRevocationListLimits(good, nil, root.cert, testNow, CRLLimits{MaxBytes: len(good), MaxEntries: 10}); err != nil {
			t.Fatalf("exactly at the byte limit: %v", err)
		}
		o := goodOpts(root)
		o.entries = []pkix.RevokedCertificate{entry(1), entry(2), entry(3)}
		many := craftCRL(t, o)
		_, err = ParseRevocationListLimits(many, nil, root.cert, testNow, CRLLimits{MaxBytes: 1 << 20, MaxEntries: 2})
		wantRule(t, err, RuleTooManyEntries)
		if _, err := ParseRevocationListLimits(many, nil, root.cert, testNow, CRLLimits{MaxBytes: 1 << 20, MaxEntries: 3}); err != nil {
			t.Fatalf("exactly at the entry limit: %v", err)
		}
		// Unconfigured limits are not "unbounded".
		_, err = ParseRevocationListLimits(good, nil, root.cert, testNow, CRLLimits{})
		wantRule(t, err, RuleMalformed)
		// The size check precedes parsing: a huge garbage input is refused as too large, not parsed.
		_, err = ParseRevocationListLimits(make([]byte, DefaultMaxCRLBytes+1), nil, root.cert, testNow, DefaultCRLLimits)
		wantRule(t, err, RuleTooLarge)
	})

	t.Run("signer and chain", func(t *testing.T) {
		interCRL := craftCRL(t, goodOpts(inter))
		// The anchor did not sign it.
		_, err := parseAt(interCRL, nil, root)
		wantRule(t, err, RuleWrongIssuer)
		// An unrelated root's CRL under this anchor.
		_, err = parseAt(craftCRL(t, goodOpts(otherRoot)), []*node{otherRoot}, root)
		wantRule(t, err, RuleIssuerChain)
		// A signer without cRLSign.
		noSign := issue(t, root, spec{cn: "no-crl-sign", ca: true, ku: x509.KeyUsageCertSign})
		_, err = parseAt(craftCRL(t, goodOpts(noSign)), []*node{noSign}, root)
		wantRule(t, err, RuleNotCRLSigner)
		// A signer that is not a CA at all.
		leaf := issue(t, inter, spec{cn: "leaf", ku: x509.KeyUsageCRLSign})
		_, err = parseAt(craftCRL(t, goodOpts(leaf)), []*node{leaf, inter}, root)
		wantRule(t, err, RuleNotCRLSigner)
		// Anchor lacking cRLSign signs nothing.
		anchorNoSign := issue(t, nil, spec{cn: "anchor-no-sign", ca: true, ku: x509.KeyUsageCertSign})
		_, err = parseAt(craftCRL(t, goodOpts(anchorNoSign)), nil, anchorNoSign)
		wantRule(t, err, RuleNotCRLSigner)
		// Too many certificates in the chain.
		var long []*node
		for i := 0; i < MaxPresentedCerts; i++ {
			long = append(long, inter)
		}
		_, err = parseAt(interCRL, long, root)
		wantRule(t, err, RuleIssuerChainLength)
		// An expired signer.
		old := issue(t, root, spec{cn: "old", ca: true, notBefore: testNow.Add(-48 * time.Hour), notAfter: testNow.Add(-time.Hour)})
		_, err = parseAt(craftCRL(t, goodOpts(old)), []*node{old}, root)
		wantRule(t, err, RuleIssuerChain)
		// No anchor at all.
		_, err = ParseRevocationList(good, nil, nil, testNow)
		wantRule(t, err, RuleIssuerChain)
		// Anchor expired.
		_, err = ParseRevocationList(good, nil, root.cert, root.cert.NotAfter.Add(time.Hour))
		wantRule(t, err, RuleIssuerChain)
	})

	t.Run("a key reused under two names is two issuers", func(t *testing.T) {
		k := ecKey(t)
		caA := issue(t, root, spec{cn: "ca-A", ca: true, key: k})
		caB := issue(t, root, spec{cn: "ca-B", ca: true, key: k})
		crlB := craftCRL(t, goodOpts(caB))
		// Control: B's own CRL under B.
		rlB, err := parseAt(crlB, []*node{caB}, root)
		if err != nil {
			t.Fatal(err)
		}
		// The same bytes presented with A as the signer: same key (so the signature would
		// verify) but another name, so refused rather than guessed.
		_, err = parseAt(crlB, []*node{caA}, root)
		wantRule(t, err, RuleWrongIssuer)
		rlA, err := parseAt(craftCRL(t, goodOpts(caA)), []*node{caA}, root)
		if err != nil {
			t.Fatal(err)
		}
		if rlA.Issuer == rlB.Issuer || rlA.Issuer.Key != rlB.Issuer.Key {
			t.Fatalf("identity must be key+name: %v %v", rlA.Issuer, rlB.Issuer)
		}
	})
}

func TestParseRevocationListOutputIsDetached(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	der := craftCRL(t, goodOpts(root))
	rl, err := parseAt(der, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range der {
		der[i] = 0
	}
	if rl.Entries[0].Serial != "7" || rl.SHA256 == "" {
		t.Fatal("the parsed list aliases the input buffer")
	}
	if strings.Contains(rl.IssuerSubject, "\x00") || rl.IssuerSubject != "CN=root" {
		t.Fatalf("subject %q", rl.IssuerSubject)
	}
}

// Everything the parser accepts is re-derived here from facts that do not depend on how it got
// there, so a lenient parser cannot vouch for itself.
func checkAcceptedList(t *testing.T, rl *RevocationList, now time.Time, lim CRLLimits) {
	t.Helper()
	if rl == nil || rl.Signer == nil || rl.Number == nil {
		t.Fatal("accepted with a missing field")
	}
	if rl.Issuer != IssuerIDOfCA(rl.Signer) {
		t.Fatal("issuer identity is not the signer's key and name")
	}
	if !rl.NextUpdate.After(rl.ThisUpdate) || rl.ThisUpdate.Sub(now) > NotBeforeSkew {
		t.Fatal("accepted with bad times")
	}
	if rl.Number.Sign() < 0 || rl.Number.BitLen() > maxCRLNumberBits {
		t.Fatal("accepted with a bad CRL number")
	}
	if len(rl.Entries) > lim.MaxEntries {
		t.Fatal("accepted over the entry bound")
	}
	seen := map[string]bool{}
	for _, e := range rl.Entries {
		if e.Serial == "" || e.Serial[0] == '0' || strings.ToLower(e.Serial) != e.Serial || seen[e.Serial] {
			t.Fatalf("serial %q is not unique minimal lowercase hex", e.Serial)
		}
		n, ok := new(big.Int).SetString(e.Serial, 16)
		if !ok || n.Sign() <= 0 {
			t.Fatalf("serial %q is not positive", e.Serial)
		}
		seen[e.Serial] = true
		if _, ok := reasonNames[reasonCodeOf(e.Reason)]; !ok {
			t.Fatalf("reason %q", e.Reason)
		}
	}
	if rl.Signer.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Fatal("accepted a signer without cRLSign")
	}
}

func reasonCodeOf(r RevocationReason) int {
	for c, n := range reasonNames {
		if n == r {
			return c
		}
	}
	return -1
}

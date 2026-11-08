// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"math/big"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

// ocspBuild describes one OCSP response. The zero value is a GOOD answer for leaf under issuer,
// signed by issuer, valid across testNow; each negative control changes exactly one field.
type ocspBuild struct {
	// For whom: the certificate and the issuer whose hashes the CertID carries.
	cert, hashIssuer *x509.Certificate
	serial           *big.Int
	hash             crypto.Hash
	// Who signs, and how the response names and carries the signer.
	signer      *node
	responderID *asn1.RawValue
	embed       []*x509.Certificate
	sigOID      asn1.ObjectIdentifier
	// What it says.
	statusTag   int // 0 good (default), 1 revoked, 2 unknown
	revokedAt   time.Time
	reasonCode  int
	thisUpdate  time.Time
	nextUpdate  time.Time
	noNext      bool
	critical    bool
	criticalTop bool
	version     int
	extraSingle []ocspSingleASN1
	respStatus  ocsp.ResponseStatus
	tamper      bool
}

func byName(c *x509.Certificate) *asn1.RawValue {
	return &asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true, Bytes: c.RawSubject}
}

func byKey(t testing.TB, c *x509.Certificate) *asn1.RawValue {
	t.Helper()
	k, ok := spkiKeyBytes(c)
	if !ok {
		t.Fatal("spki")
	}
	inner, err := asn1.Marshal(ocspDigest(crypto.SHA1, k))
	if err != nil {
		t.Fatal(err)
	}
	return &asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 2, IsCompound: true, Bytes: inner}
}

func certID(t testing.TB, h crypto.Hash, issuer *x509.Certificate, serial *big.Int) ocspCertIDASN1 {
	t.Helper()
	if h == 0 {
		h = crypto.SHA1
	}
	oid := map[crypto.Hash]asn1.ObjectIdentifier{crypto.SHA1: oidHashSHA1, crypto.SHA256: oidHashSHA256}[h]
	k, _ := spkiKeyBytes(issuer)
	return ocspCertIDASN1{
		HashAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oid, Parameters: asn1.NullRawValue},
		NameHash:      ocspDigest(h, issuer.RawSubject),
		IssuerKeyHash: ocspDigest(h, k),
		SerialNumber:  serial,
	}
}

func statusValue(t testing.TB, b ocspBuild) asn1.RawValue {
	t.Helper()
	switch b.statusTag {
	case 1:
		at, err := asn1.MarshalWithParams(b.revokedAt.UTC(), "generalized")
		if err != nil {
			t.Fatal(err)
		}
		if b.reasonCode >= 0 {
			code, _ := asn1.Marshal(asn1.Enumerated(b.reasonCode))
			wrapped, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: code})
			at = append(at, wrapped...)
		}
		return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true, Bytes: at}
	case 2:
		return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 2}
	}
	return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0}
}

func (b ocspBuild) der(t testing.TB) []byte {
	t.Helper()
	if b.respStatus != ocsp.Success {
		out, err := asn1.Marshal(ocspResponseASN1{Status: asn1.Enumerated(b.respStatus)})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	serialN := b.serial
	if serialN == nil {
		serialN = b.cert.SerialNumber
	}
	hi := b.hashIssuer
	single := ocspSingleASN1{
		CertID:     certID(t, b.hash, hi, serialN),
		Status:     statusValue(t, b),
		ThisUpdate: b.thisUpdate,
		NextUpdate: b.nextUpdate,
	}
	if single.ThisUpdate.IsZero() {
		single.ThisUpdate = testNow.Add(-time.Hour)
	}
	if single.NextUpdate.IsZero() && !b.noNext {
		single.NextUpdate = testNow.Add(23 * time.Hour)
	}
	if b.critical {
		single.SingleExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 3, 4}, Critical: true, Value: []byte{5, 0}}}
	}
	tbs := ocspTBSASN1{
		RawResponderID: *byName(b.signer.cert),
		ProducedAt:     testNow,
		Responses:      append([]ocspSingleASN1{single}, b.extraSingle...),
	}
	if b.responderID != nil {
		tbs.RawResponderID = *b.responderID
	}
	tbs.Version = b.version
	if b.criticalTop {
		tbs.ResponseExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 3, 5}, Critical: true, Value: []byte{5, 0}}}
	}
	tbsDER, err := asn1.Marshal(tbs)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := b.signer.key.Sign(rand.Reader, sum256(tbsDER), crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if b.tamper {
		sig[len(sig)-1] ^= 0xff
	}
	oid := b.sigOID
	if oid == nil {
		oid = oidSigECDSAWithSHA256
	}
	basic := struct {
		TBS                asn1.RawValue
		SignatureAlgorithm pkix.AlgorithmIdentifier
		Signature          asn1.BitString
		Certificates       []asn1.RawValue `asn1:"explicit,tag:0,optional"`
	}{
		TBS:                asn1.RawValue{FullBytes: tbsDER},
		SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oid},
		Signature:          asn1.BitString{Bytes: sig, BitLength: 8 * len(sig)},
	}
	for _, c := range b.embed {
		basic.Certificates = append(basic.Certificates, asn1.RawValue{FullBytes: c.Raw})
	}
	basicDER, err := asn1.Marshal(basic)
	if err != nil {
		t.Fatal(err)
	}
	out, err := asn1.Marshal(ocspResponseASN1{
		Status:   asn1.Enumerated(ocsp.Success),
		Response: ocspResponseBytes{ResponseType: oidOCSPBasic, Response: basicDER},
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sum256(b []byte) []byte { return ocspDigest(crypto.SHA256, b) }

var ocspOpts = OCSPOptions{MaxAge: 6 * time.Hour}

// ocspFixture is root -> inter -> leaf and a delegated OCSP signing certificate under inter.
type ocspFixture struct {
	basicPKI
	delegate *node
}

func newOCSPFixture(t testing.TB) ocspFixture {
	p := newBasicPKI(t)
	del := issue(t, p.inter, spec{cn: "ocsp-responder", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}})
	return ocspFixture{p, del}
}

// good is a valid GOOD response for the leaf, signed by its issuer.
func (f ocspFixture) good() ocspBuild {
	return ocspBuild{cert: f.leaf.cert, hashIssuer: f.inter.cert, signer: f.inter}
}

func (f ocspFixture) verify(t *testing.T, b ocspBuild) (*OCSPAnswer, error) {
	t.Helper()
	return VerifyOCSPResponse(b.der(t), f.leaf.cert, f.inter.cert, testNow, ocspOpts)
}

func TestVerifyOCSPResponse_Good(t *testing.T) {
	f := newOCSPFixture(t)
	for name, b := range map[string]ocspBuild{
		"issuer signs, SHA-1 CertID, byName": f.good(),
		"issuer signs, byKey": func() ocspBuild {
			b := f.good()
			b.responderID = byKey(t, f.inter.cert)
			return b
		}(),
		"SHA-256 CertID": func() ocspBuild {
			b := f.good()
			b.hash = crypto.SHA256
			return b
		}(),
		"delegated responder, byName": func() ocspBuild {
			b := f.good()
			b.signer, b.embed = f.delegate, []*x509.Certificate{f.delegate.cert}
			return b
		}(),
		"delegated responder, byKey, junk certificate first": func() ocspBuild {
			b := f.good()
			b.signer, b.embed = f.delegate, []*x509.Certificate{f.root.cert, f.delegate.cert}
			b.responderID = byKey(t, f.delegate.cert)
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			ans, err := f.verify(t, b)
			if err != nil {
				t.Fatalf("a valid response was refused: %v", err)
			}
			if ans.Status != OCSPStatusGood {
				t.Fatalf("status = %s", ans.Status)
			}
			if want := testNow.Add(23 * time.Hour); !ans.ValidUntil.Equal(want) && ans.ValidUntil.After(want) {
				t.Fatalf("validUntil %v beyond nextUpdate %v", ans.ValidUntil, want)
			}
		})
	}
}

func TestVerifyOCSPResponse_Revoked(t *testing.T) {
	f := newOCSPFixture(t)
	b := f.good()
	b.statusTag, b.revokedAt, b.reasonCode = 1, testNow.Add(-48*time.Hour), 1
	ans, err := f.verify(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if ans.Status != OCSPStatusRevoked || ans.Reason != RevocationKeyCompromise || !ans.RevokedAt.Equal(b.revokedAt) {
		t.Fatalf("answer = %+v", ans)
	}
	b.reasonCode = -1
	if ans, err = f.verify(t, b); err != nil || ans.Reason != RevocationUnspecified {
		t.Fatalf("no reason: %+v %v", ans, err)
	}
	b.statusTag = 2
	if ans, err = f.verify(t, b); err != nil || ans.Status != OCSPStatusUnknown {
		t.Fatalf("unknown: %+v %v", ans, err)
	}
}

func TestVerifyOCSPResponse_LibraryInterop(t *testing.T) {
	f := newOCSPFixture(t)
	raw, err := ocsp.CreateResponse(f.inter.cert, f.inter.cert, ocsp.Response{
		Status: ocsp.Good, SerialNumber: f.leaf.cert.SerialNumber,
		ThisUpdate: testNow.Add(-time.Hour), NextUpdate: testNow.Add(time.Hour),
	}, f.inter.key)
	if err != nil {
		t.Fatal(err)
	}
	ans, err := VerifyOCSPResponse(raw, f.leaf.cert, f.inter.cert, testNow, ocspOpts)
	if err != nil || ans.Status != OCSPStatusGood {
		t.Fatalf("a response the library produced was refused: %+v %v", ans, err)
	}
	raw, err = ocsp.CreateResponse(f.inter.cert, f.delegate.cert, ocsp.Response{
		Status: ocsp.Revoked, SerialNumber: f.leaf.cert.SerialNumber, RevokedAt: testNow.Add(-time.Hour), RevocationReason: ocsp.KeyCompromise,
		ThisUpdate: testNow.Add(-time.Hour), NextUpdate: testNow.Add(time.Hour), Certificate: f.delegate.cert,
	}, f.delegate.key)
	if err != nil {
		t.Fatal(err)
	}
	if ans, err = VerifyOCSPResponse(raw, f.leaf.cert, f.inter.cert, testNow, ocspOpts); err != nil || ans.Status != OCSPStatusRevoked || !ans.Delegated {
		t.Fatalf("a delegated revoked response the library produced: %+v %v", ans, err)
	}
}

func TestVerifyOCSPResponse_Refusals(t *testing.T) {
	f := newOCSPFixture(t)
	other := issue(t, nil, spec{cn: "other-ca", ca: true})
	stranger := issue(t, other, spec{cn: "stranger", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}})
	noEKU := issue(t, f.inter, spec{cn: "no-eku", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	expired := issue(t, f.inter, spec{cn: "expired-delegate", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning},
		notBefore: testNow.Add(-48 * time.Hour), notAfter: testNow.Add(-time.Hour)})
	future := issue(t, f.inter, spec{cn: "future-delegate", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning},
		notBefore: testNow.Add(time.Hour), notAfter: testNow.Add(48 * time.Hour)})
	otherLeafSerial := big.NewInt(424242)
	weakKey := issue(t, f.inter, spec{cn: "weak-key", key: rsaKey(t, 1024), eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}})
	noDigitalSig := issue(t, f.inter, spec{cn: "no-digital-signature", ku: x509.KeyUsageKeyEncipherment,
		eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}})
	unhandledCritical := issue(t, f.inter, spec{cn: "unhandled-critical", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning},
		mutate: func(c *x509.Certificate) {
			c.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 7}, Critical: true, Value: []byte{5, 0}}}
		}})

	delegated := func(d *node) func(*ocspBuild) {
		return func(b *ocspBuild) { b.signer, b.embed = d, []*x509.Certificate{d.cert} }
	}
	cases := []struct {
		name   string
		mutate func(*ocspBuild)
		want   OCSPFailure
	}{
		{"wrong serial", func(b *ocspBuild) { b.serial = otherLeafSerial }, OCSPWrongCertificate},
		{"another issuer's hashes", func(b *ocspBuild) { b.hashIssuer = other.cert }, OCSPWrongCertificate},
		{"same serial and name hash but a different key (issuer key hash)", func(b *ocspBuild) {
			// a CA with the issuer's name and another key
			twin := issue(t, nil, spec{cn: "inter", ca: true})
			b.hashIssuer = twin.cert
		}, OCSPWrongCertificate},
		{"signed by an unrelated CA, naming itself", func(b *ocspBuild) { b.signer = other }, OCSPUntrustedSigner},
		{"delegate issued by another CA", delegated(stranger), OCSPUntrustedSigner},
		{"delegate without id-kp-OCSPSigning", delegated(noEKU), OCSPUntrustedSigner},
		{"delegate expired", delegated(expired), OCSPUntrustedSigner},
		{"delegate not yet valid", delegated(future), OCSPUntrustedSigner},
		{"delegate with an RSA-1024 key", delegated(weakKey), OCSPUntrustedSigner},
		{"delegate whose key usage excludes digitalSignature", delegated(noDigitalSig), OCSPUntrustedSigner},
		{"delegate with an unhandled critical extension", delegated(unhandledCritical), OCSPUntrustedSigner},
		{"delegate not embedded", func(b *ocspBuild) { b.signer = f.delegate }, OCSPUntrustedSigner},
		{"delegate embedded but the ResponderID names someone else", func(b *ocspBuild) {
			delegated(f.delegate)(b)
			b.responderID = byName(other.cert)
		}, OCSPUntrustedSigner},
		{"signature tampered", func(b *ocspBuild) { b.tamper = true }, OCSPBadSignature},
		{"signature made by the delegate while claiming the issuer", func(b *ocspBuild) {
			b.signer = f.delegate
			b.responderID = byName(f.inter.cert)
		}, OCSPBadSignature},
		{"unsupported signature algorithm", func(b *ocspBuild) { b.sigOID = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5} }, OCSPBadSignature},
		{"past nextUpdate", func(b *ocspBuild) {
			b.thisUpdate, b.nextUpdate = testNow.Add(-3*time.Hour), testNow.Add(-time.Minute)
		}, OCSPStale},
		{"exactly at nextUpdate", func(b *ocspBuild) {
			b.thisUpdate, b.nextUpdate = testNow.Add(-3*time.Hour), testNow
		}, OCSPStale},
		{"thisUpdate older than the replay bound", func(b *ocspBuild) {
			b.thisUpdate, b.nextUpdate = testNow.Add(-7*time.Hour), testNow.Add(30*24*time.Hour)
		}, OCSPStale},
		{"no nextUpdate, older than an hour", func(b *ocspBuild) {
			b.noNext, b.thisUpdate = true, testNow.Add(-61*time.Minute)
		}, OCSPStale},
		{"thisUpdate in the future", func(b *ocspBuild) {
			b.thisUpdate, b.nextUpdate = testNow.Add(10*time.Minute), testNow.Add(time.Hour)
		}, OCSPNotYetValid},
		{"thisUpdate after nextUpdate", func(b *ocspBuild) {
			b.thisUpdate, b.nextUpdate = testNow.Add(-time.Minute), testNow.Add(-2*time.Minute)
		}, OCSPMalformed},
		{"critical single extension", func(b *ocspBuild) { b.critical = true }, OCSPMalformed},
		{"critical response extension", func(b *ocspBuild) { b.criticalTop = true }, OCSPMalformed},
		{"response data version other than v1", func(b *ocspBuild) { b.version = 1 }, OCSPMalformed},
		{"responder error status", func(b *ocspBuild) { b.respStatus = ocsp.TryLater }, OCSPNotSuccessful},
		{"a second single response for the same certificate", func(b *ocspBuild) {
			b.extraSingle = []ocspSingleASN1{{
				CertID: certID(t, 0, f.inter.cert, f.leaf.cert.SerialNumber), Status: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true,
					Bytes: mustMarshalTime(t, testNow)},
				ThisUpdate: testNow.Add(-time.Hour), NextUpdate: testNow.Add(time.Hour),
			}}
		}, OCSPWrongCertificate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := f.good()
			tc.mutate(&b)
			ans, err := f.verify(t, b)
			if err == nil {
				t.Fatalf("trusted a response it must refuse: %+v", ans)
			}
			if !errors.Is(err, ErrOCSP) || OCSPFailureOf(err) != tc.want {
				t.Fatalf("refused as %q (%v), want %q", OCSPFailureOf(err), err, tc.want)
			}
		})
	}
}

func mustMarshalTime(t testing.TB, at time.Time) []byte {
	b, err := asn1.MarshalWithParams(at.UTC(), "generalized")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The status of ANOTHER certificate that the same response also carries must not be taken for the
// checked certificate's: the matching single response is the only one read.
func TestVerifyOCSPResponse_OtherCertificatesStatusIsIgnored(t *testing.T) {
	f := newOCSPFixture(t)
	b := f.good()
	b.extraSingle = []ocspSingleASN1{{
		CertID: certID(t, 0, f.inter.cert, big.NewInt(777)), Status: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true, Bytes: mustMarshalTime(t, testNow)},
		ThisUpdate: testNow.Add(-time.Hour), NextUpdate: testNow.Add(time.Hour),
	}}
	ans, err := f.verify(t, b)
	if err != nil || ans.Status != OCSPStatusGood {
		t.Fatalf("%+v %v", ans, err)
	}
}

func TestVerifyOCSPResponse_NoNextUpdateIsBoundedByAnHour(t *testing.T) {
	f := newOCSPFixture(t)
	b := f.good()
	b.noNext, b.thisUpdate = true, testNow.Add(-10*time.Minute)
	ans, err := f.verify(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := b.thisUpdate.Add(time.Hour); !ans.ValidUntil.Equal(want) {
		t.Fatalf("validUntil = %v, want thisUpdate+1h = %v", ans.ValidUntil, want)
	}
}

func TestVerifyOCSPResponse_ValidUntilIsTheEarliestBound(t *testing.T) {
	f := newOCSPFixture(t)
	// maxAge binds before a far nextUpdate.
	b := f.good()
	b.thisUpdate, b.nextUpdate = testNow.Add(-time.Hour), testNow.Add(72*time.Hour)
	ans, err := f.verify(t, b)
	if err != nil || !ans.ValidUntil.Equal(b.thisUpdate.Add(ocspOpts.MaxAge)) {
		t.Fatalf("maxAge bound: %+v %v", ans, err)
	}
	// a delegate's notAfter binds before both.
	short := issue(t, f.inter, spec{cn: "short-delegate", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}, notAfter: testNow.Add(90 * time.Minute)})
	b = f.good()
	b.signer, b.embed = short, []*x509.Certificate{short.cert}
	b.nextUpdate = testNow.Add(20 * time.Hour)
	ans, err = f.verify(t, b)
	if err != nil || !ans.ValidUntil.Equal(short.cert.NotAfter) {
		t.Fatalf("delegate notAfter bound: %+v %v", ans, err)
	}
}

func TestVerifyOCSPResponse_Inputs(t *testing.T) {
	f := newOCSPFixture(t)
	raw := f.good().der(t)
	for name, fn := range map[string]func() error{
		"nil certificate": func() error { _, e := VerifyOCSPResponse(raw, nil, f.inter.cert, testNow, ocspOpts); return e },
		"nil issuer":      func() error { _, e := VerifyOCSPResponse(raw, f.leaf.cert, nil, testNow, ocspOpts); return e },
		"no replay bound": func() error {
			_, e := VerifyOCSPResponse(raw, f.leaf.cert, f.inter.cert, testNow, OCSPOptions{})
			return e
		},
		"no clock": func() error {
			_, e := VerifyOCSPResponse(raw, f.leaf.cert, f.inter.cert, time.Time{}, ocspOpts)
			return e
		},
		"empty": func() error { _, e := VerifyOCSPResponse(nil, f.leaf.cert, f.inter.cert, testNow, ocspOpts); return e },
		"oversize": func() error {
			_, e := VerifyOCSPResponse(make([]byte, MaxOCSPResponseBytes+1), f.leaf.cert, f.inter.cert, testNow, ocspOpts)
			return e
		},
		"trailing byte": func() error {
			_, e := VerifyOCSPResponse(append(bytes.Clone(raw), 0), f.leaf.cert, f.inter.cert, testNow, ocspOpts)
			return e
		},
		"truncated": func() error {
			_, e := VerifyOCSPResponse(raw[:len(raw)/2], f.leaf.cert, f.inter.cert, testNow, ocspOpts)
			return e
		},
		"not the issuer":     func() error { _, e := VerifyOCSPResponse(raw, f.leaf.cert, f.root.cert, testNow, ocspOpts); return e },
		"cert is its own CA": func() error { _, e := VerifyOCSPResponse(raw, f.inter.cert, f.inter.cert, testNow, ocspOpts); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestBuildOCSPRequest(t *testing.T) {
	f := newOCSPFixture(t)
	der, err := BuildOCSPRequest(f.leaf.cert, f.inter.cert)
	if err != nil {
		t.Fatal(err)
	}
	req, err := ocsp.ParseRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	if req.SerialNumber.Cmp(f.leaf.cert.SerialNumber) != 0 || req.HashAlgorithm != crypto.SHA1 {
		t.Fatalf("request = %+v", req)
	}
	if bytes.Contains(der, []byte{0x06, 0x09, 0x2b, 0x06, 0x01, 0x05, 0x05, 0x07, 0x30, 0x01, 0x02}) {
		t.Fatal("the request carries a nonce extension")
	}
	if _, err := BuildOCSPRequest(f.leaf.cert, f.root.cert); err == nil {
		t.Fatal("built a request against a certificate that is not the issuer")
	}
	if _, err := BuildOCSPRequest(nil, f.root.cert); err == nil {
		t.Fatal("built a request for no certificate")
	}
}

func TestOCSPResponderURL(t *testing.T) {
	c := &x509.Certificate{OCSPServer: []string{"ldap://x/y", "ftp://z", "http://", "https://ocsp.example/r", "http://later"}}
	if got := OCSPResponderURL(c); got != "https://ocsp.example/r" {
		t.Fatalf("got %q", got)
	}
	if OCSPResponderURL(&x509.Certificate{}) != "" {
		t.Fatal("a certificate with no AIA has a responder")
	}
}

// ─── the decision ───

func twoPathPKI(t *testing.T) (leaf *node, v *Verified, alt *node) {
	t.Helper()
	root := issue(t, nil, spec{cn: "root", ca: true})
	inter := issue(t, root, spec{cn: "inter", ca: true})
	leaf = issue(t, inter, spec{cn: "device"})
	v, err := VerifyClientChain(root.cert, chainOf(leaf, inter), testNow)
	if err != nil {
		t.Fatal(err)
	}
	return leaf, v, inter
}

func resultsFor(v *Verified, per ...OCSPOutcome) OCSPResults {
	res := OCSPResults{}
	for i, t := range v.OCSPTargets() {
		if i < len(per) {
			res[t.Key()] = per[i]
		}
	}
	return res
}

func TestApplyOCSP(t *testing.T) {
	_, v, _ := twoPathPKI(t)
	if n := len(v.OCSPTargets()); n != 2 {
		t.Fatalf("targets = %d, want leaf and intermediate", n)
	}
	type want struct {
		reason     Reason
		incomplete bool
	}
	for _, tc := range []struct {
		name string
		mode OCSPMode
		res  OCSPResults
		want want
	}{
		{"enforce: all good", OCSPEnforceKnown, resultsFor(v, OCSPGood, OCSPGood), want{}},
		{"enforce: leaf revoked", OCSPEnforceKnown, resultsFor(v, OCSPRevoked, OCSPGood), want{reason: ReasonRevoked}},
		{"enforce: intermediate revoked", OCSPEnforceKnown, resultsFor(v, OCSPGood, OCSPRevoked), want{reason: ReasonRevoked}},
		{"enforce: leaf no answer, intermediate revoked (revoked wins over an early failure)", OCSPEnforceKnown, resultsFor(v, OCSPNoAnswer, OCSPRevoked), want{reason: ReasonRevoked}},
		{"require: leaf no answer, intermediate revoked", OCSPRequireCurrent, resultsFor(v, OCSPNoAnswer, OCSPRevoked), want{reason: ReasonRevoked}},
		{"enforce: nothing answered is admitted and flagged", OCSPEnforceKnown, OCSPResults{}, want{incomplete: true}},
		{"enforce: leaf good, intermediate unanswered is admitted and flagged", OCSPEnforceKnown, resultsFor(v, OCSPGood, OCSPNoAnswer), want{incomplete: true}},
		{"require: all good", OCSPRequireCurrent, resultsFor(v, OCSPGood, OCSPGood), want{}},
		{"require: nothing answered", OCSPRequireCurrent, OCSPResults{}, want{reason: ReasonRevocationUnknown}},
		{"require: intermediate unanswered", OCSPRequireCurrent, resultsFor(v, OCSPGood, OCSPNoAnswer), want{reason: ReasonRevocationUnknown}},
		{"require: leaf unanswered", OCSPRequireCurrent, resultsFor(v, OCSPNoAnswer, OCSPGood), want{reason: ReasonRevocationUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := v.ApplyOCSP(tc.mode, tc.res)
			if tc.want.reason != "" {
				if err == nil || ReasonOf(err) != tc.want.reason || d.Survivor != nil {
					t.Fatalf("got %+v, %v; want a %s refusal", d, err, tc.want.reason)
				}
				return
			}
			if err != nil || d.Survivor == nil || d.Incomplete != tc.want.incomplete {
				t.Fatalf("got %+v, %v; want admitted, incomplete=%v", d, err, tc.want.incomplete)
			}
		})
	}
}

func TestApplyOCSPRefusesByDefault(t *testing.T) {
	_, v, _ := twoPathPKI(t)
	for _, m := range []OCSPMode{OCSPOff, "", "BOGUS"} {
		if d, err := v.ApplyOCSP(m, resultsFor(v, OCSPGood, OCSPGood)); err == nil || d.Survivor != nil {
			t.Fatalf("mode %q admitted", m)
		}
	}
	if _, err := (*Verified)(nil).ApplyOCSP(OCSPEnforceKnown, nil); err == nil {
		t.Fatal("a nil Verified was admitted")
	}
	if _, err := (&Verified{}).ApplyOCSP(OCSPEnforceKnown, nil); err == nil {
		t.Fatal("a Verified with no path was admitted")
	}
}

// With two verified paths (a cross-signed intermediate), a revoked answer for a certificate on
// EITHER refuses the connect: revoked wins, and an alternate path does not hide it.
func TestApplyOCSPRevokedOnAnyPathWins(t *testing.T) {
	rootKey := ecKey(t)
	root := issue(t, nil, spec{cn: "root", ca: true, key: rootKey})
	interKey := ecKey(t)
	interA := issue(t, root, spec{cn: "inter", ca: true, key: interKey})
	// the same intermediate key and name, issued a second time (a re-issue): two verified paths.
	interB := issue(t, root, spec{cn: "inter", ca: true, key: interKey})
	leaf := issue(t, interA, spec{cn: "device"})
	v, err := VerifyClientChain(root.cert, chainOf(leaf, interA, interB), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Chains) != 2 {
		t.Skipf("the verifier built %d path(s) here", len(v.Chains))
	}
	res := OCSPResults{}
	for _, tg := range v.OCSPTargets() {
		res[tg.Key()] = OCSPGood
	}
	for _, tg := range v.OCSPTargets() {
		if tg.Cert == interB.cert {
			res[tg.Key()] = OCSPRevoked
		}
	}
	if _, err := v.ApplyOCSP(OCSPRequireCurrent, res); ReasonOf(err) != ReasonRevoked {
		t.Fatalf("a revoked certificate on one path was bypassed through the other: %v", err)
	}
}

// A name does not identify a key: a delegated responder that shares the issuer's subject (so a
// byName ResponderID matches both) must still be found as the signer, not refused because the
// issuer's key was tried first.
func TestVerifyOCSPResponse_DelegateSharingTheIssuersName(t *testing.T) {
	f := newOCSPFixture(t)
	twin := issue(t, f.inter, spec{cn: f.inter.cert.Subject.CommonName, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}})
	b := f.good()
	b.signer, b.embed = twin, []*x509.Certificate{twin.cert}
	ans, err := f.verify(t, b)
	if err != nil || ans.Status != OCSPStatusGood || !ans.Delegated {
		t.Fatalf("a delegate sharing the issuer's name was refused: %+v %v", ans, err)
	}
	// Control: the same response without the delegate embedded is refused.
	b.embed = nil
	if _, err := f.verify(t, b); OCSPFailureOf(err) != OCSPBadSignature {
		t.Fatalf("control: want bad_signature, got %v", err)
	}
}

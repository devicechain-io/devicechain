// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

func caPEM(t testing.TB, cn string) (string, *node) {
	n := issue(t, nil, spec{cn: cn, ca: true})
	return n.pem(), n
}

func TestParseCaBundleAcceptsCertificatesAndCanonicalisesThem(t *testing.T) {
	a, an := caPEM(t, "Acme Root")
	b, _ := caPEM(t, "Acme Issuing")

	// Comments between blocks and a duplicate are dropped; the stored text is the certificates
	// alone.
	in := "# Acme roots\n" + a + "\nsome note\n" + b + a
	canon, certs, err := ParseCaBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("%d certificates, want 2", len(certs))
	}
	if canon != a+b || strings.Contains(canon, "note") {
		t.Fatalf("canonical form is wrong: %q", canon)
	}
	sum := sha256.Sum256(an.cert.Raw)
	if certs[0].FingerprintSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("fingerprint is not the SHA-256 of the DER")
	}
	if certs[0].Subject != "CN=Acme Root" || !certs[0].IsCA || !certs[0].NotAfter.After(time.Now().Add(-24*365*time.Hour)) {
		t.Fatalf("description is wrong: %+v", certs[0])
	}
}

func TestParseCaBundleEmptyMeansNoBundle(t *testing.T) {
	for _, in := range []string{"", "  \n\t"} {
		canon, certs, err := ParseCaBundle(in)
		if err != nil || canon != "" || len(certs) != 0 {
			t.Fatalf("%q: %q %v %v", in, canon, certs, err)
		}
	}
}

// A certificate that is not a CA is accepted by the bundle parser (pinning a provider's own
// certificate is legitimate) and reported as such. A trust ANCHOR refuses it (see anchor_test).
func TestParseCaBundleReportsANonCA(t *testing.T) {
	leaf := issue(t, nil, spec{cn: "leafy"})
	_, certs, err := ParseCaBundle(leaf.pem())
	if err != nil || len(certs) != 1 || certs[0].IsCA {
		t.Fatalf("%v %+v", err, certs)
	}
}

func TestParseCaBundleRefusesAPrivateKeyWithoutEchoingIt(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	ec, _ := x509.MarshalECPrivateKey(key)
	sec1 := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ec}))
	ca, _ := caPEM(t, "r")
	other, _ := caPEM(t, "s")

	cases := map[string]string{
		"pkcs8 alone":           pkcs8,
		"sec1 alone":            sec1,
		"after a certificate":   ca + pkcs8,
		"before a certificate":  sec1 + ca,
		"truncated key block":   ca + "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49\n",
		"lower-case marker":     ca + "-----begin private key-----\nAAAA\n",
		"encrypted key marker":  ca + "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n",
		"rsa key marker, junk":  ca + "-----BEGIN RSA PRIVATE KEY-----\n!!!\n",
		"key between two certs": ca + pkcs8 + other,
	}
	body := strings.TrimSpace(strings.SplitN(strings.SplitN(pkcs8, "\n", 2)[1], "\n-----END", 2)[0])
	for name, in := range cases {
		canon, certs, err := ParseCaBundle(in)
		if !errors.Is(err, ErrBundleInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
		if canon != "" || len(certs) != 0 {
			t.Fatalf("%s: a refusal must return nothing", name)
		}
		// Neither the key's body nor any certificate bytes may travel in the error.
		if strings.Contains(err.Error(), body) || strings.Contains(err.Error(), "MIGH") {
			t.Fatalf("%s: the error echoes the key", name)
		}
	}
}

func TestParseCaBundleRefusesWhatIsNotACertificate(t *testing.T) {
	ca, n := caPEM(t, "r")
	badDER := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")}))
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: n.cert.Raw}))
	withHeaders := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: n.cert.Raw}))

	for name, in := range map[string]string{
		"prose":              "this is not PEM at all",
		"unterminated block": "-----BEGIN CERTIFICATE-----\nMIIB\n",
		"garbage base64":     "-----BEGIN CERTIFICATE-----\n!!!!\n-----END CERTIFICATE-----\n",
		"valid PEM, bad DER": badDER,
		"a public key":       pub,
		"block with headers": withHeaders,
		"a URL":              "https://pki.example.com/root.pem",
		"one bad of two":     ca + badDER,
	} {
		_, _, err := ParseCaBundle(in)
		if !errors.Is(err, ErrBundleInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(err.Error(), "pki.example.com") {
			t.Fatalf("%s: the error echoes the input", name)
		}
	}
}

func TestParseCaBundleRefusesAnOversizedBundle(t *testing.T) {
	ca, _ := caPEM(t, "r")
	_, _, err := ParseCaBundle(ca)
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	_, _, err = ParseCaBundle(ca + strings.Repeat("# padding\n", MaxBundleBytes/10))
	if !errors.Is(err, ErrBundleInvalid) || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("%v", err)
	}
}

func TestParseCaBundleCountsAndMarkers(t *testing.T) {
	var pems []string
	for i := 0; i < MaxBundleCerts+1; i++ {
		p, _ := caPEM(t, "ca-"+string(rune('A'+i%26))+string(rune('a'+i/26)))
		pems = append(pems, p)
	}
	_, certs, err := ParseCaBundle(strings.Join(pems[:MaxBundleCerts], ""))
	if err != nil || len(certs) != MaxBundleCerts {
		t.Fatalf("%d certificates: %v", len(certs), err)
	}
	if _, _, err = ParseCaBundle(strings.Join(pems, "")); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("65 certificates: %v", err)
	}

	// A middle block corrupted past decoding is refused, not silently dropped.
	broken := "-----BEGIN CERTIFICATE-----\n!!!not base64!!!\n-----END CERTIFICATE-----\n"
	_, _, err = ParseCaBundle(pems[0] + broken + pems[1])
	if !errors.Is(err, ErrBundleInvalid) || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("%v", err)
	}
	if _, _, err = ParseCaBundle(pems[0] + pems[1]); err != nil {
		t.Fatalf("control: %v", err)
	}
}

// --- trust anchors ---------------------------------------------------------------------

func TestParseTrustAnchor(t *testing.T) {
	pemText, n := caPEM(t, "Maker Root")
	a, err := ParseTrustAnchor("# my CA\n"+pemText, testNow)
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	if a.PEM != pemText || a.Info.FingerprintSHA256 != Fingerprint(n.cert) || !a.Certificate.Equal(n.cert) {
		t.Fatalf("anchor is wrong: %+v", a.Info)
	}

	other, _ := caPEM(t, "Other")
	nonCA := issue(t, nil, spec{cn: "leafy"}).pem()
	noSign := issue(t, nil, spec{cn: "r", ca: true, ku: x509.KeyUsageCRLSign}).pem()
	expired := issue(t, nil, spec{cn: "r", ca: true, notBefore: testNow.Add(-48 * time.Hour), notAfter: testNow.Add(-time.Hour)}).pem()
	weak := issue(t, nil, spec{cn: "r", ca: true, key: rsaKey(t, 1024)}).pem()
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("k")}))

	for name, in := range map[string]string{
		"empty":            "",
		"two certificates": pemText + other,
		"not a CA":         nonCA,
		"no keyCertSign":   noSign,
		"expired":          expired,
		"RSA-1024":         weak,
		"a private key":    pemText + key,
		"prose":            "hello",
	} {
		got, err := ParseTrustAnchor(in, testNow)
		if got != nil || !errors.Is(err, ErrAnchorInvalid) {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	// A bundle-level refusal is visible through both sentinels.
	_, err = ParseTrustAnchor(pemText+key, testNow)
	if !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("the underlying parse reason is lost: %v", err)
	}
	// An anchor is also refused when it is too large as text.
	if _, err = ParseTrustAnchor(pemText+strings.Repeat("#\n", MaxAnchorBytes), testNow); !errors.Is(err, ErrAnchorInvalid) {
		t.Fatalf("%v", err)
	}
}

// --- presented chains as PEM -----------------------------------------------------------

func TestParseChainPEM(t *testing.T) {
	p := newBasicPKI(t)
	got, err := ParseChainPEM([]string{p.leaf.pem(), p.inter.pem()})
	if err != nil || len(got) != 2 || !got[0].Equal(p.leaf.cert) {
		t.Fatalf("control: %v", err)
	}

	two := p.leaf.pem() + p.inter.pem()
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("k")}))
	six := make([]string, 6)
	for i := range six {
		six[i] = p.leaf.pem()
	}
	cases := map[string]struct {
		in   []string
		want Reason
	}{
		"none":                    {nil, ReasonNoCertificate},
		"six certificates":        {six, ReasonTooLong},
		"oversized":               {[]string{p.leaf.pem(), strings.Repeat("A", 2*MaxPresentedDERBytes+1)}, ReasonTooLarge},
		"garbage":                 {[]string{"nope"}, ReasonMalformed},
		"two blocks in one entry": {[]string{two}, ReasonMalformed},
		"a private key":           {[]string{key}, ReasonMalformed},
		"valid PEM, invalid DER":  {[]string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}))}, ReasonMalformed},
		"headers":                 {[]string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"a": "b"}, Bytes: p.leaf.cert.Raw}))}, ReasonMalformed},
	}
	for name, c := range cases {
		_, err := ParseChainPEM(c.in)
		if got := ReasonOf(err); got != c.want {
			t.Errorf("%s: %q (%v), want %q", name, got, err, c.want)
		}
	}
}

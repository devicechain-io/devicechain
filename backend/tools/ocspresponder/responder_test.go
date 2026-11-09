// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

type pki struct {
	ca, leaf, wrong *x509.Certificate
	caKey, wrongKey crypto.Signer
}

func mint(t *testing.T) pki {
	t.Helper()
	now := time.Now()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	self := func(cn string, k *ecdsa.PrivateKey, eku []x509.ExtKeyUsage) *x509.Certificate {
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
			KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: eku,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	caKey, fakeKey, wrongKey, leafKey := key(), key(), key(), key()
	ca := self("rig CA", caKey, nil)
	// The rig's N13 signer: a delegate issued by a fake CA with the real CA's exact name.
	fake := self("rig CA", fakeKey, nil)
	dt := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "rig OCSP delegate"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning},
	}
	dder, err := x509.CreateCertificate(rand.Reader, dt, fake, &wrongKey.PublicKey, fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	wrong, _ := x509.ParseCertificate(dder)
	lt := &x509.Certificate{
		SerialNumber: new(big.Int).SetBytes([]byte{0x00, 0x1a, 0xbc, 0xde}), Subject: pkix.Name{CommonName: "device"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, lt, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return pki{ca: ca, leaf: leaf, wrong: wrong, caKey: caKey, wrongKey: wrongKey}
}

func newResponder(t *testing.T, p pki, statusJSON string) (*Responder, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	sf := filepath.Join(dir, "status.json")
	if statusJSON != "" {
		if err := os.WriteFile(sf, []byte(statusJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	return &Responder{Issuer: p.ca, IssuerKey: p.caKey, WrongCert: p.wrong, WrongKey: p.wrongKey, StatusFile: sf, Log: &log}, &log
}

func ask(t *testing.T, r *Responder, p pki, path string) (*http.Response, []byte) {
	t.Helper()
	body, err := ocsp.CreateRequest(p.leaf, p.ca, &ocsp.RequestOptions{Hash: crypto.SHA1})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	return rec.Result(), rec.Body.Bytes()
}

// The serial is written the way the rig writes it: lower-case hex with no leading zeros.
func TestSerialKey(t *testing.T) {
	if got := SerialKey(new(big.Int).SetBytes([]byte{0x00, 0x1a, 0xbc, 0xde})); got != "1abcde" {
		t.Fatalf("SerialKey = %q, want 1abcde", got)
	}
}

func TestOKAnswersTheStatusFileSignedByTheIssuer(t *testing.T) {
	p := mint(t)
	for _, tc := range []struct {
		file string
		want int
	}{
		{`{"1abcde":"good"}`, ocsp.Good},
		{`{"1abcde":"revoked"}`, ocsp.Revoked},
		{`{"other":"good"}`, ocsp.Unknown},
		{``, ocsp.Unknown},
	} {
		r, log := newResponder(t, p, tc.file)
		resp, der := ask(t, r, p, "/ok")
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/ocsp-response" {
			t.Fatalf("%s: HTTP %d %q", tc.file, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		got, err := ocsp.ParseResponseForCert(der, p.leaf, p.ca)
		if err != nil {
			t.Fatalf("%s: a /ok answer must verify against the issuer: %v", tc.file, err)
		}
		if got.Status != tc.want {
			t.Fatalf("%s: status %d, want %d", tc.file, got.Status, tc.want)
		}
		if !got.NextUpdate.After(time.Now()) || got.ThisUpdate.After(time.Now()) {
			t.Fatalf("%s: a /ok answer must be current: thisUpdate %v nextUpdate %v", tc.file, got.ThisUpdate, got.NextUpdate)
		}
		if !strings.Contains(log.String(), `"path":"/ok","serial":"1abcde"`) {
			t.Fatalf("%s: the request was not logged: %s", tc.file, log.String())
		}
	}
}

func TestWrongKeyIsNotTheIssuersSignature(t *testing.T) {
	p := mint(t)
	// The delegate NAMES the issuer byte for byte, and only its signature does not verify: a
	// verifier that matched by name alone would believe it.
	if !bytes.Equal(p.wrong.RawIssuer, p.ca.RawSubject) {
		t.Fatal("the fixture's delegate does not name the issuer: the plant would stop at a name check")
	}
	if p.wrong.CheckSignatureFrom(p.ca) == nil {
		t.Fatal("the fixture's delegate verifies against the issuer: it plants no defect")
	}
	r, _ := newResponder(t, p, `{"1abcde":"good"}`)
	_, der := ask(t, r, p, "/wrongkey")
	if _, err := ocsp.ParseResponseForCert(der, p.leaf, p.ca); err == nil {
		t.Fatal("a /wrongkey answer verified against the issuer: it plants no defect")
	}
	// It is well-formed and signed by its own (embedded) signer: only the trust in that signer is wrong.
	got, err := ocsp.ParseResponse(der, nil)
	if err != nil || got.Certificate == nil || !got.Certificate.Equal(p.wrong) {
		t.Fatalf("a /wrongkey answer must be a well-formed response carrying the wrong signer: %v", err)
	}
	if err := got.CheckSignatureFrom(p.wrong); err != nil {
		t.Fatalf("a /wrongkey answer must be validly signed by the wrong key: %v", err)
	}
}

func TestStaleLapsedBeforeItWasSent(t *testing.T) {
	p := mint(t)
	r, _ := newResponder(t, p, `{"1abcde":"good"}`)
	_, der := ask(t, r, p, "/stale")
	got, err := ocsp.ParseResponseForCert(der, p.leaf, p.ca)
	if err != nil {
		t.Fatalf("a /stale answer is signed by the issuer and must verify as a signature: %v", err)
	}
	if got.Status != ocsp.Good || !got.NextUpdate.Before(time.Now()) {
		t.Fatalf("a /stale answer must say good with a nextUpdate in the past: status %d nextUpdate %v", got.Status, got.NextUpdate)
	}
}

func TestABrokenFixtureFailsLoudly(t *testing.T) {
	p := mint(t)
	for _, tc := range []struct{ file, path string }{
		{`{"1abcde":"goood"}`, "/ok"}, // a typo is not "unknown"
		{`not json`, "/ok"},
		{`{"1abcde":"good"}`, "/nonsense"},
	} {
		r, log := newResponder(t, p, tc.file)
		resp, _ := ask(t, r, p, tc.path)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("%s %s: HTTP %d, want 500", tc.file, tc.path, resp.StatusCode)
		}
		if !strings.Contains(log.String(), `"error"`) {
			t.Fatalf("%s %s: the failure was not logged: %s", tc.file, tc.path, log.String())
		}
	}
}

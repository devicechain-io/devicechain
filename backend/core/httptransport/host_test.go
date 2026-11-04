// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httptransport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// hostOnlyCert is a self-signed certificate valid for one DNS name and NO IP address,
// so a client dialling the server by IP verifies it only by presenting that name.
func hostOnlyCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// A TLS server whose certificate is for a hostname, dialled by IP: it verifies only if
// the client presents that hostname as SNI, and it routes only if Host matches.
func TestPresentHostSendsHostAndSNIWhileDialingTheAddress(t *testing.T) {
	const host = "devicechain.test"
	cert, pool := hostOnlyCert(t, host)

	var mu sync.Mutex
	var seenHost, seenSNI string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenHost = r.Host
		mu.Unlock()
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		seenSNI = h.ServerName
		mu.Unlock()
		return nil, nil
	}}
	srv.StartTLS()
	defer srv.Close()

	tr := New()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	resp, err := (&http.Client{Transport: PresentHost(tr, host)}).Get(srv.URL) // https://127.0.0.1:port
	if err != nil {
		t.Fatalf("verification against the presented host failed: %v", err)
	}
	resp.Body.Close()
	mu.Lock()
	if seenHost != host || seenSNI != host {
		t.Errorf("server saw Host %q, SNI %q; want %q for both", seenHost, seenSNI, host)
	}
	mu.Unlock()

	// Negative control: the same dial without the host is refused, because the
	// certificate names a host and not the address.
	plain := New()
	plain.TLSClientConfig = &tls.Config{RootCAs: pool}
	if resp, err := (&http.Client{Transport: plain}).Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Errorf("control: dialling by IP without the host verified, so the test proves nothing")
	}
}

// Loopback is exempt from the proxy environment, so a dial test cannot show this; the
// transport's Proxy func is what decides, and it must stay nil.
func TestPresentHostKeepsTheProjectTransportAndNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	rt := PresentHost(New(), "devicechain.local")
	ht, ok := rt.(*hostTransport)
	if !ok {
		t.Fatalf("unexpected round tripper %T", rt)
	}
	tr := ht.next.(*http.Transport)
	if tr.Proxy != nil {
		t.Errorf("the transport honours the proxy environment")
	}
	if tr.ResponseHeaderTimeout != ResponseHeaderTimeout || tr.DialContext == nil {
		t.Errorf("the project transport's bounds were lost")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.ServerName != "devicechain.local" || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS config = %+v", tr.TLSClientConfig)
	}
}

func TestHostName(t *testing.T) {
	for in, want := range map[string]string{"a.io": "a.io", "a.io:8443": "a.io", "[::1]:80": "::1"} {
		if got := HostName(in); got != want {
			t.Errorf("HostName(%q)=%q want %q", in, got, want)
		}
	}
}

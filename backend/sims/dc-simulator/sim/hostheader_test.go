// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostOnlyCert is a self-signed certificate valid for one DNS name and no IP, so a
// client that dials the server by address verifies it only by presenting the name.
func hostOnlyCert(t *testing.T, host string) tls.Certificate {
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
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// The runtime built from a handshake carrying hostHeader must reach a Gateway that is
// addressed by IP while presenting the instance host as Host and SNI, on the client
// that emits AND on the one the login session rides.
func TestRuntimePresentsTheHandshakeHostToTheGateway(t *testing.T) {
	const host = "devicechain.test"
	cert := hostOnlyCert(t, host)
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	var mu sync.Mutex
	var hosts []string
	var snis []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		// A login answer the session accepts is out of scope; any JSON is enough to see the request.
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		snis = append(snis, h.ServerName)
		mu.Unlock()
		return nil, nil
	}}
	srv.StartTLS()
	defer srv.Close()

	hs := &Handshake{
		Tenant: "acme", SimEmail: "a@b.c", SimPassword: "pw", InstanceId: "devicechain",
		Endpoints:  Endpoints{UserGraphQL: srv.URL + "/graphql", Ingress: srv.URL},
		HostHeader: host,
	}
	trustForRuntimes(t, pool)
	rt, err := NewRuntime(hs, Load{DeviceCount: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rt.HostHeader != host {
		t.Fatalf("Runtime.HostHeader = %q", rt.HostHeader)
	}

	resp, err := rt.HTTPClient.Post(srv.URL+"/devicechain/acme/events", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("the emit client could not reach the Gateway by address: %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(hosts) == 0 || hosts[0] != host || len(snis) == 0 || snis[0] != host {
		t.Errorf("Gateway saw Host %v, SNI %v; want %q for both", hosts, snis, host)
	}
}

// Negative control: the same handshake without the field dials the address and cannot
// verify a certificate that names only the host, which is the failure being fixed.
func TestRuntimeWithoutAHostHeaderSendsTheAddress(t *testing.T) {
	const host = "devicechain.test"
	cert := hostOnlyCert(t, host)
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	trustForRuntimes(t, pool)
	rt, err := NewRuntime(&Handshake{Tenant: "acme", Endpoints: Endpoints{Ingress: srv.URL}}, Load{DeviceCount: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := rt.HTTPClient.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("control: reached a host-only certificate by IP with no host header, so the test above proves nothing")
	}
}

// trustForRuntimes makes the transports NewRuntime clones from http.DefaultTransport
// trust pool, for the duration of the test.
func trustForRuntimes(t *testing.T, pool *x509.CertPool) {
	t.Helper()
	dt := http.DefaultTransport.(*http.Transport)
	prev := dt.TLSClientConfig
	dt.TLSClientConfig = &tls.Config{RootCAs: pool}
	t.Cleanup(func() { dt.TLSClientConfig = prev })
}

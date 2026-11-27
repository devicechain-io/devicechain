// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// clientCert is a throwaway client certificate and key, PEM-encoded.
func clientCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "alice"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

// checkRows runs every probe of p and returns its check rows.
func checkRows(ctx context.Context, p cloudProbes) []installflow.PreflightObservation {
	var rows []installflow.PreflightObservation
	d := &doctor{observe: func(o installflow.PreflightObservation) {
		if o.Observation == installflow.ObserveCheck {
			rows = append(rows, o)
		}
	}}
	d.checkCloud(ctx, prodTarget, p)
	return rows
}

// 🔴 FINAL 1: the checks never go through a proxy. Go would make an https proxy's own
// handshake with the cluster's TLS settings, client certificate included, so a proxy
// could ask for the cluster's client certificate and get it. A context with a proxy
// reads NOT CHECKED, and the proxy is never contacted.
func TestAProxyIsNeverUsed(t *testing.T) {
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	conns := &connCounter{}
	proxy.Config.ConnState = conns.track
	proxy.StartTLS()
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	certPEM, keyPEM := clientCert(t)
	cfg := &rest.Config{Host: "https://127.0.0.1:1", Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: rest.TLSClientConfig{CertData: certPEM, KeyData: keyPEM}}
	p, err := cloudProbesFrom(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.http = cloudHTTPClient(newCloudFakes(t))
	rows := checkRows(context.Background(), p)
	if n := conns.opened.Load(); n != 0 {
		t.Fatalf("the proxy was sent %d connection(s)", n)
	}
	for _, prefix := range []string{"API server 127.0.0.1", "authentication", "may create namespaces"} {
		if r := row(t, rows, prefix); r.Verdict != installflow.CheckNotChecked || !strings.Contains(r.Text.String(), "proxy") {
			t.Errorf("%q reads %s %q", prefix, r.Verdict, r.Text)
		}
	}
}

// The environment's proxy is never used either: the registries it covers, and the API
// server, read NOT CHECKED and are not asked.
func TestAnEnvironmentProxyIsNeverUsed(t *testing.T) {
	old := envProxy
	t.Cleanup(func() { envProxy = old })
	proxyURL, _ := url.Parse("http://proxy.example:3128")
	envProxy = func(*url.URL) (*url.URL, error) { return proxyURL, nil }
	f := newCloudFakes(t)
	p, err := cloudProbesFrom(context.Background(), &rest.Config{Host: testServer, BearerToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	p.http = cloudHTTPClient(f)
	rows := checkRows(context.Background(), p)
	if len(f.methods) != 0 {
		t.Errorf("a registry was asked through the proxy: %v", f.methods)
	}
	for _, prefix := range []string{"API server api.prod.example", "OpenTofu provider registry", "image registry"} {
		if r := row(t, rows, prefix); r.Verdict != installflow.CheckNotChecked || !strings.Contains(r.Text.String(), "proxy") {
			t.Errorf("%q reads %s %q", prefix, r.Verdict, r.Text)
		}
	}
	// And the transports have no proxy to use.
	c, _, err := cloudKubeConfig(context.Background(), &rest.Config{Host: testServer, Proxy: http.ProxyURL(proxyURL)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport.(*http.Transport).Proxy != nil || cloudHTTPClient(nil).Transport.(readOnlyTransport).next.(*http.Transport).Proxy != nil {
		t.Error("a diagnostics transport has a proxy")
	}
}

// 🔴 FINAL 2: an Exit ends connection setup too. Go dials on a context detached from the
// request, so a TLS handshake a server never completes would outlive the run by up to
// the handshake timeout; the run's end now cancels it.
func TestAnExitEndsAStalledHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c // and never answer the handshake
		}
	}()
	cfg := &rest.Config{Host: "https://" + ln.Addr().String(), BearerToken: "t"}
	f := newCloudFakes(t)
	r := cloudDiagnosticsRunner(prodTarget, func(ctx context.Context, _ cloudTarget) (cloudProbes, error) {
		p, err := cloudProbesFrom(ctx, cfg)
		p.http = cloudHTTPClient(f)
		return p, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	if _, err := r.RunPreflight(ctx, func(installflow.PreflightObservation) {}); err != nil {
		t.Fatal(err)
	}
	var c net.Conn
	select {
	case c = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("the API server was never dialled")
	}
	defer c.Close()
	// The client's side must be gone well inside the screen's join budget.
	_ = c.SetReadDeadline(time.Now().Add(commandWaitDelay / 2))
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatal("the handshake was still open after the run had ended")
			}
			return // closed by the client: EOF or reset
		}
	}
}

// 🔴 FINAL 3: loading the kubeconfig is bounded: one that never finishes loading reads
// UNKNOWN when the budget runs out, instead of holding the screen's Exit.
func TestAKubeconfigThatNeverLoadsIsBounded(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	oldLoad, oldAPI, oldJoin := loadRestConfig, cloudAPITimeout, probeJoinDelay
	t.Cleanup(func() { loadRestConfig, cloudAPITimeout, probeJoinDelay = oldLoad, oldAPI, oldJoin })
	cloudAPITimeout, probeJoinDelay = 100*time.Millisecond, 10*time.Millisecond
	release := make(chan struct{})
	defer close(release)
	loadRestConfig = func(string) (*rest.Config, error) {
		<-release // a read that never returns, and ignores everything
		return nil, errors.New("late")
	}
	var rows []installflow.PreflightObservation
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cloudDiagnosticsRunner(prodTarget, liveCloudProbes).RunPreflight(context.Background(),
			func(o installflow.PreflightObservation) {
				if o.Observation == installflow.ObserveCheck {
					rows = append(rows, o)
				}
			})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a kubeconfig that never loads held the run")
	}
	if len(rows) != 1 || rows[0].Verdict != installflow.CheckUnknown || !strings.Contains(rows[0].Text.String(), "did not finish") {
		t.Fatalf("rows %+v", rows)
	}
}

// A token file is read once, while loading, inside the bound: client-go's refreshing
// reader, which reads the file again on a request, is never used.
func TestATokenFileIsNotReadAgainOnARequest(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	var api recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		_, _ = io.WriteString(w, versionJSON)
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok-on-disk-now"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldLoad := loadRestConfig
	t.Cleanup(func() { loadRestConfig = oldLoad })
	// As clientcmd loads it: the token as read at load time, and the file kept for refresh.
	loadRestConfig = func(string) (*rest.Config, error) {
		return &rest.Config{Host: srv.URL, BearerToken: "tok-as-loaded", BearerTokenFile: tokenFile}, nil
	}
	p, err := liveCloudProbes(context.Background(), prodTarget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.kube.Discovery().ServerVersionWithContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, auth := api.snapshot(); len(auth) != 1 || auth[0] != "Bearer tok-as-loaded" {
		t.Fatalf("the request carried %v", auth)
	}
}

// 🔴 FINAL 4: a SelfSubjectReview that names nobody is not an authentication.
func TestAnEmptyIdentityIsNotAuthenticated(t *testing.T) {
	f := newCloudFakes(t)
	f.ssrEmpty = true
	rows, _ := runCloud(t, f, testServer)
	if r := row(t, rows, "authentication"); r.Verdict == installflow.CheckPass {
		t.Fatalf("an empty identity reads %s %q", r.Verdict, r.Text)
	}
}

// 🔴 FINAL 5: static credentials beside a plugin are used, as client-go uses them: they
// take precedence over the plugin, which is still never run.
func TestStaticCredentialsBesideAPluginAreUsed(t *testing.T) {
	var api recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, versionJSON)
		case ssrPath:
			_, _ = io.WriteString(w, `{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1","status":{"userInfo":{"username":"alice"}}}`)
		default:
			_, _ = io.WriteString(w, `{"kind":"SelfSubjectAccessReview","apiVersion":"authorization.k8s.io/v1","status":{"allowed":true}}`)
		}
	}))
	defer srv.Close()
	cfg := &rest.Config{Host: srv.URL, BearerToken: "tok-static", ExecProvider: &clientcmdapi.ExecConfig{
		Command: "/nonexistent/dcctl-test-plugin", APIVersion: "client.authentication.k8s.io/v1",
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode}}
	if credentialsOf(cfg) != credStatic {
		t.Errorf("a token beside a plugin is classified %v", credentialsOf(cfg))
	}
	p, err := cloudProbesFrom(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.http = cloudHTTPClient(newCloudFakes(t))
	rows := checkRows(context.Background(), p)
	if r := row(t, rows, "authentication"); r.Verdict != installflow.CheckPass {
		t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
	}
	if _, auth := api.snapshot(); len(auth) == 0 || auth[0] != "Bearer tok-static" {
		t.Errorf("the requests carried %v", auth)
	}
}

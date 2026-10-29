// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/devicechain-io/dc-broker/nats-server/internal/smoke"
)

// TestOutsideReloadIsRepairedAndRotationStillServed: a reload from outside this binary (what a
// $SYS.REQ.SERVER.<id>.RELOAD request makes the server do to itself) installs raw TLS configs, so
// the MQTT listener stops requesting certificates and this binary's wrappers are orphaned. The next
// reload() must notice, take the full path and put both back; the rotation that follows it must be
// served, not swallowed by a hot swap into the orphans.
func TestOutsideReloadIsRepairedAndRotationStillServed(t *testing.T) {
	r := startBrokerWith(t, true, true)
	if err := r.s.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.requested(t) {
		t.Fatal("fixture: an outside Reload should have dropped the certificate request")
	}
	want, err := smoke.RotateServerCert(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	if n := r.b.hotSwaps.Load(); n != 0 {
		t.Fatalf("%d hot swaps after an outside reload: they would land in orphaned wrappers", n)
	}
	if !r.requested(t) {
		t.Fatal("after an outside reload and this binary's reload the listener does not request certificates")
	}
	if got, err := smoke.ServedSerial(r.dir, r.mqtt); err != nil || got != want {
		t.Fatalf("served serial %q (%v), want the rotated %s", got, err, want)
	}
	// The repair left a working hot path: the next rotation is hot, and served.
	want, err = smoke.RotateServerCert(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	if n := r.b.hotSwaps.Load(); n != 1 {
		t.Fatalf("hot swaps = %d after the repaired broker's rotation, want 1", n)
	}
	if got, err := smoke.ServedSerial(r.dir, r.mqtt); err != nil || got != want {
		t.Fatalf("served serial %q (%v), want the rotated %s", got, err, want)
	}
}

// TestHotReloadKeepsRequestingCertificates: the shape the chart runs (MQTT ca_file set), after a
// hot reload, still sends a CertificateRequest, with no acceptable-CA list.
func TestHotReloadKeepsRequestingCertificates(t *testing.T) {
	r := startBrokerWith(t, true, true)
	if _, err := smoke.RotateServerCert(r.dir); err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	if r.b.hotSwaps.Load() != 1 {
		t.Fatal("the reload was not hot: this test would prove nothing about the hot path")
	}
	if !r.requested(t) {
		t.Fatal("after a hot reload the MQTT listener sends no CertificateRequest")
	}
	if n, err := smoke.AcceptableCAs(r.dir, r.mqtt); err != nil || n != 0 {
		t.Fatalf("after a hot reload the CertificateRequest lists %d acceptable CAs (%v), want none", n, err)
	}
	r.b.check()
	if r.b.metrics.clientAuthRequest.Load() != 1 {
		t.Fatal("gauge is not 1 after a hot reload")
	}
	if got, want := r.b.metrics.certNotAfter.Load(), notAfter(r.b.mqttTLS); got == 0 || got != want {
		t.Fatalf("cert_not_after gauge = %d, want the served certificate's %d", got, want)
	}
}

// TestCABundleChangeTakesTheFullReload: a changed CA bundle is not key material only. The compare
// of the CA pools is what sends it to the server, whose reload re-reads the pools.
func TestCABundleChangeTakesTheFullReload(t *testing.T) {
	r := startBrokerWith(t, true, true)
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	if n := r.b.hotSwaps.Load(); n != 1 {
		t.Fatalf("control: an unchanged-CA reload made %d hot swaps, want 1", n)
	}
	other, err := os.ReadFile(filepath.Join(r.dir, "device-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "server-ca.crt"), other, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	if n := r.b.hotSwaps.Load(); n != 1 {
		t.Fatalf("a changed CA bundle was swapped hot (%d swaps); it must take the full reload", n)
	}
}

// TestServerAddedHooksDisableTheHotPath: OCSP stapling and ocsp_peer set GetCertificate /
// VerifyConnection on a listener's config; GetConfigForClient would bypass them (ocsp_peer verify
// would fail open). With a hook present the reload is the full one.
func TestServerAddedHooksDisableTheHotPath(t *testing.T) {
	hooks := map[string]func(*tls.Config){
		"GetCertificate": func(c *tls.Config) {
			c.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, nil }
		},
		"VerifyConnection": func(c *tls.Config) {
			c.VerifyConnection = func(tls.ConnectionState) error { return nil }
		},
	}
	for name, hook := range hooks {
		t.Run(name, func(t *testing.T) {
			r := startBrokerWith(t, true, true)
			if !liveIsOurs(r.b.live) {
				t.Fatal("fixture: the live configs are not ours to begin with")
			}
			hook(r.b.live.MQTT.TLSConfig)
			if liveIsOurs(r.b.live) {
				t.Fatal("liveIsOurs ignores a server-added hook")
			}
			if _, err := smoke.RotateServerCert(r.dir); err != nil {
				t.Fatal(err)
			}
			if err := r.b.reload(); err != nil {
				t.Fatal(err)
			}
			if n := r.b.hotSwaps.Load(); n != 0 {
				t.Fatalf("hot swap taken with a %s hook on the listener", name)
			}
		})
	}
}

// TestUncoveredTLSDisablesWrapping: OCSP stapling configured, or TLS on a listener the wrappers do
// not reach, means nothing is wrapped and every reload is the full one.
func TestUncoveredTLSDisablesWrapping(t *testing.T) {
	cases := map[string]func(*server.Options){
		"ocsp":            func(o *server.Options) { o.OCSPConfig = &server.OCSPConfig{Mode: server.OCSPModeNever} },
		"websocket":       func(o *server.Options) { o.Websocket.TLSConfig = &tls.Config{} },
		"leafnode":        func(o *server.Options) { o.LeafNode.TLSConfig = &tls.Config{} },
		"leafnode remote": func(o *server.Options) { o.LeafNode.Remotes = []*server.RemoteLeafOpts{{TLSConfig: &tls.Config{}}} },
		"gateway":         func(o *server.Options) { o.Gateway.TLSConfig = &tls.Config{} },
		"remote gateway": func(o *server.Options) {
			o.Gateway.Gateways = []*server.RemoteGatewayOpts{{TLSConfig: &tls.Config{}}}
		},
		"https monitoring": func(o *server.Options) { o.HTTPSPort = 8443 },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			if wrapOptions(&server.Options{TLSConfig: &tls.Config{}}) == nil {
				t.Fatal("control: plain options were not wrapped")
			}
			o := &server.Options{TLSConfig: &tls.Config{}}
			set(o)
			if h := wrapOptions(o); h != nil {
				t.Fatal("options with uncovered TLS were wrapped")
			}
			if o.TLSConfig.GetConfigForClient != nil {
				t.Fatal("a refused wrap still modified the config")
			}
		})
	}
}

// TestWrapperServesAndDialsWithTheSwappedCertificate: the server-side hook (listeners and inbound
// routes) and the client-side hook (routes this server dials) both hand out the rotated leaf.
// A full two-node route cluster is not run here: it needs a second server with its own cluster
// listener and TLS pair for no extra coverage of the swap, which is these two callbacks.
func TestWrapperServesAndDialsWithTheSwappedCertificate(t *testing.T) {
	dir := t.TempDir()
	if err := smoke.Prepare(dir, smoke.PrepareOptions{}); err != nil {
		t.Fatal(err)
	}
	load := func() *tls.Config {
		c, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
		if err != nil {
			t.Fatal(err)
		}
		return &tls.Config{Certificates: []tls.Certificate{c}}
	}
	serial := func(c *tls.Certificate) string {
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf.SerialNumber.Text(16)
	}
	opts := &server.Options{Cluster: server.ClusterOpts{TLSConfig: load()}}
	h := wrapOptions(opts)
	w := opts.Cluster.TLSConfig
	first := serial(&w.Certificates[0])
	want, err := smoke.RotateServerCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want == first {
		t.Fatal("rotation did not change the serial")
	}
	next := &server.Options{Cluster: server.ClusterOpts{TLSConfig: load()}}
	if !h.swapIfKeyMaterialOnly(next) {
		t.Fatal("a key-material-only change was refused")
	}
	served, err := w.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil || serial(&served.Certificates[0]) != want {
		t.Fatalf("server side presents %v (%v), want %s", served, err, want)
	}
	dialed, err := w.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil || serial(dialed) != want {
		t.Fatalf("client side presents %v (%v), want %s", dialed, err, want)
	}
}

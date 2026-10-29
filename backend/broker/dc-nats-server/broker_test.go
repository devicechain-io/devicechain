// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/devicechain-io/dc-broker/nats-server/internal/smoke"
)

// running is an embedded broker started the way main starts one, from a config file on disk.
type running struct {
	dir  string
	s    *server.Server
	b    *broker
	mqtt string
	nats string
}

// startBroker writes a broker config (MQTT over TLS, an auth callout, JetStream) and starts a
// server from it. withHook=false starts it WITHOUT this binary's option preparation — the upstream
// shape, which the negative controls use.
func startBroker(t *testing.T, withHook bool) *running {
	t.Helper()
	return startBrokerWith(t, withHook, false)
}

// startBrokerWith is startBroker with the MQTT listener's ca_file as the chart sets it
// (clientCA), which is what makes the server's TLS config carry a ClientCAs pool.
func startBrokerWith(t *testing.T, withHook, clientCA bool) *running {
	t.Helper()
	dir := t.TempDir()
	if err := smoke.Prepare(dir, smoke.PrepareOptions{
		PidFile:    filepath.ToSlash(filepath.Join(dir, "nats.pid")),
		StoreDir:   filepath.ToSlash(filepath.Join(dir, "js")),
		ClientPort: -1, MQTTPort: -1, HTTPPort: -1, MQTTClientCA: clientCA,
	}); err != nil {
		t.Fatal(err)
	}
	opts, err := server.ProcessConfigFile(filepath.Join(dir, "nats.conf"))
	if err != nil {
		t.Fatal(err)
	}
	opts.NoLog = true
	var hot *hotTLS
	if withHook {
		hot = prepareOptions(opts)
	} else {
		opts.NoSigs = true
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(30 * time.Second) {
		s.Shutdown()
		t.Fatal("server not ready")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	vz, err := s.Varz(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &running{
		dir:  dir,
		s:    s,
		b:    newBroker(s, opts, newBrokerMetrics(), hot),
		mqtt: net.JoinHostPort("127.0.0.1", strconv.Itoa(vz.MQTT.Port)),
		nats: "tls://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(vz.Port)),
	}
}

// requested is the outside observation: does the listener send a CertificateRequest?
func (r *running) requested(t *testing.T) bool {
	t.Helper()
	req, err := smoke.Probe(r.dir, r.mqtt)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return req
}

func TestApplyDeviceTLS(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *tls.Config
		want tls.ClientAuthType
	}{
		{"no client certificate is raised to request", &tls.Config{ClientAuth: tls.NoClientCert}, tls.RequestClientCert},
		{"verify (require and verify) is never lowered", &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert}, tls.RequireAndVerifyClientCert},
		{"request is left as it is", &tls.Config{ClientAuth: tls.RequestClientCert}, tls.RequestClientCert},
		{"require-any is left as it is", &tls.Config{ClientAuth: tls.RequireAnyClientCert}, tls.RequireAnyClientCert},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.in.ClientAuth
			opts := &server.Options{}
			opts.MQTT.TLSConfig = tc.in
			applyDeviceTLS(opts)
			if got := opts.MQTT.TLSConfig.ClientAuth; got != tc.want {
				t.Fatalf("ClientAuth = %v, want %v", got, tc.want)
			}
			if tc.in.ClientAuth != before {
				t.Fatal("the original *tls.Config was modified; it must be cloned")
			}
		})
	}
	t.Run("the acceptable-CA pool is dropped from a clone, not from the original", func(t *testing.T) {
		pool := x509.NewCertPool()
		in := &tls.Config{ClientAuth: tls.NoClientCert, ClientCAs: pool}
		opts := &server.Options{}
		opts.MQTT.TLSConfig = in
		applyDeviceTLS(opts)
		if opts.MQTT.TLSConfig.ClientCAs != nil {
			t.Fatal("ClientCAs survived: the CertificateRequest would carry an acceptable-CA list")
		}
		if in.ClientCAs != pool {
			t.Fatal("the original *tls.Config lost its ClientCAs; it must be cloned")
		}
	})
	t.Run("no MQTT TLS is left without one", func(t *testing.T) {
		opts := &server.Options{}
		applyDeviceTLS(opts)
		if opts.MQTT.TLSConfig != nil {
			t.Fatal("a TLS config appeared where there was none")
		}
	})
}

// TestForeignCAClientCertificateIsPresented is the deployed shape's test: the MQTT listener has a
// ca_file (the instance CA), the devices' certificates come from ANOTHER CA, and a stock Go
// crypto/tls client (Certificates set the ordinary way, no GetClientCertificate) must present its
// certificate and have it reach the callout. A request that names the instance CA as the one
// acceptable issuer makes such a client withhold the certificate, which is how every certificate
// device was refused with no_cert.
func TestForeignCAClientCertificateIsPresented(t *testing.T) {
	r := startBrokerWith(t, true, true)
	n, err := smoke.AcceptableCAs(r.dir, r.mqtt)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the CertificateRequest names %d acceptable CAs, want 0", n)
	}
	steps, err := smoke.Check(r.dir, r.nats, r.mqtt, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Err != nil {
			t.Errorf("%s: %v", s.Name, s.Err)
		}
	}
}

// TestStartRequestsClientCertificates: a server prepared the way main prepares one requests a
// certificate on its MQTT listener, and its own gauge says so.
func TestStartRequestsClientCertificates(t *testing.T) {
	r := startBroker(t, true)
	if !r.requested(t) {
		t.Fatal("the MQTT listener sent no CertificateRequest")
	}
	r.b.check()
	if r.b.metrics.mqttTLS.Load() != 1 || r.b.metrics.clientAuthRequest.Load() != 1 {
		t.Fatalf("gauges tls=%d client_auth_request=%d, want 1 and 1", r.b.metrics.mqttTLS.Load(), r.b.metrics.clientAuthRequest.Load())
	}
}

// TestReloadKeepsClientCertificateRequest is the reload trap's guard, with its negative control
// beside it: the SAME server, reloaded the way upstream reloads (Server.Reload), stops requesting
// certificates while reporting success, and the gauge falls to 0. Reloaded this binary's way, it
// requests them again.
func TestReloadKeepsClientCertificateRequest(t *testing.T) {
	r := startBroker(t, true)

	if err := r.b.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !r.requested(t) {
		t.Fatal("after this binary's reload the MQTT listener sent no CertificateRequest")
	}
	r.b.check()
	if r.b.metrics.clientAuthRequest.Load() != 1 {
		t.Fatal("gauge is not 1 after this binary's reload")
	}

	// Negative control: upstream's reload path SUCCEEDS and silently drops the setting.
	if err := r.s.Reload(); err != nil {
		t.Fatalf("upstream Reload: %v", err)
	}
	if r.requested(t) {
		t.Fatal("NEGATIVE CONTROL FAILED: after upstream's Reload the listener still requests certificates, so this test cannot tell the hook from its absence")
	}
	r.b.check()
	if r.b.metrics.clientAuthRequest.Load() != 0 || r.b.metrics.mqttTLS.Load() != 1 {
		t.Fatalf("after upstream's Reload the gauges read tls=%d client_auth_request=%d, want 1 and 0 — the watcher did not see it",
			r.b.metrics.mqttTLS.Load(), r.b.metrics.clientAuthRequest.Load())
	}

	// And this binary's reload puts it back. (The full path: upstream's Reload above replaced the
	// wrapped configs, which only a full reload installs again.)
	if err := r.b.reloadFull(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !r.requested(t) {
		t.Fatal("this binary's reload did not restore the CertificateRequest")
	}
}

// TestFailedReloadKeepsRunningSetting: a config the server refuses leaves it on the options it
// had, which request certificates.
func TestFailedReloadKeepsRunningSetting(t *testing.T) {
	r := startBroker(t, true)
	conf := filepath.Join(r.dir, "nats.conf")
	good, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, append(good, []byte("\nthis is { not a config\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err == nil {
		t.Fatal("a broken config reloaded without error")
	}
	if !r.requested(t) {
		t.Fatal("a refused reload left the listener not requesting certificates")
	}
}

// TestReloadWithoutConfigFileIsRefused mirrors upstream's refusal.
func TestReloadWithoutConfigFileIsRefused(t *testing.T) {
	b := &broker{}
	if err := b.reload(); err != errNoConfigFile {
		t.Fatalf("reload with no config file = %v, want %v", err, errNoConfigFile)
	}
}

// TestCertificateChainReachesTheCallout is the wire test: through a real auth callout, a device
// with a certificate completes TLS and the callout receives its chain as Certs (never
// VerifiedChains, since nothing is verified at the broker); a 16 KiB chain arrives whole; token
// devices, with or without a certificate, connect as before; a certificate signed with the wrong
// key fails the handshake. The second half is the negative control: the same checks against the
// upstream shape must fail.
func TestCertificateChainReachesTheCallout(t *testing.T) {
	r := startBroker(t, true)
	steps, err := smoke.Check(r.dir, r.nats, r.mqtt, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Err != nil {
			t.Errorf("%s: %v", s.Name, s.Err)
		} else {
			t.Logf("ok %s: %s", s.Name, s.Note)
		}
	}
	if len(steps) < 6 {
		t.Fatalf("only %d steps ran", len(steps))
	}
}

func TestCertificateChainCheckFailsOnUpstreamShape(t *testing.T) {
	r := startBroker(t, false)
	steps, err := smoke.Check(r.dir, r.nats, r.mqtt, true)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, s := range steps {
		if s.Err != nil {
			failed[s.Name] = true
		}
	}
	// Every step that depends on the certificate request must fail; the token device without a
	// certificate must not (the upstream broker serves it).
	for _, s := range steps {
		wantFail := !strings.HasPrefix(s.Name, "a token device presenting no certificate")
		if failed[s.Name] != wantFail {
			t.Errorf("against the upstream shape, %q failed=%v, want %v (err=%v)", s.Name, failed[s.Name], wantFail, s.Err)
		}
	}
	// ...and the same broker passes when asked what upstream actually does.
	steps, err = smoke.Check(r.dir, r.nats, r.mqtt, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := smoke.Failed(steps); n != 0 {
		for _, s := range steps {
			t.Logf("%s: %v %s", s.Name, s.Err, s.Note)
		}
		t.Fatalf("%d steps failed with expect-request=false against the upstream shape", n)
	}
}

// TestProbeWithoutMQTTTLS: no TLS on the listener is a deployment choice, reported as tls=0 rather
// than as the failure.
func TestProbeWithoutMQTTTLS(t *testing.T) {
	r := startBroker(t, true)
	r.b.mqttTLS = nil
	r.b.check()
	if r.b.metrics.mqttTLS.Load() != 0 || r.b.metrics.clientAuthRequest.Load() != 0 {
		t.Fatalf("gauges tls=%d client_auth_request=%d, want 0 and 0", r.b.metrics.mqttTLS.Load(), r.b.metrics.clientAuthRequest.Load())
	}
	if r.b.metrics.probeErrors.Load() != 0 {
		t.Fatal("a listener without TLS was counted as a probe error")
	}
}

// TestProbeSkippedInLameDuck: once a lame duck shutdown has begun, closing listeners are not
// reported as a lost setting.
func TestProbeSkippedInLameDuck(t *testing.T) {
	r := startBroker(t, true)
	r.b.check()
	r.b.ldm.Store(true)
	before := r.b.metrics.probes.Load()
	r.b.check()
	if r.b.metrics.probes.Load() != before {
		t.Fatal("a probe ran during lame duck mode")
	}
}

func TestMetricsExposition(t *testing.T) {
	m := newBrokerMetrics()
	m.mqttTLS.Store(1)
	m.clientAuthRequest.Store(1)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`dc_broker_info{library_version="` + server.VERSION + `",build="`,
		"\ndc_broker_mqtt_tls 1\n",
		"\ndc_broker_mqtt_client_auth_request 1\n",
		"\ndc_broker_mqtt_client_auth_probes_total 0\n",
		"\ndc_broker_mqtt_client_auth_probe_errors_total 0\n",
		"# TYPE dc_broker_mqtt_client_auth_request gauge\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition lacks %q:\n%s", want, body)
		}
	}
	// The build label is the toolchain and nothing that moves per commit, so an unchanged broker
	// keeps its image digest across releases.
	if bi, ok := debug.ReadBuildInfo(); ok && m.build != bi.GoVersion {
		t.Errorf("dc_broker_info build = %q, want the Go version %q alone", m.build, bi.GoVersion)
	}
	if got := labelValue("a\"b\\c\nd"); got != `a\"b\\c\nd` {
		t.Errorf("labelValue = %q", got)
	}
}

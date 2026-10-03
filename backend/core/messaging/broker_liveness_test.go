// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// A broker connection that died WITHOUT being closed must be given up within seconds.
//
// A server whose machine is reset or lost, or that a partition cuts off, closes nothing,
// so the client finds out only from its own liveness checks; with the library's defaults
// (a 2-minute ping, two unanswered allowed, a 1-minute write deadline that does not drop
// the connection) a lost node stopped all HTTP ingest for five and a half minutes.
//
// 🔴 EVERY FIGURE HERE IS A LITERAL, and every test here goes through ExecuteInitialize
// and the public nats.go API only. That is what lets this file be dropped onto a tree
// WITHOUT the fix and fail there by value ("PingInterval = 2m0s", "still CONNECTED
// after 35s"), instead of failing to compile, which is not a verdict. The assertions
// that need the fix's own names (which detector fired, what was logged, the counter)
// live in broker_liveness_attribution_test.go and are reached through
// livenessAttribution.
//
// The figures are published: the node-loss and observability docs, both locales, and
// the BrokerConnectionDiedSilently alert quote them. Change them there too.

// livenessAttribution, when set (broker_liveness_attribution_test.go sets it), checks
// what the fix records about the connection at each phase of a dead-connection test:
// detectedBy is "" while the connection is healthy, then "ping" or "write".
var livenessAttribution func(t *testing.T, logs *dctest.LogSink, nmgr *NatsManager, detectedBy string)

func attribute(t *testing.T, logs *dctest.LogSink, nmgr *NatsManager, detectedBy string) {
	t.Helper()
	if livenessAttribution != nil {
		livenessAttribution(t, logs, nmgr, detectedBy)
	}
}

// The main connection carries the bounded ping and write timeout, through the production
// connect path, in plaintext AND over TLS, which is how a real instance's broker runs.
func TestTheMainConnectionCarriesTheLivenessOptions(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "plaintext"
		if secure {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			addr, ca := startLivenessBroker(t, secure)
			nmgr := managerAt(t, addr, ca)
			if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
				t.Fatalf("connecting to the embedded broker: %v", err)
			}
			t.Cleanup(func() { terminateAndWait(t, logs, nmgr) })
			if _, err := nmgr.nc.TLSConnectionState(); (err == nil) != secure {
				t.Fatalf("TLS in use = %v, want %v: the variant does not test what it is named for",
					err == nil, secure)
			}

			o := nmgr.nc.Opts
			if o.PingInterval != 10*time.Second {
				t.Errorf("PingInterval = %s, want 10s", o.PingInterval)
			}
			if o.MaxPingsOut != 2 {
				t.Errorf("MaxPingsOut = %d, want 2", o.MaxPingsOut)
			}
			if o.FlusherTimeout != 10*time.Second {
				t.Errorf("FlusherTimeout = %s, want 10s", o.FlusherTimeout)
			}
			if o.CustomDialer == nil {
				t.Error("no CustomDialer: a write that times out does not close the connection, so a " +
					"loaded connection to a dead server is kept until the ping catches up with it")
			}
		})
	}
}

// An IDLE connection whose server stops answering is given up by the ping, within
// (2+1) x 10 s of the last answer (plus slack), and the client reconnects.
func TestADeadIdleConnectionIsGivenUpWithinTheBound(t *testing.T) {
	runDeadConnection(t, false, false, 30*time.Second+5*time.Second, "ping")
}

// A LOADED connection is given up by the write timeout: once the socket stops draining, a
// write blocks, and after 10 s (plus slack) the connection is closed and the client
// reconnects. Without the closer, the ping queues behind the blocked writers and the
// connection is kept for minutes. Over TLS too: the closer sits beneath the TLS layer.
func TestADeadLoadedConnectionIsGivenUpByTheWriteTimeout(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "plaintext"
		if secure {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			runDeadConnection(t, secure, true, 10*time.Second+5*time.Second, "write")
		})
	}
}

// runDeadConnection connects a manager through a proxy, proves the connection is NOT given
// up while its server answers (for longer than a ping interval and a write timeout), then
// freezes the proxy and requires the client to leave CONNECTED within bound and to
// reconnect through a fresh, relayed connection.
func runDeadConnection(t *testing.T, secure, loaded bool, bound time.Duration, detectedBy string) {
	t.Helper()
	logs := captureLogs(t)
	addr, ca := startLivenessBroker(t, secure)
	proxy := dctest.StartTCPProxy(t, addr.String())
	paddr, err := net.ResolveTCPAddr("tcp", proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	nmgr := managerAt(t, paddr, ca)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting through the proxy: %v", err)
	}
	t.Cleanup(func() { terminateAndWait(t, logs, nmgr) })
	nc := nmgr.nc
	if !nc.IsConnected() {
		t.Fatal("not connected after ExecuteInitialize; nothing below would mean anything")
	}
	statuses := nc.StatusChanged(nats.RECONNECTING, nats.DISCONNECTED, nats.CLOSED)

	stopLoad := make(chan struct{})
	loadDone := make(chan struct{})
	if loaded {
		// Paced, not a tight loop: about 2 MiB/s fills the frozen socket's buffers in a few
		// seconds, and stays deterministic under -race on a busy machine.
		go func() {
			defer close(loadDone)
			payload := make([]byte, 16<<10)
			tick := time.NewTicker(8 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stopLoad:
					return
				case <-tick.C:
					_ = nc.Publish("liveness.load", payload)
				}
			}
		}()
	} else {
		close(loadDone)
	}
	stop := func() {
		select {
		case <-stopLoad:
		default:
			close(stopLoad)
		}
		<-loadDone
	}
	t.Cleanup(stop)

	// Healthy control: longer than one ping interval and one write timeout. A closer that
	// fires on a healthy connection, or a ping that gives one up, fails here.
	select {
	case s := <-statuses:
		t.Fatalf("the client left CONNECTED (%s) while its server was answering", s)
	case <-time.After(12 * time.Second):
	}
	if err := nc.FlushTimeout(5 * time.Second); err != nil {
		t.Fatalf("a round trip on the healthy connection failed: %v", err)
	}
	attribute(t, logs, nmgr, "")

	proxy.Freeze()
	frozen := time.Now()
	select {
	case s := <-statuses:
		if s != nats.RECONNECTING {
			t.Fatalf("the client went to %s, want RECONNECTING: a dead connection is reconnected, "+
				"never given up for good", s)
		}
		t.Logf("given up %s after the server stopped answering", time.Since(frozen).Round(time.Millisecond))
	case <-time.After(bound):
		t.Fatalf("still CONNECTED %s after the server stopped answering; a connection that died "+
			"without being closed must be given up within %s (the %s path)", bound, bound, detectedBy)
	}
	stop()

	deadline := time.Now().Add(10 * time.Second)
	for !nc.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatalf("not reconnected 10s after giving the dead connection up (status %s)", nc.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := nc.FlushTimeout(5 * time.Second); err != nil {
		t.Fatalf("a round trip on the reconnected connection failed: %v", err)
	}
	attribute(t, logs, nmgr, detectedBy)
}

// managerAt builds a manager through the real constructor, pointed at addr, dialling
// over TLS when ca is non-empty.
func managerAt(t *testing.T, addr *net.TCPAddr, ca string) *NatsManager {
	t.Helper()
	cfg := &config.InstanceConfiguration{}
	cfg.ApplyDefaults()
	cfg.Infrastructure.Nats.Hostname = addr.IP.String()
	cfg.Infrastructure.Nats.Port = uint32(addr.Port)
	if ca != "" {
		cfg.Infrastructure.Nats.Tls = config.NatsTlsConfiguration{Enabled: true, Ca: ca}
	}
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: areaFor(t), InstanceConfiguration: *cfg}
	return NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
}

// startLivenessBroker starts an embedded broker on an ephemeral port, terminating TLS
// with a freshly minted certificate for 127.0.0.1 when secure. It returns the address
// and, when secure, the PEM a client verifies it with.
func startLivenessBroker(t *testing.T, secure bool) (*net.TCPAddr, string) {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  dctest.JetStreamStoreDir(t),
	}
	var caPEM string
	if secure {
		cert, pemBytes := selfSignedServerCert(t)
		opts.TLS = true
		opts.TLSTimeout = 5
		opts.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		caPEM = string(pemBytes)
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("embedded nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	return srv.Addr().(*net.TCPAddr), caPEM
}

// selfSignedServerCert mints a self-signed certificate for 127.0.0.1 that is its own CA.
func selfSignedServerCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "liveness-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

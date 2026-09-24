// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// guardAllowing builds a real egress guard with the given allowances. No allowances is
// production's default.
func guardAllowing(prefixes ...string) *egress.Guard {
	ps := make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		ps = append(ps, netip.MustParsePrefix(p))
	}
	return egress.NewGuard(ps)
}

func sendCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// countingListener counts accepted connections and hands each to serve.
type countingListener struct {
	net.Listener
	accepts atomic.Int32
	wg      sync.WaitGroup
}

func listen(t *testing.T, network, addr string, serve func(net.Conn)) *countingListener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatalf("listen %s %s: %v", network, addr, err)
	}
	cl := &countingListener{Listener: ln}
	cl.wg.Add(1)
	go func() {
		defer cl.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			cl.accepts.Add(1)
			cl.wg.Add(1)
			go func() {
				defer cl.wg.Done()
				defer c.Close()
				if serve != nil {
					serve(c)
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		cl.wg.Wait()
	})
	return cl
}

func (l *countingListener) port() string {
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}

// fakeBroker is a minimal MQTT 3.1.1 broker: it acknowledges CONNECT, records each PUBLISH
// and acknowledges QoS 1, and answers PINGREQ.
type fakeBroker struct {
	mu        sync.Mutex
	published []fakePublish
	clientIDs []string
}

type fakePublish struct {
	topic   string
	payload string
}

func (b *fakeBroker) messages() []fakePublish {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]fakePublish(nil), b.published...)
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; i < 4; i++ {
		c, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(c&0x7f) * mult
		mult *= 128
		if c&0x80 == 0 {
			break
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return h, body, nil
}

func (b *fakeBroker) serve(c io.ReadWriter) {
	r := bufio.NewReader(c)
	for {
		h, body, err := readPacket(r)
		if err != nil {
			return
		}
		switch h >> 4 {
		case 1: // CONNECT: protocol name, level, flags, keepalive, then the client id
			if len(body) >= 12 {
				pl := int(binary.BigEndian.Uint16(body[0:2]))
				off := 2 + pl + 4
				if off+2 <= len(body) {
					idl := int(binary.BigEndian.Uint16(body[off : off+2]))
					if off+2+idl <= len(body) {
						b.mu.Lock()
						b.clientIDs = append(b.clientIDs, string(body[off+2:off+2+idl]))
						b.mu.Unlock()
					}
				}
			}
			if _, err := c.Write([]byte{0x20, 0x02, 0x00, 0x00}); err != nil {
				return
			}
		case 3: // PUBLISH
			qos := (h >> 1) & 0x3
			tl := int(binary.BigEndian.Uint16(body[0:2]))
			topic := string(body[2 : 2+tl])
			rest := body[2+tl:]
			var id []byte
			if qos > 0 {
				id, rest = rest[:2], rest[2:]
			}
			b.mu.Lock()
			b.published = append(b.published, fakePublish{topic: topic, payload: string(rest)})
			b.mu.Unlock()
			if qos == 1 {
				if _, err := c.Write([]byte{0x40, 0x02, id[0], id[1]}); err != nil {
					return
				}
			}
		case 12: // PINGREQ
			if _, err := c.Write([]byte{0xD0, 0x00}); err != nil {
				return
			}
		case 14: // DISCONNECT
			return
		}
	}
}

// startBroker runs a fake MQTT broker on addr ("127.0.0.1:0").
func startBroker(t *testing.T, addr string) (*fakeBroker, *countingListener) {
	t.Helper()
	b := &fakeBroker{}
	return b, listen(t, "tcp", addr, func(c net.Conn) { b.serve(c) })
}

// testCA is a throwaway certificate authority for the TLS tests.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "devicechain test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// writeCA stores the CA's certificate and key in dir so a child process can issue leaves
// from the same CA its root pool trusts.
func writeCA(t *testing.T, ca *testCA, dir string) {
	t.Helper()
	keyDER, err := x509.MarshalECPrivateKey(ca.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.der"), ca.cert.Raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), keyDER, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCA(t *testing.T, dir string) *testCA {
	t.Helper()
	der, err := os.ReadFile(filepath.Join(dir, "ca.der"))
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

// leaf issues a server certificate for the given DNS name and 127.0.0.1.
func (ca *testCA) leaf(t *testing.T, dnsName string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// sniRecorder records the SNI each TLS handshake presented.
type sniRecorder struct {
	mu    sync.Mutex
	names []string
}

func (s *sniRecorder) config(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			s.mu.Lock()
			s.names = append(s.names, h.ServerName)
			s.mu.Unlock()
			return nil, nil
		},
	}
}

func (s *sniRecorder) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...)
}

// wsBrokerHandler upgrades to WebSocket and speaks MQTT through the same adapter the client
// uses.
func wsBrokerHandler(b *fakeBroker) http.Handler {
	up := websocket.Upgrader{Subprotocols: []string{"mqtt"}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		b.serve(newWSConn(c))
	})
}

// childEnv marks a re-executed test process. Tests that must change process-global state
// the runtime reads once (the system root pool, proxy environment variables) run their body
// in a child with that state set, so the parent's other tests are unaffected.
const childEnv = "DC_PUBLISH_TEST_CHILD"

// runInChild re-executes the current test binary running only the named test with env
// added, and fails the parent if the child fails. It returns true in the child, where the
// caller runs its body.
func runInChild(t *testing.T, name string, env ...string) bool {
	t.Helper()
	if os.Getenv(childEnv) == name {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^"+name+"$", "-test.count=1", "-test.v")
	cmd.Env = append(append(os.Environ(), childEnv+"="+name), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child %s failed: %v\n%s", name, err, out)
	}
	t.Logf("child %s:\n%s", name, out)
	return false
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// openFDs counts this process's open file descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot count open descriptors here: %v", err)
	}
	return len(ents)
}

// settlesTo polls until f() <= want or the timeout passes, returning the last value.
func settlesTo(timeout time.Duration, want int, f func() int) int {
	deadline := time.Now().Add(timeout)
	for {
		got := f()
		if got <= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func goroutines() int { return runtime.NumGoroutine() }

// heapGrowth reports bytes allocated while f ran.
func heapGrowth(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func isBlocked(err error) bool { return errors.Is(err, egress.ErrBlocked) }

func mqttTarget(t *testing.T, urls ...string) connectorspec.MQTTTarget {
	t.Helper()
	m := connectorspec.MQTTTarget{Topic: "alerts/1", QoS: 1}
	for _, u := range urls {
		m.Brokers = append(m.Brokers, mustURL(t, u))
	}
	return m
}

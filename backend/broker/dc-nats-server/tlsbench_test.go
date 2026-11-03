// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

// A reproducible throughput/CPU benchmark for the broker's TLS write path.
//
// Two nats-server nodes run as CHILD PROCESSES (this test binary re-executed), joined by a
// route cluster, so the cost of the servers is measured apart from the clients' by reading each
// child's own user+system CPU time at exit. Subscribers are split across both nodes and
// publishers feed both, so client-leg and route-leg writes are both exercised.
//
// It is skipped unless DCB_RUN=1 (it is a measurement, not a test). Knobs, all environment:
//
//	DCB_TLS=1|0        TLS on client and route connections (0 = the plaintext negative control)
//	DCB_RATE=6000      aggregate published msgs/s; 0 = unthrottled blast of DCB_MSGS messages
//	DCB_SECS=15        duration at a fixed rate
//	DCB_MSGS=200000    messages in blast mode
//	DCB_SIZE=300       payload bytes
//	DCB_SUBS=8         subscribers (each receives every message)
//	DCB_PUBS=4         publishers
//
// With -tags dcbench and a nats-server build that exports RawWrites/RawWriteBytes (the bench
// fork used while evaluating the coalescing patch), each node also reports its raw socket
// writes. Without the tag that column is zero.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// benchNodeMain is the child: DCB_NODE=<name>, DCB_CPORT, DCB_RPORT, DCB_PEER (route url or ""),
// DCB_DIR (cert dir), DCB_TLS. It runs until its stdin closes, then prints a stats line.
func benchNodeMain() {
	useTLS := os.Getenv("DCB_TLS") == "1"
	dir := os.Getenv("DCB_DIR")
	cport, _ := strconv.Atoi(os.Getenv("DCB_CPORT"))
	rport, _ := strconv.Atoi(os.Getenv("DCB_RPORT"))
	opts := &server.Options{
		ServerName: os.Getenv("DCB_NODE"), Host: "127.0.0.1", Port: cport,
		NoLog: true, NoSigs: true,
		Cluster: server.ClusterOpts{Name: "bench", Host: "127.0.0.1", Port: rport},
	}
	if os.Getenv("DCB_JS") == "1" {
		opts.JetStream = true
		opts.StoreDir = os.Getenv("DCB_STORE")
	}
	if useTLS {
		cert, err := tls.LoadX509KeyPair(dir+"/c.pem", dir+"/k.pem")
		if err != nil {
			panic(err)
		}
		pool := x509.NewCertPool()
		pemBytes, _ := os.ReadFile(dir + "/c.pem")
		pool.AppendCertsFromPEM(pemBytes)
		cfg := &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ClientCAs: pool, MinVersion: tls.VersionTLS13}
		opts.TLSConfig = cfg
		opts.TLSTimeout = 5
		opts.Cluster.TLSConfig = cfg
		opts.Cluster.TLSTimeout = 5
	}
	if peer := os.Getenv("DCB_PEER"); peer != "" {
		opts.Routes = server.RoutesFromStr(peer)
	}
	if os.Getenv("DCB_LOG") == "1" {
		opts.NoLog = false
		opts.Debug = false
	}
	s, err := server.NewServer(opts)
	if os.Getenv("DCB_LOG") == "1" {
		s.ConfigureLogger()
	}
	if err != nil {
		panic(err)
	}
	go s.Start()
	if !s.ReadyForConnections(90 * time.Second) {
		panic("not ready")
	}
	fmt.Println("READY")
	// Block until the parent closes our stdin.
	_, _ = bufio.NewReader(os.Stdin).ReadString(0)
	fmt.Printf("SLOW %d\n", s.NumSlowConsumers())
	s.Shutdown()
	s.WaitForShutdown()
	w, wb := benchRawWrites()
	fmt.Printf("STATS %d %d\n", w, wb)
}

func writeBenchCerts(t *testing.T, dir string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "bench"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(dir+"/c.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(dir+"/k.pem", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
}

type benchNode struct {
	cmd    *exec.Cmd
	stdin  interface{ Close() error }
	out    *bufio.Scanner
	cport  int
	rport  int
	cpu    time.Duration
	slow   int64
	writes int64
	wbytes int64
}

func startBenchNode(t *testing.T, name, dir string, cport, rport int, peer string, useTLS bool, extra ...string) *benchNode {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	tl := "0"
	if useTLS {
		tl = "1"
	}
	cmd.Env = append(append(os.Environ(), extra...), "DCB_CHILD=1", "DCB_NODE="+name, "DCB_DIR="+dir, "DCB_TLS="+tl,
		"DCB_CPORT="+strconv.Itoa(cport), "DCB_RPORT="+strconv.Itoa(rport), "DCB_PEER="+peer)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	n := &benchNode{cmd: cmd, stdin: in, out: bufio.NewScanner(out), cport: cport, rport: rport}
	for n.out.Scan() {
		if n.out.Text() == "READY" {
			return n
		}
	}
	t.Fatalf("node %s did not become ready", name)
	return nil
}

func (n *benchNode) stop(t *testing.T) {
	n.stdin.Close()
	for n.out.Scan() {
		if f := strings.Fields(n.out.Text()); len(f) == 2 && f[0] == "SLOW" {
			n.slow, _ = strconv.ParseInt(f[1], 10, 64)
		} else if len(f) == 3 && f[0] == "STATS" {
			n.writes, _ = strconv.ParseInt(f[1], 10, 64)
			n.wbytes, _ = strconv.ParseInt(f[2], 10, 64)
		}
	}
	n.cmd.Wait()
	n.cpu = n.cmd.ProcessState.UserTime() + n.cmd.ProcessState.SystemTime()
}

func TestTLSThroughputBench(t *testing.T) {
	if os.Getenv("DCB_RUN") != "1" {
		t.Skip("measurement only: set DCB_RUN=1")
	}
	useTLS := envInt("DCB_TLS", 1) == 1
	rate := envInt("DCB_RATE", 6000)
	secs := envInt("DCB_SECS", 15)
	blast := envInt("DCB_MSGS", 200000)
	size := envInt("DCB_SIZE", 300)
	nsubs := envInt("DCB_SUBS", 8)
	npubs := envInt("DCB_PUBS", 4)

	dir := t.TempDir()
	writeBenchCerts(t, dir)
	cA, rA, cB, rB := freePort(), freePort(), freePort(), freePort()
	a := startBenchNode(t, "A", dir, cA, rA, "", useTLS)
	b := startBenchNode(t, "B", dir, cB, rB, fmt.Sprintf("nats-route://127.0.0.1:%d", rA), useTLS)

	pool := x509.NewCertPool()
	pb, _ := os.ReadFile(dir + "/c.pem")
	pool.AppendCertsFromPEM(pb)
	connect := func(port int) *nats.Conn {
		o := []nats.Option{nats.MaxReconnects(0)}
		url := fmt.Sprintf("nats://127.0.0.1:%d", port)
		if useTLS {
			o = append(o, nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}))
			url = fmt.Sprintf("tls://127.0.0.1:%d", port)
		}
		nc, err := nats.Connect(url, o...)
		if err != nil {
			t.Fatal(err)
		}
		return nc
	}

	var got atomic.Int64
	var subs []*nats.Conn
	var subList []*nats.Subscription
	for i := 0; i < nsubs; i++ {
		port := cA
		if i%2 == 1 {
			port = cB
		}
		nc := connect(port)
		subs = append(subs, nc)
		sub, _ := nc.Subscribe("bench.>", func(m *nats.Msg) { got.Add(1) })
		subList = append(subList, sub)
		nc.Flush()
	}
	var pubs []*nats.Conn
	for i := 0; i < npubs; i++ {
		port := cA
		if i%2 == 1 {
			port = cB
		}
		pubs = append(pubs, connect(port))
	}
	// Let route interest propagate before measuring.
	time.Sleep(1500 * time.Millisecond)

	payload := make([]byte, size)
	var total int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	if rate > 0 {
		total = rate * secs
		per := rate / npubs
		done := make(chan struct{}, npubs)
		for pi, nc := range pubs {
			go func(pi int, nc *nats.Conn) {
				tick := 10 * time.Millisecond
				perTick := per * int(tick/time.Millisecond) / 1000
				tk := time.NewTicker(tick)
				defer tk.Stop()
				deadline := start.Add(time.Duration(secs) * time.Second)
				subj := fmt.Sprintf("bench.%d", pi)
				for time.Now().Before(deadline) {
					<-tk.C
					for i := 0; i < perTick; i++ {
						nc.Publish(subj, payload)
					}
				}
				nc.Flush()
				done <- struct{}{}
			}(pi, nc)
		}
		for range pubs {
			<-done
		}
	} else {
		total = blast
		done := make(chan struct{}, npubs)
		for pi, nc := range pubs {
			go func(pi int, nc *nats.Conn) {
				subj := fmt.Sprintf("bench.%d", pi)
				for i := 0; i < blast/npubs; i++ {
					nc.Publish(subj, payload)
				}
				nc.Flush()
				done <- struct{}{}
			}(pi, nc)
		}
		for range pubs {
			<-done
		}
		total = (blast / npubs) * npubs
	}
	want := int64(total) * int64(nsubs)
	deadline := time.Now().Add(30 * time.Second)
	for got.Load() < want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	elapsed := time.Since(start)
	delivered := got.Load()
	_ = ctx
	var clientDropped int64
	for _, sb := range subList {
		d, _ := sb.Dropped()
		clientDropped += int64(d)
	}
	for _, nc := range append(subs, pubs...) {
		nc.Close()
	}
	a.stop(t)
	b.stop(t)
	cpu := a.cpu + b.cpu
	writes := a.writes + b.writes
	fmt.Printf("RESULT tls=%v rate=%d size=%d subs=%d pubs=%d published=%d delivered=%d/%d wall=%.2fs "+
		"msgs/s(pub)=%.0f deliveries/s=%.0f servercpu=%.2fs cpu_s_per_1k_published=%.4f cpu_s_per_1k_deliveries=%.4f rawwrites=%d rawwrites_per_delivery=%.4f slow_consumers=%d client_dropped=%d\n",
		useTLS, rate, size, nsubs, npubs, total, delivered, want, elapsed.Seconds(),
		float64(total)/elapsed.Seconds(), float64(delivered)/elapsed.Seconds(), cpu.Seconds(),
		cpu.Seconds()/float64(total)*1000, cpu.Seconds()/float64(delivered)*1000,
		writes, float64(writes)/float64(delivered), a.slow+b.slow, clientDropped)
}

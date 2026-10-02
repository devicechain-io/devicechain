// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// go test runs packages in parallel processes, and a cluster fixture's servers must never
// reach another fixture's. These tests pin the two things that keep them apart: a cluster
// name of its own, and routes that dial only addresses the fixture holds.

// Every construction has a cluster name no other one has, within this process (the
// counter) and across processes (the process id). Three constructions, of both public
// fixtures, in one test. Uniqueness across processes cannot be seen from inside one, so
// what is asserted is the part that gives it: each name carries this process's id.
func TestEveryClusterHasANameOfItsOwn(t *testing.T) {
	a := StartJetStreamCluster(t, 3)
	b := StartJetStreamCluster(t, 3)
	c, _ := StartJetStreamClusterWithRouteFaults(t, 3)
	pattern := regexp.MustCompile(`^dctest-\d+-\d+$`)
	ofThisProcess := fmt.Sprintf("dctest-%d-", os.Getpid())
	seen := map[string]int{}
	for k, servers := range [][]*natsserver.Server{a, b, c} {
		name := servers[0].ClusterName()
		for _, s := range servers {
			if s.ClusterName() != name {
				t.Fatalf("cluster %d: %s is in cluster %q, %s in %q", k, servers[0].Name(), name, s.Name(), s.ClusterName())
			}
		}
		if !pattern.MatchString(name) {
			t.Errorf("cluster %d is named %q, which does not match %s", k, name, pattern)
		}
		if !strings.HasPrefix(name, ofThisProcess) {
			t.Errorf("cluster %d is named %q, which does not begin %q: without this process's id, another "+
				"test binary's construction can have the same name", k, name, ofThisProcess)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("clusters %d and %d are both named %q", prev, k, name)
		}
		seen[name] = k
	}
}

// A server of another construction that reaches one of this cluster's route listeners,
// the way another test binary's server can, is refused by the server it reached, and the
// cluster stays as it was. The foreign server is named n2, as another construction's
// second server is.
func TestAServerOfAnotherClusterThatReachesARoutePortIsRefused(t *testing.T) {
	c := startedCluster(t, 3)
	f, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		ServerName: "n2",
		JetStream:  true,
		StoreDir:   JetStreamStoreDir(t),
		Cluster:    natsserver.ClusterOpts{Name: clusterName(), Host: "127.0.0.1", Port: -1},
		Routes:     natsserver.RoutesFromStr("nats-route://" + c.servers[0].ClusterAddr().String()),
	})
	if err != nil {
		t.Fatalf("the foreign server: %v", err)
	}
	go f.Start()
	defer f.Shutdown()

	// The refusal must be SEEN before the counts below are trusted: a foreign server
	// that never dialled would leave them unchanged as well.
	const refused = "does not match"
	deadline := time.Now().Add(10 * time.Second)
	for !logged(c.logs[0], refused) {
		if time.Now().After(deadline) {
			t.Fatalf("n1 never logged a refusal (%q) in 10 s, so the foreign server's dial never reached it "+
				"and this proves nothing; n1 logged %q", refused, c.logs[0].snapshot())
		}
		time.Sleep(50 * time.Millisecond)
	}
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		for _, s := range c.servers {
			if n := s.NumRemotes(); n != 2 {
				t.Fatalf("%s is routed to %d servers, want 2: the foreign server was admitted", s.Name(), n)
			}
		}
		if n := f.NumRemotes(); n != 0 {
			t.Fatalf("the foreign server is routed to %d servers, want 0", n)
		}
		for _, s := range c.servers {
			if !s.JetStreamIsLeader() {
				continue
			}
			jsz, err := s.Jsz(&natsserver.JSzOptions{})
			if err == nil && jsz.Meta != nil && len(jsz.Meta.Replicas) != 2 {
				t.Fatalf("meta leader %s reports %d peers, want 2", s.Name(), len(jsz.Meta.Replicas))
			}
		}
	}
}

func logged(l *serverLog, substr string) bool {
	for _, line := range l.snapshot() {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// heldByAnother reports whether something already holds addr: whether this process
// cannot listen on it.
func heldByAnother(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// Every route a server is given points at an address that is held from before the server
// is created: none is free for another process to take between the fixture choosing it
// and the server dialling it. Each server is given a route to every other server.
func TestEveryRouteAServerIsGivenIsHeldWhenTheServerIsCreated(t *testing.T) {
	// The negative control: the check must call a released port unowned.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	released := l.Addr().String()
	l.Close()
	if heldByAnother(released) {
		t.Fatalf("heldByAnother called the released address %s held, so it proves nothing", released)
	}

	rec := &recordingTB{TB: t}
	defer rec.runCleanups()
	var mu sync.Mutex
	var problems []string
	h := defaultClusterHooks()
	start := h.start
	h.start = func(s *natsserver.Server) {
		defer start(s)
		vz, err := s.Varz(nil)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			problems = append(problems, fmt.Sprintf("varz of %s: %v", s.Name(), err))
			return
		}
		if n := len(vz.Cluster.URLs); n != 2 {
			problems = append(problems, fmt.Sprintf("%s was given %d routes, want 2", s.Name(), n))
		}
		targets := append([]string(nil), vz.Cluster.URLs...)
		if vz.Cluster.Port > 0 {
			targets = append(targets, fmt.Sprintf("127.0.0.1:%d", vz.Cluster.Port))
		}
		for _, u := range targets {
			if !heldByAnother(u) {
				problems = append(problems, fmt.Sprintf("route target %s of %s was unowned when %s was created",
					u, s.Name(), s.Name()))
			}
		}
	}
	c, err := startCluster(rec, 3, h, clusterStartBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer c.faults.close()
	defer shutdownServers(c.servers)
	mu.Lock()
	defer mu.Unlock()
	if len(problems) > 0 {
		t.Fatal(strings.Join(problems, "; "))
	}
}

// The address each server advertises is held for as long as the cluster runs, and it
// REFUSES a connection rather than accepting and closing one. A server that dials a
// gossiped address and is refused gives up; one whose connection is accepted and then
// lost before the handshake reconnects at once, for the life of the cluster.
func TestEveryAdvertisedRouteAddressIsHeldAndRefusesWhileTheClusterRuns(t *testing.T) {
	c := startedCluster(t, 3)
	for i, o := range c.opts {
		addr := o.Cluster.Advertise
		if addr == "" {
			t.Fatalf("%s advertises nothing, so it gossips its own route listener", c.servers[i].Name())
		}
		if !heldByAnother(addr) {
			t.Fatalf("the address %s advertises, %s, could be taken by another process", c.servers[i].Name(), addr)
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("a connection to the address %s advertises, %s, was accepted; want it refused",
				c.servers[i].Name(), addr)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("a connection to the address %s advertises, %s, failed with %v; want it refused",
				c.servers[i].Name(), addr, err)
		}
	}
}

// A proxy finds its server as a connection arrives, and dials nothing once that server
// has shut down, even when another process has since bound the port the server's route
// listener had.
func TestAProxyNeverDialsAServerThatHasShutDown(t *testing.T) {
	servers, faults := StartJetStreamClusterWithRouteFaults(t, 3)

	// The positive control: a proxy to a running server does forward, so the silence
	// on the shut-down server's proxy below means something.
	live, err := net.Dial("tcp", faults.proxyAddr(0, 1))
	if err != nil {
		t.Fatalf("dialling the proxy to n2: %v", err)
	}
	defer live.Close()
	if got := readFor(live, 2*time.Second); !bytes.Contains(got, []byte("INFO {")) {
		t.Fatalf("the proxy to the running n2 did not forward its INFO in 2 s; read %q", got)
	}

	p := servers[2].ClusterAddr().Port
	servers[2].Shutdown()
	stand, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Skipf("another process bound the freed port %d first, so the test cannot stand in for it: %v", p, err)
	}
	defer stand.Close()
	accepted := make(chan struct{}, 64)
	go func() {
		for {
			c, err := stand.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			c.Close()
		}
	}()

	conn, err := net.Dial("tcp", faults.proxyAddr(0, 2))
	if err != nil {
		t.Fatalf("dialling the proxy to n3: %v", err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("x"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, rerr := conn.Read(buf)
	if n > 0 || rerr == nil {
		t.Fatalf("the proxy to n3, which has shut down, answered %q", buf[:n])
	}
	var ne net.Error
	if errors.As(rerr, &ne) && ne.Timeout() {
		t.Fatal("the proxy to n3, which has shut down, held the connection open instead of closing it")
	}
	select {
	case <-accepted:
		t.Fatalf("a proxy dialled port %d, which n3 had before it shut down", p)
	case <-time.After(2 * time.Second):
	}
}

// readFor reads from c for d and returns what arrived.
func readFor(c net.Conn, d time.Duration) []byte {
	c.SetReadDeadline(time.Now().Add(d))
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil || bytes.Contains(out, []byte("INFO {")) {
			return out
		}
	}
}

// A cluster started by the plain fixture runs on the proxy mesh too: every route its
// servers report is one a proxy carries.
func TestEveryRouteOfAPlainClusterIsCarriedByAProxy(t *testing.T) {
	c := startedCluster(t, 3)
	for k := 0; k < 5; k++ {
		routes := 0
		for _, s := range c.servers {
			routes += s.NumRoutes()
		}
		if proxied := c.faults.LiveRouteConnections(); routes == 0 || routes != 2*proxied {
			t.Fatalf("the servers report %d route connections and the proxies carry %d; want twice as many", routes, proxied)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A cluster whose routes go around the proxies fails to start, as a route that bypasses
// them. Advertise is what keeps gossiped routes off the servers' real route listeners in
// every other construction; without it, the servers gossip those addresses and dial them
// directly, and only awaitAllRoutesProxied stands between that cluster and a test whose
// Silence would silence nothing.
func TestAClusterWhoseRoutesBypassTheProxiesFailsToStart(t *testing.T) {
	rec := &recordingTB{TB: t}
	defer rec.runCleanups()
	h := defaultClusterHooks()
	h.configure = func(_ int, o *natsserver.Options) { o.Cluster.Advertise = "" }

	c, err := startCluster(rec, 3, h, clusterStartBudget)
	if err == nil {
		defer c.faults.close()
		defer shutdownServers(c.servers)
		t.Fatal("a cluster that advertises its real route listeners was reported started, so routes that " +
			"bypass the proxies go unnoticed")
	}
	if !errors.Is(err, errRoutesBypassProxies) {
		t.Fatalf("the start failed, but not as a route that bypasses the proxies: %v", err)
	}
}

// A cluster of fewer than two servers is refused at once, loudly: a clustered server
// needs a route to another, and one built anyway would never form.
func TestAClusterOfFewerThanTwoServersIsRefused(t *testing.T) {
	for _, size := range []int{0, 1} {
		rec := &recordingTB{TB: t}
		c, err := startCluster(rec, size, defaultClusterHooks(), 10*time.Second)
		if err == nil {
			shutdownServers(c.servers)
			c.faults.close()
			rec.runCleanups()
			t.Fatalf("a cluster of %d server(s) was reported started", size)
		}
		rec.runCleanups()
		if want := fmt.Sprintf("a JetStream cluster of %d server(s) cannot be built", size); !strings.Contains(err.Error(), want) {
			t.Fatalf("a cluster of %d server(s) failed, but not by being refused (%q): %v", size, want, err)
		}
	}
}

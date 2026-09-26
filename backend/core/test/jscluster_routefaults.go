// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// RouteFaults controls the proxies that carry every route between the servers of a
// cluster started by StartJetStreamClusterWithRouteFaults.
//
// 🔑 IT EXISTS BECAUSE A CLEAN SHUTDOWN DOES NOT REPRODUCE A LOST NODE. Shutdown closes
// the node's sockets, so every other server drops its routes, and the interest behind
// them, at once. A node that drops off the network does not close anything: the other
// servers keep its routes until their pings to it go unanswered (a route pings at most
// every 30 s and gives up after two unanswered pings), and for that minute or so they go
// on forwarding requests to it that will never be answered. That is the state Silence
// produces, and the one a lost node on a real network is in.
type RouteFaults struct {
	mu       sync.Mutex
	cond     *sync.Cond
	silenced map[int]bool
	held     map[int]int64
	closed   bool

	listeners []net.Listener
	conns     map[net.Conn]struct{}
	// live counts the proxied connection pairs that are open: one per route connection
	// the servers can have made through a proxy.
	live int
}

// Silence stops every byte to and from server i on its routes WITHOUT closing any
// connection, the way a node dropped off the network behaves: the other servers keep the
// routes, and the interest behind them, until their pings run out.
func (f *RouteFaults) Silence(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.silenced[i] = true
	f.held[i] = 0
}

// Restore lets the routes of server i carry traffic again. What was held back is
// delivered, in order, so each route's byte stream stays intact.
func (f *RouteFaults) Restore(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.silenced, i)
	f.cond.Broadcast()
}

// Held reports the bytes held back on server i's routes since Silence(i): a test's
// evidence that the silence reached traffic that was actually flowing.
func (f *RouteFaults) Held(i int) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held[i]
}

// LiveRouteConnections reports how many connections are open through the proxies.
func (f *RouteFaults) LiveRouteConnections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live
}

func (f *RouteFaults) close() {
	f.mu.Lock()
	f.closed = true
	f.cond.Broadcast()
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	listeners := f.listeners
	f.mu.Unlock()
	for _, l := range listeners {
		l.Close()
	}
	for _, c := range conns {
		c.Close()
	}
}

// StartJetStreamClusterWithRouteFaults starts an in-process JetStream cluster of size
// servers whose every route runs through a proxy the returned RouteFaults can silence.
// Like StartJetStreamCluster it returns once every server is in the JetStream meta group,
// and shuts everything down when tb ends.
//
// Server i reaches server j only through the proxy for the ordered pair (i, j), so
// silencing server i gates every proxy with i at either end.
//
// 🔴 A ROUTE THAT BYPASSES THE PROXIES WOULD MAKE Silence SILENCE NOTHING, and two things
// keep that from happening. The servers gossip each other's route address and connect to
// whatever they are told about, so each server advertises a closed port: a gossiped
// address is refused, and the only routes that can form are the configured ones through
// the proxies. And readiness is not taken on trust: every route connection every server
// reports must be matched by a proxied connection, or the construction is retried.
func StartJetStreamClusterWithRouteFaults(tb testing.TB, size int) ([]*natsserver.Server, *RouteFaults) {
	tb.Helper()
	const attempts = 3
	for attempt := 1; ; attempt++ {
		servers, faults, err := tryStartJetStreamClusterWithRouteFaults(tb, size)
		if err == nil {
			tb.Cleanup(func() {
				for _, s := range servers {
					s.Shutdown()
				}
				faults.close()
			})
			return servers, faults
		}
		if attempt == attempts {
			tb.Fatalf("could not start a %d-node route-fault JetStream cluster in %d attempts: %v", size, attempts, err)
		}
		tb.Logf("route-fault cluster attempt %d/%d failed (%v); retrying on fresh ports", attempt, attempts, err)
	}
}

// reservePorts reserves n distinct ports and releases them, for the reason freePorts in
// core/messaging gives: holding all of them open until each is chosen makes a duplicate
// impossible.
func reservePorts(n int) ([]int, error) {
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("reserving a port: %w", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

func tryStartJetStreamClusterWithRouteFaults(tb testing.TB, size int) ([]*natsserver.Server, *RouteFaults, error) {
	// size cluster ports, plus size closed ports each server advertises instead of its own.
	ports, err := reservePorts(2 * size)
	if err != nil {
		return nil, nil, err
	}
	clusterPorts, advertised := ports[:size], ports[size:]

	faults := &RouteFaults{
		silenced: map[int]bool{},
		held:     map[int]int64{},
		conns:    map[net.Conn]struct{}{},
	}
	faults.cond = sync.NewCond(&faults.mu)

	// proxyTo[i][j] is the address server i dials to reach server j.
	proxyTo := make([][]string, size)
	for i := 0; i < size; i++ {
		proxyTo[i] = make([]string, size)
		for j := 0; j < size; j++ {
			if i == j {
				continue
			}
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				faults.close()
				return nil, nil, fmt.Errorf("proxy listener: %w", err)
			}
			faults.listeners = append(faults.listeners, l)
			proxyTo[i][j] = l.Addr().String()
			go faults.accept(l, i, j, fmt.Sprintf("127.0.0.1:%d", clusterPorts[j]))
		}
	}

	servers := make([]*natsserver.Server, 0, size)
	shutdown := func() {
		for _, s := range servers {
			s.Shutdown()
		}
		faults.close()
	}
	for i := 0; i < size; i++ {
		routes := ""
		for j := 0; j < size; j++ {
			if j != i {
				routes += "nats-route://" + proxyTo[i][j] + ","
			}
		}
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host:       "127.0.0.1",
			Port:       -1,
			ServerName: fmt.Sprintf("n%d", i+1),
			JetStream:  true,
			StoreDir:   JetStreamStoreDir(tb),
			Cluster: natsserver.ClusterOpts{
				Name:      "dctest",
				Host:      "127.0.0.1",
				Port:      clusterPorts[i],
				Advertise: fmt.Sprintf("127.0.0.1:%d", advertised[i]),
			},
			Routes: natsserver.RoutesFromStr(routes[:len(routes)-1]),
		})
		if err != nil {
			shutdown()
			return nil, nil, fmt.Errorf("new clustered nats server %d: %w", i, err)
		}
		go srv.Start()
		servers = append(servers, srv)
	}
	for i, srv := range servers {
		if !srv.ReadyForConnections(15 * time.Second) {
			shutdown()
			return nil, nil, fmt.Errorf("clustered nats server %d not ready", i)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		joined := false
		for _, srv := range servers {
			if len(srv.JetStreamClusterPeers()) == size {
				joined = true
				break
			}
		}
		if joined {
			break
		}
		if time.Now().After(deadline) {
			shutdown()
			return nil, nil, fmt.Errorf("JetStream meta group never reached %d peers", size)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := faults.awaitAllRoutesProxied(servers, 15*time.Second); err != nil {
		shutdown()
		return nil, nil, err
	}
	return servers, faults, nil
}

// awaitAllRoutesProxied waits until every server is routed to every other one and every
// route connection the servers report is carried by a proxy.
//
// Each proxied connection is one route connection at each of its two ends, so the sum of
// the servers' route counts must be exactly twice the proxied connections. A route that
// went around the proxies is counted by the servers and not by the proxies, and breaks
// the equality for as long as it lives. The equality has to hold on consecutive checks,
// because a route handshake or a duplicate being closed breaks it for a moment.
func (f *RouteFaults) awaitAllRoutesProxied(servers []*natsserver.Server, within time.Duration) error {
	deadline := time.Now().Add(within)
	stable := 0
	var routes, proxied int
	for {
		routes = 0
		meshed := true
		for _, srv := range servers {
			routes += srv.NumRoutes()
			if srv.NumRemotes() != len(servers)-1 {
				meshed = false
			}
		}
		proxied = f.LiveRouteConnections()
		if meshed && routes > 0 && routes == 2*proxied {
			stable++
			if stable == 5 {
				return nil
			}
		} else {
			stable = 0
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("routes are not all carried by the proxies: the servers report %d route "+
				"connections, the proxies carry %d (each should be counted twice)", routes, proxied)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *RouteFaults) accept(l net.Listener, from, to int, target string) {
	for {
		in, err := l.Accept()
		if err != nil {
			return
		}
		out, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err != nil {
			in.Close()
			continue
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			in.Close()
			out.Close()
			return
		}
		f.conns[in] = struct{}{}
		f.conns[out] = struct{}{}
		f.live++
		f.mu.Unlock()

		var once sync.Once
		done := func() {
			once.Do(func() {
				in.Close()
				out.Close()
				f.mu.Lock()
				delete(f.conns, in)
				delete(f.conns, out)
				f.live--
				f.mu.Unlock()
			})
		}
		go f.pipe(out, in, from, to, done)
		go f.pipe(in, out, from, to, done)
	}
}

// pipe copies src to dst, holding each chunk back while either end is silenced. It never
// closes a connection because of a silence; only a read or write error does.
func (f *RouteFaults) pipe(dst, src net.Conn, a, b int, done func()) {
	defer done()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			f.mu.Lock()
			counted := false
			for !f.closed && (f.silenced[a] || f.silenced[b]) {
				if !counted {
					for _, s := range []int{a, b} {
						if f.silenced[s] {
							f.held[s] += int64(n)
						}
					}
					counted = true
				}
				f.cond.Wait()
			}
			closed := f.closed
			f.mu.Unlock()
			if closed {
				return
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// RouteFaults is the proxy mesh every route of a fixture cluster runs through, and the
// controls a test uses to fault it. StartJetStreamClusterWithRouteFaults hands it to the
// test; StartJetStreamCluster keeps it to itself.
//
// 🔑 IT EXISTS BECAUSE A CLEAN SHUTDOWN DOES NOT REPRODUCE A LOST NODE. Shutdown closes
// the node's sockets, so every other server drops its routes, and the interest behind
// them, at once. A node that drops off the network does not close anything: the other
// servers keep its routes until their pings to it go unanswered (a route pings at most
// every 30 s and gives up after two unanswered pings), and for that minute or so they go
// on forwarding requests to it that will never be answered. That is the state Silence
// produces, and the one a lost node on a real network is in.
//
// 🔑 IT IS ALSO WHAT KEEPS ONE CLUSTER OUT OF ANOTHER. Every address a server is given to
// dial is a listener of this mesh, held from before the servers are created until the
// cluster is shut down, so no other process can bind it. A proxy finds its server's route
// listener when a connection arrives, and dials nothing once that server has shut down
// (its route listener is gone): the other servers go on redialling a server that has
// shut down, and those dials end here rather than at a port the operating system may by
// then have handed to another process's server.
type RouteFaults struct {
	mu       sync.Mutex
	cond     *sync.Cond
	silenced map[int]bool
	held     map[int]int64
	closed   bool
	// silencedN is how many servers are silenced, so a proxy copies without taking mu
	// while none is.
	silencedN atomic.Int32

	// servers are the servers the proxies forward to, set before any of them starts.
	servers []*natsserver.Server
	// proxies[from][to] is the listener server from dials to reach server to; nil where
	// from == to, since no server is given a route to itself.
	proxies [][]net.Listener
	// listened[i] is set once server i's route listener has been seen.
	listened map[int]bool
	// refusers[i] is the address server i advertises: held, and refusing connections.
	refusers []*refusingAddr
	// conns maps each open proxied connection to the ordered pair of servers (from, to)
	// whose route it carries.
	conns map[net.Conn][2]int
	// live counts the proxied connection pairs that are open: one per route connection
	// the servers can have made through a proxy.
	live int
}

// newRouteMesh makes the mesh for size servers: a proxy listener for every ordered pair
// of two different servers, and an address for each server to advertise. Every one is
// held until close.
func newRouteMesh(size int) (*RouteFaults, error) {
	f := &RouteFaults{
		silenced: map[int]bool{},
		held:     map[int]int64{},
		conns:    map[net.Conn][2]int{},
		listened: map[int]bool{},
		proxies:  make([][]net.Listener, size),
	}
	f.cond = sync.NewCond(&f.mu)
	for i := 0; i < size; i++ {
		f.proxies[i] = make([]net.Listener, size)
		for j := 0; j < size; j++ {
			if i == j {
				continue
			}
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				f.close()
				return nil, fmt.Errorf("proxy listener: %w", err)
			}
			f.proxies[i][j] = l
			go f.accept(l, i, j)
		}
		r, err := holdRefusingAddr()
		if err != nil {
			f.close()
			return nil, fmt.Errorf("an address to advertise: %w", err)
		}
		f.refusers = append(f.refusers, r)
	}
	return f, nil
}

// proxyAddr is the held address server from dials to reach server to.
func (f *RouteFaults) proxyAddr(from, to int) string {
	return f.proxies[from][to].Addr().String()
}

// refuserAddr is the held address server i advertises in place of its route listener.
//
// The servers gossip each other's advertised address and connect to whatever they are
// told about, so a server that advertised its own route listener could be reached around
// the proxies. A held address that REFUSES (a socket bound and never listening) keeps
// that from happening, and keeps the address from anyone else: a gossiped connect is
// refused and not tried again. An address that accepted and closed at once would not do:
// the server takes a connection it made to a gossiped address and lost before the
// handshake as a route to reconnect, at once and forever.
func (f *RouteFaults) refuserAddr(i int) string {
	return f.refusers[i].addr
}

// setServers tells the proxies which servers they forward to. Call it before any of them
// starts.
func (f *RouteFaults) setServers(servers []*natsserver.Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers = servers
}

// target returns the address of server to's route listener, or "" while it has none:
// before it has bound it, and after it has shut down. The second result reports whether
// the server has had one: a proxy waits for a server that is still starting, and not for
// one that has shut down.
func (f *RouteFaults) target(to int) (string, bool) {
	f.mu.Lock()
	servers := f.servers
	f.mu.Unlock()
	if to >= len(servers) {
		return "", false
	}
	addr := servers[to].ClusterAddr()
	f.mu.Lock()
	defer f.mu.Unlock()
	if addr == nil {
		return "", f.listened[to]
	}
	f.listened[to] = true
	return addr.String(), true
}

// Silence stops every byte to and from server i on its routes WITHOUT closing any
// connection, the way a node dropped off the network behaves: the other servers keep the
// routes, and the interest behind them, until their pings run out.
func (f *RouteFaults) Silence(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.silenced[i] {
		f.silenced[i] = true
		f.silencedN.Add(1)
	}
	f.held[i] = 0
}

// Restore lets the routes of server i carry traffic again. What was held back is
// delivered, in order, so each route's byte stream stays intact.
func (f *RouteFaults) Restore(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.silenced[i] {
		delete(f.silenced, i)
		f.silencedN.Add(-1)
	}
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

// sever closes every proxied route connection with server i at either end and reports
// how many connection pairs it closed. Unlike Silence it DOES close them, so the servers
// drop those routes at once and dial them again through the proxies, which keep
// listening. It exists to reproduce what registering a route again does to a running
// cluster: see awaitJetStreamClusterFormed.
func (f *RouteFaults) sever(i int) int {
	f.mu.Lock()
	var hit []net.Conn
	for c, pair := range f.conns {
		if pair[0] == i || pair[1] == i {
			hit = append(hit, c)
		}
	}
	f.mu.Unlock()
	for _, c := range hit {
		c.Close()
	}
	return len(hit) / 2
}

// close closes every proxy listener, every advertised address and every proxied
// connection. Call it after the servers are shut down.
func (f *RouteFaults) close() {
	f.mu.Lock()
	f.closed = true
	f.cond.Broadcast()
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	proxies, refusers := f.proxies, f.refusers
	f.mu.Unlock()
	for _, row := range proxies {
		for _, l := range row {
			if l != nil {
				l.Close()
			}
		}
	}
	for _, r := range refusers {
		r.close()
	}
	for _, c := range conns {
		c.Close()
	}
}

// StartJetStreamClusterWithRouteFaults starts an in-process JetStream cluster of size
// servers whose every route runs through a proxy the returned RouteFaults can silence.
// It is the same construction as StartJetStreamCluster (see there for what it
// guarantees), which returns once the cluster is formed and shuts everything down when
// tb ends; this one also hands the test the proxies.
//
// Server i reaches server j only through the proxy for the ordered pair (i, j), so
// silencing server i gates every proxy with i at either end.
//
// 🔴 A ROUTE THAT BYPASSES THE PROXIES WOULD MAKE Silence SILENCE NOTHING, and two things
// keep that from happening. Each server advertises a held address that refuses
// connections (refuserAddr), so a gossiped address is refused and the only routes that
// can form are the configured ones through the proxies. And readiness is not taken on
// trust: every route connection every server reports must be matched by a proxied
// connection (awaitAllRoutesProxied), or the fixture fails the test.
func StartJetStreamClusterWithRouteFaults(tb testing.TB, size int) ([]*natsserver.Server, *RouteFaults) {
	tb.Helper()
	c := startedCluster(tb, size)
	return c.servers, c.faults
}

// errRoutesBypassProxies marks a cluster whose servers hold more route connections than
// the proxies carry: some route went around them, so Silence would not silence it.
var errRoutesBypassProxies = errors.New("a route bypasses the proxies")

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
			err := fmt.Errorf("routes are not all carried by the proxies: the servers report %d route "+
				"connections, the proxies carry %d (each should be counted twice)", routes, proxied)
			if meshed && routes > 2*proxied {
				return fmt.Errorf("%w: %w", errRoutesBypassProxies, err)
			}
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// accept hands each connection proxies[from][to] accepts to forward.
func (f *RouteFaults) accept(l net.Listener, from, to int) {
	for {
		in, err := l.Accept()
		if err != nil {
			return
		}
		go f.forward(in, from, to)
	}
}

// forward connects in to server to's route listener, looked up as the connection
// arrives.
//
// A server that has not bound its route listener YET is waited for, and the connection
// is held meanwhile: closing it would send the dialling server into its reconnect delay,
// and a meta group whose first election finds its peers unreachable waits a whole
// election timeout (seconds) before it tries again. A server that HAS shut down is not
// waited for: the connection is closed at once, and nothing is dialled.
func (f *RouteFaults) forward(in net.Conn, from, to int) {
	target, listened := f.target(to)
	for target == "" && !listened {
		f.mu.Lock()
		closed := f.closed
		f.mu.Unlock()
		if closed {
			in.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
		target, listened = f.target(to)
	}
	if target == "" {
		in.Close()
		return
	}
	out, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		in.Close()
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		in.Close()
		out.Close()
		return
	}
	f.conns[in] = [2]int{from, to}
	f.conns[out] = [2]int{from, to}
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

// pipe copies src to dst, holding each chunk back while either end is silenced. It never
// closes a connection because of a silence; only a read or write error does. While no
// server is silenced it copies without taking the lock.
func (f *RouteFaults) pipe(dst, src net.Conn, a, b int, done func()) {
	defer done()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if f.silencedN.Load() != 0 && !f.holdWhileSilenced(a, b, n) {
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

// holdWhileSilenced blocks while server a or b is silenced, counting the n bytes held
// back against each that is, and reports false when the mesh was closed meanwhile.
func (f *RouteFaults) holdWhileSilenced(a, b, n int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	return !f.closed
}

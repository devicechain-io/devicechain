// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
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

	// listenerPair maps each proxy listener's address to the ordered pair (from, to) it
	// carries: the address server from dials to reach server to. Fixed by newRouteMesh.
	listenerPair map[string][2]int
	// dialedPair maps every connection a proxy has dialled to a server's route listener,
	// by its local address and the server it reached, to the pair (from, to) it carries.
	// The server it reached is part of the key because the operating system can give two
	// connections to DIFFERENT destinations the same local address at once. An entry is
	// added before the connection's first byte is copied, and is never removed (see
	// attribute).
	dialedPair map[dialKey][2]int
}

// dialKey names a connection a proxy dialled as the server it reached sees it: the
// connection's local address, which that server reports as the route's remote address,
// and the index of that server.
type dialKey struct {
	local string
	to    int
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

		listenerPair: map[string][2]int{},
		dialedPair:   map[dialKey][2]int{},
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
			f.listenerPair[l.Addr().String()] = [2]int{i, j}
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
// trust: every route connection every server reports must run over a connection a proxy
// carries for that pair of servers (awaitAllRoutesProxied), or the fixture fails the test.
func StartJetStreamClusterWithRouteFaults(tb testing.TB, size int) ([]*natsserver.Server, *RouteFaults) {
	tb.Helper()
	c := startedCluster(tb, size)
	return c.servers, c.faults
}

// errRoutesBypassProxies marks a route a server holds over a connection no proxy of the
// mesh carries for that pair of servers: Silence would not silence it.
var errRoutesBypassProxies = errors.New("a route bypasses the proxies")

// routeConnsPerPeer is how many route connections a server holds to each other server
// once its routes are complete: a pool of DEFAULT_ROUTE_POOL_SIZE (no fixture server sets
// a pool size of its own), and one more dedicated to the system account, as nats-server's
// own route tests count them.
const routeConnsPerPeer = natsserver.DEFAULT_ROUTE_POOL_SIZE + 1

// routeBypass is one route a server holds over a connection that no proxy of the mesh
// carries for that pair of servers.
type routeBypass struct {
	server string // the server that holds the route
	peer   string // the server at its other end, as the route names it
	remote string // the route connection's remote address, as the server reports it
	why    string
}

func (b routeBypass) String() string {
	return fmt.Sprintf("%s holds a route to %s over a connection from %q, %s", b.server, b.peer, b.remote, b.why)
}

// routeBypassError is every route around the proxies that one look at the servers found.
type routeBypassError struct {
	bypasses []routeBypass
	// after is how long after awaitAllRoutesProxied began the bypasses were seen.
	after time.Duration
}

func (e *routeBypassError) Error() string {
	each := make([]string, len(e.bypasses))
	for k, b := range e.bypasses {
		each[k] = b.String()
	}
	return fmt.Sprintf("%v, seen %s after the route wait began: %s", errRoutesBypassProxies,
		e.after.Round(time.Millisecond), strings.Join(each, "; "))
}

func (e *routeBypassError) Unwrap() error { return errRoutesBypassProxies }

// attribute reports why the route that server i holds to the server named peer, whose
// connection has the remote address remote, is not carried by this mesh for that pair of
// servers; "" when it is. names[k] is server k's name.
//
// A route connection has two ends, and each end reports the OTHER end's address:
//   - the end that dialled reports the address it dialled, which for a proxied route is a
//     proxy listener: listenerPair[remote] must be (i, k) with names[k] == peer;
//   - the end that accepted reports the dialler's local address, which for a proxied
//     route is a connection a proxy dialled to server i: dialedPair[(remote, i)] must be
//     (k, i) with names[k] == peer.
//
// A direct route is seen at its dialling end, whatever its other end shows: the address it
// dialled is a server's route listener, which is neither a proxy listener nor a
// connection a proxy dialled.
//
// dialedPair keeps the connections a proxy has closed: a server still lists a route for
// a moment after its connection was closed, and that route was carried by the mesh.
// Forgetting it would report a bypass that never existed.
//
// An empty remote, which is not an address, fails closed here; unproxiedRoutes does not
// ask about one (see there).
func (f *RouteFaults) attribute(i int, peer, remote string, names []string) string {
	nameOf := func(k int) string {
		if k < 0 || k >= len(names) {
			return fmt.Sprintf("server %d", k)
		}
		return names[k]
	}
	if remote == "" {
		return "which is not a TCP address the mesh can recognize"
	}
	f.mu.Lock()
	byListener, isListener := f.listenerPair[remote]
	byDial, isDial := f.dialedPair[dialKey{local: remote, to: i}]
	f.mu.Unlock()
	switch {
	case isListener:
		if byListener[0] != i || nameOf(byListener[1]) != peer {
			return fmt.Sprintf("which is the proxy for %s to %s, not for this pair",
				nameOf(byListener[0]), nameOf(byListener[1]))
		}
		return ""
	case isDial:
		if nameOf(byDial[0]) != peer {
			return fmt.Sprintf("which a proxy dialled to carry %s's route to %s, not this pair",
				nameOf(byDial[0]), nameOf(byDial[1]))
		}
		return ""
	}
	return "which is neither a proxy of this cluster nor a connection one dialled"
}

// unproxiedRoutes reads every route each of servers holds, with Routez (which lists the
// same routes NumRoutes counts: both walk forEachRoute), and returns each route this mesh
// does not carry, and each route it could not attribute because it has no address.
//
// A route with no address is one whose connection is closing: the server lets go of the
// connection before it takes the route off its list, and routes do close while a cluster
// starts (a duplicate, a redial). It is NOT a bypass, since a route around the proxies is
// a live connection whose dialling end reports a real address; it is a route not yet
// settled either way.
func (f *RouteFaults) unproxiedRoutes(servers []*natsserver.Server) (bypasses []routeBypass, unaddressed []string, err error) {
	names := make([]string, len(servers))
	for k, srv := range servers {
		names[k] = srv.Name()
	}
	for i, srv := range servers {
		rz, err := srv.Routez(&natsserver.RoutezOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("routez of %s: %w", srv.Name(), err)
		}
		for _, ri := range rz.Routes {
			if ri.IP == "" {
				unaddressed = append(unaddressed, fmt.Sprintf("%s's route to %s", srv.Name(), ri.RemoteName))
				continue
			}
			remote := net.JoinHostPort(ri.IP, strconv.Itoa(ri.Port))
			if why := f.attribute(i, ri.RemoteName, remote, names); why != "" {
				bypasses = append(bypasses, routeBypass{server: srv.Name(), peer: ri.RemoteName, remote: remote, why: why})
			}
		}
	}
	return bypasses, unaddressed, nil
}

// awaitAllRoutesProxied waits until every server holds its complete set of route
// connections to every other one, each carried by a proxy for that pair of servers.
//
// A bypass is decided by attribution, one route connection at a time (unproxiedRoutes):
// the address each server reports for a route must be a proxy listener or a connection a
// proxy dialled, for that pair. A route that is neither ends the wait AT ONCE, as a
// routeBypassError naming every such route that look found: no route the mesh carries
// can be mistaken for one, so a single sighting is a finding, not a transient.
//
// The count says only when the routes have settled. Each proxied connection is one route
// connection at each of its two ends, so the sum of the servers' route counts is twice
// the proxied connections once no handshake is under way and no duplicate is being
// closed, and that has to hold on consecutive checks. The count alone could not decide a
// bypass: a route around the proxies and a proxied connection that is not (yet) a route
// cancel out.
//
// The complete set is waited for because route connections keep arriving after the first
// ones are up: a server dials a route it learns of from another server's INFO at the
// address that INFO carries, which is the advertised one (processImplicitRoute in
// nats-server's route.go). Checked before every pool is full, the routes can all be
// carried while a connection made around the proxies a moment later goes unseen; under
// load that window outlasted the five checks.
func (f *RouteFaults) awaitAllRoutesProxied(servers []*natsserver.Server, within time.Duration) error {
	began := time.Now()
	deadline := began.Add(within)
	want := (len(servers) - 1) * routeConnsPerPeer
	stable := 0
	var routes, proxied int
	var each, unaddressed []string
	var last error
	for {
		bypasses, unaddr, err := f.unproxiedRoutes(servers)
		if len(bypasses) > 0 {
			return &routeBypassError{bypasses: bypasses, after: time.Since(began)}
		}
		if err != nil {
			last = err
		}
		unaddressed = unaddr
		routes = 0
		meshed, complete := true, true
		each = each[:0]
		for _, srv := range servers {
			n := srv.NumRoutes()
			routes += n
			each = append(each, fmt.Sprintf("%s %d", srv.Name(), n))
			if srv.NumRemotes() != len(servers)-1 {
				meshed = false
			}
			if n != want {
				complete = false
			}
		}
		proxied = f.LiveRouteConnections()
		if err == nil && len(unaddr) == 0 && meshed && complete && routes == 2*proxied {
			stable++
			if stable == 5 {
				return nil
			}
		} else {
			stable = 0
		}
		if time.Now().After(deadline) {
			detail := ""
			if len(unaddressed) > 0 {
				detail += fmt.Sprintf("; routes with no address to attribute: %s", strings.Join(unaddressed, ", "))
			}
			if last != nil {
				detail += fmt.Sprintf("; last error: %v", last)
			}
			return fmt.Errorf("routes are not all complete and carried by the proxies: the servers report %d route "+
				"connections (%s; each should hold %d), the proxies carry %d (each should be counted twice)%s",
				routes, strings.Join(each, ", "), want, proxied, detail)
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
	// Recorded BEFORE either pipe starts: server to registers the route only after it has
	// read the dialling server's handshake, and those bytes reach it only through the
	// pipe from in to out, so no route can be listed before its connection is recorded.
	if !f.recordDialed(in, out, from, to) {
		in.Close()
		out.Close()
		return
	}

	var once sync.Once
	done := func() {
		once.Do(func() {
			in.Close()
			out.Close()
			f.forgetConn(in, out)
		})
	}
	go f.pipe(out, in, from, to, done)
	go f.pipe(in, out, from, to, done)
}

// recordDialed records the connection pair a proxy for (from, to) has made: in, accepted
// from server from, and out, dialled to server to's route listener. It reports false,
// recording nothing, when the mesh has been closed.
func (f *RouteFaults) recordDialed(in, out net.Conn, from, to int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.conns[in] = [2]int{from, to}
	f.conns[out] = [2]int{from, to}
	f.live++
	f.dialedPair[dialKey{local: out.LocalAddr().String(), to: to}] = [2]int{from, to}
	return true
}

// forgetConn drops a connection pair recordDialed recorded, once its pipes have ended.
// It keeps the pair's dialedPair entry: see attribute.
func (f *RouteFaults) forgetConn(in, out net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conns, in)
	delete(f.conns, out)
	f.live--
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

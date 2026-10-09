// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// StartJetStreamCluster starts an in-process JetStream cluster of size servers and returns
// them once the cluster is formed, shutting them down when tb ends. A client connects to
// any of them with srv.ClientURL().
//
// Both cluster fixtures, this one and StartJetStreamClusterWithRouteFaults, are the same
// construction (startCluster); this one only keeps the route proxies to itself. What that
// construction guarantees:
//
//   - ITS ROUTES REACH ONLY ITS OWN SERVERS. go test runs packages in parallel processes,
//     and several of them build clusters at once. Every route a server is given points at
//     a proxy listener the fixture holds for the cluster's whole life, and each server
//     advertises an address the fixture also holds, which refuses connections; no address
//     a server dials is ever released while the cluster lives, so nothing else can bind it.
//     The servers' own listeners take ports the operating system picks as they bind. And
//     every construction has a cluster name no other one has (clusterName), so a server
//     of another cluster that reaches a route listener anyway is refused by the server
//     itself. When this was not so, one test binary's servers were admitted into another
//     binary's cluster, which then lost its meta leader after it had been reported formed.
//   - A server slow to start is WAITED FOR, not started again. A server enables JetStream
//     before it opens any listener, and on a loaded machine that can take many seconds.
//     awaitListening waits up to clusterListenBudget for every server's client and route
//     listeners, and a server that has still not bound them fails the start, named along
//     with the listener it lacks. A listener that cannot open fails the start at once, from
//     the server's own words (errListenerFailed). Nothing is retried: with no port chosen
//     in advance there is no race a second construction could win.
//   - Readiness is, first, every server holding its complete set of route connections,
//     each one attributed to the proxy that carries it (awaitAllRoutesProxied), which
//     reports a route around the proxies the moment it sees one; then
//     awaitJetStreamClusterFormed: a meta leader holding statistics for every server, and
//     a stream just placed replicated on every server. Neither a meta leader nor a meta
//     group holding every server is enough, and that function says why. A cluster that
//     binds but does not form is not rebuilt: it fails, naming what it lacks. Routes are
//     not attributed again once the cluster has formed, and that rests on an argument,
//     not a test: the only route a server makes that it was not given is one it hears of
//     from gossip, dialled at the address the gossip carries (processImplicitRoute in
//     nats-server's route.go), and every server advertises an address that refuses. That
//     is checked at startup, not assumed: checkAdvertisedAddresses fails a start in which
//     any server advertises anything but its held refusing address (errRealAdvertise).
//
// The isolation covers ROUTES. A client that connected to a server that is later shut
// down keeps that server's client URL in its reconnect list, and the freed client port can
// be taken by another process's server; no cluster name is checked on a client connection.
//
// Each server keeps the warnings and errors it logs (serverLog), and a test that fails
// prints them. The server reports some failures to its client only as a fixed sentence
// ("error creating store for stream") and writes the cause only to its own log.
func StartJetStreamCluster(tb testing.TB, size int) []*natsserver.Server {
	tb.Helper()
	return startedCluster(tb, size).servers
}

// cluster is one construction: its servers, their logs, the proxy mesh every route runs
// through, the options each server was created with, and the cluster's name.
type cluster struct {
	servers []*natsserver.Server
	logs    []*serverLog
	faults  *RouteFaults
	opts    []*natsserver.Options
	name    string
}

// startedCluster is what both public fixtures call: one construction with the default
// hooks, failing tb when it cannot be made, and shut down when tb ends.
func startedCluster(tb testing.TB, size int) *cluster {
	tb.Helper()
	c, err := startCluster(tb, size, defaultClusterHooks(), clusterStartBudget)
	// A route around the proxies is the fixture failing at its one job, and it is
	// reported as what it is.
	if errors.Is(err, errRoutesBypassProxies) {
		tb.Fatalf("the cluster cannot fault its routes: %v", err)
	}
	if err != nil {
		tb.Fatalf("could not start a %d-node JetStream cluster: %v", size, err)
	}
	tb.Cleanup(func() {
		shutdownServers(c.servers)
		c.faults.close()
	})
	// Registered after the shutdown, so it runs before it: cleanups run last in, first out.
	reportServerLogsOnFailure(tb, c.logs)
	return c
}

// clusterListenBudget bounds how long a construction waits for every server's client
// and route listeners to be bound.
const clusterListenBudget = 60 * time.Second

// clusterStartBudget bounds a whole construction: listening, every route being carried
// by a proxy, and forming. Each wait is given the smaller of its own budget and what is
// left of this one.
const clusterStartBudget = 2 * time.Minute

// clusterRoutesBudget bounds awaitAllRoutesProxied inside a construction. It covers the
// route mesh forming, from the moment every server listens, as well as the check that
// every route is carried by a proxy.
const clusterRoutesBudget = 30 * time.Second

// errListenerFailed marks a server that reported one of its listeners could not be
// opened. It is terminal: the operating system picks the port as the server binds, so a
// second construction would not be given anything a first one lacked.
var errListenerFailed = errors.New("a server could not open its route listener")

// errRealAdvertise marks a cluster in which a server advertises an address other than the
// held, refusing one the fixture gave it. Such a server gossips an address a later
// reconnect can dial, forming a route around the proxies that no silence can cut.
var errRealAdvertise = errors.New("a server advertises a route address other than its held refusing one")

// clusterHooks are the parts of a construction that this package's tests replace.
type clusterHooks struct {
	// configure, when set, has the last word on server i's options before it is created.
	configure func(i int, o *natsserver.Options)
	// start starts a server; the default runs Start on a goroutine of its own.
	start        func(*natsserver.Server)
	listenWithin time.Duration
	// routesWithin bounds awaitAllRoutesProxied, as listenWithin bounds awaitListening.
	routesWithin time.Duration
	// routesTo, when set, names the servers server i is given a route to; nil gives it one
	// to every other server.
	routesTo func(i, size int) []int
	// allowRealAdvertise skips checkAdvertisedAddresses. Only a test that builds a route
	// around the proxies on purpose, to exercise their detection, sets it.
	allowRealAdvertise bool
}

func defaultClusterHooks() clusterHooks {
	return clusterHooks{
		start:        func(s *natsserver.Server) { go s.Start() },
		listenWithin: clusterListenBudget,
		routesWithin: clusterRoutesBudget,
	}
}

// routePeers returns the servers server i of size is given a route to: every other one,
// unless h.routesTo names them. A route to itself, to a server that does not exist, or to
// the same server twice is refused.
func (h clusterHooks) routePeers(i, size int) ([]int, error) {
	if h.routesTo == nil {
		peers := make([]int, 0, size-1)
		for j := 0; j < size; j++ {
			if j != i {
				peers = append(peers, j)
			}
		}
		return peers, nil
	}
	peers := h.routesTo(i, size)
	seen := map[int]bool{}
	for _, j := range peers {
		switch {
		case j == i:
			return nil, fmt.Errorf("server %d is given a route to itself", i)
		case j < 0 || j >= size:
			return nil, fmt.Errorf("server %d is given a route to server %d of a cluster of %d", i, j, size)
		case seen[j]:
			return nil, fmt.Errorf("server %d is given a route to server %d twice", i, j)
		}
		seen[j] = true
	}
	return peers, nil
}

// clusters counts the constructions this process has made, so each gets its own name.
var clusters atomic.Uint64

// clusterName returns a cluster name no other construction has: the process id tells
// apart the test binaries go test runs side by side, and the counter the constructions
// one binary makes. A server refuses a route from a server whose static cluster name
// differs from its own, which is what keeps one construction out of another.
func clusterName() string {
	return fmt.Sprintf("dctest-%d-%d", os.Getpid(), clusters.Add(1))
}

// startCluster makes ONE construction and returns it once it is listening, every route is
// carried by a proxy, and it is formed. On an error it has shut everything down, and the
// error carries each server's last lines. Nothing is retried.
func startCluster(tb testing.TB, size int, h clusterHooks, within time.Duration) (*cluster, error) {
	if size < 2 {
		return nil, fmt.Errorf("a JetStream cluster of %d server(s) cannot be built: a clustered server needs a route "+
			"to at least one other", size)
	}
	deadline := time.Now().Add(within)
	peers := make([][]int, size)
	for i := range peers {
		p, err := h.routePeers(i, size)
		if err != nil {
			return nil, err
		}
		peers[i] = p
	}
	name := clusterName()
	faults, err := newRouteMesh(size)
	if err != nil {
		return nil, err
	}

	opts := make([]*natsserver.Options, size)
	for i := range opts {
		// A route to every OTHER server, through its proxy. No route to itself: a server
		// dials one and recognizes itself only once the handshake comes back, and the
		// extra route raises the meta group's bootstrap size (the server takes it from
		// its configured route count) from two to three. A three-server bootstrap
		// elects its first leader only on a vote from every server, and through the
		// proxies one route is often still being re-made when the first votes go out,
		// so most starts waited out a whole election timeout (several seconds). With
		// two, the first leader is elected by any two servers, and the third joins it.
		routes := make([]string, 0, len(peers[i]))
		for _, j := range peers[i] {
			routes = append(routes, "nats-route://"+faults.proxyAddr(i, j))
		}
		opts[i] = &natsserver.Options{
			Host:       "127.0.0.1",
			Port:       -1,
			ServerName: fmt.Sprintf("n%d", i+1),
			JetStream:  true,
			StoreDir:   JetStreamStoreDir(tb),
			Cluster: natsserver.ClusterOpts{
				Name:      name,
				Host:      "127.0.0.1",
				Port:      -1,
				Advertise: faults.refuserAddr(i),
			},
			Routes: natsserver.RoutesFromStr(strings.Join(routes, ",")),
		}
		if h.configure != nil {
			h.configure(i, opts[i])
		}
	}

	servers, logs, err := startListening(opts, faults, h, deadline)
	fail := func(err error) (*cluster, error) {
		shutdownServers(servers)
		faults.close()
		return nil, withServerLogs(err, logs)
	}
	if err != nil {
		return fail(err)
	}
	if !h.allowRealAdvertise {
		if err := checkAdvertisedAddresses(opts, faults); err != nil {
			return fail(err)
		}
	}
	if err := faults.awaitAllRoutesProxied(servers, min(h.routesWithin, time.Until(deadline))); err != nil {
		return fail(err)
	}
	if err := awaitJetStreamClusterFormed(servers, min(clusterFormBudget, time.Until(deadline))); err != nil {
		return fail(err)
	}
	return &cluster{servers: servers, logs: logs, faults: faults, opts: opts, name: name}, nil
}

// checkAdvertisedAddresses fails unless every server advertises exactly the refusing
// address the fixture holds for it (faults.refuserAddr). The address is read from the
// options each server was created with: the server keeps that very pointer (NewServer
// stores it as s.opts) and its monitoring output (Varz) does not report the advertised
// route address, so this is the one place the configured value can be read.
func checkAdvertisedAddresses(opts []*natsserver.Options, faults *RouteFaults) error {
	for i, o := range opts {
		if got, want := o.Cluster.Advertise, faults.refuserAddr(i); got != want {
			return fmt.Errorf("server n%d: %w: advertises %q, want %q", i+1, errRealAdvertise, got, want)
		}
	}
	return nil
}

// startListening creates a server for each of opts, gives each a serverLog, hands them
// all to the proxies before any of them starts, starts each through h.start, and waits
// (awaitListening) until every one is listening, for the smaller of h.listenWithin and
// what is left before deadline.
//
// It returns the servers and logs it made even with an error, so the caller can shut the
// servers down and quote the logs along with whatever else it has to tear down.
func startListening(opts []*natsserver.Options, faults *RouteFaults, h clusterHooks, deadline time.Time) (
	[]*natsserver.Server, []*serverLog, error) {
	servers := make([]*natsserver.Server, 0, len(opts))
	logs := make([]*serverLog, 0, len(opts))
	for i, o := range opts {
		srv, err := natsserver.NewServer(o)
		if err != nil {
			return servers, logs, fmt.Errorf("new clustered nats server %d: %w", i, err)
		}
		// Before Start: a listener that cannot open is reported from inside Start, and this
		// log is the only place that report goes.
		logs = append(logs, attachLog(srv))
		servers = append(servers, srv)
	}
	faults.setServers(servers)
	for _, srv := range servers {
		h.start(srv)
	}
	return servers, logs, awaitListening(servers, logs, min(h.listenWithin, time.Until(deadline)))
}

func shutdownServers(servers []*natsserver.Server) {
	for _, s := range servers {
		s.Shutdown()
	}
}

// awaitListening waits until every server has bound its client and route listeners. A
// server that reports its route listener could not open ends the wait at once, with an
// error wrapping errListenerFailed that names the server and quotes what it logged. A
// server merely slow to start is waited for until within, and then named along with the
// listeners it has still not bound.
//
// These are the conditions ReadyForConnections waits on for a server with no gateway,
// leaf node, websocket or MQTT listener. That call is not used because it answers only
// true or false, and it waits out its whole timeout even after a listener has failed.
func awaitListening(servers []*natsserver.Server, logs []*serverLog, within time.Duration) error {
	start := time.Now()
	for {
		for i, l := range logs {
			if line := l.bindFailure(); line != "" {
				return fmt.Errorf("%s: %w: %s", servers[i].Name(), errListenerFailed, line)
			}
		}
		name, missing := "", ""
		for _, srv := range servers {
			if m := missingListeners(srv); m != "" {
				name, missing = srv.Name(), m
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Since(start) >= within {
			return fmt.Errorf("%s has not bound its %s after %s", name, missing, time.Since(start).Round(time.Millisecond))
		}
		time.Sleep(clusterPollEvery)
	}
}

// missingListeners names the listeners srv has not bound yet, or "" when it has both.
func missingListeners(srv *natsserver.Server) string {
	client, route := srv.Addr() != nil, srv.ClusterAddr() != nil
	switch {
	case client && route:
		return ""
	case client:
		return "route listener"
	case route:
		return "client listener"
	default:
		return "client and route listeners"
	}
}

// clusterFormBudget bounds awaitJetStreamClusterFormed inside the fixtures. It has to
// outlast one full statsz heartbeat (10 s once backed off), since a peer whose stats
// were wiped cannot be placed on until its next one arrives.
const clusterFormBudget = 30 * time.Second

// clusterPollEvery is how often the cluster is read while it is not yet formed.
const clusterPollEvery = 100 * time.Millisecond

// clusterProbeBudget bounds one placement probe; the budget left is used when smaller.
const clusterProbeBudget = 10 * time.Second

// formedProbeStream is the stream the probe places and deletes. Its name and its
// subject are outside every prefix a test or the platform uses.
const (
	formedProbeStream  = "DCTEST_FORMED"
	formedProbeSubject = "_dctest_formed.probe"
)

// awaitJetStreamClusterFormed blocks until servers make up a cluster that can place a
// stream replicated on every one of them, and returns an error if that has not happened
// within the budget. Both cluster fixtures call it before they return.
//
// 🔴 "EVERY SERVER IS IN THE META GROUP", CHECKED ONCE, IS NOT THE SAME THING, and the
// difference is a flake that reads as a product fault. Placement refuses a peer whose
// statistics the meta leader does not hold, and reports it as "no suitable peers for
// placement, peer offline". A server FORGETS a peer's statistics whenever it registers a
// first route connection to that peer again (a route that dropped and was redialled is
// enough), and gets them back only with that peer's next statsz heartbeat, which backs
// off from 250 ms to 10 s. So a cluster that placed one R3 stream a moment ago can
// refuse the next one for several seconds.
//
// The loss IS observable from outside: the meta leader's JetStreamClusterPeers leaves out
// every peer whose statistics it does not hold. The meta group's own replica list does
// not see it (Current is raft liveness and Offline is a different flag), which is why
// both are read.
//
// Formed therefore means, on one poll (formedNow): every server routed to every other
// one, every server naming the same meta leader, and that leader reporting every other
// peer current and online and holding statistics for every server. Then a probe stream
// is placed at R=len(servers), waited on until its replicas are current, and deleted:
// the capability the tests need, asked for directly. The two are each enough to hold
// the gate shut across a wipe, and each is tested alone.
//
// Nothing here waits for the cluster to hold still first. A window of unchanged polls
// before the probe was tried, and no test or repeated run showed it doing any work, so
// it is gone. A route event AFTER this returns is outside what any readiness check can
// promise.
func awaitJetStreamClusterFormed(servers []*natsserver.Server, within time.Duration) error {
	return awaitFormed(servers, within, formedNow)
}

// awaitFormed is awaitJetStreamClusterFormed with the per-poll check as a parameter, so a
// test can take the statistics rule out and show that the probe alone holds the gate.
func awaitFormed(servers []*natsserver.Server, within time.Duration, check func([]serverView) error) error {
	start := time.Now()
	deadline := start.Add(within)
	last := errors.New("the cluster was never read")
	for {
		if err := check(collectViews(servers)); err != nil {
			last = err
		} else if err := probeReplicatedPlacement(servers, time.Until(deadline)); err != nil {
			last = fmt.Errorf("placement probe: %w", err)
		} else {
			return nil
		}
		if time.Now().After(deadline) {
			// Leave nothing behind for the caller's streams to trip over.
			_ = deleteProbeStream(servers, 2*time.Second)
			return fmt.Errorf("JetStream cluster of %d servers not formed after %s: %w",
				len(servers), time.Since(start).Round(time.Millisecond), last)
		}
		time.Sleep(clusterPollEvery)
	}
}

// serverView is what one poll reads from one server.
type serverView struct {
	name       string
	remotes    int    // servers it has a route to
	metaLeader string // the meta leader it names; "" while it knows none
	// Filled only on the meta leader.
	replicas   []*natsserver.PeerInfo // the meta group's other peers
	statsPeers []string               // JetStreamClusterPeers: the servers it holds statistics for
}

func viewOf(srv *natsserver.Server) serverView {
	v := serverView{name: srv.Name(), remotes: srv.NumRemotes()}
	if jsz, err := srv.Jsz(&natsserver.JSzOptions{}); err == nil && jsz.Meta != nil {
		v.metaLeader = jsz.Meta.Leader
		v.replicas = jsz.Meta.Replicas
	}
	v.statsPeers = srv.JetStreamClusterPeers()
	return v
}

func collectViews(servers []*natsserver.Server) []serverView {
	views := make([]serverView, len(servers))
	for i, srv := range servers {
		views[i] = viewOf(srv)
	}
	return views
}

// formedNow reports, as an error naming the server and what it lacks, the first reason
// views do not describe a formed cluster; nil when they do. It is pure, so it is tested
// in both directions without a cluster.
func formedNow(views []serverView) error {
	lv, err := metaFormed(views)
	if err != nil {
		return err
	}
	for _, v := range views {
		if !slices.Contains(lv.statsPeers, v.name) {
			return fmt.Errorf("meta leader %s has no stats for %s, so placement would refuse it as offline", lv.name, v.name)
		}
	}
	return nil
}

// metaFormed is formedNow without the statistics rule: the full route mesh and a meta
// group that agrees on its leader and reports every peer current and online. It returns
// the leader's view.
func metaFormed(views []serverView) (*serverView, error) {
	want := len(views) - 1
	for _, v := range views {
		if v.remotes != want {
			return nil, fmt.Errorf("%s is routed to %d of %d other servers", v.name, v.remotes, want)
		}
	}
	leader := views[0].metaLeader
	for _, v := range views {
		if v.metaLeader == "" {
			return nil, fmt.Errorf("%s knows no meta leader", v.name)
		}
		if v.metaLeader != leader {
			return nil, fmt.Errorf("%s names meta leader %q, %s names %q", views[0].name, leader, v.name, v.metaLeader)
		}
	}
	var lv *serverView
	for i := range views {
		if views[i].name == leader {
			lv = &views[i]
		}
	}
	if lv == nil {
		return nil, fmt.Errorf("meta leader %q is not one of these servers", leader)
	}
	if len(lv.replicas) != want {
		return nil, fmt.Errorf("meta leader %s reports %d peers, want %d", leader, len(lv.replicas), want)
	}
	for _, p := range lv.replicas {
		if !p.Current {
			return nil, fmt.Errorf("meta leader %s reports peer %s not current", leader, p.Name)
		}
		if p.Offline {
			return nil, fmt.Errorf("meta leader %s reports peer %s offline", leader, p.Name)
		}
	}
	return lv, nil
}

// probeReplicatedPlacement places formedProbeStream at R=len(servers) through a client on
// servers[0], waits for its leader and len(servers)-1 current, online replicas, deletes
// it, and waits until it is gone. Every request carries the probe's own deadline, which
// is the smaller of clusterProbeBudget and within; any error is returned for the caller
// to retry.
func probeReplicatedPlacement(servers []*natsserver.Server, within time.Duration) error {
	within = min(within, clusterProbeBudget)
	if within <= 0 {
		return errors.New("no budget left for a probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	nc, err := nats.Connect(servers[0].ClientURL(), nats.Timeout(within))
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	// A probe that timed out last time may have left its stream behind; start clean.
	if err := js.DeleteStream(formedProbeStream, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("deleting a leftover probe stream: %w", err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     formedProbeStream,
		Subjects: []string{formedProbeSubject},
		Storage:  nats.FileStorage,
		Replicas: len(servers),
	}, nats.Context(ctx)); err != nil {
		return fmt.Errorf("placing a stream at R%d: %w", len(servers), err)
	}
	var lastState string
	for {
		info, err := js.StreamInfo(formedProbeStream, nats.Context(ctx))
		if err == nil && info.Cluster != nil && info.Cluster.Leader != "" && len(info.Cluster.Replicas) == len(servers)-1 {
			ready := true
			for _, p := range info.Cluster.Replicas {
				if !p.Current || p.Offline {
					ready = false
				}
			}
			if ready {
				break
			}
		}
		switch {
		case err != nil:
			lastState = err.Error()
		case info.Cluster == nil:
			lastState = "no cluster info"
		default:
			lastState = fmt.Sprintf("leader %q, %d replicas", info.Cluster.Leader, len(info.Cluster.Replicas))
		}
		if ctx.Err() != nil {
			// The caller deletes what is left once its own budget is spent.
			return fmt.Errorf("the probe stream's replicas never came up (%s)", lastState)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return deleteProbeStreamWith(ctx, js)
}

// deleteProbeStream removes the probe stream through a connection of its own.
func deleteProbeStream(servers []*natsserver.Server, within time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	nc, err := nats.Connect(servers[0].ClientURL(), nats.Timeout(within))
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	return deleteProbeStreamWith(ctx, js)
}

// deleteProbeStreamWith deletes the probe stream and waits until the meta layer no longer
// reports it, so no test's own stream listing starts with the probe still in it. It does
// not wait for each server to finish removing its own copy (its raft group and its
// store): a server stops reporting the stream before it tears those down.
func deleteProbeStreamWith(ctx context.Context, js nats.JetStreamContext) error {
	if err := js.DeleteStream(formedProbeStream, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("deleting the probe stream: %w", err)
	}
	for {
		_, err := js.StreamInfo(formedProbeStream, nats.Context(ctx))
		if errors.Is(err, nats.ErrStreamNotFound) {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("the probe stream is still reported after its delete (last: %v)", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

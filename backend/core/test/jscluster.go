// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// StartJetStreamCluster starts an in-process JetStream cluster of size servers and returns
// them once the cluster is formed, shutting them down when tb ends. A client connects to
// any of them with srv.ClientURL().
//
// Three details decide whether this is a fixture or a flake:
//
//   - The route ports are reserved together and released together (reservePorts), so a
//     cluster can never be handed the same port twice. The release-to-bind race that
//     remains is the ONE failure a new construction cures, so it is the only one retried:
//     a server that reports its route listener could not bind (errListenerBind) ends the
//     construction at once, and a new one is made on fresh ports while clusterStartBudget
//     lasts.
//   - A server slow to start is WAITED FOR, not started again. A server enables JetStream
//     before it opens any listener, and on a loaded machine that can take many seconds.
//     awaitListening waits up to clusterListenBudget for every server's client and route
//     listeners, and a server that has still not bound them fails the start, named along
//     with the listener it lacks. Starting over would throw away a server that was nearly
//     up and ask a fresh one to do the same slow work.
//   - Readiness is awaitJetStreamClusterFormed: a meta leader holding statistics for every
//     server, and a stream just placed replicated on every server. Neither a meta leader
//     nor a meta group holding every server is enough, and that function says why. A
//     cluster that binds but does not form is not rebuilt: it fails, naming what it lacks.
//
// Each server keeps the warnings and errors it logs (serverLog), and a test that fails
// prints them. The server reports some failures to its client only as a fixed sentence
// ("error creating store for stream") and writes the cause only to its own log.
func StartJetStreamCluster(tb testing.TB, size int) []*natsserver.Server {
	tb.Helper()
	servers, logs, constructions, err := startJetStreamCluster(tb, size, defaultClusterHooks(), clusterStartBudget)
	if err != nil {
		tb.Fatalf("could not start a %d-node JetStream cluster (%d construction(s)): %v", size, constructions, err)
	}
	tb.Cleanup(func() {
		for _, s := range servers {
			s.Shutdown()
		}
	})
	// Registered after the shutdown, so it runs before it: cleanups run last in, first out.
	reportServerLogsOnFailure(tb, logs)
	return servers
}

// clusterListenBudget bounds how long one construction waits for every server's client
// and route listeners to be bound.
const clusterListenBudget = 60 * time.Second

// clusterStartBudget bounds all the constructions a cluster fixture makes: a new one is
// started only while budget is left, and each is given only what is left.
const clusterStartBudget = 2 * time.Minute

// errListenerBind marks the one failure a new construction cures: a route port that
// reservePorts released was taken before the server bound it.
var errListenerBind = errors.New("a route listener could not bind its reserved port")

// clusterHooks are the parts of a construction that this package's tests replace, to
// delay a server's start or to hand a construction a port that is already taken.
type clusterHooks struct {
	ports        func(n int) ([]int, error)
	start        func(*natsserver.Server)
	listenWithin time.Duration
}

func defaultClusterHooks() clusterHooks {
	return clusterHooks{
		ports:        reservePorts,
		start:        func(s *natsserver.Server) { go s.Start() },
		listenWithin: clusterListenBudget,
	}
}

// startJetStreamCluster makes constructions until one forms, and returns its servers,
// their logs and how many constructions were made, so a test can tell a start that waited
// from one that retried.
func startJetStreamCluster(tb testing.TB, size int, h clusterHooks, budget time.Duration) (
	[]*natsserver.Server, []*serverLog, int, error) {
	var servers []*natsserver.Server
	var logs []*serverLog
	n, err := retryOnBindFailure(tb, "cluster", budget, func(within time.Duration) error {
		var err error
		servers, logs, err = tryStartJetStreamCluster(tb, size, h, within)
		return err
	})
	if err != nil {
		return nil, nil, n, err
	}
	return servers, logs, n, nil
}

// retryOnBindFailure calls try until it succeeds, and reports how many times it called it.
// It calls it again ONLY when it failed with errListenerBind and budget is left; any other
// error is returned at once. Each call is given the budget that is left. Both cluster
// fixtures start through it, so they share one retry rule.
func retryOnBindFailure(tb testing.TB, what string, budget time.Duration, try func(within time.Duration) error) (int, error) {
	deadline := time.Now().Add(budget)
	for n := 1; ; n++ {
		err := try(time.Until(deadline))
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, errListenerBind) {
			return n, err
		}
		if time.Until(deadline) <= 0 {
			return n, fmt.Errorf("%w (and the start budget of %s is spent)", err, budget)
		}
		tb.Logf("%s construction %d could not bind a port (%v); retrying on fresh ports", what, n, err)
	}
}

// reservePorts reserves n DISTINCT ephemeral ports and releases them, so route URLs can
// be written before the servers that will listen on them start.
//
// Distinct is not a nicety. Reserving them one at a time and releasing each at once lets
// the kernel hand the same port out twice, and a cluster given a duplicate has a server
// that cannot bind its cluster port: measured at 13 collisions in 3000 triples. Holding
// all n listeners open until every port has been chosen makes a duplicate impossible.
//
// The release-to-bind race remains and cannot be designed away here, which is why both
// cluster fixtures start a new construction on fresh ports when a server reports that it
// could not bind its route port (see retryOnBindFailure), and for nothing else.
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

// tryStartJetStreamCluster makes one construction and returns its servers once they are
// listening and formed, within the budget it is given. On an error it has shut every
// server down, and the error carries the last lines each server logged.
func tryStartJetStreamCluster(tb testing.TB, size int, h clusterHooks, within time.Duration) (
	[]*natsserver.Server, []*serverLog, error) {
	deadline := time.Now().Add(within)
	ports, err := h.ports(size)
	if err != nil {
		return nil, nil, err
	}
	routes := ""
	for _, p := range ports {
		routes += fmt.Sprintf("nats-route://127.0.0.1:%d,", p)
	}
	routes = routes[:len(routes)-1]

	servers := make([]*natsserver.Server, 0, size)
	logs := make([]*serverLog, 0, size)
	fail := func(err error) ([]*natsserver.Server, []*serverLog, error) {
		for _, s := range servers {
			s.Shutdown()
		}
		return nil, nil, withServerLogs(err, logs)
	}
	for i := 0; i < size; i++ {
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host:       "127.0.0.1",
			Port:       -1,
			ServerName: fmt.Sprintf("n%d", i+1),
			JetStream:  true,
			StoreDir:   JetStreamStoreDir(tb),
			Cluster: natsserver.ClusterOpts{
				Name: "dctest",
				Host: "127.0.0.1",
				Port: ports[i],
			},
			Routes: natsserver.RoutesFromStr(routes),
		})
		if err != nil {
			return fail(fmt.Errorf("new clustered nats server %d: %w", i, err))
		}
		// Before Start: a listener that cannot bind is reported from inside Start.
		logs = append(logs, attachLog(srv))
		servers = append(servers, srv)
		h.start(srv)
	}
	if err := awaitListening(servers, logs, min(h.listenWithin, time.Until(deadline))); err != nil {
		return fail(err)
	}
	if err := awaitJetStreamClusterFormed(servers, min(clusterFormBudget, time.Until(deadline))); err != nil {
		return fail(err)
	}
	return servers, logs, nil
}

// awaitListening waits until every server has bound its client and route listeners. A
// server that reports its route listener could not bind ends the wait at once, with an
// error wrapping errListenerBind that names the server and quotes what it logged. A server
// merely slow to start is waited for until within, and then named along with the
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
				return fmt.Errorf("%s: %w: %s", servers[i].Name(), errListenerBind, line)
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

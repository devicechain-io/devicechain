// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// The census decision: the actor is the lowest id among the durable's readers that are away
// from its leader (only those ever run the census), and the target is the server a STRICT
// majority of all its readers share.
func TestDecideMove(t *testing.T) {
	const dur = "d"
	r := func(id, server string, durables ...string) censusReply {
		return censusReply{ID: id, Server: server, Durables: durables}
	}
	cases := []struct {
		name    string
		replies []censusReply
		self    string
		leader  string
		target  string
		actor   bool
		ok      bool
	}{
		{"alone", []censusReply{r("a", "s1", dur)}, "a", "s3", "s1", true, true},
		{"majority, self lowest", []censusReply{r("a", "s1", dur), r("b", "s1", dur), r("c", "s2", dur)}, "a", "s3", "s1", true, true},
		{"majority, self not lowest", []censusReply{r("a", "s1", dur), r("b", "s1", dur), r("c", "s2", dur)}, "c", "s3", "s1", false, true},
		{"split two ways", []censusReply{r("a", "s1", dur), r("b", "s2", dur)}, "a", "s3", "", true, false},
		{"a replica reading another durable does not count", []censusReply{r("a", "s1", dur), r("b", "s2", "other")}, "a", "s3", "s1", true, true},
		{"self not a reader", []censusReply{r("a", "s1", "other")}, "a", "s3", "", false, false},
		// The lowest id sits on the leader's server and never runs the census: the lowest
		// of the readers AWAY from the leader acts, or nobody ever would.
		{"lowest id is on the leader's server: the next one acts", []censusReply{r("a", "s2", dur), r("b", "s1", dur), r("c", "s1", dur)}, "b", "s2", "s1", true, true},
		{"lowest id is on the leader's server: it is not the actor", []censusReply{r("a", "s2", dur), r("b", "s1", dur), r("c", "s1", dur)}, "a", "s2", "s1", false, true},
		{"every reader on the leader's server: no actor", []censusReply{r("a", "s1", dur), r("b", "s1", dur)}, "a", "s1", "s1", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, actor, ok := decideMove(c.replies, dur, c.self, c.leader)
			if target != c.target || actor != c.actor || ok != c.ok {
				t.Fatalf("decideMove = (%q, %v, %v), want (%q, %v, %v)", target, actor, ok, c.target, c.actor, c.ok)
			}
		})
	}
}

// The headless-Service guess for a server the cluster does not advertise.
func TestHeadlessURL(t *testing.T) {
	cases := []struct{ server, host, want string }{
		{"dc-nats-2", "dc-nats.dc-infra", "nats://dc-nats-2.dc-nats-headless.dc-infra:4222"},
		{"dc-nats-0", "dc-nats.dc-infra.svc.cluster.local", "nats://dc-nats-0.dc-nats-headless.dc-infra.svc.cluster.local:4222"},
		{"dc-nats-1", "dc-nats", "nats://dc-nats-1.dc-nats-headless:4222"},
		{"other-1", "dc-nats.dc-infra", ""},
		{"dc-nats-1", "127.0.0.1", ""},
		{"dc-nats-1", "localhost", ""},
		{"", "dc-nats.dc-infra", ""},
	}
	for _, c := range cases {
		if got := headlessURL(c.server, c.host, 4222); got != c.want {
			t.Errorf("headlessURL(%q, %q) = %q, want %q", c.server, c.host, got, c.want)
		}
	}
}

// A writer whose stream has a new leader does not wait for its window to settle while a
// failure episode is open: the episode keeps slots back until a publish succeeds, so the
// window cannot settle until the submitter publishes again, and waiting would only block the
// probe that ends the episode. Once the episode is over the writer moves.
//
// GUARD: without the episode check follow blocks the submitter for affinitySwapSettle (7 s)
// with nothing in flight, on every election that failed a publish.
func TestAWriterDoesNotWaitOutAFailureEpisodeToChangeConnection(t *testing.T) {
	srv, nmgr := orderedManager(t)
	ow := newOrdered(t, nmgr, 4).(*orderedWriter)
	leader, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Close)
	ow.target.Store(&pubTarget{nc: leader, server: "leader"})

	// One slot held back by the episode, nothing in flight.
	ow.slots <- struct{}{}
	ow.inEpisode.Store(true)
	start := time.Now()
	ow.follow()
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("follow waited %v during a failure episode; it must publish on and retry later", waited)
	}
	if ow.cur != ow.main {
		t.Fatalf("the writer changed connection during a failure episode")
	}

	// The episode ends (a success frees the held slot); the next submission moves.
	<-ow.slots
	ow.inEpisode.Store(false)
	ow.follow()
	if ow.cur.nc != leader || ow.conn.Load() != leader {
		t.Fatalf("the writer did not move to the leader's connection once the episode was over")
	}
}

// A leader connection is kept only when the server that answers is the one asked for: a
// candidate address that lands on another server (a load-balanced name, a stale address, a
// headless name that resolves elsewhere) is closed and the next one tried, and when none
// answers as that server there is no connection at all.
//
// GUARD: without the check the first address that accepts the credential is taken as the
// leader, and the writer publishes through a server that forwards every publish, while the
// gauge and the log say it publishes to the leader.
func TestALeaderConnectionIsKeptOnlyWhenTheRightServerAnswers(t *testing.T) {
	own, nmgr := orderedManager(t)
	other := startEmbeddedServer(t)
	a := nmgr.aff()
	a.leaderURLs = func(string) []string { return []string{own.ClientURL(), other.ClientURL()} }

	nc, err := a.connTo(other.Name())
	if err != nil {
		t.Fatalf("connTo the second server: %v", err)
	}
	if got := nc.ConnectedServerName(); got != other.Name() {
		t.Fatalf("connTo(%q) kept a connection to %q", other.Name(), got)
	}

	if nc, err := a.connTo("not-a-server"); err == nil {
		t.Fatalf("connTo a server no candidate answers as returned a connection to %q", nc.ConnectedServerName())
	}
	t.Cleanup(func() { a.closeConns(t.Context()) })
}

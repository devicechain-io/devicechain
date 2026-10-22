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

// What settles a triggered retarget: a leader was read, the target is on it, and it is the
// leader the advisory named (when one did).
//
// The counterweight is the first case: a pass that read the advisory's leader and is on it
// settles. Without it a retargetRetry that never settles would pass every other case, and
// would only cost lookups until affinityRetargetFor.
func TestRetargetRetrySettles(t *testing.T) {
	cases := []struct {
		name, want, leader string
		dialled, settles   bool
	}{
		{"the advisory's leader, on it", "n2", "n2", true, true},
		{"a registration (no leader named), on the leader", "", "n2", true, true},
		{"no leader read (an election in progress, or the lookup failed)", "n2", "", false, false},
		{"no leader read, though nothing was dialled", "", "", true, false},
		{"the lookup named the old leader", "n2", "n3", true, false},
		{"the leader could not be dialled", "n2", "n2", false, false},
		{"a registration whose leader could not be dialled", "", "n2", false, false},
	}
	for _, c := range cases {
		r := newRetargetRetry(c.want, time.Now())
		if got := r.settles(c.leader, c.dialled); got != c.settles {
			t.Errorf("%s: settles(%q, %v) with want %q = %v, want %v", c.name, c.leader, c.dialled, c.want, got, c.settles)
		}
	}
}

// An unsettled retarget is tried again, sooner first: affinityRetargetRetry doubling to
// affinityRetargetRetryMax, and never after affinityRetargetFor.
//
// GUARD: a retry with no cap backs off past the sampler's interval, and one with no end asks
// the broker every affinityRetargetRetryMax for as long as a stream has no leader.
func TestRetargetRetryBacksOffAndEnds(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	r := newRetargetRetry("n2", start)
	now := start
	var gaps []time.Duration
	for r.next(now) {
		gaps = append(gaps, r.due.Sub(now))
		now = r.due
		if len(gaps) > 1000 {
			t.Fatal("the retries never end")
		}
	}
	if gaps[0] != affinityRetargetRetry {
		t.Fatalf("first retry after %v, want %v", gaps[0], affinityRetargetRetry)
	}
	for i := 1; i < len(gaps); i++ {
		if want := min(2*gaps[i-1], affinityRetargetRetryMax); gaps[i] != want {
			t.Fatalf("retry %d after %v, want %v (gaps %v)", i, gaps[i], want, gaps)
		}
	}
	if last := gaps[len(gaps)-1]; last != affinityRetargetRetryMax {
		t.Fatalf("the backoff stopped at %v, short of its cap %v", last, affinityRetargetRetryMax)
	}
	if now.Sub(start) > affinityRetargetFor+affinityRetargetRetryMax {
		t.Fatalf("retries went on for %v, past affinityRetargetFor (%v)", now.Sub(start), affinityRetargetFor)
	}
	if now.Sub(start) < affinityRetargetFor-affinityRetargetRetryMax {
		t.Fatalf("retries ended after %v, well short of affinityRetargetFor (%v)", now.Sub(start), affinityRetargetFor)
	}
}

// A pass that ran under a retry a newer trigger has since replaced leaves the newer one alone,
// even when the pass settled: the newer trigger (a later election) has not been followed yet.
//
// GUARD: without the check a pass that read the previous leader ends the next election's
// retry, and that election is followed with one pass again, as before the retry existed.
func TestASupersededRetryDoesNotEndTheNewerOne(t *testing.T) {
	_, nmgr := orderedManager(t)
	a := nmgr.aff()
	now := time.Now()
	older := newRetargetRetry("n2", now)
	newer := newRetargetRetry("n3", now)
	// Due well after the test, so the running loop does not pick the stream up.
	newer.due = now.Add(time.Hour)
	a.mu.Lock()
	a.retries["superseded"] = newer
	a.mu.Unlock()
	t.Cleanup(func() {
		a.mu.Lock()
		delete(a.retries, "superseded")
		a.mu.Unlock()
	})

	a.settle("superseded", older, "n2", true)
	a.mu.Lock()
	got := a.retries["superseded"]
	a.mu.Unlock()
	if got != newer {
		t.Fatalf("a pass under the replaced retry changed the newer one: retry is %+v, want %+v", got, newer)
	}
}

// The loop's timer is set for the EARLIEST pending retry, whichever stream it belongs to.
//
// GUARD: a timer set for the latest leaves a stream whose retry is due sooner waiting for
// another stream's backoff.
func TestTheLoopWakesForTheEarliestRetry(t *testing.T) {
	now := time.Now()
	a := &affinity{retries: map[string]*retargetRetry{}}
	if _, ok := a.nextRetry(now); ok {
		t.Fatal("a retry is reported pending with none")
	}
	for s, in := range map[string]time.Duration{"late": 2 * time.Second, "soon": 250 * time.Millisecond, "mid": time.Second} {
		r := newRetargetRetry("", now)
		r.due = now.Add(in)
		a.retries[s] = r
	}
	if d, ok := a.nextRetry(now); !ok || d != 250*time.Millisecond {
		t.Fatalf("nextRetry = %v, %v; want 250ms, true", d, ok)
	}
	if d, _ := a.nextRetry(now.Add(5 * time.Second)); d != 0 {
		t.Fatalf("nextRetry for an overdue retry = %v, want 0", d)
	}
}

// The leader a leader-elected advisory names. The payload is the broker's
// JSStreamLeaderElectedAdvisory, as sent.
func TestElectedLeader(t *testing.T) {
	adv := `{"type":"io.nats.jetstream.advisory.v1.stream_leader_elected","id":"x","timestamp":"2026-10-22T15:15:51Z",` +
		`"stream":"test_resolved-events","leader":"n2","replicas":[{"name":"n3","current":false,"active":0}]}`
	if got := electedLeader([]byte(adv)); got != "n2" {
		t.Fatalf("electedLeader = %q, want n2", got)
	}
	if got := electedLeader([]byte("not json")); got != "" {
		t.Fatalf("electedLeader of a payload that is not JSON = %q, want none", got)
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

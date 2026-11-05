// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"fmt"
	"testing"

	nats "github.com/nats-io/nats.go"
)

func peers(leader string, all ...string) []Peer {
	var out []Peer
	for _, s := range all {
		if s != leader {
			out = append(out, Peer{Name: s, Current: true})
		}
	}
	return out
}

func everything(string) bool { return true }

var all = PlanOptions{Movable: everything}

var three = []string{"dc-nats-0", "dc-nats-1", "dc-nats-2"}

// afterRoll is the placement the GKE round measured: the last server restarted leads nothing.
func afterRoll() Snapshot {
	var snap Snapshot
	for i := 0; i < 9; i++ {
		leader := three[1+i%2]
		name := fmt.Sprintf("s%d", i)
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
		snap.Consumers = append(snap.Consumers, Consumer{Stream: name, Name: "c", Leader: leader, Peers: peers(leader, three...), Durable: true})
	}
	return snap
}

func TestPlanRebalanceEvensOutAServerThatLeadsNothing(t *testing.T) {
	plan := PlanRebalance(afterRoll(), all)
	if plan.StreamsBefore["dc-nats-0"] != 0 {
		t.Fatalf("fixture should leave dc-nats-0 leading nothing: %v", plan.StreamsBefore)
	}
	if !plan.StreamsAfter.Balanced() || !plan.ConsumersAfter.Balanced() {
		t.Fatalf("not balanced: streams %v consumers %v", plan.StreamsAfter, plan.ConsumersAfter)
	}
	if got := plan.StreamsAfter["dc-nats-0"]; got != 3 {
		t.Errorf("dc-nats-0 should lead 3 of 9 streams, leads %d", got)
	}
	// Each move must go from a server that leads more to one that leads fewer.
	seen := map[string]bool{}
	for _, m := range plan.Moves {
		k := m.Stream + "/" + m.Consumer
		if seen[k] {
			t.Errorf("group %s moved twice", k)
		}
		seen[k] = true
		if m.From == m.To {
			t.Errorf("no-op move %+v", m)
		}
	}
	if len(plan.Moves) != 6 {
		t.Errorf("want 3 stream + 3 consumer moves, got %d", len(plan.Moves))
	}
}

func TestPlanRebalanceLeavesABalancedBrokerAlone(t *testing.T) {
	var snap Snapshot
	for i := 0; i < 6; i++ {
		l := three[i%3]
		snap.Objects = append(snap.Objects, Object{Name: fmt.Sprint("s", i), Replicas: 3, Leader: l, Peers: peers(l, three...)})
	}
	if plan := PlanRebalance(snap, all); len(plan.Moves) != 0 {
		t.Fatalf("balanced broker produced moves: %+v", plan.Moves)
	}
}

func TestPlanRebalanceNeverMovesToAnUnfitReplica(t *testing.T) {
	var snap Snapshot
	for i := 0; i < 4; i++ {
		name := fmt.Sprint("s", i)
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: "a", Peers: []Peer{
			{Name: "b", Current: false}, {Name: "c", Current: true, Offline: true}}})
	}
	// Single replica groups cannot move at all.
	snap.Objects = append(snap.Objects, Object{Name: "r1", Replicas: 1, Leader: "a"})
	if plan := PlanRebalance(snap, all); len(plan.Moves) != 0 {
		t.Fatalf("moved to a lagging or offline replica: %+v", plan.Moves)
	}
}

func TestPlanRebalanceOnlyMovesToAReplicaOfTheGroup(t *testing.T) {
	snap := Snapshot{Objects: []Object{
		{Name: "x", Replicas: 2, Leader: "a", Peers: []Peer{{Name: "b", Current: true}}},
		{Name: "y", Replicas: 2, Leader: "a", Peers: []Peer{{Name: "b", Current: true}}},
		{Name: "z", Replicas: 2, Leader: "a", Peers: []Peer{{Name: "b", Current: true}}},
		{Name: "w", Replicas: 1, Leader: "c"},
	}}
	plan := PlanRebalance(snap, all)
	for _, m := range plan.Moves {
		if m.To != "b" {
			t.Errorf("moved %s to %s, which does not replicate it", m.Stream, m.To)
		}
	}
	if len(plan.Moves) == 0 {
		t.Fatal("expected a move to b")
	}
}

func TestPlanRebalanceHonoursTheBound(t *testing.T) {
	if plan := PlanRebalance(afterRoll(), PlanOptions{Movable: everything, MaxMoves: 2}); len(plan.Moves) != 2 {
		t.Fatalf("want exactly 2 moves, got %d", len(plan.Moves))
	}
}

func TestStepDownOutcomeNeverReadsAFailureAsAMove(t *testing.T) {
	ok := &nats.Msg{Data: []byte(`{"success":true}`)}
	if err := stepDownOutcome(ok, nil); err != nil {
		t.Errorf("a confirmed step-down failed: %v", err)
	}
	for name, msg := range map[string]*nats.Msg{
		"an API error":    {Data: []byte(`{"error":{"code":400,"description":"no replacement peer connected"}}`)},
		"no confirmation": {Data: []byte(`{}`)},
		"unreadable":      {Data: []byte(`nope`)},
	} {
		if err := stepDownOutcome(msg, nil); err == nil {
			t.Errorf("%s was read as a move that happened", name)
		}
	}
	if err := stepDownOutcome(nil, nats.ErrTimeout); err == nil {
		t.Error("a request that timed out was read as a move that happened")
	}
	// The subject names the right verb for a stream and for a consumer.
	if got := stepDownSubject(Move{Stream: "s"}); got != "$JS.API.STREAM.LEADER.STEPDOWN.s" {
		t.Errorf("stream subject %q", got)
	}
	if got := stepDownSubject(Move{Stream: "s", Consumer: "c"}); got != "$JS.API.CONSUMER.LEADER.STEPDOWN.s.c" {
		t.Errorf("consumer subject %q", got)
	}
}

// One group more on one server than another is as even as whole groups allow. Moving it would only
// swap which server has the extra, so it is not a move.
func TestPlanRebalanceDoesNotSwapAnExtraGroupBetweenServers(t *testing.T) {
	var snap Snapshot
	for i := 0; i < 10; i++ {
		l := three[0]
		if i >= 4 {
			l = three[1+i%2]
		}
		snap.Objects = append(snap.Objects, Object{Name: fmt.Sprint("s", i), Replicas: 3, Leader: l, Peers: peers(l, three...)})
	}
	plan := PlanRebalance(snap, all)
	if hi, lo := plan.StreamsBefore.Max(); hi-lo != 1 {
		t.Fatalf("fixture should differ by exactly one: %v", plan.StreamsBefore)
	}
	if len(plan.Moves) != 0 {
		t.Fatalf("moved groups between servers that differ by one: %+v", plan.Moves)
	}
}

// Ingest streams are never moved, and with no Movable at all nothing is: a plan that does not know
// what is unsafe moves nothing.
func TestPlanRebalanceOnlyTouchesWhatTheCallerNames(t *testing.T) {
	snap := afterRoll()
	none := PlanRebalance(snap, PlanOptions{})
	if len(none.Moves) != 0 {
		t.Fatalf("a plan with no Movable moved %d groups", len(none.Moves))
	}
	noS0 := func(stream string) bool { return stream != "s0" && stream != "s1" }
	plan := PlanRebalance(snap, PlanOptions{Movable: noS0})
	for _, m := range plan.Moves {
		if m.Stream == "s0" || m.Stream == "s1" {
			t.Errorf("moved %s, which the caller did not name", m.Stream)
		}
	}
	if len(plan.Moves) == 0 {
		t.Fatal("expected moves on the streams that are named")
	}
}

// Separation: the movable per-event stream is led away from the fixed ones, onto the replica with the
// lowest led count, and is pinned there for the count balancing. The fixed ones never move.
func TestPlanRebalanceSeparatesTheMovablePerEventStream(t *testing.T) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	add("capture", "dc-nats-0")
	add("inbound", "dc-nats-1")
	add("resolved", "dc-nats-0") // shares with capture
	add("cold-a", "dc-nats-1")
	add("cold-b", "dc-nats-1")
	add("cold-c", "dc-nats-2")
	opts := PlanOptions{
		Movable:  func(s string) bool { return s != "capture" && s != "inbound" },
		PerEvent: func(s string) bool { return s == "capture" || s == "inbound" || s == "resolved" },
	}
	plan := PlanRebalance(snap, opts)
	if len(plan.Moves) == 0 || plan.Moves[0].Stream != "resolved" || plan.Moves[0].To != "dc-nats-2" {
		t.Fatalf("resolved should go to the only server leading no per-event stream: %+v", plan.Moves)
	}
	for _, m := range plan.Moves {
		if m.Stream == "capture" || m.Stream == "inbound" {
			t.Errorf("moved a fixed stream: %+v", m)
		}
	}
	if plan.PerEventShared || plan.PerEventLeaders["resolved"] != "dc-nats-2" {
		t.Errorf("leaders after: %v shared=%v", plan.PerEventLeaders, plan.PerEventShared)
	}

	// Control: without PerEvent the same broker is left sharing, by the count plan.
	plain := PlanRebalance(snap, PlanOptions{Movable: opts.Movable})
	for _, m := range plain.Moves {
		if m.Stream == "resolved" && m.To == "dc-nats-2" {
			t.Errorf("control: the count plan moved resolved to the free server by itself: %+v", m)
		}
	}
}

// With no replica on a free server nothing is forced, and the plan says the leaders are shared.
func TestPlanRebalanceReportsSharedLeadersWhenThereIsNowhereToGo(t *testing.T) {
	var snap Snapshot
	snap.Objects = []Object{
		{Name: "capture", Replicas: 2, Leader: "dc-nats-0", Peers: peers("dc-nats-0", "dc-nats-0", "dc-nats-1")},
		{Name: "inbound", Replicas: 2, Leader: "dc-nats-1", Peers: peers("dc-nats-1", "dc-nats-0", "dc-nats-1")},
		{Name: "resolved", Replicas: 2, Leader: "dc-nats-0", Peers: peers("dc-nats-0", "dc-nats-0", "dc-nats-1")},
	}
	plan := PlanRebalance(snap, PlanOptions{
		Movable:  func(s string) bool { return s == "resolved" },
		PerEvent: func(string) bool { return true },
	})
	for _, m := range plan.Moves {
		if m.Stream == "resolved" {
			t.Errorf("resolved moved onto a server leading a per-event stream: %+v", m)
		}
	}
	if !plan.PerEventShared {
		t.Errorf("should read as shared: %v", plan.PerEventLeaders)
	}
}

// A move is refused while any replica of its group is offline or behind, even when the target itself
// is fine: an outage that began after the plan was made is no time to start an election.
func TestPrecheckRefusesAMoveDuringAnOutage(t *testing.T) {
	m := Move{Stream: "s", From: "a", To: "b"}
	healthy := []Peer{{Name: "b", Current: true}, {Name: "c", Current: true}}
	if skip, err := precheck("a", healthy, m); skip || err != nil {
		t.Fatalf("healthy group: skip=%v err=%v", skip, err)
	}
	for name, peers := range map[string][]Peer{
		"other replica offline": {{Name: "b", Current: true}, {Name: "c", Current: true, Offline: true}},
		"other replica behind":  {{Name: "b", Current: true}, {Name: "c"}},
	} {
		if skip, err := precheck("a", peers, m); skip || err == nil {
			t.Errorf("%s: skip=%v err=%v, want a refusal", name, skip, err)
		}
	}
	if skip, _ := precheck("b", healthy, m); !skip {
		t.Error("a group already led by the target is skipped")
	}
	if skip, _ := precheck("x", healthy, m); !skip {
		t.Error("a group led by neither server is skipped")
	}
	if skip, err := precheck("a", []Peer{{Name: "b"}, {Name: "c", Current: true}}, m); !skip || err != nil {
		t.Errorf("an unfit target is skipped: skip=%v err=%v", skip, err)
	}
}

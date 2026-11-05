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

// hotLoad is the placement the GKE rounds measured after a roll: counts read even (3/3/3), but one
// server leads both streams every event passes through (inbound, which may not move, and resolved,
// which may) while the other two lead cold streams only.
func hotLoad() (Snapshot, PlanOptions) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
		snap.Consumers = append(snap.Consumers, Consumer{Stream: name, Name: "c", Leader: leader, Peers: peers(leader, three...), Durable: true})
	}
	add("inbound", "dc-nats-2")
	add("resolved", "dc-nats-2")
	add("cold-a", "dc-nats-2")
	add("cold-b", "dc-nats-0")
	add("cold-c", "dc-nats-0")
	add("cold-d", "dc-nats-0")
	add("cold-e", "dc-nats-1")
	add("cold-f", "dc-nats-1")
	add("cold-g", "dc-nats-1")
	weights := map[string]int{"inbound": 100, "resolved": 100}
	return snap, PlanOptions{
		// inbound is on the acknowledged-ingest path: weighed, never moved.
		Movable: func(s string) bool { return s != "inbound" },
		Weight: func(s string) int {
			if w, ok := weights[s]; ok {
				return w
			}
			return 1
		},
	}
}

// NEGATIVE CONTROL. Counting groups, this broker is already even and the plan does nothing, which
// is the bug: both per-event streams sit on one server. The weighted plan must move resolved away.
func TestPlanRebalanceByTrafficSeparatesTheHotStreams(t *testing.T) {
	snap, opts := hotLoad()

	counted := PlanRebalance(snap, PlanOptions{Movable: opts.Movable})
	if len(counted.Moves) != 0 {
		t.Fatalf("control: the count-based plan should see nothing to do, got %+v", counted.Moves)
	}

	plan := PlanRebalance(snap, opts)
	var resolved *Move
	for i, m := range plan.Moves {
		if m.Stream == "inbound" {
			t.Errorf("moved a stream that may not move: %+v", m)
		}
		if m.Stream == "resolved" && !m.IsConsumer() {
			resolved = &plan.Moves[i]
		}
	}
	if resolved == nil || resolved.From != "dc-nats-2" || resolved.To == "dc-nats-2" {
		t.Fatalf("resolved should leave the server that leads inbound: %+v", plan.Moves)
	}
	if got := plan.LoadBefore["dc-nats-2"]; got != 402 {
		t.Errorf("load before on dc-nats-2 = %d, want 402 (inbound+resolved+cold-a, streams and consumers)", got)
	}
	// What stays is the fixed inbound stream and its consumer (200), at most one cold pair more.
	if got := plan.LoadAfter["dc-nats-2"]; got > 202 {
		t.Errorf("the hot server still leads resolved: %v", plan.LoadAfter)
	}
}

// Idempotence: planning again over the state a plan leaves moves nothing, so a second run (or the
// upgrade's step after a manual one) is a no-op and the plan cannot ping-pong a stream.
func TestPlanRebalanceByTrafficIsIdempotent(t *testing.T) {
	snap, opts := hotLoad()
	plan := PlanRebalance(snap, opts)
	apply := func(leader *string, stream, consumer string) {
		for _, m := range plan.Moves {
			if m.Stream == stream && m.Consumer == consumer {
				*leader = m.To
			}
		}
	}
	for i := range snap.Objects {
		apply(&snap.Objects[i].Leader, snap.Objects[i].Name, "")
		snap.Objects[i].Peers = peers(snap.Objects[i].Leader, three...)
	}
	for i := range snap.Consumers {
		apply(&snap.Consumers[i].Leader, snap.Consumers[i].Stream, snap.Consumers[i].Name)
		snap.Consumers[i].Peers = peers(snap.Consumers[i].Leader, three...)
	}
	if again := PlanRebalance(snap, opts); len(again.Moves) != 0 {
		t.Fatalf("a second plan over the result moved %+v", again.Moves)
	}
}

// A fixed hot stream is an anchor, never a candidate: with the movable streams all light, the plan
// must not move the heavy fixed one however uneven the load, and must not move a group onto the
// server whose gap it would widen.
func TestPlanRebalanceByTrafficNeverMovesAFixedStreamOrWidensAGap(t *testing.T) {
	snap, opts := hotLoad()
	opts.Movable = func(s string) bool { return s != "inbound" && s != "resolved" }
	plan := PlanRebalance(snap, opts)
	for _, m := range plan.Moves {
		if m.Stream == "inbound" || m.Stream == "resolved" {
			t.Errorf("moved a fixed stream: %+v", m)
		}
		if m.From == "dc-nats-0" || m.From == "dc-nats-1" {
			// the light servers are never drained toward the heavy one
			if m.To == "dc-nats-2" {
				t.Errorf("moved load onto the heaviest server: %+v", m)
			}
		}
	}
	hi, _ := plan.LoadAfter.Max()
	if before, _ := plan.LoadBefore.Max(); hi > before {
		t.Errorf("max load rose from %d to %d", before, hi)
	}
}

// Weights only add anchors: with no Weight the plan is the count-based one, and a stream the
// options do not make movable is left out of the arithmetic entirely.
func TestPlanRebalanceWithoutWeightsIgnoresUnmovableStreams(t *testing.T) {
	snap, opts := hotLoad()
	plan := PlanRebalance(snap, PlanOptions{Movable: opts.Movable})
	if got := plan.StreamsBefore["dc-nats-2"]; got != 2 {
		t.Errorf("unmovable inbound counted without weights: dc-nats-2 leads %d, want 2", got)
	}
}

// A server pays for the consumers it leads as well as the streams, so they are one pool. Planned
// apart, resolved's stream would go to C (it leads no stream) although C already leads two
// consumers of equally hot streams: 200/200/200 would become 100/200/300.
func TestPlanRebalanceByTrafficCountsConsumersAndStreamsTogether(t *testing.T) {
	var snap Snapshot
	stream := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	consumer := func(name, leader string) {
		snap.Consumers = append(snap.Consumers, Consumer{Stream: name, Name: "c", Leader: leader, Peers: peers(leader, three...), Durable: true})
	}
	a, b, c := three[0], three[1], three[2]
	stream("inbound", a)
	stream("capture", b)
	stream("resolved", a)
	consumer("inbound", c)
	consumer("capture", c)
	consumer("resolved", b)
	plan := PlanRebalance(snap, PlanOptions{
		Movable: func(s string) bool { return s == "resolved" },
		Weight:  func(string) int { return 100 },
	})
	if hi, lo := plan.LoadBefore.Max(); hi != 200 || lo != 200 {
		t.Fatalf("fixture should start level at 200 each: %v", plan.LoadBefore)
	}
	if len(plan.Moves) != 0 {
		t.Fatalf("a level broker was disturbed: %+v (load after %v)", plan.Moves, plan.LoadAfter)
	}
}

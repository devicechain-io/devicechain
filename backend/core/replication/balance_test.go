// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// A move is refused while a replica of its group is offline, at once; one that is merely behind is
// reported as such, for the caller to wait on. An outage that began after the plan was made is no time
// to start an election.
func TestPrecheckRefusesAMoveDuringAnOutage(t *testing.T) {
	m := Move{Stream: "s", From: "a", To: "b"}
	healthy := []Peer{{Name: "b", Current: true}, {Name: "c", Current: true}}
	if skip, err := precheck("a", healthy, m); skip || err != nil {
		t.Fatalf("healthy group: skip=%v err=%v", skip, err)
	}
	if skip, err := precheck("a", []Peer{{Name: "b", Current: true}, {Name: "c", Current: true, Offline: true}}, m); skip || err != errOffline {
		t.Errorf("other replica offline: skip=%v err=%v", skip, err)
	}
	if skip, err := precheck("a", []Peer{{Name: "b", Current: true}, {Name: "c"}}, m); skip || err != errBehind {
		t.Errorf("other replica behind: skip=%v err=%v", skip, err)
	}
	if skip, err := precheck("a", []Peer{{Name: "b"}, {Name: "c", Current: true}}, m); skip || err != errBehind {
		t.Errorf("target behind is waited for, not dropped: skip=%v err=%v", skip, err)
	}
	if skip, _ := precheck("b", healthy, m); !skip {
		t.Error("a group already led by the target is skipped")
	}
	if skip, _ := precheck("x", healthy, m); !skip {
		t.Error("a group led by neither server is skipped")
	}
	if skip, err := precheck("a", []Peer{{Name: "b", Offline: true}, {Name: "c", Current: true}}, m); !skip || err != nil {
		t.Errorf("an offline target is skipped: skip=%v err=%v", skip, err)
	}
}

// fakeGroup is a broker group whose replicas read as the script says, one reading per call.
type fakeGroup struct {
	readings [][]Peer
	reads    int
	sent     int
	stuck    bool // the leader never leaves "a"
}

func (f *fakeGroup) state(Move) (string, []Peer, error) {
	i := min(f.reads, len(f.readings)-1)
	f.reads++
	if f.sent > 0 && !f.stuck {
		return "b", f.readings[i], nil // the step-down took effect
	}
	return "a", f.readings[i], nil
}

func (f *fakeGroup) io() moveIO {
	return moveIO{state: f.state, stepDown: func(context.Context, Move) error { f.sent++; return nil }}
}

var fastApply = ApplyOptions{Poll: time.Millisecond, Behind: 50 * time.Millisecond, Settle: 50 * time.Millisecond}

// Under load a follower reads not-current for a moment. The move waits for it instead of being
// refused on the first reading. (Negative control: with Behind forced to zero the move is refused.)
func TestApplyMovesWaitsForALaggingReplicaToCatchUp(t *testing.T) {
	behind := []Peer{{Name: "b", Current: true}, {Name: "c"}}
	ok := []Peer{{Name: "b", Current: true}, {Name: "c", Current: true}}
	g := &fakeGroup{readings: [][]Peer{behind, behind, ok}}
	res := applyMoves(context.Background(), g.io(), []Move{{Stream: "s", From: "a", To: "b"}}, fastApply)
	// After the step-down the fake still reports leader "a"; the move itself was sent, which is the point.
	if g.sent != 1 {
		t.Fatalf("the move was not sent after the replica caught up: %+v", res)
	}
	g2 := &fakeGroup{readings: [][]Peer{behind, behind, ok}}
	zero := fastApply
	zero.Behind = time.Nanosecond // effectively no wait
	if r := applyMoves(context.Background(), g2.io(), []Move{{Stream: "s", From: "a", To: "b"}}, zero); g2.sent != 0 || r[0].Err == nil {
		t.Errorf("control: with no wait the first reading refuses: sent=%d %+v", g2.sent, r[0])
	}
}

// A replica that stays behind past the window refuses the move, and nothing is sent. An offline one
// refuses it at once, without waiting.
func TestApplyMovesRefusesAndSendsNothingWhileAReplicaIsDownOrStuckBehind(t *testing.T) {
	behind := []Peer{{Name: "b", Current: true}, {Name: "c"}}
	g := &fakeGroup{readings: [][]Peer{behind}}
	r := applyMoves(context.Background(), g.io(), []Move{{Stream: "s", From: "a", To: "b"}}, fastApply)
	if g.sent != 0 || r[0].Err != errBehind || r[0].Skipped {
		t.Errorf("stuck behind: sent=%d %+v", g.sent, r[0])
	}
	down := &fakeGroup{readings: [][]Peer{{{Name: "b", Current: true}, {Name: "c", Current: true, Offline: true}}}}
	slow := fastApply
	slow.Behind = time.Minute
	start := time.Now()
	r = applyMoves(context.Background(), down.io(), []Move{{Stream: "s", From: "a", To: "b"}}, slow)
	if down.sent != 0 || r[0].Err != errOffline || down.reads != 1 || time.Since(start) > time.Second {
		t.Errorf("offline: sent=%d reads=%d %+v", down.sent, down.reads, r[0])
	}
}

// The pin: after resolved is separated onto the only free server (n2), the count balancing sees n2
// leading three movable streams and n0 and n1 none. Its candidates were read before the move, so n1
// (inbound's server) is among them, and unpinned it is the first move the balancing makes. With the
// pin, something else moves.
func TestPlanRebalanceKeepsTheSeparatedStreamWhereItWasPut(t *testing.T) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	add("capture", "dc-nats-0")
	add("inbound", "dc-nats-1")
	add("resolved", "dc-nats-0")
	add("z1", "dc-nats-2")
	add("z2", "dc-nats-2")
	opts := PlanOptions{
		Movable:  func(s string) bool { return s != "capture" && s != "inbound" },
		PerEvent: func(s string) bool { return s == "capture" || s == "inbound" || s == "resolved" },
	}
	plan := PlanRebalance(snap, opts)
	var resolvedMoves []Move
	for _, m := range plan.Moves {
		if m.Stream == "resolved" {
			resolvedMoves = append(resolvedMoves, m)
		}
	}
	if len(resolvedMoves) != 1 || resolvedMoves[0].To != "dc-nats-2" {
		t.Fatalf("resolved should move once, to the free server: %+v", plan.Moves)
	}
	if plan.PerEventLeaders["resolved"] != "dc-nats-2" || plan.PerEventShared {
		t.Errorf("leaders after: %v shared=%v", plan.PerEventLeaders, plan.PerEventShared)
	}
	if len(plan.Moves) < 2 {
		t.Errorf("the count balancing should still have moved something else off dc-nats-2: %+v", plan.Moves)
	}
}

// The fixed pair sharing a server is reported as information; the movable stream is still separated
// from it, and the plan is not marked shared.
func TestPlanRebalanceReportsAFixedPairSharingAServerApart(t *testing.T) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	add("capture", "dc-nats-0")
	add("inbound", "dc-nats-0")
	add("resolved", "dc-nats-1")
	plan := PlanRebalance(snap, PlanOptions{
		Movable:  func(s string) bool { return s == "resolved" },
		PerEvent: func(string) bool { return true },
	})
	if plan.PerEventShared {
		t.Errorf("resolved is separate; the fixed pair must not make the plan read as shared: %v", plan.PerEventLeaders)
	}
	if len(plan.FixedShared) != 1 || plan.FixedShared[0].Server != "dc-nats-0" || len(plan.FixedShared[0].Streams) != 2 {
		t.Errorf("fixed pair not reported: %+v", plan.FixedShared)
	}
}

// Handoff is confirmed only once the leader has left the old server. A reading of the old leader after
// an accepted step-down means the election is not seen yet, so the next move is not started on it.
func TestApplyMovesDoesNotStartTheNextMoveUntilTheHandoffIsSeen(t *testing.T) {
	ok := []Peer{{Name: "b", Current: true}, {Name: "c", Current: true}}
	g := &fakeGroup{readings: [][]Peer{ok}, stuck: true}
	moves := []Move{{Stream: "s1", From: "a", To: "b"}, {Stream: "s2", From: "a", To: "b"}}
	res := applyMoves(context.Background(), g.io(), moves, fastApply)
	if g.sent != 1 {
		t.Fatalf("the second move was sent on an unseen handoff: sent=%d %+v", g.sent, res)
	}
	if res[0].Err == nil || res[1].Err == nil {
		t.Errorf("neither move should read as done: %+v", res)
	}
	// Control: when the handoff lands, the second move goes out. Each group stays on "a" until its own
	// step-down, so an executor that stopped after the first handoff would send one request, not two.
	stepped := map[string]bool{}
	sent := 0
	r2 := applyMoves(context.Background(), moveIO{state: func(m Move) (string, []Peer, error) {
		if stepped[m.Stream] {
			return "b", ok, nil
		}
		return "a", ok, nil
	}, stepDown: func(_ context.Context, m Move) error { stepped[m.Stream] = true; sent++; return nil }}, moves, fastApply)
	if sent != 2 || r2[0].Err != nil || r2[1].Err != nil || r2[1].Skipped {
		t.Errorf("control: both moves should be sent and done: sent=%d %+v", sent, r2)
	}
}

// The step-down names a preferred placement, which the broker may not honour. A group that settled on
// a third server is steady, so the next move still goes out, but the move is reported as not to plan
// rather than as the separated placement it was meant to produce.
func TestApplyMovesReportsALeaderThatSettledOnAThirdServer(t *testing.T) {
	ok := []Peer{{Name: "b", Current: true}, {Name: "c", Current: true}}
	stepped := map[string]bool{}
	moves := []Move{{Stream: "s1", From: "a", To: "b"}, {Stream: "s2", From: "a", To: "b"}}
	res := applyMoves(context.Background(), moveIO{state: func(m Move) (string, []Peer, error) {
		switch {
		case !stepped[m.Stream]:
			return "a", ok, nil
		case m.Stream == "s1":
			return "c", ok, nil // elected elsewhere
		}
		return "b", ok, nil
	}, stepDown: func(_ context.Context, m Move) error { stepped[m.Stream] = true; return nil }}, moves, fastApply)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "not to plan") || !strings.Contains(res[0].Err.Error(), `"c"`) {
		t.Errorf("a third-server election must not read as the planned move: %+v", res[0])
	}
	if res[1].Err != nil || res[1].Skipped || !stepped["s2"] {
		t.Errorf("a settled group must not stop the run: %+v", res[1])
	}
}

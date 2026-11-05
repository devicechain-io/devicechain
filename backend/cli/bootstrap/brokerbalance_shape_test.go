// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The tests here run the planner on the REAL instance shape: every declared stream, replicated three
// ways, and one durable consumer per durable or ack-floor reader the declaration names (so
// resolved-events carries its three). A fixture without the consumers cannot see what they do to a
// plan, which is how a design that weighed them looked right on paper.

const shapeInstance = "dc"

var shapeServers = []string{"n0", "n1", "n2"}

func shapePeers(leader string) []replication.Peer {
	var out []replication.Peer
	for _, s := range shapeServers {
		if s != leader {
			out = append(out, replication.Peer{Name: s, Current: true})
		}
	}
	return out
}

// shapeSnapshot places every stream and consumer of the instance with the given chooser.
func shapeSnapshot(pick func(name string) string) replication.Snapshot {
	var snap replication.Snapshot
	for _, s := range streams.All {
		name := messaging.StreamName(shapeInstance, s.Suffix)
		l := pick(name)
		snap.Objects = append(snap.Objects, replication.Object{Name: name, Replicas: 3, Leader: l, Peers: shapePeers(l)})
		for _, r := range streams.ReadersOf(s.Suffix) {
			if r.Mode != streams.ReadDurable && r.Mode != streams.ReadAckFloor {
				continue
			}
			cname := fmt.Sprintf("%s-%d", r.Area, r.Mode)
			cl := pick(name + "/" + cname)
			snap.Consumers = append(snap.Consumers, replication.Consumer{Stream: name, Name: cname, Durable: true, Leader: cl, Peers: shapePeers(cl)})
		}
	}
	return snap
}

func shapeOptions(perEvent bool) replication.PlanOptions {
	movable := map[string]bool{}
	for _, n := range messaging.RebalanceableStreams(shapeInstance) {
		movable[n] = true
	}
	opts := replication.PlanOptions{Movable: func(s string) bool { return movable[s] }, MaxMoves: brokerRebalanceMaxMoves}
	if perEvent {
		pe := map[string]bool{}
		for _, n := range messaging.PerEventStreams(shapeInstance) {
			pe[n] = true
		}
		opts.PerEvent = func(s string) bool { return pe[s] }
	}
	return opts
}

func applyMoves(snap replication.Snapshot, moves []replication.Move) replication.Snapshot {
	out := replication.Snapshot{Objects: append([]replication.Object(nil), snap.Objects...), Consumers: append([]replication.Consumer(nil), snap.Consumers...)}
	for _, m := range moves {
		if m.IsConsumer() {
			for i := range out.Consumers {
				if out.Consumers[i].Stream == m.Stream && out.Consumers[i].Name == m.Consumer {
					out.Consumers[i].Leader, out.Consumers[i].Peers = m.To, shapePeers(m.To)
				}
			}
			continue
		}
		for i := range out.Objects {
			if out.Objects[i].Name == m.Stream {
				out.Objects[i].Leader, out.Objects[i].Peers = m.To, shapePeers(m.To)
			}
		}
	}
	return out
}

func leaderOfStream(snap replication.Snapshot, suffix string) string {
	n := messaging.StreamName(shapeInstance, suffix)
	for _, o := range snap.Objects {
		if o.Name == n {
			return o.Leader
		}
	}
	return ""
}

// alertFires is BrokerLeadershipImbalanced: over the movable streams, the server leading the fewest
// leads fewer than half the average, and the average is at least three.
func alertFires(snap replication.Snapshot) bool {
	movable := map[string]bool{}
	for _, n := range messaging.RebalanceableStreams(shapeInstance) {
		movable[n] = true
	}
	counts := map[string]int{}
	for _, s := range shapeServers {
		counts[s] = 0
	}
	total := 0
	for _, o := range snap.Objects {
		if movable[o.Name] {
			counts[o.Leader]++
			total++
		}
	}
	avg := float64(total) / float64(len(shapeServers))
	lo := total
	for _, c := range counts {
		lo = min(lo, c)
	}
	return avg >= 3 && float64(lo) < 0.5*avg
}

func randomPlacement(rng *rand.Rand) replication.Snapshot {
	return shapeSnapshot(func(string) string { return shapeServers[rng.Intn(len(shapeServers))] })
}

// resolvedSeparated reports whether resolved-events is led away from the capture and inbound
// leaders. With three servers and two fixed leaders a free server always exists, and every replica
// is current in these fixtures, so separation is always possible.
func resolvedSeparated(snap replication.Snapshot) bool {
	capture, inbound, resolved := leaderOfStream(snap, streams.DeviceEventsCapture), leaderOfStream(snap, streams.InboundEvents), leaderOfStream(snap, streams.ResolvedEvents)
	return resolved != capture && resolved != inbound
}

// movedFixed names a move of a group the options do not let move, or "".
func movedFixed(moves []replication.Move, opts replication.PlanOptions) string {
	for _, m := range moves {
		if !opts.Movable(m.Stream) {
			return m.Stream
		}
	}
	return ""
}

const sweepSeeds = 2000

func TestPlacementPropertiesOnTheRealInstanceShape(t *testing.T) {
	opts := shapeOptions(true)
	for seed := 0; seed < sweepSeeds; seed++ {
		snap := randomPlacement(rand.New(rand.NewSource(int64(seed))))
		plan := replication.PlanRebalance(snap, opts)
		after := applyMoves(snap, plan.Moves)

		// (a) resolved-events ends off the servers that lead the other two.
		if !resolvedSeparated(after) {
			t.Fatalf("seed %d: resolved-events shares a server with capture/inbound after the plan", seed)
		}
		// (b) the plan never leaves the imbalance alert firing when the count plan alone would not.
		if alertFires(after) && !alertFires(applyMoves(snap, replication.PlanRebalance(snap, shapeOptions(false)).Moves)) {
			t.Fatalf("seed %d: the plan leaves BrokerLeadershipImbalanced firing where the count plan would not", seed)
		}
		// (c) a second plan over the result is a no-op.
		if again := replication.PlanRebalance(after, opts); len(again.Moves) != 0 {
			t.Fatalf("seed %d: a second plan still moves %+v", seed, again.Moves)
		}
		// (d) the fixed groups (ingest streams and their consumers) never move.
		if s := movedFixed(plan.Moves, opts); s != "" {
			t.Fatalf("seed %d: moved a fixed group of %s", seed, s)
		}
	}
}

// The shape the review simulated: capture n0, inbound n1, resolved n2 is already right, and the plan
// must not disturb resolved-events.
func TestAlreadySeparatedLeadersStayPut(t *testing.T) {
	place := map[string]string{
		messaging.StreamName(shapeInstance, streams.DeviceEventsCapture): "n0",
		messaging.StreamName(shapeInstance, streams.InboundEvents):       "n1",
		messaging.StreamName(shapeInstance, streams.ResolvedEvents):      "n2",
	}
	snap := shapeSnapshot(func(name string) string {
		if l, ok := place[name]; ok {
			return l
		}
		return "n1"
	})
	plan := replication.PlanRebalance(snap, shapeOptions(true))
	for _, m := range plan.Moves {
		if m.Stream == messaging.StreamName(shapeInstance, streams.ResolvedEvents) && !m.IsConsumer() {
			t.Errorf("resolved-events was moved off a correct placement: %+v", m)
		}
	}
	if plan.PerEventShared || !resolvedSeparated(applyMoves(snap, plan.Moves)) {
		t.Errorf("per-event leaders should be separate: %v", plan.PerEventLeaders)
	}
	if line := perEventLine(plan, nil); !strings.Contains(line, "(separated)") || !strings.Contains(line, "resolved-events=n2") {
		t.Errorf("summary line: %q", line)
	}
}

// With nowhere to put resolved-events (two servers, capture and inbound on both of them) nothing is
// forced and the summary says SHARED. Control: the same fixture with a third server separates it.
func TestNoFreeServerIsReportedNotForced(t *testing.T) {
	cap, in, res := messaging.StreamName(shapeInstance, streams.DeviceEventsCapture), messaging.StreamName(shapeInstance, streams.InboundEvents), messaging.StreamName(shapeInstance, streams.ResolvedEvents)
	place := map[string]string{cap: "n0", in: "n1", res: "n0"}
	snap := shapeSnapshot(func(name string) string {
		if l, ok := place[name]; ok {
			return l
		}
		return "n0"
	})
	two := func(leader string) []replication.Peer {
		if leader == "n0" {
			return []replication.Peer{{Name: "n1", Current: true}}
		}
		return []replication.Peer{{Name: "n0", Current: true}}
	}
	three := replication.Snapshot{Objects: append([]replication.Object(nil), snap.Objects...), Consumers: snap.Consumers}
	for i := range snap.Objects {
		snap.Objects[i].Peers = two(snap.Objects[i].Leader)
	}
	for i := range snap.Consumers {
		snap.Consumers[i].Peers = two(snap.Consumers[i].Leader)
	}
	plan := replication.PlanRebalance(snap, shapeOptions(true))
	if !plan.PerEventShared {
		t.Errorf("resolved-events has no free server and must read as shared: %v", plan.PerEventLeaders)
	}
	if line := perEventLine(plan, nil); !strings.Contains(line, "(SHARED)") {
		t.Errorf("the summary should say SHARED: %q", line)
	}
	if ctl := replication.PlanRebalance(three, shapeOptions(true)); ctl.PerEventShared {
		t.Errorf("control: with a replica on the third server it separates: %v", ctl.PerEventLeaders)
	}
}

// ---- negative controls: each property's detector must go red on what it guards against. ----

// (a) without the separation step (the count plan alone) resolved-events is left sharing a server.
func TestControlSeparationOffLeavesResolvedSharing(t *testing.T) {
	bad := 0
	for seed := 0; seed < sweepSeeds; seed++ {
		snap := randomPlacement(rand.New(rand.NewSource(int64(seed))))
		if !resolvedSeparated(applyMoves(snap, replication.PlanRebalance(snap, shapeOptions(false)).Moves)) {
			bad++
		}
	}
	if bad == 0 {
		t.Fatal("control: the count plan never left resolved-events sharing, so property (a) detects nothing")
	}
	t.Logf("count plan alone: %d of %d placements end with resolved-events sharing a server", bad, sweepSeeds)
}

// weightedControlPlan is a compact copy of the PREVIOUS design, for the control only: one pool,
// every consumer weighing what its stream does (per-event 100, other hot 10, cold 1), fixed groups
// counted as load.
func weightedControlPlan(snap replication.Snapshot, opts replication.PlanOptions) []replication.Move {
	pe := map[string]bool{}
	for _, n := range messaging.PerEventStreams(shapeInstance) {
		pe[n] = true
	}
	hot := map[string]bool{}
	for _, s := range streams.All {
		hot[messaging.StreamName(shapeInstance, s.Suffix)] = s.Tier == streams.Hot
	}
	weight := func(stream string) int {
		switch {
		case pe[stream]:
			return 100
		case hot[stream]:
			return 10
		}
		return 1
	}
	type g struct {
		stream, consumer, leader string
		w                        int
		movable, done            bool
	}
	var gs []*g
	load := map[string]int{}
	for _, o := range snap.Objects {
		gs = append(gs, &g{o.Name, "", o.Leader, weight(o.Name), opts.Movable(o.Name), false})
		load[o.Leader] += weight(o.Name)
	}
	for _, c := range snap.Consumers {
		gs = append(gs, &g{c.Stream, c.Name, c.Leader, weight(c.Stream), opts.Movable(c.Stream), false})
		load[c.Leader] += weight(c.Stream)
	}
	sort.Slice(gs, func(i, j int) bool {
		if gs[i].stream != gs[j].stream {
			return gs[i].stream < gs[j].stream
		}
		return gs[i].consumer < gs[j].consumer
	})
	var moves []replication.Move
	for {
		best, bi, bto := 0, -1, ""
		for i, x := range gs {
			if !x.movable || x.done {
				continue
			}
			for _, c := range shapeServers {
				gap := load[x.leader] - load[c]
				if c == x.leader || gap <= x.w {
					continue
				}
				if sc := 2 * x.w * (gap - x.w); sc > best {
					best, bi, bto = sc, i, c
				}
			}
		}
		if bi < 0 {
			return moves
		}
		x := gs[bi]
		moves = append(moves, replication.Move{Stream: x.stream, Consumer: x.consumer, From: x.leader, To: bto})
		load[x.leader] -= x.w
		load[bto] += x.w
		x.leader, x.done = bto, true
	}
}

// (b) the previous design leaves the imbalance alert firing where the count plan clears it.
func TestControlTheWeightedSinglePoolLeavesTheAlertFiring(t *testing.T) {
	bad := 0
	for seed := 0; seed < sweepSeeds; seed++ {
		snap := randomPlacement(rand.New(rand.NewSource(int64(seed))))
		after := applyMoves(snap, weightedControlPlan(snap, shapeOptions(false)))
		if alertFires(after) && !alertFires(applyMoves(snap, replication.PlanRebalance(snap, shapeOptions(false)).Moves)) {
			bad++
		}
	}
	if bad == 0 {
		t.Fatal("control: the weighted planner never left the alert firing, so property (b) detects nothing")
	}
	t.Logf("weighted single-pool plan: alert firing where the count plan clears it in %d of %d placements", bad, sweepSeeds)
}

// (d) the detector flags a plan that moves an ingest-path stream.
func TestControlAMovedFixedGroupIsDetected(t *testing.T) {
	opts := shapeOptions(true)
	bad := []replication.Move{{Stream: messaging.StreamName(shapeInstance, streams.InboundEvents), From: "n0", To: "n1"}}
	if movedFixed(bad, opts) == "" {
		t.Fatal("control: moving inbound-events was not flagged")
	}
}

// (c) the detector flags a plan that is not a fixed point: the first half of a plan is not.
func TestControlAPartialPlanIsNotAFixedPoint(t *testing.T) {
	opts := shapeOptions(true)
	for seed := 0; seed < sweepSeeds; seed++ {
		snap := randomPlacement(rand.New(rand.NewSource(int64(seed))))
		plan := replication.PlanRebalance(snap, opts)
		if len(plan.Moves) < 2 {
			continue
		}
		half := applyMoves(snap, plan.Moves[:len(plan.Moves)/2])
		if len(replication.PlanRebalance(half, opts).Moves) != 0 {
			return
		}
	}
	t.Fatal("control: no half-applied plan ever planned further moves, so property (c) detects nothing")
}

// A separation move that failed or was skipped must not be reported as a separation.
func TestSummaryDoesNotClaimASeparationThatDidNotLand(t *testing.T) {
	snap := shapeSnapshot(func(string) string { return "n0" })
	plan := replication.PlanRebalance(snap, shapeOptions(true))
	var mv replication.Move
	for _, m := range plan.Moves {
		if m.Stream == messaging.StreamName(shapeInstance, streams.ResolvedEvents) && !m.IsConsumer() {
			mv = m
		}
	}
	if mv.Stream == "" {
		t.Fatal("fixture should plan a move of resolved-events")
	}
	if line := perEventLine(plan, []replication.MoveResult{{Move: mv}}); !strings.Contains(line, "(separated)") && !strings.Contains(line, "(SHARED)") {
		t.Errorf("a landed move leaves the plain state: %q", line)
	}
	for name, r := range map[string]replication.MoveResult{"failed": {Move: mv, Err: fmt.Errorf("boom")}, "skipped": {Move: mv, Skipped: true}} {
		if line := perEventLine(plan, []replication.MoveResult{r}); !strings.Contains(line, "(NOT CONFIRMED)") {
			t.Errorf("%s: %q", name, line)
		}
	}
}

// The settle gate reads the fixed per-event streams for their LEADER only: a leaderless one holds the
// plan back, a lagging or offline follower does not (a follower of capture is not the plan's
// business), and their consumers are not read. Negative control: the same snapshots, with the
// anchors read in full as the earlier gate did, are NOT settled.
func TestSettleGateReadsTheFixedPerEventLeadersOnly(t *testing.T) {
	opts := shapeOptions(true)
	capture := messaging.StreamName(shapeInstance, streams.DeviceEventsCapture)
	ok := replication.Peer{Name: "n1", Current: true}
	leaderless := replication.Object{Name: capture, Replicas: 3, Leader: "", Peers: []replication.Peer{ok}}
	if brokerSettled(replication.Snapshot{Objects: []replication.Object{leaderless}}, opts.Movable, opts.PerEvent) {
		t.Error("a leaderless anchor must hold the plan back")
	}
	for name, o := range map[string]replication.Object{
		"lagging follower": {Name: capture, Replicas: 3, Leader: "n0", Peers: []replication.Peer{{Name: "n1"}}},
	} {
		snap := replication.Snapshot{Objects: []replication.Object{o}}
		if !brokerSettled(snap, opts.Movable, opts.PerEvent) {
			t.Errorf("%s of a fixed stream must not hold the plan back", name)
		}
		// control: treated as a movable stream (read in full), the same snapshot is unsettled
		if brokerSettled(snap, func(s string) bool { return s == capture }, opts.PerEvent) {
			t.Errorf("control, %s: read in full the snapshot should be unsettled", name)
		}
	}
	// An offline follower of a fixed stream is a partial outage even when every movable group is
	// healthy (five servers, three replicas): the gate holds. Control: lagging only does not.
	offline := replication.Object{Name: capture, Replicas: 3, Leader: "n0", Peers: []replication.Peer{{Name: "n1", Current: true, Offline: true}}}
	if brokerSettled(replication.Snapshot{Objects: []replication.Object{offline}}, opts.Movable, opts.PerEvent) {
		t.Error("an offline follower of a fixed stream must hold the plan back")
	}
	lagConsumer := replication.Snapshot{Consumers: []replication.Consumer{{Stream: capture, Name: "c", Durable: true, Leader: "n0", Peers: []replication.Peer{{Name: "n1"}}}}}
	if !brokerSettled(lagConsumer, opts.Movable, opts.PerEvent) {
		t.Error("a fixed stream's consumers are not read by the plan")
	}
	// a MOVABLE stream is still read in full
	movableName := messaging.StreamName(shapeInstance, streams.ResolvedEvents)
	lagging := replication.Snapshot{Objects: []replication.Object{{Name: movableName, Replicas: 3, Leader: "n0", Peers: []replication.Peer{{Name: "n1"}}}}}
	if brokerSettled(lagging, opts.Movable, opts.PerEvent) {
		t.Error("a lagging replica of a movable stream must hold the plan back")
	}
}

// The fixed pair sharing a server is reported apart and does not make the movable stream's state
// SHARED.
func TestSummaryReportsAFixedPairApart(t *testing.T) {
	place := map[string]string{
		messaging.StreamName(shapeInstance, streams.DeviceEventsCapture): "n0",
		messaging.StreamName(shapeInstance, streams.InboundEvents):       "n0",
		messaging.StreamName(shapeInstance, streams.ResolvedEvents):      "n1",
	}
	snap := shapeSnapshot(func(name string) string {
		if l, ok := place[name]; ok {
			return l
		}
		return "n2"
	})
	line := perEventLine(replication.PlanRebalance(snap, shapeOptions(true)), nil)
	if !strings.Contains(line, "(separated)") || !strings.Contains(line, "device-events-capture/inbound-events share n0 (fixed, not moved)") {
		t.Errorf("summary line: %q", line)
	}
}

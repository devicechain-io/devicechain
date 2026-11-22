// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"fmt"
	"math/rand"
	"testing"
)

// No group is moved twice in one plan: every move is an election, and a consumer the per-event step
// has just placed must not be picked up again by the count balancing. Checked over many random
// placements of a broker with fixed and movable per-event streams, their consumers and cold groups.
func TestPlanRebalanceNeverMovesAGroupTwice(t *testing.T) {
	rng := rand.New(rand.NewSource(35))
	fixed := map[string]bool{"capture": true, "mqtt-msgs": true, "inbound": true}
	opts := PlanOptions{
		Movable:  func(s string) bool { return !fixed[s] },
		PerEvent: func(s string) bool { return fixed[s] || s == "resolved" },
	}
	pick := func() string { return three[rng.Intn(3)] }
	for round := 0; round < 3000; round++ {
		var snap Snapshot
		add := func(name string, consumers int) {
			l := pick()
			snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: l, Peers: peers(l, three...)})
			for c := 0; c < consumers; c++ {
				cl := pick()
				snap.Consumers = append(snap.Consumers, Consumer{Stream: name, Name: fmt.Sprint("c", c), Leader: cl, Peers: peers(cl, three...), Durable: true})
			}
		}
		add("capture", 1)
		add("mqtt-msgs", 0)
		add("inbound", 1)
		add("resolved", 1+rng.Intn(5))
		for i := 0; i < rng.Intn(12); i++ {
			add(fmt.Sprint("cold", i), rng.Intn(3))
		}
		plan := PlanRebalance(snap, opts)
		seen := map[string]bool{}
		for _, m := range plan.Moves {
			k := m.Stream + "/" + m.Consumer
			if seen[k] {
				t.Fatalf("round %d: %s moved twice: %+v", round, k, plan.Moves)
			}
			seen[k] = true
		}
	}
}

// occLayout is a layout the stream step leaves as it is: per-event occupancy n0=2 (capture and the
// MQTT store), n1=1 (inbound), n2=1 (resolved).
func occLayout() (Snapshot, PlanOptions) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	add("capture", "dc-nats-0")
	add("mqtt-msgs", "dc-nats-0")
	add("inbound", "dc-nats-1")
	add("resolved", "dc-nats-2")
	fixed := map[string]bool{"capture": true, "mqtt-msgs": true, "inbound": true}
	return snap, PlanOptions{
		Movable:  func(s string) bool { return !fixed[s] },
		PerEvent: func(s string) bool { return fixed[s] || s == "resolved" },
	}
}

// The per-event consumer step, and nothing else, moves a resolved consumer off a busier server: the
// consumer counts are already even, so the count balancing alone would not move it. Exactly once.
func TestThePerEventConsumerStepMovesAConsumerTheCountBalancingWouldNot(t *testing.T) {
	snap, opts := occLayout()
	for i, l := range three { // one consumer per server: even counts
		snap.Consumers = append(snap.Consumers, Consumer{Stream: "resolved", Name: fmt.Sprint("r", i), Leader: l, Peers: peers(l, three...), Durable: true})
	}
	plan := PlanRebalance(snap, opts)
	var moves []Move
	for _, m := range plan.Moves {
		if m.Consumer == "r0" {
			moves = append(moves, m)
		}
	}
	if len(moves) != 1 || moves[0].From != "dc-nats-0" {
		t.Fatalf("resolved/r0 on the busiest server should move exactly once, got %+v (all: %+v)", moves, plan.Moves)
	}
	if plain := PlanRebalance(snap, PlanOptions{Movable: opts.Movable}); len(plain.Moves) != 0 {
		t.Fatalf("control: the count balancing alone moved something: %+v", plain.Moves)
	}
}

// The most constrained consumers are placed first, so the flexible ones fill the other server: a and b
// may go to n1 or n2, c and d only to n1 (their n2 replica is behind). Placing a and b first in name
// order would put three on n1 and one on n2, and pinned, the count balancing could not fix it.
func TestThePerEventConsumerStepPlacesTheMostConstrainedFirst(t *testing.T) {
	snap, opts := occLayout()
	for _, c := range []string{"a", "b", "c", "d"} {
		ps := peers("dc-nats-0", three...)
		if c == "c" || c == "d" {
			for i := range ps {
				if ps[i].Name == "dc-nats-2" {
					ps[i].Current = false
				}
			}
		}
		snap.Consumers = append(snap.Consumers, Consumer{Stream: "resolved", Name: c, Leader: "dc-nats-0", Peers: ps, Durable: true})
	}
	plan := PlanRebalance(snap, opts)
	if got := plan.ConsumersAfter; got["dc-nats-1"] != 2 || got["dc-nats-2"] != 2 {
		t.Fatalf("want two consumers on each of n1 and n2, got %v (moves %+v)", got, plan.Moves)
	}
}

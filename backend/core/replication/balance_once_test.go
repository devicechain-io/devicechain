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

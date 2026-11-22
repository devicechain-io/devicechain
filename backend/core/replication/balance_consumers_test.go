// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"testing"
)

// r34's layout, by name: one server leads capture and the MQTT message store, one leads inbound, one
// leads nothing; resolved and its three readers sit on the hot server.
func r34Layout() (Snapshot, PlanOptions) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	addC := func(stream, name, leader string) {
		snap.Consumers = append(snap.Consumers, Consumer{Stream: stream, Name: name, Leader: leader, Peers: peers(leader, three...), Durable: true})
	}
	add("capture", "dc-nats-2")
	add("mqtt-msgs", "dc-nats-2")
	add("inbound", "dc-nats-1")
	add("resolved", "dc-nats-2")
	addC("resolved", "store", "dc-nats-2")
	addC("resolved", "state", "dc-nats-2")
	addC("resolved", "rules", "dc-nats-2")
	addC("capture", "reader", "dc-nats-2")
	fixed := map[string]bool{"capture": true, "mqtt-msgs": true, "inbound": true}
	return snap, PlanOptions{
		Movable:  func(s string) bool { return !fixed[s] },
		PerEvent: func(s string) bool { return fixed[s] || s == "resolved" },
	}
}

func leaderAfter(plan RebalancePlan, snap Snapshot, stream, consumer string) string {
	l := ""
	for _, c := range snap.Consumers {
		if c.Stream == stream && c.Name == consumer {
			l = c.Leader
		}
	}
	for _, m := range plan.Moves {
		if m.Stream == stream && m.Consumer == consumer {
			l = m.To
		}
	}
	return l
}

// The consumers of the movable per-event stream leave the server that leads the most per-event
// streams, for servers that lead fewer, spread by count; the fixed stream's consumer stays.
func TestPlanRebalanceMovesThePerEventConsumersOffTheHotServer(t *testing.T) {
	snap, opts := r34Layout()
	plan := PlanRebalance(snap, opts)
	if plan.PerEventLeaders["resolved"] != "dc-nats-0" {
		t.Fatalf("resolved should be separated to the free server: %v", plan.PerEventLeaders)
	}
	on := map[string]int{}
	for _, c := range []string{"store", "state", "rules"} {
		l := leaderAfter(plan, snap, "resolved", c)
		if l == "dc-nats-2" {
			t.Errorf("resolved/%s left on the server leading capture and the MQTT store", c)
		}
		on[l]++
	}
	if on["dc-nats-0"] == 3 || on["dc-nats-1"] == 3 {
		t.Errorf("the consumers piled onto one server instead of spreading: %v", on)
	}
	// Each consumer is placed once: the step spreads them itself rather than piling them on one server
	// and leaving the count balancing to move some of them a second time (an extra election each).
	seen := map[string]bool{}
	for _, m := range plan.Moves {
		k := m.Stream + "/" + m.Consumer
		if seen[k] {
			t.Errorf("%s moved twice: %+v", k, plan.Moves)
		}
		seen[k] = true
	}
	if l := leaderAfter(plan, snap, "capture", "reader"); l != "dc-nats-2" {
		t.Errorf("a fixed stream's consumer moved to %s", l)
	}
	// Running the plan again over its own result moves nothing: the placement is stable.
	again := applyPlan(snap, plan)
	if p2 := PlanRebalance(again, opts); len(p2.Moves) != 0 {
		t.Errorf("a second plan over the first one's result moved again: %+v", p2.Moves)
	}
}

// A consumer of the movable per-event stream is never moved onto a server leading MORE per-event streams
// than the one it is on, by either step: here resolved stays with capture on dc-nats-0 (no server leads
// none), so dc-nats-0 is the hottest server and the count balancing must spread the consumers elsewhere.
func TestPlanRebalanceNeverMovesAPerEventConsumerOntoAHotterServer(t *testing.T) {
	var snap Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, Object{Name: name, Replicas: 3, Leader: leader, Peers: peers(leader, three...)})
	}
	add("capture", "dc-nats-0")
	add("mqtt-msgs", "dc-nats-1")
	add("inbound", "dc-nats-2")
	add("resolved", "dc-nats-0")
	for _, c := range []string{"store", "state", "rules"} {
		snap.Consumers = append(snap.Consumers, Consumer{Stream: "resolved", Name: c, Leader: "dc-nats-1", Peers: peers("dc-nats-1", three...), Durable: true})
	}
	fixed := map[string]bool{"capture": true, "mqtt-msgs": true, "inbound": true}
	plan := PlanRebalance(snap, PlanOptions{
		Movable:  func(s string) bool { return !fixed[s] },
		PerEvent: func(s string) bool { return fixed[s] || s == "resolved" },
	})
	for _, m := range plan.Moves {
		if m.Stream == "resolved" && m.To == "dc-nats-0" {
			t.Errorf("moved %+v onto the server leading capture and resolved", m)
		}
	}
	// The count balancing still evens the three out over the two servers it may use.
	if plan.ConsumersAfter["dc-nats-1"] > 2 {
		t.Errorf("the consumers were left piled on dc-nats-1: %v", plan.ConsumersAfter)
	}
}

// The bound holds over the consumer step too.
func TestPlanRebalancePerEventConsumersHonourTheBound(t *testing.T) {
	snap, opts := r34Layout()
	opts.MaxMoves = 2
	if plan := PlanRebalance(snap, opts); len(plan.Moves) != 2 {
		t.Fatalf("want exactly 2 moves, got %+v", plan.Moves)
	}
}

func applyPlan(snap Snapshot, plan RebalancePlan) Snapshot {
	out := Snapshot{Objects: append([]Object(nil), snap.Objects...), Consumers: append([]Consumer(nil), snap.Consumers...)}
	for _, m := range plan.Moves {
		if m.IsConsumer() {
			for i := range out.Consumers {
				if out.Consumers[i].Stream == m.Stream && out.Consumers[i].Name == m.Consumer {
					out.Consumers[i].Leader, out.Consumers[i].Peers = m.To, peers(m.To, three...)
				}
			}
			continue
		}
		for i := range out.Objects {
			if out.Objects[i].Name == m.Stream {
				out.Objects[i].Leader, out.Objects[i].Peers = m.To, peers(m.To, three...)
			}
		}
	}
	return out
}

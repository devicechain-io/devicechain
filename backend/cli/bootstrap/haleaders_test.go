// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
)

// leadersSnap is the per-event shape with resolved's readers, each placed by pick.
func leadersSnap(capture, inbound, mqtt, resolved string, consumers ...string) replication.Snapshot {
	snap := mqttLayout(capture, inbound, mqtt, resolved)
	res := messaging.StreamName(shapeInstance, streams.ResolvedEvents)
	for i, l := range consumers {
		snap.Consumers = append(snap.Consumers, replication.Consumer{Stream: res, Name: []string{"store", "state", "rules"}[i], Durable: true, Leader: l, Peers: shapePeers(l)})
	}
	// A cold stream, so the counts are not only per-event ones.
	cold := messaging.StreamName(shapeInstance, streams.AlarmEvents)
	snap.Objects = append(snap.Objects, replication.Object{Name: cold, Replicas: 3, Leader: "n1", Peers: shapePeers("n1")})
	return snap
}

// r34's layout: capture, the MQTT store and resolved (and its readers) on one server, another server
// leading nothing. The check FAILS it and names the moves the rebalance would make.
func TestHaLeadersFailsTheR34Layout(t *testing.T) {
	rep := buildLeadersReport(leadersSnap("n2", "n1", "n2", "n2", "n2", "n2", "n2"), shapeInstance)
	if rep.Verdict != LeadersFail {
		t.Fatalf("verdict %s, want FAIL:\n%s", rep.Verdict, rep.Format())
	}
	if len(rep.Pending) == 0 {
		t.Fatal("a FAIL must name the moves that would fix it")
	}
	out := rep.Format()
	for _, want := range []string{"FAIL", "dcctl broker rebalance", "device-events-capture=n2", "$MQTT_msgs=n2", "resolved-events=n2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The same broker after the rebalance's own plan PASSES: the check holds a broker to exactly the
// placement the rebalance makes, no more.
func TestHaLeadersPassesWhatTheRebalanceLeaves(t *testing.T) {
	snap := leadersSnap("n2", "n1", "n2", "n2", "n2", "n2", "n2")
	plan := replication.PlanRebalance(snap, rebalancePlanOptions(shapeInstance, 0))
	after := applyMoves(snap, plan.Moves)
	rep := buildLeadersReport(after, shapeInstance)
	if rep.Verdict != LeadersPass {
		t.Fatalf("verdict %s after the rebalance's plan, want PASS:\n%s", rep.Verdict, rep.Format())
	}
	// The fixed pair still shares a server: reported, not failed.
	if !strings.Contains(rep.Format(), "fixed") {
		t.Errorf("the fixed pair sharing n2 is not reported:\n%s", rep.Format())
	}
}

// One per-event stream per server and resolved with one of them is unavoidable sharing: PASS. A count
// imbalance among resolved's readers is the count balancing's business, not this check's.
func TestHaLeadersPassesUnavoidableSharing(t *testing.T) {
	// resolved's readers all on n1: the count balancing would move one to n2, which leads as many
	// per-event streams as n1. That is a count move, not a separation, so the check still PASSES.
	snap := leadersSnap("n0", "n1", "n2", "n0", "n1", "n1", "n1")
	if plan := replication.PlanRebalance(snap, rebalancePlanOptions(shapeInstance, 0)); len(plan.Moves) == 0 {
		t.Fatal("fixture: the count balancing was expected to move a reader between n1 and n2")
	}
	rep := buildLeadersReport(snap, shapeInstance)
	if rep.Verdict != LeadersPass {
		t.Fatalf("verdict %s, want PASS:\n%s", rep.Verdict, rep.Format())
	}
}

// A lagging or offline replica is UNSETTLED, never PASS: it hides the candidates a move would use, so
// "nothing to move" would be a reading of the lag, not of the placement.
func TestHaLeadersIsUnsettledWhileAReplicaIsBehindOrALeaderIsMissing(t *testing.T) {
	snap := leadersSnap("n0", "n1", "n2", "n0", "n1", "n1", "n2")
	for i := range snap.Objects {
		if snap.Objects[i].Name == messaging.StreamName(shapeInstance, streams.ResolvedEvents) {
			snap.Objects[i].Peers[0].Current = false
		}
	}
	if rep := buildLeadersReport(snap, shapeInstance); rep.Verdict != LeadersUnsettled {
		t.Fatalf("a lagging replica of resolved: verdict %s, want UNSETTLED:\n%s", rep.Verdict, rep.Format())
	}
	snap = leadersSnap("n0", "n1", "n2", "n0")
	snap.Objects[0].Leader = "" // capture mid-election
	if rep := buildLeadersReport(snap, shapeInstance); rep.Verdict != LeadersUnsettled {
		t.Fatalf("a leaderless capture: verdict %s, want UNSETTLED:\n%s", rep.Verdict, rep.Format())
	}
}

// An instance no MQTT client has reached has no message store yet. That is said, on its own line, and is
// not a failure; the separation is judged over what exists.
func TestHaLeadersSaysTheMqttStoreIsNotCreatedYet(t *testing.T) {
	snap := leadersSnap("n0", "n1", "n2", "n2")
	var kept []replication.Object
	for _, o := range snap.Objects {
		if o.Name != messaging.MqttMessageStore {
			kept = append(kept, o)
		}
	}
	snap.Objects = kept
	rep := buildLeadersReport(snap, shapeInstance)
	if rep.Verdict != LeadersPass {
		t.Fatalf("verdict %s, want PASS:\n%s", rep.Verdict, rep.Format())
	}
	if !strings.Contains(rep.Format(), "no MQTT client has connected") {
		t.Errorf("the missing store is not explained:\n%s", rep.Format())
	}
}

// A broker holding none of the per-event streams gives the check nothing to judge: never a PASS.
func TestHaLeadersRefusesToPassOverNothing(t *testing.T) {
	snap := replication.Snapshot{Objects: []replication.Object{{Name: "unrelated", Replicas: 3, Leader: "n0", Peers: shapePeers("n0")}}}
	if rep := buildLeadersReport(snap, shapeInstance); rep.Verdict == LeadersPass {
		t.Fatalf("passed a broker with no per-event stream:\n%s", rep.Format())
	}
}

// The JSON is what a harness reads: the verdict and every per-event leader, by short name.
func TestHaLeadersJSONCarriesTheLeaders(t *testing.T) {
	rep := buildLeadersReport(leadersSnap("n2", "n1", "n2", "n2", "n2"), shapeInstance)
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back["verdict"] != string(LeadersFail) {
		t.Errorf("verdict %v", back["verdict"])
	}
	leaders, _ := back["perEventLeaders"].(map[string]any)
	if leaders["device-events-capture"] != "n2" || leaders["$MQTT_msgs"] != "n2" || leaders["inbound-events"] != "n1" {
		t.Errorf("perEventLeaders %v", leaders)
	}
}

// Each of the instance's own per-event streams is required: a broker missing one is UNSETTLED, never a
// PASS over the ones that remain. Only the MQTT store may be absent (no MQTT client yet).
func TestHaLeadersIsUnsettledWhenAnInstanceStreamIsMissing(t *testing.T) {
	for _, suffix := range []string{streams.DeviceEventsCapture, streams.InboundEvents, streams.ResolvedEvents} {
		snap := leadersSnap("n0", "n1", "n2", "n2")
		name := messaging.StreamName(shapeInstance, suffix)
		var kept []replication.Object
		for _, o := range snap.Objects {
			if o.Name != name {
				kept = append(kept, o)
			}
		}
		snap.Objects = kept
		if rep := buildLeadersReport(snap, shapeInstance); rep.Verdict != LeadersUnsettled {
			t.Errorf("without %s: verdict %s, want UNSETTLED:\n%s", suffix, rep.Verdict, rep.Format())
		}
	}
}

// The check never recommends a rebalance the rebalance would refuse to plan: a lagging replica of a
// cold movable stream holds every move back (brokerSettled), so the answer is UNSETTLED, not FAIL.
func TestHaLeadersIsUnsettledWhenTheRebalanceWouldWait(t *testing.T) {
	snap := leadersSnap("n2", "n1", "n2", "n2", "n2", "n2", "n2") // r34: a FAIL when settled
	for i := range snap.Objects {
		if snap.Objects[i].Name == messaging.StreamName(shapeInstance, streams.AlarmEvents) {
			snap.Objects[i].Peers[0].Current = false
		}
	}
	if rep := buildLeadersReport(snap, shapeInstance); rep.Verdict != LeadersUnsettled {
		t.Fatalf("verdict %s, want UNSETTLED:\n%s", rep.Verdict, rep.Format())
	}
}

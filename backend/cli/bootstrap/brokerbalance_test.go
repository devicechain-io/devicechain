// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
	"k8s.io/client-go/kubernetes"
)

func withRebalance(t *testing.T, f func(context.Context, *State, *kubernetes.Clientset) (string, error)) {
	t.Helper()
	old := brokerRebalance
	brokerRebalance = f
	t.Cleanup(func() { brokerRebalance = old })
}

// A rebalance that fails is a warning: runBrokerRebalance returns nothing, so there is no error
// to propagate into the upgrade, and what it prints says the upgrade is complete.
func TestAFailedRebalanceWarnsAndNeverFailsTheUpgrade(t *testing.T) {
	withRebalance(t, func(context.Context, *State, *kubernetes.Clientset) (string, error) {
		return "", errors.New("connecting to the broker: refused")
	})
	out := captureStdout(t, func() { runBrokerRebalance(context.Background(), &State{Instance: "x"}, nil) })
	for _, want := range []string{"Warning", "connecting to the broker: refused", "upgrade is complete", "dcctl broker rebalance"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAPartialRebalanceSaysWhatLanded(t *testing.T) {
	withRebalance(t, func(context.Context, *State, *kubernetes.Clientset) (string, error) {
		return "5 of 6 leader moves landed", errors.New("1 move(s) failed: s to n1: nope")
	})
	out := captureStdout(t, func() { runBrokerRebalance(context.Background(), &State{Instance: "x"}, nil) })
	if !strings.Contains(out, "5 of 6") || !strings.Contains(out, "1 move(s) failed") {
		t.Errorf("summary or failure missing:\n%s", out)
	}
}

func TestTheRebalanceSummaryShowsBeforeAndAfter(t *testing.T) {
	plan := replication.PlanRebalance(replication.Snapshot{Objects: []replication.Object{
		{Name: "a", Replicas: 3, Leader: "n1", Peers: []replication.Peer{{Name: "n0", Current: true}, {Name: "n2", Current: true}}},
		{Name: "b", Replicas: 3, Leader: "n1", Peers: []replication.Peer{{Name: "n0", Current: true}, {Name: "n2", Current: true}}},
		{Name: "c", Replicas: 3, Leader: "n1", Peers: []replication.Peer{{Name: "n0", Current: true}, {Name: "n2", Current: true}}},
	}}, replication.PlanOptions{Movable: func(string) bool { return true }})
	results := make([]replication.MoveResult, len(plan.Moves))
	for i, m := range plan.Moves {
		results[i] = replication.MoveResult{Move: m}
	}
	got := summarizeRebalance(plan, results)
	for _, want := range []string{"2 of 2 leader moves landed", "streams led 0/3/0", "streams led 1/1/1"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
	if err := rebalanceFailures(results); err != nil {
		t.Errorf("no failures expected: %v", err)
	}
	results[0].Err = errors.New("boom")
	if err := rebalanceFailures(results); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failed move must be reported: %v", err)
	}
}

func TestAnEvenBrokerIsLeftAlone(t *testing.T) {
	plan := replication.PlanRebalance(replication.Snapshot{}, replication.PlanOptions{})
	if got := summarizeRebalance(plan, nil); !strings.Contains(got, "already even") {
		t.Errorf("got %q", got)
	}
}

func TestAnUnsettledBrokerIsNotPlannedOver(t *testing.T) {
	all := func(string) bool { return true }
	lagging := replication.Snapshot{Objects: []replication.Object{{Name: "a", Replicas: 3, Leader: "n1",
		Peers: []replication.Peer{{Name: "n2", Current: false}}}}}
	if brokerSettled(lagging, all) {
		t.Error("a replica that is not current must hold the plan back")
	}
	leaderless := replication.Snapshot{Objects: []replication.Object{{Name: "a", Replicas: 3}}}
	if brokerSettled(leaderless, all) {
		t.Error("a replicated group with no leader must hold the plan back")
	}
	// A consumer's replicas count as much as a stream's.
	laggingConsumer := replication.Snapshot{Consumers: []replication.Consumer{{Stream: "a", Name: "c", Durable: true, Leader: "n1",
		Peers: []replication.Peer{{Name: "n2", Current: false}}}}}
	if brokerSettled(laggingConsumer, all) {
		t.Error("a consumer replica that is not current must hold the plan back")
	}
	// What is not movable is not waited on: an ingest stream mid-election does not hold the rest.
	if !brokerSettled(lagging, func(string) bool { return false }) {
		t.Error("a group that is never moved should not hold the plan back")
	}
	if !brokerSettled(replication.Snapshot{}, all) {
		t.Error("an empty snapshot has nothing to wait for")
	}
}

func TestBrokerRolledOnlyWhenThePodsWereReplaced(t *testing.T) {
	same := map[string]string{"dc-nats-0": "a", "dc-nats-1": "b", "dc-nats-2": "c"}
	if brokerRolled(same, map[string]string{"dc-nats-0": "a", "dc-nats-1": "b", "dc-nats-2": "c"}) {
		t.Error("pods that did not change were read as a roll")
	}
	if !brokerRolled(same, map[string]string{"dc-nats-0": "a", "dc-nats-1": "b", "dc-nats-2": "NEW"}) {
		t.Error("a replaced pod was not read as a roll")
	}
	if !brokerRolled(same, map[string]string{"dc-nats-0": "a"}) {
		t.Error("a changed pod count was not read as a roll")
	}
	if !brokerRolled(nil, same) {
		t.Error("an unreadable reading must not skip the step")
	}
}

// 🔴 THE BLOCKER'S TEST. No stream on the acknowledged-ingest path may be named movable, whatever
// else changes: the broker's MQTT streams, the device capture stream and everything an ingest area
// writes. A leader change on them has lost messages already PUBACKed to devices.
func TestNoIngestStreamIsEverMovable(t *testing.T) {
	movable := map[string]bool{}
	for _, n := range messaging.RebalanceableStreams("inst") {
		movable[n] = true
	}
	if len(movable) == 0 {
		t.Fatal("nothing is movable: the test would pass over nothing")
	}
	for _, n := range []string{
		messaging.MqttSessionStore, "$MQTT_msgs", "$MQTT_rmsgs", "$MQTT_out", "$MQTT_sess",
		messaging.StreamName("inst", streams.DeviceEventsCapture),
		messaging.StreamName("inst", streams.InboundEvents),
		messaging.StreamName("inst", streams.DeviceReports),
		messaging.StreamName("inst", streams.CommandResponses),
		messaging.StreamName("inst", streams.FailedDecode),
		"KV_inst_dc_leases", "some-undeclared-stream",
	} {
		if movable[n] {
			t.Errorf("%s is movable", n)
		}
	}
	for _, st := range streams.All {
		if st.OnIngestPath() && movable[messaging.StreamName("inst", st.Suffix)] {
			t.Errorf("%s is on the ingest path and movable", st.Suffix)
		}
	}
	// And the other side: the streams that carry no acknowledged ingest are moved.
	for _, suffix := range []string{streams.ResolvedEvents, streams.DerivedEvents, streams.AlarmEvents} {
		if !movable[messaging.StreamName("inst", suffix)] {
			t.Errorf("%s should be movable", suffix)
		}
	}
	// A plan over a snapshot holding an ingest stream led by one server never touches it.
	peers := []replication.Peer{{Name: "n1", Current: true}, {Name: "n2", Current: true}}
	var snap replication.Snapshot
	for _, n := range []string{"$MQTT_msgs", "$MQTT_rmsgs", "inst_device-events-capture", "inst_inbound-events"} {
		snap.Objects = append(snap.Objects, replication.Object{Name: n, Replicas: 3, Leader: "n0", Peers: peers})
		snap.Consumers = append(snap.Consumers, replication.Consumer{Stream: n, Name: "c", Durable: true, Leader: "n0", Peers: peers})
	}
	plan := replication.PlanRebalance(snap, replication.PlanOptions{Movable: func(n string) bool { return movable[n] }})
	if len(plan.Moves) != 0 {
		t.Fatalf("a plan moved ingest groups: %+v", plan.Moves)
	}
}

// 🔴 TESTING THE STEP IS NOT TESTING THAT THE UPGRADE RUNS IT, and the upgrade needs a cluster.
// Read the source: Upgrade must call runBrokerRebalance, after upgradeRollout (the services have
// rolled) and not as the operand of a return (which would let its outcome fail the upgrade).
func TestTheUpgradeRebalancesTheBrokerAfterTheServicesRoll(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "upgrade.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var rollout, rebalance token.Pos
	var asExpr bool
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "Upgrade" {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok {
					switch id.Name {
					case "upgradeRollout":
						rollout = x.Pos()
					case "runBrokerRebalance":
						rebalance = x.Pos()
					}
				}
			case *ast.ReturnStmt:
				for _, r := range x.Results {
					if c, ok := r.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "runBrokerRebalance" {
							asExpr = true
						}
					}
				}
			}
			return true
		})
	}
	if !rebalance.IsValid() {
		t.Fatal("Upgrade never calls runBrokerRebalance")
	}
	if !rollout.IsValid() || rebalance < rollout {
		t.Error("the rebalance must come after the services' rollout")
	}
	if asExpr {
		t.Error("the rebalance's outcome must not be returned from Upgrade")
	}
}

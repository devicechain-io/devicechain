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

	"github.com/devicechain-io/dc-microservice/replication"
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
	}}, 0)
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
	plan := replication.PlanRebalance(replication.Snapshot{}, 0)
	if got := summarizeRebalance(plan, nil); !strings.Contains(got, "already even") {
		t.Errorf("got %q", got)
	}
}

func TestAnUnsettledBrokerIsNotPlannedOver(t *testing.T) {
	lagging := replication.Snapshot{Objects: []replication.Object{{Name: "a", Replicas: 3, Leader: "n1",
		Peers: []replication.Peer{{Name: "n2", Current: false}}}}}
	if brokerSettled(lagging) {
		t.Error("a replica that is not current must hold the plan back")
	}
	leaderless := replication.Snapshot{Objects: []replication.Object{{Name: "a", Replicas: 3}}}
	if brokerSettled(leaderless) {
		t.Error("a replicated group with no leader must hold the plan back")
	}
	if !brokerSettled(replication.Snapshot{}) {
		t.Error("an empty snapshot has nothing to wait for")
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

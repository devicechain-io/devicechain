// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"
)

// Evening out broker leadership after the broker rolls.
//
// 🔴 WHY. A rolling restart hands every group the departing server led to the others, and a RAFT
// group never gives leadership back on its own. After any roll that touches all the servers the one
// restarted last leads nothing and the other two lead everything: measured on a three-server
// broker, that doubled the publish-acknowledgement p99 (25 to 50-60 ms) and cost about 3.5% of
// HTTP ingest at the same offered load. Every `dcctl upgrade` that rolls the broker does it.
//
// 🔴 BEST EFFORT, AND IT SAYS SO. An uneven broker is slower, not broken, so a failure here is a
// warning and never fails the upgrade: the services are already rolled and healthy, and the
// operator can run the step again by upgrading (or ignore it). The whole step is bounded by
// brokerRebalanceTimeout and brokerRebalanceMaxMoves.
const (
	brokerRebalanceTimeout  = 3 * time.Minute
	brokerRebalanceMaxMoves = 512
	// brokerRebalanceSettle is how long to wait for every replica to be current and every group to
	// have a leader before planning: a plan made over a broker still catching up would move
	// leadership onto a replica that cannot take it, or skip a group that is only briefly leaderless.
	brokerRebalanceSettle  = 60 * time.Second
	brokerRebalancePerMove = 10 * time.Second
)

// brokerRebalance is the seam the upgrade calls, replaced in tests.
var brokerRebalance = rebalanceBrokerLeadership

// rebalanceBrokerLeadership connects to the instance's broker and evens out which server leads
// what. It returns a one-line summary, and an error only for something the operator should be
// told about; the caller prints it as a warning.
func rebalanceBrokerLeadership(ctx context.Context, st *State, typed *kubernetes.Clientset) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, brokerRebalanceTimeout)
	defer cancel()

	restCfg, err := RestConfig(st.KubeContext)
	if err != nil {
		return "", fmt.Errorf("building kube config: %w", err)
	}
	cfg, err := deployedInstanceConfig(ctx, typed, st.Instance)
	if err != nil {
		return "", err
	}
	ns := InstanceNamespace(st.Instance)
	pods, err := natsPods(ctx, typed, ns)
	if err != nil {
		return "", err
	}
	if len(pods) < 2 {
		return "single broker server; nothing to rebalance", nil
	}
	local, stop, err := forwardPort(restCfg, ns, pods[0].Name, int(cfg.Infrastructure.Nats.Port))
	if err != nil {
		return "", fmt.Errorf("opening a port-forward to broker pod %s: %w", pods[0].Name, err)
	}
	defer stop()
	nc, err := dialBroker(fmt.Sprintf("nats://127.0.0.1:%d", local), cfg.Infrastructure.Nats)
	if err != nil {
		return "", err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return "", err
	}
	areas, err := deployedAreas(ctx, typed, st.Instance)
	if err != nil {
		return "", err
	}
	exp := messaging.ReplicationExpectationFor(st.Instance, int(cfg.Infrastructure.Nats.StreamReplicas), areas,
		cfg.Infrastructure.Nats.Auth.IdentityAccount)

	// Settle: every group led, every replica current. Bounded; a broker that does not settle is
	// planned over as it is, because the plan only moves onto replicas that report current.
	var snap replication.Snapshot
	deadline := time.Now().Add(brokerRebalanceSettle)
	for {
		snap, err = replication.Collect(js, exp)
		if err == nil && brokerSettled(snap) {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err != nil {
				return "", fmt.Errorf("reading the broker's leadership: %w", err)
			}
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}

	plan := replication.PlanRebalance(snap, brokerRebalanceMaxMoves)
	if len(plan.Moves) == 0 {
		return summarizeRebalance(plan, nil), nil
	}
	results := replication.ApplyMoves(ctx, nc, plan.Moves, brokerRebalancePerMove)
	return summarizeRebalance(plan, results), rebalanceFailures(results)
}

// brokerSettled is whether every replicated group has a leader and all its replicas are current.
func brokerSettled(snap replication.Snapshot) bool {
	for _, o := range snap.Objects {
		if o.Replicas > 1 && o.Leader == "" {
			return false
		}
		for _, p := range o.Peers {
			if !p.Current || p.Offline {
				return false
			}
		}
	}
	return true
}

// rebalanceFailures folds the failed moves into one error, or nil when none failed.
func rebalanceFailures(results []replication.MoveResult) error {
	var msgs []string
	for _, r := range results {
		if r.Err == nil {
			continue
		}
		what := r.Move.Stream
		if r.Move.IsConsumer() {
			what += "/" + r.Move.Consumer
		}
		msgs = append(msgs, fmt.Sprintf("%s to %s: %v", what, r.Move.To, r.Err))
	}
	if len(msgs) == 0 {
		return nil
	}
	if len(msgs) > 3 {
		msgs = append(msgs[:3], fmt.Sprintf("and %d more", len(msgs)-3))
	}
	return fmt.Errorf("%d move(s) failed: %s", countFailed(results), strings.Join(msgs, "; "))
}

func countFailed(results []replication.MoveResult) int {
	n := 0
	for _, r := range results {
		if r.Err != nil {
			n++
		}
	}
	return n
}

// summarizeRebalance is the one line the operator reads: streams and consumers led per server
// before and after, and how many moves landed.
func summarizeRebalance(plan replication.RebalancePlan, results []replication.MoveResult) string {
	if len(plan.Moves) == 0 {
		return "leadership already even (" + spreadLine("streams", plan.StreamsBefore, plan.Servers) + ")"
	}
	landed := len(results) - countFailed(results)
	return fmt.Sprintf("%d of %d leader moves landed; %s -> %s; %s -> %s", landed, len(plan.Moves),
		spreadLine("streams", plan.StreamsBefore, plan.Servers), spreadLine("streams", plan.StreamsAfter, plan.Servers),
		spreadLine("consumers", plan.ConsumersBefore, plan.Servers), spreadLine("consumers", plan.ConsumersAfter, plan.Servers))
}

func spreadLine(kind string, sp replication.Spread, servers []string) string {
	names := append([]string(nil), servers...)
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, s := range names {
		parts = append(parts, fmt.Sprint(sp[s]))
	}
	return kind + " led " + strings.Join(parts, "/")
}

// runBrokerRebalance is the upgrade's step: it runs the rebalance, prints its summary and turns
// every failure into a warning. It never returns an error.
func runBrokerRebalance(ctx context.Context, st *State, typed *kubernetes.Clientset) {
	fmt.Printf("%s", color.WhiteString("Evening out broker leadership... "))
	summary, err := brokerRebalance(ctx, st, typed)
	switch {
	case err != nil && summary == "":
		fmt.Println(color.YellowString("skipped."))
		fmt.Println(color.YellowString("  Warning: could not even out broker leadership: %v", err))
		fmt.Println(color.YellowString("  The upgrade is complete. Until leadership is even, publish latency is higher than it need be; " +
			"`dcctl broker rebalance` repeats this step."))
	case err != nil:
		fmt.Println(color.YellowString("partly done."))
		fmt.Println(color.YellowString("  Warning: %s", summary))
		fmt.Println(color.YellowString("  %v", err))
	default:
		fmt.Println(color.GreenString("done (%s).", summary))
	}
}

// RebalanceBrokerOptions names the instance whose broker leadership is evened out.
type RebalanceBrokerOptions struct {
	Options
}

// RebalanceBroker evens out which broker server leads what, for an installed instance. Unlike the
// step inside an upgrade it reports a failure as an error, because here it is the whole job.
func RebalanceBroker(ctx context.Context, opts RebalanceBrokerOptions) error {
	binding, source := ResolveBinding(opts.Options)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}
	if err := RefuseConflictingContext(source, binding, opts.KubeContext, opts.Instance); err != nil {
		return err
	}
	announceBinding(binding, source, opts.Instance)
	_, _, typed, err := kubeClients(binding.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}
	st := &State{Instance: opts.Instance, KubeContext: binding.KubeContext}
	summary, err := brokerRebalance(ctx, st, typed)
	if summary != "" {
		fmt.Println(summary)
	}
	return err
}

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
	local, stop, err := forwardPortCtx(ctx, restCfg, ns, pods[0].Name, int(cfg.Infrastructure.Nats.Port))
	if err != nil {
		return "", fmt.Errorf("opening a port-forward to broker pod %s: %w", pods[0].Name, err)
	}
	defer stop()
	nc, err := dialBrokerCtx(ctx, fmt.Sprintf("nats://127.0.0.1:%d", local), cfg.Infrastructure.Nats)
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

	// 🔴 ONLY STREAMS OFF THE ACKNOWLEDGED-INGEST PATH ARE EVER MOVED. A leader change on the broker's
	// MQTT streams or the capture stream has lost messages already PUBACKed to devices (59,964 in one
	// capture step-down on GKE), and a latency imbalance is never worth that. The set is a positive
	// list derived from the stream declarations (messaging.RebalanceableStreams); there is no flag
	// that widens it. The imbalance on the ingest streams clears at the next roll, or on its own.
	movable := map[string]bool{}
	for _, n := range messaging.RebalanceableStreams(st.Instance) {
		movable[n] = true
	}
	isMovable := func(stream string) bool { return movable[stream] }

	// Settle: every movable group led, every replica of it current. Bounded, and when it does not
	// settle nothing is planned: a plan over a broker still catching up would move leadership onto
	// a replica that cannot take it.
	var snap replication.Snapshot
	deadline := time.Now().Add(brokerRebalanceSettle)
	for {
		snap, err = replication.Collect(js, exp)
		if err == nil && brokerSettled(snap, isMovable) {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err != nil {
				return "", fmt.Errorf("reading the broker's leadership: %w", err)
			}
			return "", fmt.Errorf("the broker had not settled after %s (a group without a leader, or a replica not current), so no "+
				"leadership was moved; run `dcctl broker rebalance %s` once it has", brokerRebalanceSettle, st.Instance)
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}

	plan := replication.PlanRebalance(snap, replication.PlanOptions{Movable: isMovable, MaxMoves: brokerRebalanceMaxMoves})
	if len(plan.Moves) == 0 {
		return summarizeRebalance(plan, nil), nil
	}
	results := replication.ApplyMoves(ctx, nc, js, plan.Moves, replication.ApplyOptions{PerMove: brokerRebalancePerMove})
	return summarizeRebalance(plan, results), rebalanceFailures(results)
}

// brokerSettled is whether every movable group has a leader and all its replicas are current,
// consumers' included.
func brokerSettled(snap replication.Snapshot, movable func(string) bool) bool {
	for _, o := range snap.Objects {
		if !movable(o.Name) {
			continue
		}
		if o.Replicas > 1 && o.Leader == "" {
			return false
		}
		for _, p := range o.Peers {
			if !p.Current || p.Offline {
				return false
			}
		}
	}
	for _, c := range snap.Consumers {
		if !movable(c.Stream) || !c.Durable {
			continue
		}
		if len(c.Peers) > 0 && c.Leader == "" {
			return false
		}
		for _, p := range c.Peers {
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
	landed, skipped := 0, 0
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Err == nil:
			landed++
		}
	}
	extra := ""
	if skipped > 0 {
		extra = fmt.Sprintf(" (%d no longer needed)", skipped)
	}
	return fmt.Sprintf("%d of %d leader moves landed%s; %s -> %s; %s -> %s", landed, len(plan.Moves), extra,
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

// brokerPodUIDs maps each broker pod's name to its UID. A roll replaces the pods, so a pod whose UID
// differs afterwards was restarted. nil when the pods could not be listed.
func brokerPodUIDs(ctx context.Context, typed *kubernetes.Clientset, namespace string) map[string]string {
	pods, err := natsPods(ctx, typed, namespace)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(pods))
	for _, p := range pods {
		out[p.Name] = string(p.UID)
	}
	return out
}

// brokerRolled is whether the broker's pods differ between two readings. An unreadable "before"
// (nil) counts as rolled: not knowing is no reason to skip a step that is harmless on a broker that
// is already even.
func brokerRolled(before, after map[string]string) bool {
	if before == nil || after == nil {
		return true
	}
	if len(before) != len(after) {
		return true
	}
	for name, uid := range after {
		if before[name] != uid {
			return true
		}
	}
	return false
}

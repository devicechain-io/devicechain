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
	nats "github.com/nats-io/nats.go"
	"k8s.io/client-go/kubernetes"
)

// Where the broker's hot leaders sit, judged by the rules the rebalance moves them by.
//
// 🔴 WHY. Every device event is a replicated write on the capture stream, the inbound stream, the
// resolved stream and (over MQTT) the broker's MQTT message store, and each stream's LEADER does the
// replication fan-out. On GKE (r34) one server led capture, the MQTT store and resolved, and ran at
// 3.5-3.7 cores of a 4-vCPU node while another led nothing: the knee of the whole platform was that one
// server. Nothing in the deployment shows this, so a performance round, a drill or an operator who wants
// to know has to read it from the broker.
//
// 🔴 THE VERDICT IS THE REBALANCE'S OWN DRY RUN, and nothing stricter. FAIL means `dcctl broker
// rebalance` would move a per-event stream or one of its consumers to a server leading fewer per-event
// streams; PASS means it would not, which is "as separated as the safe rules allow", not "fast enough".
// The capture stream, the inbound stream and the MQTT store are never moved (see rebalancePlanOptions),
// so when two of them share a server that is reported and is not a failure.

// LeadersVerdict is the check's answer.
type LeadersVerdict string

const (
	// LeadersPass: no move the rebalance may make would separate the per-event leaders further.
	LeadersPass LeadersVerdict = "PASS"
	// LeadersFail: the rebalance would move a per-event stream or consumer to a less occupied server.
	LeadersFail LeadersVerdict = "FAIL"
	// LeadersUnsettled: a per-event group has no leader, or a replica is offline or behind, so which
	// moves are possible cannot be read. Never a PASS.
	LeadersUnsettled LeadersVerdict = "UNSETTLED"
)

// ServerLeadership is what one broker server leads.
type ServerLeadership struct {
	Server            string   `json:"server"`
	Streams           int      `json:"streams"`
	Consumers         int      `json:"consumers"`
	PerEvent          []string `json:"perEvent"`
	PerEventConsumers []string `json:"perEventConsumers"`
}

// LeadersReport is the check's result.
type LeadersReport struct {
	Instance string         `json:"instance"`
	Verdict  LeadersVerdict `json:"verdict"`
	// PerEventLeaders maps each per-event stream present, by short name, to the server leading it.
	PerEventLeaders map[string]string `json:"perEventLeaders"`
	// FixedShared lists, per server, the never-moved per-event streams it leads when it leads two or more.
	FixedShared map[string][]string `json:"fixedShared,omitempty"`
	Servers     []ServerLeadership  `json:"servers"`
	// Absent lists the per-event streams the broker does not hold.
	Absent []string `json:"absent,omitempty"`
	// Pending are the moves that make this a FAIL.
	Pending   []replication.Move `json:"pending,omitempty"`
	Unsettled []string           `json:"unsettled,omitempty"`
}

// OK reports a PASS.
func (r LeadersReport) OK() bool { return r.Verdict == LeadersPass }

// shortStream drops the instance prefix from a stream name; the broker's own streams have none.
func shortStream(instance, name string) string {
	return strings.TrimPrefix(name, messaging.StreamName(instance, ""))
}

// buildLeadersReport judges one snapshot. Pure: the cluster plumbing is InspectLeadership.
func buildLeadersReport(snap replication.Snapshot, instance string) LeadersReport {
	opts := rebalancePlanOptions(instance, 0) // the dry run is never cut short by the move budget
	rep := LeadersReport{Instance: instance, PerEventLeaders: map[string]string{}}
	byServer := map[string]*ServerLeadership{}
	server := func(name string) *ServerLeadership {
		if byServer[name] == nil {
			byServer[name] = &ServerLeadership{Server: name, PerEvent: []string{}, PerEventConsumers: []string{}}
		}
		return byServer[name]
	}
	objects := map[string]replication.Object{}
	for _, o := range snap.Objects {
		objects[o.Name] = o
		for _, p := range o.Peers {
			server(p.Name)
		}
		if o.Leader != "" {
			server(o.Leader).Streams++
		}
	}
	for _, c := range snap.Consumers {
		if c.Durable && c.Leader != "" {
			server(c.Leader).Consumers++
		}
	}

	fixedOn := map[string][]string{}
	for _, name := range messaging.PerEventLeaderStreams(instance) {
		short := shortStream(instance, name)
		o, ok := objects[name]
		if !ok {
			rep.Absent = append(rep.Absent, short)
			if name != messaging.MqttMessageStore {
				// The instance's own per-event streams exist from install on. One missing is a broker
				// that cannot be judged, never a PASS over the streams that remain.
				rep.Unsettled = append(rep.Unsettled, short+" is not on the broker")
			}
			continue
		}
		if o.Replicas > 1 && o.Leader == "" {
			rep.Unsettled = append(rep.Unsettled, short+" has no leader (an election is under way)")
			continue
		}
		rep.PerEventLeaders[short] = o.Leader
		server(o.Leader).PerEvent = append(server(o.Leader).PerEvent, short)
		movable := opts.Movable(name)
		if !movable {
			fixedOn[o.Leader] = append(fixedOn[o.Leader], short)
		}
		for _, p := range o.Peers {
			switch {
			case p.Offline:
				rep.Unsettled = append(rep.Unsettled, fmt.Sprintf("%s: the replica on %s is offline", short, p.Name))
			case movable && !p.Current:
				rep.Unsettled = append(rep.Unsettled, fmt.Sprintf("%s: the replica on %s is behind", short, p.Name))
			}
		}
		if !movable {
			continue
		}
		for _, c := range snap.Consumers {
			if c.Stream != name || !c.Durable {
				continue
			}
			if c.Leader == "" {
				rep.Unsettled = append(rep.Unsettled, fmt.Sprintf("%s/%s has no leader", short, c.Name))
				continue
			}
			server(c.Leader).PerEventConsumers = append(server(c.Leader).PerEventConsumers, short+"/"+c.Name)
			for _, p := range c.Peers {
				if p.Offline || !p.Current {
					rep.Unsettled = append(rep.Unsettled, fmt.Sprintf("%s/%s: the replica on %s is offline or behind", short, c.Name, p.Name))
				}
			}
		}
	}
	for srv, names := range fixedOn {
		if len(names) > 1 {
			if rep.FixedShared == nil {
				rep.FixedShared = map[string][]string{}
			}
			sort.Strings(names)
			rep.FixedShared[srv] = names
		}
	}
	for _, s := range byServer {
		sort.Strings(s.PerEvent)
		sort.Strings(s.PerEventConsumers)
		rep.Servers = append(rep.Servers, *s)
	}
	sort.Slice(rep.Servers, func(i, j int) bool { return rep.Servers[i].Server < rep.Servers[j].Server })

	// The rebalance plans nothing until every movable group and its consumers are led and current
	// (brokerSettled). A FAIL here must be a broker the rebalance would act on, so a broker it would
	// wait on is UNSETTLED, whatever the per-event groups look like.
	if !brokerSettled(snap, opts.Movable, opts.PerEvent) {
		rep.Unsettled = append(rep.Unsettled, "a stream or consumer the rebalance may move has no leader, or a replica offline or behind, so `dcctl broker rebalance` would wait")
	}
	switch {
	case len(rep.Unsettled) > 0:
		rep.Verdict = LeadersUnsettled
		return rep
	case len(rep.PerEventLeaders) == 0:
		rep.Verdict = LeadersUnsettled
		rep.Unsettled = append(rep.Unsettled, "the broker holds none of the per-event streams, so there is nothing to judge")
		return rep
	}

	// The dry run. A per-event stream move is always a separation; a consumer move is one only when it
	// leaves a server leading more per-event streams (after the stream moves) for one leading fewer.
	// Moves the count balancing makes between equally occupied servers are not this check's business.
	plan := replication.PlanRebalance(snap, opts)
	occ := map[string]int{}
	for _, l := range plan.PerEventLeaders {
		occ[l]++
	}
	for _, m := range plan.Moves {
		if !opts.PerEvent(m.Stream) {
			continue
		}
		if !m.IsConsumer() || occ[m.From] > occ[m.To] {
			rep.Pending = append(rep.Pending, m)
		}
	}
	rep.Verdict = LeadersPass
	if len(rep.Pending) > 0 {
		rep.Verdict = LeadersFail
	}
	return rep
}

// Format is the report an operator reads.
func (r LeadersReport) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "broker leadership for instance %q, %d server(s)\n", r.Instance, len(r.Servers))
	for _, s := range r.Servers {
		fmt.Fprintf(&b, "  %s: leads %d stream(s), %d consumer(s)", s.Server, s.Streams, s.Consumers)
		if len(s.PerEvent) > 0 {
			fmt.Fprintf(&b, "; per-event: %s", strings.Join(s.PerEvent, ", "))
		}
		if len(s.PerEventConsumers) > 0 {
			fmt.Fprintf(&b, "; per-event consumers: %s", strings.Join(s.PerEventConsumers, ", "))
		}
		b.WriteString("\n")
	}
	names := make([]string, 0, len(r.PerEventLeaders))
	for n := range r.PerEventLeaders {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+r.PerEventLeaders[n])
	}
	fmt.Fprintf(&b, "per-event leaders: %s\n", strings.Join(parts, " "))
	srvs := make([]string, 0, len(r.FixedShared))
	for s := range r.FixedShared {
		srvs = append(srvs, s)
	}
	sort.Strings(srvs)
	for _, s := range srvs {
		fmt.Fprintf(&b, "  %s share %s (fixed: never moved, because a leader change there can lose acknowledged device messages)\n",
			strings.Join(r.FixedShared[s], " and "), s)
	}
	for _, a := range r.Absent {
		if a == messaging.MqttMessageStore {
			fmt.Fprintf(&b, "  NOT PRESENT: %s (no MQTT client has connected since the broker started, so nats-server has not created it; it is judged once one has)\n", a)
			continue
		}
		fmt.Fprintf(&b, "  NOT PRESENT: %s\n", a)
	}
	switch r.Verdict {
	case LeadersPass:
		b.WriteString("PASS: no leader move the rebalance may make would separate the per-event streams further.\n")
	case LeadersFail:
		fmt.Fprintf(&b, "FAIL: %d leader move(s) would separate the per-event streams further:\n", len(r.Pending))
		for _, m := range r.Pending {
			what := shortStream(r.Instance, m.Stream)
			if m.IsConsumer() {
				what += "/" + m.Consumer
			}
			fmt.Fprintf(&b, "  %s: %s -> %s\n", what, m.From, m.To)
		}
		fmt.Fprintf(&b, "Run `dcctl broker rebalance %s` to make them.\n", r.Instance)
	default:
		b.WriteString("UNSETTLED: the placement cannot be judged right now:\n")
		for _, u := range r.Unsettled {
			fmt.Fprintf(&b, "  %s\n", u)
		}
		b.WriteString("Wait for the broker to settle (or pass --settle) and run the check again.\n")
	}
	return b.String()
}

// LeadersOptions name the instance whose broker leadership is read.
type LeadersOptions struct {
	KubeContext string
	InstanceId  string
	// NatsUrl bypasses the port-forward, as for `ha verify`.
	NatsUrl string
	// Settle re-reads an UNSETTLED broker until it settles or this expires. A FAIL is returned at once.
	Settle  time.Duration
	Timeout time.Duration
}

// InspectLeadership reads the instance's broker and judges where its per-event leaders sit. It moves
// nothing. A collection failure is an error, never a verdict.
func InspectLeadership(ctx context.Context, opts LeadersOptions) (LeadersReport, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	if opts.InstanceId == "" {
		return LeadersReport{}, fmt.Errorf("an instance id is required")
	}
	restCfg, err := RestConfig(opts.KubeContext)
	if err != nil {
		return LeadersReport{}, fmt.Errorf("building kube config: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return LeadersReport{}, err
	}
	cfg, err := deployedInstanceConfigWithLogins(ctx, typed, opts.InstanceId)
	if err != nil {
		return LeadersReport{}, err
	}
	ns := InstanceNamespace(opts.InstanceId)
	url := opts.NatsUrl
	if url == "" {
		pods, err := natsPods(ctx, typed, ns)
		if err != nil {
			return LeadersReport{}, err
		}
		if len(pods) == 0 {
			return LeadersReport{}, fmt.Errorf("no NATS pods matched %q in namespace %s; pass --nats-url if the broker is reachable another way", natsPodSelector, ns)
		}
		local, stop, err := forwardPortCtx(ctx, restCfg, ns, pods[0].Name, int(cfg.Infrastructure.Nats.Port))
		if err != nil {
			return LeadersReport{}, fmt.Errorf("opening a port-forward to broker pod %s: %w", pods[0].Name, err)
		}
		defer stop()
		url = fmt.Sprintf("nats://127.0.0.1:%d", local)
	}
	nc, err := dialBrokerCtx(ctx, url, cfg.Infrastructure.Nats)
	if err != nil {
		return LeadersReport{}, err
	}
	defer nc.Close()
	// Each JetStream API request is bounded, so a stalled broker cannot hold the check past its timeout
	// by much more than one request.
	js, err := nc.JetStream(nats.MaxWait(10 * time.Second))
	if err != nil {
		return LeadersReport{}, err
	}
	areas, err := deployedAreas(ctx, typed, opts.InstanceId)
	if err != nil {
		return LeadersReport{}, err
	}
	exp := messaging.ReplicationExpectationFor(opts.InstanceId, int(cfg.Infrastructure.Nats.StreamReplicas), areas,
		cfg.Infrastructure.Nats.Auth.IdentityAccount)
	deadline := time.Now().Add(opts.Settle)
	for {
		snap, err := replication.Collect(js, exp)
		if err != nil {
			return LeadersReport{}, err
		}
		if ctx.Err() != nil {
			// A reading finished after the time ran out is not a verdict the caller asked for.
			return LeadersReport{}, fmt.Errorf("the check ran out of time: %w", ctx.Err())
		}
		rep := buildLeadersReport(snap, opts.InstanceId)
		if rep.Verdict != LeadersUnsettled || !time.Now().Before(deadline) {
			return rep, nil
		}
		select {
		case <-ctx.Done():
			return LeadersReport{}, fmt.Errorf("the broker had not settled when the check ran out of time: %w", ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

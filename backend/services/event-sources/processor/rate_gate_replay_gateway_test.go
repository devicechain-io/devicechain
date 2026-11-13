// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// ---- arm 3: the real lag-credited gate (LagCreditIngest) ----

// gatewayGate drives the gate the gateway sources run, through the calls handle() and the decode worker
// make, on the harness clock. Unlike the model arm it is not told the true lag or skew: it MEASURES them,
// from the delivery metadata a JetStream consumer reports (the append stamp, the stream sequence and how
// many messages were still queued behind the delivery), exactly as production does.
type gatewayGate struct {
	sc     *replayScenario
	clk    *replayClock
	lc     *LagCreditIngest
	queues [][]replayMsg
	heads  []int
	meters map[[2]int]ingestMeter
	// probes counts the idle probes the harness simulated; sheds the shed hook's tally by cause.
	probes int
	sheds  map[core.IngestCause]int
	byName map[string]int
}

func newGatewayGate(sc *replayScenario, clk *replayClock) replayGate {
	g := &gatewayGate{sc: sc, clk: clk}
	g.queues = make([][]replayMsg, len(sc.sources))
	g.heads = make([]int, len(sc.sources))
	for _, m := range sc.messages() {
		if m.src >= 0 {
			g.queues[m.src] = append(g.queues[m.src], m)
		}
	}
	for k := range g.queues {
		sort.SliceStable(g.queues[k], func(i, j int) bool { return g.queues[k][i].send.Before(g.queues[k][j].send) })
	}
	g.byName = map[string]int{}
	for i, tn := range sc.tenants {
		g.byName[tn.name] = i
	}
	g.restart()
	return g
}

func (g *gatewayGate) restart() {
	g.sheds = map[core.IngestCause]int{}
	base := g.sc.resolver()
	g.lc = NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: base,
		Reading: governance.ReadingCeiling(base),
		Factor:  func(tenant string) float64 { return g.sc.factorOf(g.byName[tenant]) },
		Clock:   g.clk.now,
		Hooks:   LagCreditHooks{Shed: func(_, _, _ string, _ core.IngestRoute, cause core.IngestCause) { g.sheds[cause]++ }},
	})
	for k := range g.sc.sources {
		g.lc.view(k).start()
	}
	g.meters = map[[2]int]ingestMeter{}
}

// probe simulates the idle probe of every stream the message is NOT from: a reader with nothing queued
// that has been quiet for a probe interval is seen caught up. A stalled stream has messages queued, so it
// is never probed.
func (g *gatewayGate) probe(except int) {
	for k := range g.queues {
		if k == except {
			continue
		}
		if at, seen := g.lc.view(k).lastRead(); seen && g.clk.t.Sub(at) < idleProbeEvery {
			continue
		}
		q := g.queues[k]
		if g.heads[k] < len(q) && !q[g.heads[k]].send.After(g.clk.t) {
			continue // something is queued
		}
		g.lc.view(k).caughtUp(g.clk.t)
		g.probes++
	}
}

func (g *gatewayGate) message(m replayMsg) replayOutcome {
	key := [2]int{m.tenant, m.index}
	tenant := g.sc.tenantName(m.tenant)
	g.probe(m.src)
	if m.src < 0 {
		g.meters[key] = ingestMeter{lc: g.lc, stream: -1, route: core.RouteLive, stagedAt: g.clk.t}
		return replayOutcome{admitted: g.lc.admitLive("ext", tenant), route: core.RouteLive}
	}
	q := g.queues[m.src]
	h := g.heads[m.src]
	require := q[h]
	if require.tenant != m.tenant || require.index != m.index {
		panic("gateway arm: the source's queue is out of step with the delivery order")
	}
	// Queued behind this delivery at the broker: everything already appended, not yet delivered.
	rest := q[h+1:]
	pending := sort.Search(len(rest), func(i int) bool { return rest[i].send.After(g.clk.t) })
	g.heads[m.src]++
	msg := messaging.Message{AppendTime: m.stamp, StreamSeq: uint64(h + 1), NumPending: uint64(pending), NumDelivered: 1}
	meter, ok := g.lc.admit("src", tenant, m.src, msg)
	g.meters[key] = meter
	return replayOutcome{admitted: ok, route: meter.route}
}

func (g *gatewayGate) reading(m replayMsg, _ time.Duration) bool {
	return g.meters[[2]int{m.tenant, m.index}].readings("src", g.sc.tenantName(m.tenant), 1)
}

// THE GATEWAY ARM. The real gate, measuring lag and skew from delivery metadata, meets every row of the
// table the legacy gate loses, and every row legacy meets.
func TestReplayLagCreditGateway(t *testing.T) {
	for _, sc := range replayTable() {
		sc := sc
		t.Run(sc.id, func(t *testing.T) {
			res := sc.run(newGatewayGate)
			t.Logf("gateway: %s", res.summary())
			if sc.gatewayResidual != "" {
				require.Errorf(t, res.expect(), "%s: documented as a residual (%s) but the gate now meets it; drop the marker", sc.id, sc.gatewayResidual)
				return
			}
			require.NoErrorf(t, res.expect(), "%s (%s)", sc.id, sc.about)
		})
	}
}

// The arm must be able to FAIL. A gate with its lag credit switched off (every stream reported caught
// up) is the legacy defect in the new gate's clothes: it must lose the rows that depend on the credit.
func TestReplayGatewayArmFailsWithoutTheLagCredit(t *testing.T) {
	losing := 0
	for _, sc := range replayTable() {
		if !sc.legacyLoses {
			continue
		}
		res := sc.run(func(sc *replayScenario, clk *replayClock) replayGate {
			g := newGatewayGate(sc, clk).(*gatewayGate)
			return &blindGate{g}
		})
		if res.expect() != nil {
			losing++
		}
	}
	require.Greater(t, losing, 5, "a gate that never credits lag must fail the rows the credit exists for")
}

// blindGate is the gateway arm with the lag measurement broken: the delivery metadata claims every
// delivery is caught up and fresh (NumPending 0 and an append stamp of now), so no credit is ever earned.
type blindGate struct{ *gatewayGate }

func (b *blindGate) message(m replayMsg) replayOutcome {
	m.stamp = b.clk.t
	return b.gatewayGate.message(m)
}

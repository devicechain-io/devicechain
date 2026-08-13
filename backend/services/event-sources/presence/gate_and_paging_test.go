// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔴 THE ADMISSION GATE, WHICH IS THE ONLY THING STANDING BETWEEN A PURGED TENANT'S
// DEVICES AND WRITES INTO THE TENANT BEING ERASED. A deleted tenant's devices keep
// their broker credentials for up to 12h, so their connects and disconnects keep
// arriving long after the delete — and this path does not go through the decode gate
// that covers ordinary ingest. The same gate also carries the per-tenant ceiling, so a
// device reconnecting in a loop cannot mint unbounded durable writes.
func TestARefusedTenantIsNotEmitted(t *testing.T) {
	emitter := newRecordingEmitter()
	refused := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_refused"})
	// A gate that refuses exactly one tenant, so the test can tell "the gate is
	// consulted" from "nothing is ever emitted".
	gate := func(_, tenant string, _ time.Time, _ bool) bool { return tenant != "purged" }
	tap := NewTap(testInstance, "mqtt1", emitter, gate, Metrics{Refused: refused})

	tap.Apply(context.Background(), Transition{
		Tenant: "purged", DeviceToken: "sensor-001",
		Event: presenceEventAt(true, 100, time.Now()),
	})
	require.Empty(t, emitter.all(), "a transition for a refused tenant was emitted anyway")
	require.Equal(t, 1.0, counter(t, refused), "a refusal must be counted, not silently dropped")

	// The counterweight: an allowed tenant still passes. Without it a gate that refused
	// everything would satisfy the assertion above and silently disable the feature.
	tap.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "sensor-001",
		Event: presenceEventAt(true, 100, time.Now()),
	})
	emitter.await(t, "an allowed tenant's transition", isDevice("acme", "sensor-001", true))
}

// Reconciliation's synthetic transitions pass the SAME gate. A repair must not be a way
// around the deleted-tenant refusal — it is the path most likely to be pointed at a
// tenant whose fleet is being torn down.
func TestReconciliationAlsoPassesTheGate(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter,
		func(string, string, time.Time, bool) bool { return false }, Metrics{})
	r := NewReconciler(tap, nil, nil, nil, ReconcileMetrics{}, time.Second, time.Now)

	live := LiveDevice{Tenant: "purged", DeviceToken: "sensor-001", SessionId: 1786552664076882575}
	r.reconcileTenant(context.Background(), "purged", inventoryOf(true, live), projection())

	require.Empty(t, emitter.all(), "reconciliation emitted into a tenant the gate refuses")
}

func presenceEventAt(connected bool, session uint64, at time.Time) adapter.PresenceEvent {
	return adapter.PresenceEvent{Connected: connected, SessionId: session, OccurredAt: at}
}

// pagingRequester serves a fixed set of connections one page at a time, honouring the
// offset/limit in the request — so the paging loop is driven rather than described.
type pagingRequester struct {
	serverID string
	conns    []connzConn
	requests int
}

func (p *pagingRequester) Gather(context.Context, string, []byte, time.Duration) ([][]byte, error) {
	return nil, fmt.Errorf("unused")
}

func (p *pagingRequester) RequestWithContext(_ context.Context, subject string, data []byte) (*nats.Msg, error) {
	if subject != fmt.Sprintf(sysServerDirect, p.serverID, "CONNZ") {
		return nil, fmt.Errorf("no responder for %s", subject)
	}
	p.requests++
	var req struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	end := min(req.Offset+req.Limit, len(p.conns))
	page := []connzConn{}
	if req.Offset < len(p.conns) {
		page = p.conns[req.Offset:end]
	}
	raw, err := json.Marshal(map[string]any{
		"server": map[string]any{"id": p.serverID},
		"data": map[string]any{
			// total is the TRUE count; the page carries only what the limit allowed.
			// That gap is the broker's real behaviour and the thing paging exists for.
			"total": len(p.conns), "num_connections": len(page),
			"offset": req.Offset, "connections": page,
		},
	})
	if err != nil {
		return nil, err
	}
	return &nats.Msg{Data: raw}, nil
}

// 🔴 A CONNZ REPLY IS TRUNCATED AT ITS LIMIT AND SAYS SO ONLY IN `total`. The broker's
// own default is 1024, so an instance larger than that reads as having exactly 1024
// connections — and in the disconnect direction every device past the cut looks gone.
//
// The fixture is deliberately larger than one page so the loop must actually iterate;
// asserting the arithmetic on a single page would pass against an implementation that
// never pages at all.
func TestTheInventoryPagesPastATruncatedReply(t *testing.T) {
	total := connzPageSize + 137
	conns := make([]connzConn, 0, total)
	for i := 0; i < total; i++ {
		conns = append(conns, connzConn{
			MQTTClient: fmt.Sprintf("%s:acme:sensor-%04d", testInstance, i),
			Type:       "mqtt",
			Start:      startAt(int64(1786552664076882575 + i)),
		})
	}
	p := &pagingRequester{serverID: "A", conns: conns}

	devices := map[string]LiveDevice{}
	require.NoError(t, collectServer(t.Context(), p, testInstance, "A", devices))

	require.Len(t, devices, total, "the inventory stopped at a page boundary")
	require.Greater(t, p.requests, 1, "only one request was made, so nothing was paged")
	// The last device specifically: an off-by-one in the offset arithmetic loses the
	// tail rather than the whole remainder, which a count alone can miss.
	_, ok := devices[DeviceKey("acme", fmt.Sprintf("sensor-%04d", total-1))]
	require.True(t, ok, "the final connection is missing, so paging dropped its tail")
}

// 🔴 A FULL PARTITION MUST NOT DECLARE ITSELF COMPLETE, AND THE OBVIOUS RULE DOES.
// Completeness compares who answered against the cluster size the answerers CLAIM — and
// a server that has lost its routes claims to be alone. So in a partition every server
// we can still reach says "cluster of one", the subset satisfies its own claim, and the
// pass marks the unreachable side's whole fleet offline: the mass false death the rule
// exists to prevent, arriving through the rule itself.
//
// This test found that hole. The fix is a high-water mark — a cluster does not shrink in
// normal operation, so once three servers have been seen, two answering is a partition.
func TestAShrunkenClusterIsNotComplete(t *testing.T) {
	r := NewReconciler(nil, nil, nil, nil, ReconcileMetrics{}, time.Second, time.Now)

	// A healthy three-node pass establishes the mark and is complete.
	full := r.applyClusterHighWater(Inventory{Servers: 3, Expected: 3, Complete: true})
	require.True(t, full.Complete, "a full cluster answering in full must be complete")

	// The partition: two servers answer, and both now believe they are alone. Believed,
	// this pass would be "complete" and would kill the third server's devices.
	split := r.applyClusterHighWater(Inventory{Servers: 2, Expected: 1, Complete: true})
	require.False(t, split.Complete,
		"a partitioned pass declared itself complete; the unreachable node's devices would be marked offline")
	require.Equal(t, 3, split.Expected, "the pass must still report the cluster size it knows about")

	// The counterweight: recovery must restore completeness, or the mark would be a
	// one-way latch that disables the disconnect direction for the process's lifetime.
	healed := r.applyClusterHighWater(Inventory{Servers: 3, Expected: 3, Complete: true})
	require.True(t, healed.Complete, "the pass never recovered completeness after the partition healed")
}

// The number that ANSWERED is itself evidence: three replies prove three servers exist,
// whatever any of them claims. Without this, a cluster whose members all under-report
// from the very first pass would never establish a mark at all.
func TestAnswersCountAsEvidenceOfClusterSize(t *testing.T) {
	r := NewReconciler(nil, nil, nil, nil, ReconcileMetrics{}, time.Second, time.Now)

	first := r.applyClusterHighWater(Inventory{Servers: 3, Expected: 1, Complete: true})
	require.Equal(t, 3, first.Expected, "three servers answered but the pass expected one")

	later := r.applyClusterHighWater(Inventory{Servers: 2, Expected: 1, Complete: true})
	require.False(t, later.Complete, "a pass short of the servers already seen must not be complete")
}

// A pass already judged incomplete by the fetch (an unreachable server) stays incomplete
// however the counts land — the high-water mark can only ever take completeness AWAY.
func TestTheHighWaterMarkNeverGrantsCompleteness(t *testing.T) {
	r := NewReconciler(nil, nil, nil, nil, ReconcileMetrics{}, time.Second, time.Now)

	got := r.applyClusterHighWater(Inventory{Servers: 3, Expected: 3, Complete: false})
	require.False(t, got.Complete, "an inventory that failed to collect a server was promoted to complete")
}

// 🔴 A REGRESSED SESSION IS COUNTED, NOT DROPPED — and the difference matters because
// this replica's view is partial. The queue group means it has seen only some of a
// device's transitions, and the watermark is recorded BEFORE the emit (including on emit
// failures), so it can sit ahead of anything the projection ever stored. Dropping a
// "regressed" transition would then discard a perfectly valid disconnect. The projection's
// stored session is the authority; this counter only makes the clock-skew hazard visible.
func TestARegressedSessionIsStillEmitted(t *testing.T) {
	emitter := newRecordingEmitter()
	regressed := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_regressed"})
	tap := NewTap(testInstance, "mqtt1", emitter,
		func(string, string, time.Time, bool) bool { return true },
		Metrics{RegressedSessions: regressed})

	later := Transition{Tenant: "acme", DeviceToken: "sensor-001",
		Event: presenceEventAt(true, 200, time.Now())}
	earlier := Transition{Tenant: "acme", DeviceToken: "sensor-001",
		Event: presenceEventAt(false, 100, time.Now())}

	require.True(t, tap.Apply(context.Background(), later))
	require.Equal(t, 0.0, counter(t, regressed), "the first transition for a device cannot be a regression")

	require.True(t, tap.Apply(context.Background(), earlier),
		"a lower-session transition was DROPPED; the projection is the authority on staleness, not this replica")
	require.Equal(t, 1.0, counter(t, regressed), "the regression was not counted")
	require.Len(t, emitter.all(), 2, "both transitions must reach the stream")
}

// The watermark is per device: one device stepping backwards must not make another
// device's ordinary progress read as a regression.
func TestTheRegressionWatermarkIsPerDevice(t *testing.T) {
	w := newSessionWatermarks()
	require.False(t, w.regressed("acme", "a", 200))
	require.False(t, w.regressed("acme", "b", 100), "device b was judged against device a's session")
	require.True(t, w.regressed("acme", "a", 100))
	require.False(t, w.regressed("acme", "b", 300))

	// Tenants are part of the key too: two tenants may legitimately own a device token
	// of the same name.
	require.False(t, w.regressed("globex", "a", 50), "a same-named device in another tenant was conflated")
}

// 🔴🔴 THESE TWO GO THROUGH FetchInventory, AND THAT IS THE ENTIRE POINT. The earlier
// completeness tests build an Inventory by hand or call collectServer directly, so they
// reimplement the very loop under test — and both the max-claim rule and the
// unreachable-server downgrade could be DELETED with the whole suite still green.
// Measured, both of them. Testing the callee is not testing the caller.

// The servers' own cluster-size claim is what protects the FIRST pass after a restart,
// when the high-water mark is still zero. Two servers answering while both report a
// three-node cluster must read as incomplete: the third's devices are absent, not gone.
func TestFetchInventoryTrustsTheServersClusterClaim(t *testing.T) {
	f := &fakeRequester{
		servers: []string{"A", "B"},
		claimed: 3, // both know about a third server that did not answer
		conns:   map[string][]connzConn{},
	}

	inv, err := FetchInventory(t.Context(), f, testInstance, time.Millisecond)
	require.NoError(t, err)
	require.False(t, inv.Complete,
		"two servers answered while claiming a cluster of three, and the pass called itself complete — "+
			"the third server's devices would all be marked offline")
	require.Equal(t, 3, inv.Expected)

	// The counterweight: when the claim is satisfied the pass IS complete, or the rule
	// above would be satisfied by an implementation that never completes at all.
	whole := &fakeRequester{servers: []string{"A", "B", "C"}, claimed: 3, conns: map[string][]connzConn{}}
	got, err := FetchInventory(t.Context(), whole, testInstance, time.Millisecond)
	require.NoError(t, err)
	require.True(t, got.Complete, "a fully-answering cluster was judged incomplete")
}

// A server that answers the ping but fails its connection listing contributes nothing,
// so the pass must not call itself complete — its whole population would be synthetically
// killed.
func TestFetchInventoryIsIncompleteWhenAServerCannotBeListed(t *testing.T) {
	live := connzConn{
		MQTTClient: testInstance + ":acme:sensor-001", Type: "mqtt",
		Start: startAt(1786552664076882575),
	}
	f := &fakeRequester{
		servers: []string{"A", "B"},
		claimed: 2,
		conns:   map[string][]connzConn{"A": {live}},
		failFor: map[string]bool{"B": true},
	}

	inv, err := FetchInventory(t.Context(), f, testInstance, time.Millisecond)
	require.NoError(t, err)
	require.False(t, inv.Complete,
		"a server answered the ping but could not be listed, and the pass called itself complete")
	// The reachable server's devices are still genuinely connected and must survive: an
	// unlistable peer voids completeness, not the whole pass.
	require.Contains(t, inv.Devices, DeviceKey("acme", "sensor-001"),
		"the reachable server's devices were discarded along with the unreachable one's")
}

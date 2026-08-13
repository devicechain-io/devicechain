// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/presence"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The reconciler's inventory half runs against a real broker in broker_test.go. What
// is exercised here is the DIFF: which direction is emitted, what session and time a
// synthetic transition carries, and what the completeness rule withholds. Those are
// decisions, not broker behaviour, and each one has a failure mode that is silent.

// fakeRequester replays canned monitoring replies, so a diff test can pose an
// inventory — including an INCOMPLETE one, which a healthy broker will not produce on
// demand.
type fakeRequester struct {
	servers   []string // server ids that answer the ping
	claimed   int      // the cluster size those servers claim
	conns     map[string][]connzConn
	failFor   map[string]bool // server ids whose CONNZ request errors
	inboxSubs map[string]chan *nats.Msg
}

// Gather answers the fan-out ping with one statsz reply per server, each claiming the
// cluster size this fixture was built with.
func (f *fakeRequester) Gather(_ context.Context, subject string, _ []byte, _ time.Duration) ([][]byte, error) {
	if subject != sysServerPing {
		return nil, fmt.Errorf("no responder for %s", subject)
	}
	out := make([][]byte, 0, len(f.servers))
	for _, id := range f.servers {
		raw, err := json.Marshal(map[string]any{
			"server": map[string]any{"id": id},
			"statsz": map[string]any{"active_servers": f.claimed},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func (f *fakeRequester) RequestWithContext(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
	for _, id := range f.servers {
		if subject != fmt.Sprintf(sysServerDirect, id, "CONNZ") {
			continue
		}
		if f.failFor[id] {
			return nil, fmt.Errorf("server %s is unreachable", id)
		}
		conns := f.conns[id]
		reply := map[string]any{
			"server": map[string]any{"id": id},
			"data": map[string]any{
				"total": len(conns), "num_connections": len(conns), "offset": 0,
				"connections": conns,
			},
		}
		raw, err := json.Marshal(reply)
		if err != nil {
			return nil, err
		}
		return &nats.Msg{Data: raw}, nil
	}
	return nil, fmt.Errorf("no responder for %s", subject)
}

// inventoryOf builds an Inventory directly, which is what the diff consumes. The
// fetch path has its own coverage against a real broker; posing an inventory here
// keeps these tests about the diff.
// believedOnline / believedOffline turn a broker-shaped LiveDevice into what the
// PROJECTION holds for it. The split is the point: an asserted row that reads offline is
// the repairable case, and it carries the stored session id a repair has to defer to.
func believedOnline(d LiveDevice) StoredDevice {
	return StoredDevice{Tenant: d.Tenant, DeviceToken: d.DeviceToken, SessionId: d.SessionId, Active: true}
}

func believedOffline(tenant, token string, session uint64) StoredDevice {
	return StoredDevice{Tenant: tenant, DeviceToken: token, SessionId: session, Active: false}
}

func projection(devices ...StoredDevice) map[string]StoredDevice {
	m := map[string]StoredDevice{}
	for _, d := range devices {
		m[DeviceKey(d.Tenant, d.DeviceToken)] = d
	}
	return m
}

func inventoryOf(complete bool, devices ...LiveDevice) Inventory {
	inv := Inventory{Devices: map[string]LiveDevice{}, Complete: complete, Servers: 1, Expected: 1}
	if !complete {
		inv.Servers, inv.Expected = 1, 3
	}
	for _, d := range devices {
		inv.Devices[DeviceKey(d.Tenant, d.DeviceToken)] = d
	}
	return inv
}

func reconcilerFor(t *testing.T, e *recordingEmitter, at time.Time) *Reconciler {
	t.Helper()
	tap := NewTap(testInstance, "mqtt1", e, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	return NewReconciler(tap, nil, nil, nil, ReconcileMetrics{}, time.Second, func() time.Time { return at })
}

// A device the broker holds that the projection does not know about is repaired with a
// synthetic CONNECT carrying the CONNECTION's session — not a fresh one, so a repair
// and a late advisory for the same connection are one transition rather than two.
func TestAConnectedDeviceTheProjectionMissedIsRepaired(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())
	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575}

	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live), projection())

	require.Equal(t, [3]int{1, 0, 0}, [3]int{c, d, w})
	got := emitter.await(t, "a repair connect", isDevice("acme", "sensor-001", true))
	if got.Event.SessionId != live.SessionId {
		t.Errorf("repair session = %d, want the connection's own %d", got.Event.SessionId, live.SessionId)
	}
}

// 🔴 A REPAIR CONNECT MUST BE NEWER THAN ANY TIME THE PROJECTION COULD HOLD FOR THAT
// SESSION, OR IT IS REJECTED FOREVER. The tempting stamp is the connection's start — it
// is truthful and it is what the lost advisory carried. It also wedges: after a synthetic
// death (same session, stamped NOW), the row reads offline at an instant well after the
// connection began, and presence.Decide takes a same-session transition only when it is
// NEWER. A repair at the start is older, so it is rejected on this pass and on every
// pass after it, while the counter reports a repair each time.
//
// This drives presence.Decide directly rather than restating its rule, because the rule
// is the thing under test.
func TestARepairConnectIsNotRejectedAfterAFalseDeath(t *testing.T) {
	emitter := newRecordingEmitter()
	now := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	r := reconcilerFor(t, emitter, now)

	session := uint64(1786552664076882575)
	start := time.Unix(0, int64(session)).UTC()
	// The projection's state after a false death: same session, marked offline at a
	// moment after the connection began.
	falseDeathAt := start.Add(time.Hour)
	prior := presence.Prior{SessionId: session, Time: falseDeathAt, HasTime: true, Connected: false}

	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: session}
	c, _, _ := r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live), projection())
	require.Equal(t, 1, c)
	got := emitter.await(t, "a repair connect", isDevice("acme", "sensor-001", true))

	d := presence.Decide(prior, got.Event.SessionId, got.Event.OccurredAt, got.Event.Connected)
	require.True(t, d.Ordered,
		"the repair at %v is not ordered against a row last written at %v, so it is silently discarded "+
			"on this pass and on every pass after it", got.Event.OccurredAt, falseDeathAt)
	require.True(t, d.Flipped, "the repair was ordered but did not bring the device back online")

	// 🔑 THE COUNTERWEIGHT that makes this test mean something: the START — the stamp
	// this test exists to reject — really would be discarded. Without it, the assertion
	// above passes against any implementation whose clock happens to run forward.
	stale := presence.Decide(prior, session, start, true)
	require.False(t, stale.Ordered,
		"a repair stamped at the connection's start was ACCEPTED, so this test cannot tell the two "+
			"implementations apart")
}

// A device already known to be online is left alone. Without this the pass would
// re-emit a StateChange for every connected device every time it ran — a durable event
// row per device per pass, through the resolver, the projection, DETECT and the
// historian.
func TestAnAlreadyOnlineDeviceIsNotReEmitted(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())
	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 42}
	online := projection(believedOnline(live))

	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live), online)

	require.Equal(t, [3]int{0, 0, 0}, [3]int{c, d, w})
	require.Empty(t, emitter.all(), "a steady-state pass emitted events")
}

// 🔑 THE SYNTHETIC DEATH MUST CARRY THE SESSION THE PROJECTION ALREADY HOLDS. Any
// other id is a DIFFERENT session, and presence.Decide takes a different session only
// when it is HIGHER — so a repair carrying a lower or invented id is rejected on every
// pass, forever, while the counter says the repair was made. That is the failure that
// looks exactly like success.
func TestASyntheticDeathReusesTheStoredSession(t *testing.T) {
	emitter := newRecordingEmitter()
	now := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	r := reconcilerFor(t, emitter, now)
	stored := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575}
	online := projection(believedOnline(stored))

	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(true), online)

	require.Equal(t, [3]int{0, 1, 0}, [3]int{c, d, w})
	got := emitter.await(t, "a repair disconnect", isDevice("acme", "sensor-001", false))
	if got.Event.SessionId != stored.SessionId {
		t.Errorf("death session = %d, want the STORED %d — anything else is silently rejected",
			got.Event.SessionId, stored.SessionId)
	}
	// Same session, so the time must be strictly newer or Decide will not apply it.
	if !got.Event.OccurredAt.Equal(now) {
		t.Errorf("death occurredAt = %v, want now (%v)", got.Event.OccurredAt, now)
	}
	if !got.Event.OccurredAt.After(time.Unix(0, int64(stored.SessionId))) {
		t.Error("the death is not newer than the session it closes, so it would be rejected as stale")
	}
}

// 🔴 THE COMPLETENESS RULE, WHICH IS THE DIFFERENCE BETWEEN A REPAIR AND A MASS FALSE
// DEATH. The inventory is a fan-out: one silent server means every device attached to
// it is absent, indistinguishable from gone. Marking those offline would hold a live
// device's commands under the delivery gate.
func TestAnIncompleteInventoryWithholdsDeathsButStillRepairsConnects(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())
	seen := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 100}
	missing := LiveDevice{Tenant: "acme", DeviceToken: "sensor-002", SessionId: 200}
	online := projection(believedOnline(missing))

	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(false, seen), online)

	if d != 0 || w != 1 {
		t.Errorf("disconnects=%d withheld=%d, want 0 and 1 — an unproven inventory must not kill anyone", d, w)
	}
	emitter.refute(t, "a death from an incomplete inventory", isDevice("acme", "sensor-002", false))

	// The counterweight, in the same pass: the CONNECT direction is positive evidence
	// and must still run. Without it, "incomplete ⇒ do nothing" would pass this test
	// while leaving reconnecting devices unrepaired during exactly the partial-cluster
	// conditions that lose the most advisories.
	if c != 1 {
		t.Errorf("connects=%d, want 1 — positive evidence does not need a complete inventory", c)
	}
	emitter.await(t, "a repair connect during an incomplete pass", isDevice("acme", "sensor-001", true))
}

// Another tenant's rows must not be touched while reconciling this one — the diff is
// per tenant, and the inventory is instance-wide.
func TestTheDiffDoesNotCrossTenants(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())
	other := LiveDevice{Tenant: "globex", DeviceToken: "sensor-009", SessionId: 7}

	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(true, other), projection())

	require.Equal(t, [3]int{0, 0, 0}, [3]int{c, d, w},
		"reconciling acme acted on a globex device")
	require.Empty(t, emitter.all())
}

// An unreachable server voids COMPLETENESS without voiding the pass: the devices the
// other servers reported are still genuinely connected, and the disconnect direction is
// what must stand down.
func TestOneUnreachableServerVoidsCompleteness(t *testing.T) {
	f := &fakeRequester{
		servers: []string{"A", "B"},
		claimed: 2,
		conns: map[string][]connzConn{
			"A": {{MQTTClient: testInstance + ":acme:sensor-001", Type: "mqtt", Start: startAt(1786552664076882575)}},
		},
		failFor: map[string]bool{"B": true},
	}
	inv := Inventory{Devices: map[string]LiveDevice{}, Servers: 2, Expected: 2, Complete: true}
	for _, id := range f.servers {
		if err := collectServer(t.Context(), f, testInstance, id, inv.Devices); err != nil {
			inv.Complete = false
		}
	}

	if inv.Complete {
		t.Error("a pass with an unreachable server reported itself complete")
	}
	if _, ok := inv.Devices[DeviceKey("acme", "sensor-001")]; !ok {
		t.Error("the reachable server's devices were discarded along with the unreachable one's")
	}
}

func startAt(nanos int64) *time.Time {
	t := time.Unix(0, nanos).UTC()
	return &t
}

// fakeTenants and fakeProjection complete the fake stack so Run itself can be driven —
// not just the diff it delegates to.
type fakeTenants struct{ tokens []string }

func (f fakeTenants) TenantTokens(context.Context) ([]string, error) { return f.tokens, nil }

type fakeProjection struct {
	states map[string]map[string]StoredDevice
}

func (f fakeProjection) AssertedStates(_ context.Context, tenant, _ string) (map[string]StoredDevice, error) {
	if m, ok := f.states[tenant]; ok {
		return m, nil
	}
	return map[string]StoredDevice{}, nil
}

// 🔴 THIS TESTS Run, NOT reconcileTenant — and that distinction is the whole reason it
// exists. Every other test here calls the diff directly, so all of them pass against a
// Run that never applies the cluster high-water mark at all. Measured: removing that one
// line from Run left the entire suite green, which is the same shape that let a defect
// through in the previous slice. Testing the callee is not testing the caller.
func TestRunWillNotKillDevicesAfterTheClusterShrinks(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	stored := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575}
	reads := fakeProjection{states: map[string]map[string]StoredDevice{
		"acme": projection(believedOnline(stored)),
	}}

	// Pass 1: a healthy three-node cluster holding nothing. The device is gone, the
	// inventory is complete, so the death is emitted — and that is what establishes both
	// the high-water mark and that this fixture CAN produce a death.
	full := &fakeRequester{servers: []string{"A", "B", "C"}, claimed: 3, conns: map[string][]connzConn{}}
	r := NewReconciler(tap, full, fakeTenants{[]string{"acme"}}, reads, ReconcileMetrics{}, time.Second, time.Now)
	require.NoError(t, r.Run(t.Context()))
	emitter.await(t, "a death from a complete pass", isDevice("acme", "sensor-001", false))

	// Pass 2: the cluster partitions. Two servers answer and both now claim to be alone,
	// which by their own account is a complete cluster. Without the high-water mark this
	// pass marks the unreachable node's devices offline.
	emitter2 := newRecordingEmitter()
	tap2 := NewTap(testInstance, "mqtt1", emitter2, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	r.tap = tap2
	r.requests = &fakeRequester{servers: []string{"A", "B"}, claimed: 1, conns: map[string][]connzConn{}}
	require.NoError(t, r.Run(t.Context()))
	emitter2.refute(t, "a death emitted during a cluster partition", isDevice("acme", "sensor-001", false))
}

// 🔴🔴 THE PERMANENT WEDGE: A BROKER SESSION THAT WENT BACKWARDS. Session ids are minted
// from the wall clock of whichever broker node the device landed on, so a reconnect onto
// a node with a trailing clock carries a LOWER id than the projection holds. Decide takes
// a DIFFERENT session only when it is higher, so the real CONNECT is rejected while the
// old session's DISCONNECT — same session, newer time — is not. The row reads offline
// while the device publishes, and a repair carrying the connection's own low id is
// rejected the same way on every pass, forever, while the repair counter reports success.
//
// The repair therefore defers to the STORED session, which makes it
// same-session-newer-time. That is the trick direction 2 already relies on, mirrored.
func TestARepairDefersToTheStoredSessionWhenTheBrokerSessionHasRegressed(t *testing.T) {
	emitter := newRecordingEmitter()
	now := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	r := reconcilerFor(t, emitter, now)

	storedSession := uint64(1786552664076882575)
	regressed := storedSession - uint64(90*time.Second) // a node ~90s behind its peers
	diedAt := now.Add(-time.Minute)
	prior := presence.Prior{SessionId: storedSession, Time: diedAt, HasTime: true, Connected: false}

	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: regressed}
	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live),
		projection(believedOffline("acme", "sensor-001", storedSession)))

	require.Equal(t, [3]int{1, 0, 0}, [3]int{c, d, w})
	got := emitter.await(t, "a repair connect", isDevice("acme", "sensor-001", true))
	require.Equal(t, storedSession, got.Event.SessionId,
		"the repair carried the broker's regressed session, which presence ordering refuses")

	dec := presence.Decide(prior, got.Event.SessionId, got.Event.OccurredAt, got.Event.Connected)
	require.True(t, dec.Ordered, "the repair is not ordered against the stored row, so it is discarded")
	require.True(t, dec.Flipped, "the repair was ordered but did not bring the device back online")

	// 🔑 THE COUNTERWEIGHT. Without it this test passes against the old implementation on
	// any fixture whose broker session happens to be the higher one — which is every
	// other fixture in this file.
	stale := presence.Decide(prior, regressed, got.Event.OccurredAt, true)
	require.False(t, stale.Ordered,
		"a repair carrying the broker's regressed session was ACCEPTED, so this test cannot tell the "+
			"two implementations apart")
}

// The deference is narrow on purpose. When the broker's session is genuinely newer, the
// repair must carry the CONNECTION's id — that is the session a late advisory would name,
// and claiming the stored one instead would file this connection under a dead session's
// identity and leave its real death unmatchable.
func TestARepairKeepsTheConnectionsSessionWhenItIsNewerThanTheStoredOne(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())

	stored := uint64(1786552664076882575)
	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: stored + uint64(time.Minute)}

	c, _, _ := r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live),
		projection(believedOffline("acme", "sensor-001", stored)))

	require.Equal(t, 1, c)
	got := emitter.await(t, "a repair connect", isDevice("acme", "sensor-001", true))
	require.Equal(t, live.SessionId, got.Event.SessionId,
		"a newer broker session was overridden by the stored one")
}

// An asserted row the projection ALREADY believes is offline must not produce a death.
// Direction 2 walks the same map direction 1 does now, so without the Active filter every
// dead device would be re-killed on every pass — a durable event row per dead device per
// pass, forever, for a state that has not changed.
func TestAnAlreadyOfflineDeviceIsNotKilledAgain(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())

	c, d, w := r.reconcileTenant(t.Context(), "acme", inventoryOf(true),
		projection(believedOffline("acme", "sensor-001", 1786552664076882575)))

	require.Equal(t, [3]int{0, 0, 0}, [3]int{c, d, w})
	emitter.refute(t, "a death for a device already believed offline", isDevice("acme", "sensor-001", false))
}

// 🔴 TWO PASSES MUST NOT COLLAPSE INTO ONE WRITE. The presence dedup key is
// (tenant, device, session, state) with no time in it, and once the connect direction can
// re-activate a row under its STORED session, that pair alternates: repair-connect,
// real-drop-rejected, direction-2 death, real-reconnect-rejected, repair-connect again —
// the same key, minutes apart, inside a 30-minute duplicate window. The second one would be
// discarded at the stream while the publish reported success (the ack is dropped) and the
// repair counter recorded a repair that did not happen. Each pass therefore stamps a nonce.
func TestTwoReconcilePassesDoNotCollapseIntoOneWrite(t *testing.T) {
	emitter := newRecordingEmitter()
	first := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	r := reconcilerFor(t, emitter, first)

	stored := uint64(1786552664076882575)
	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: stored - 1}
	view := projection(believedOffline("acme", "sensor-001", stored))

	r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live), view)
	a := emitter.await(t, "the first pass's repair", isDevice("acme", "sensor-001", true))

	// A later pass over the same divergence: identical tenant, device, session and state.
	r.now = func() time.Time { return first.Add(5 * time.Minute) }
	emitter2 := newRecordingEmitter()
	r.tap = NewTap(testInstance, "mqtt1", emitter2, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	r.reconcileTenant(t.Context(), "acme", inventoryOf(true, live), view)
	b := emitter2.await(t, "the second pass's repair", isDevice("acme", "sensor-001", true))

	require.Equal(t, a.Event.SessionId, b.Event.SessionId, "the fixture must pose the SAME session both passes")
	require.Equal(t, a.Event.Connected, b.Event.Connected, "the fixture must pose the SAME state both passes")
	require.NotEmpty(t, b.Event.DedupNonce, "a repair carried no dedup nonce, so a later pass is swallowed")
	require.NotEqual(t, a.Event.DedupNonce, b.Event.DedupNonce,
		"two passes stamped the same nonce, so the stream collapses them and the second repair is "+
			"counted without being written")
}

// The death direction needs it for the same reason and by the same route: it is the other
// half of the alternating pair.
func TestASyntheticDeathCarriesAPassNonce(t *testing.T) {
	emitter := newRecordingEmitter()
	r := reconcilerFor(t, emitter, time.Now())

	stored := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575}
	_, d, _ := r.reconcileTenant(t.Context(), "acme", inventoryOf(true), projection(believedOnline(stored)))

	require.Equal(t, 1, d)
	got := emitter.await(t, "a synthetic death", isDevice("acme", "sensor-001", false))
	require.Equal(t, stored.SessionId, got.Event.SessionId, "the death must reuse the stored session")
	require.NotEmpty(t, got.Event.DedupNonce, "a death carried no dedup nonce, so a retry is swallowed")
}

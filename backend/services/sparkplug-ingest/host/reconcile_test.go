// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

// The protocol-neutral Reconciler.AssertedActive read (device-state → floor input) is
// tested with the adapter (adapter/reconcile_test.go). What stays here is the Sparkplug
// CLIENT failover wiring — establishEpochFloor + reconcileProbe — which is coupled to the
// session machine (epoch floor, rebirth publisher, seen-set) and the source config.

// --- fakes + helpers --------------------------------------------------------

// fakeReconciler is a canned device-state read; it records the tenant + source it was
// asked for so a test can pin the source scoping.
type fakeReconciler struct {
	devices   []AssertedDevice
	max       uint64
	err       error
	gotTenant string
	gotSource string
	callCount int
}

func (f *fakeReconciler) AssertedActive(_ context.Context, tenant, source string) ([]AssertedDevice, uint64, error) {
	f.callCount++
	f.gotTenant = tenant
	f.gotSource = source
	return f.devices, f.max, f.err
}

// reconcileClient builds a client wired with a fake ingester + reconciler, a tiny probe
// window, and a rebirth publisher captured into rebirthed (all succeed by default), ready
// for a direct establishEpochFloor / reconcileProbe call.
func reconcileClient(fake *fakeIngester, rec reconcileSource) (*Client, *[]nodeKey) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, fake, fixedNow, Metrics{})
	c.SetReconciler(rec)
	c.probeWindow = 5 * time.Millisecond
	rebirthed := &[]nodeKey{}
	c.rebirthPub = func(g, n string) bool {
		*rebirthed = append(*rebirthed, nodeKey{group: g, node: n})
		return true
	}
	return c, rebirthed
}

// --- establishEpochFloor (phase 1: floor before births) ---------------------

// TestEstablishEpochFloorRaisesFloorAndScopes proves phase 1 reads the projection for
// THIS source, raises the epoch floor above the max stored session (so a later birth can
// never mint a stale-rejected epoch — F3/Major-4), and returns the asserted set. The
// floor is load-bearing: max is chosen above the receipt clock's nanoseconds, so if
// SetEpochFloor were removed the next minted epoch would be BELOW max and this fails.
func TestEstablishEpochFloorRaisesFloorAndScopes(t *testing.T) {
	const bigSession = uint64(5_000_000_000_000_000_000) // > fixedNow (≈1.7e18)
	rec := &fakeReconciler{
		devices: []AssertedDevice{{ExternalId: "plant-a/n1", SessionId: bigSession}},
		max:     bigSession,
	}
	c, _ := reconcileClient(&fakeIngester{}, rec)

	devices := c.establishEpochFloor(context.Background())
	require.Len(t, devices, 1, "the asserted set is returned for the probe")
	assert.Equal(t, "acme", rec.gotTenant)
	assert.Equal(t, "sparkplug:h1", rec.gotSource, "phase 1 reads are scoped to this source")
	assert.Greater(t, c.sessions.MintEpoch(), bigSession,
		"the generator must be floored above the max stored session (a removed SetEpochFloor fails this)")
}

func TestEstablishEpochFloorReadErrorReturnsNil(t *testing.T) {
	c, _ := reconcileClient(&fakeIngester{}, &fakeReconciler{err: errors.New("device-state down")})
	assert.Nil(t, c.establishEpochFloor(context.Background()), "a read error yields no probe set (retried next reconnect)")
}

func TestEstablishEpochFloorEmptyReturnsNil(t *testing.T) {
	c, _ := reconcileClient(&fakeIngester{}, &fakeReconciler{devices: nil, max: 0})
	assert.Nil(t, c.establishEpochFloor(context.Background()))
}

// --- reconcileProbe (phase 2: probe → force-disconnect) ---------------------

// TestReconcileProbeDisconnectsOnlyRebirthedUnseen is the check-that-cannot-fail for the
// missed-death hole (ADR-067 SP4b): among the nodes it actually rebirthed, a device that
// produced traffic is left alone and a silent one is DISCONNECTED. The mutation control
// is the "alive" device — drop the seen guard and presenceCount becomes 2. The floor is
// set (as phase 1 would) above the receipt clock so the stale epoch exceeds the stored
// session and the projection guard accepts the DISCONNECT.
func TestReconcileProbeDisconnectsOnlyRebirthedUnseen(t *testing.T) {
	const bigSession = uint64(5_000_000_000_000_000_000)
	fake := &fakeIngester{}
	c, rebirthed := reconcileClient(fake, &fakeReconciler{})
	c.sessions.SetEpochFloor(bigSession + 1) // phase 1 would have done this

	devices := []AssertedDevice{
		{ExternalId: "plant-a/alive", SessionId: bigSession},
		{ExternalId: "plant-a/dead", SessionId: bigSession},
	}
	c.resetSeen()
	c.markSeen("plant-a/alive")
	c.reconcileProbe(context.Background(), devices)

	assert.ElementsMatch(t, []nodeKey{{group: "plant-a", node: "alive"}, {group: "plant-a", node: "dead"}}, *rebirthed,
		"both nodes are rebirthed before the probe")
	require.Equal(t, 1, fake.presenceCount(), "only the silent, rebirthed device is force-disconnected")
	ev := fake.presence[0]
	assert.Equal(t, "plant-a/dead", ev.ExternalId)
	assert.False(t, ev.Connected)
	assert.Equal(t, "reconcile-timeout", ev.Reason)
	assert.Greater(t, ev.SessionId, bigSession, "the stale epoch must exceed the stored session so the guard accepts it")
	assert.Equal(t, fixedNow(), ev.OccurredAt, "stamped with the host receipt clock")
}

// TestReconcileProbeSkipsUnrebirthedNodes proves F7: a node whose rebirth never reached
// the wire is NOT force-disconnected for staying silent — we never asked it to speak.
func TestReconcileProbeSkipsUnrebirthedNodes(t *testing.T) {
	fake := &fakeIngester{}
	c, _ := reconcileClient(fake, &fakeReconciler{})
	c.sessions.SetEpochFloor(1000)
	// Rebirth publishing FAILS (e.g. queue/broker issue) for every node.
	c.rebirthPub = func(_, _ string) bool { return false }

	c.resetSeen()
	c.reconcileProbe(context.Background(), []AssertedDevice{{ExternalId: "plant-a/n1", SessionId: 10}})
	assert.Equal(t, 0, fake.presenceCount(), "a node whose rebirth never went out must not be declared dead")
}

// TestReconcileProbeAbortsOnConnectionLoss is the BLOCKER (F1) guard: if the broker
// connection drops during the probe, the probe must abort WITHOUT force-disconnecting
// anyone — a probe that went deaf must never declare a mass death. Here the rebirth
// publisher cancels the session context as its side effect, so by the time the probe
// reaches its window the connection is "gone".
func TestReconcileProbeAbortsOnConnectionLoss(t *testing.T) {
	fake := &fakeIngester{}
	c, _ := reconcileClient(fake, &fakeReconciler{})
	c.sessions.SetEpochFloor(1000)
	c.probeWindow = 10 * time.Second // long — the abort, not the timer, must end it

	ctx, cancel := context.WithCancel(context.Background())
	c.rebirthPub = func(_, _ string) bool { cancel(); return true } // connection drops mid-reconcile

	c.resetSeen()
	c.reconcileProbe(ctx, []AssertedDevice{{ExternalId: "plant-a/dead", SessionId: 10}})
	assert.Equal(t, 0, fake.presenceCount(), "a probe whose connection dropped must declare nobody dead")
}

func TestReconcileProbeEmptyIsNoOp(t *testing.T) {
	fake := &fakeIngester{}
	c, _ := reconcileClient(fake, &fakeReconciler{})
	c.resetSeen()
	c.reconcileProbe(context.Background(), nil)
	assert.Equal(t, 0, fake.presenceCount())
}

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

// --- Reconciler.AssertedActive (device-state read → floor input) ------------

// TestAssertedActiveParsesFloorsAndSkips pins the reconcile read: sessionId arrives as
// a String (a UnixNano that overflows a 32-bit Int), the max across the set is the
// epoch-floor input, and a row that cannot anchor the reconcile — a null external id
// (no Sparkplug identity) or an unparseable sessionId (would poison the floor) — is
// dropped rather than allowed to corrupt the result.
func TestAssertedActiveParsesFloorsAndSkips(t *testing.T) {
	gql := &fakeGraphQL{responder: func(_ string, _ map[string]any) (any, error) {
		return map[string]any{"assertedActiveDeviceStates": []map[string]any{
			{"externalId": "plant-a/n1", "sessionId": "100"},
			{"externalId": "plant-a/n1/d1", "sessionId": "250"},
			{"externalId": nil, "sessionId": "999"},             // null external id → skipped
			{"externalId": "plant-a/n2", "sessionId": "notnum"}, // unparseable session → skipped
		}}, nil
	}}
	r := NewReconciler(gql, "http://device-state/graphql")

	devices, max, err := r.AssertedActive(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, uint64(250), max, "the floor is the max sessionId among the kept rows")
	require.Len(t, devices, 2, "the null-externalId and bad-session rows are dropped")
	assert.Equal(t, "plant-a/n1", devices[0].ExternalId)
	assert.Equal(t, uint64(100), devices[0].SessionId)
	assert.Equal(t, "plant-a/n1/d1", devices[1].ExternalId)
	assert.Equal(t, uint64(250), devices[1].SessionId)
}

func TestAssertedActivePropagatesReadError(t *testing.T) {
	gql := &fakeGraphQL{responder: func(_ string, _ map[string]any) (any, error) {
		return nil, errors.New("device-state unreachable")
	}}
	r := NewReconciler(gql, "http://device-state/graphql")
	_, _, err := r.AssertedActive(context.Background(), "acme")
	require.Error(t, err, "a read failure must surface so reconcile aborts rather than guessing")
}

// --- Client.reconcile (probe → force-disconnect) ----------------------------

// fakeReconciler is a canned device-state read for the reconcile probe tests.
type fakeReconciler struct {
	devices []AssertedDevice
	max     uint64
	err     error
}

func (f *fakeReconciler) AssertedActive(_ context.Context, _ string) ([]AssertedDevice, uint64, error) {
	return f.devices, f.max, f.err
}

// reconcileClient builds a client wired with a fake ingester + reconciler and a tiny
// probe window, ready for a direct reconcile() call.
func reconcileClient(fake *fakeIngester, rec reconcileSource, groups ...string) *Client {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h", Groups: groups},
		Broker{}, fake, fixedNow, Metrics{})
	c.SetReconciler(rec)
	c.probeWindow = 5 * time.Millisecond
	return c
}

func drainRebirths(c *Client) []nodeKey {
	out := make([]nodeKey, 0)
	for {
		select {
		case k := <-c.rebirthCh:
			out = append(out, k)
		default:
			return out
		}
	}
}

// TestReconcileDisconnectsOnlyUnseenAssertedDevices is the check-that-cannot-fail for
// the missed-death hole (ADR-067 SP4b): a device that produced traffic this session is
// left alone; a device that stayed silent through the probe is declared DISCONNECTED.
// The mutation control is the "alive" device — if the seen guard were dropped, it too
// would be disconnected and presenceCount would be 2, not 1. maxSession is chosen ABOVE
// the receipt clock's nanoseconds so the epoch floor is load-bearing: without
// SetEpochFloor the minted stale epoch would be BELOW maxSession and the projection
// guard would reject the DISCONNECT — so the `> maxSession` assertion fails if the floor
// is ever removed.
func TestReconcileDisconnectsOnlyUnseenAssertedDevices(t *testing.T) {
	const bigSession = uint64(5_000_000_000_000_000_000) // > fixedNow (≈1.7e18): the floor must lift past it
	fake := &fakeIngester{}
	c := reconcileClient(fake, &fakeReconciler{
		devices: []AssertedDevice{
			{ExternalId: "plant-a/alive", SessionId: bigSession},
			{ExternalId: "plant-a/dead", SessionId: bigSession},
		},
		max: bigSession,
	})

	c.resetSeen()
	c.markSeen("plant-a/alive") // this node re-birthed / kept talking this session
	c.reconcile(context.Background())

	require.Equal(t, 1, fake.presenceCount(), "only the silent device is force-disconnected")
	ev := fake.presence[0]
	assert.Equal(t, "plant-a/dead", ev.ExternalId)
	assert.False(t, ev.Connected, "a reconcile timeout is a DISCONNECTED")
	assert.Equal(t, "reconcile-timeout", ev.Reason)
	assert.Greater(t, ev.SessionId, bigSession,
		"the stale epoch must exceed every stored session (SetEpochFloor) so the projection guard accepts it")
	assert.Equal(t, fixedNow(), ev.OccurredAt, "stamped with the host receipt clock, not a payload ts")
}

// TestReconcileRebirthsDistinctInScopeNodes proves the rebirth-all step collapses the
// asserted devices to their distinct edge nodes (a leaf device folds onto its node) and
// commands each in-scope node to rebirth, while an out-of-scope group is skipped.
func TestReconcileRebirthsDistinctInScopeNodes(t *testing.T) {
	fake := &fakeIngester{}
	c := reconcileClient(fake, &fakeReconciler{
		devices: []AssertedDevice{
			{ExternalId: "plant-a/n1", SessionId: 10},
			{ExternalId: "plant-a/n1/d1", SessionId: 20}, // same node n1
			{ExternalId: "plant-a/n2", SessionId: 30},
			{ExternalId: "plant-b/n9", SessionId: 40}, // out of scope
		},
		max: 40,
	}, "plant-a")

	c.resetSeen()
	// Mark every in-scope id seen so this test isolates the rebirth behavior (nobody
	// gets force-disconnected).
	c.markSeen("plant-a/n1")
	c.markSeen("plant-a/n1/d1")
	c.markSeen("plant-a/n2")
	c.reconcile(context.Background())

	nodes := drainRebirths(c)
	set := map[nodeKey]bool{}
	for _, n := range nodes {
		set[n] = true
	}
	assert.Len(t, nodes, 2, "the two distinct in-scope nodes each get one rebirth")
	assert.True(t, set[nodeKey{group: "plant-a", node: "n1"}], "node n1 rebirthed")
	assert.True(t, set[nodeKey{group: "plant-a", node: "n2"}], "node n2 rebirthed")
	assert.False(t, set[nodeKey{group: "plant-b", node: "n9"}], "an out-of-scope group is never rebirthed")
	assert.Equal(t, 0, fake.presenceCount(), "all in-scope devices were seen — nobody disconnected")
}

// TestReconcileSkipsOutOfScopeGroup proves a device belonging to a group this source
// does NOT subscribe to (another Sparkplug source on the same tenant owns it) is
// neither rebirthed nor force-disconnected — the two sources cannot cross-disconnect.
func TestReconcileSkipsOutOfScopeGroup(t *testing.T) {
	fake := &fakeIngester{}
	c := reconcileClient(fake, &fakeReconciler{
		devices: []AssertedDevice{{ExternalId: "plant-b/n9", SessionId: 40}},
		max:     40,
	}, "plant-a")

	c.resetSeen()
	c.reconcile(context.Background())

	assert.Equal(t, 0, fake.presenceCount(), "an out-of-scope device is never force-disconnected")
	assert.Empty(t, drainRebirths(c), "and never rebirthed")
}

// TestReconcileEmptyIsNoOp proves a cold/empty projection (nothing believed online)
// does nothing: no rebirths, no disconnects.
func TestReconcileEmptyIsNoOp(t *testing.T) {
	fake := &fakeIngester{}
	c := reconcileClient(fake, &fakeReconciler{devices: nil, max: 0})
	c.resetSeen()
	c.reconcile(context.Background())
	assert.Equal(t, 0, fake.presenceCount())
	assert.Empty(t, drainRebirths(c))
}

// TestReconcileReadErrorFailsSafe proves a device-state read error aborts the reconcile
// — it force-disconnects NOBODY (the next reconnect retries) rather than guessing.
func TestReconcileReadErrorFailsSafe(t *testing.T) {
	fake := &fakeIngester{}
	c := reconcileClient(fake, &fakeReconciler{err: errors.New("device-state down")})
	c.resetSeen()
	c.reconcile(context.Background())
	assert.Equal(t, 0, fake.presenceCount(), "a read error must abort, never guess-disconnect")
	assert.Empty(t, drainRebirths(c))
}

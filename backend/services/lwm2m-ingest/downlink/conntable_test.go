// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConn is a minimal mux.Conn for the ConnTable: it drives AddOnClose on close and exposes
// Done()/Context() for session.ConnDead. It embeds mux.Conn so every method the table never
// calls is nil (calling one panics — deliberately loud). It models the go-coap teardown WINDOW
// (ctx cancelled + onClose consumed, done still open) via beginShutdown.
type fakeConn struct {
	mux.Conn
	id          int
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	shuttingDwn bool
	onClose     []func()
}

func newFakeConn(id int) *fakeConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeConn{id: id, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (c *fakeConn) Context() context.Context { return c.ctx }
func (c *fakeConn) Done() <-chan struct{}    { return c.done }

func (c *fakeConn) AddOnClose(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shuttingDwn {
		return // onClose list already consumed by shutdown — mirror go-coap: this never fires
	}
	select {
	case <-c.done:
		return
	default:
		c.onClose = append(c.onClose, f)
	}
}

// close models a full go-coap teardown: cancel ctx, consume+fire onClose, close done.
func (c *fakeConn) close() {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	c.cancel()
	c.shuttingDwn = true
	fns := append([]func(){}, c.onClose...)
	c.onClose = nil
	close(c.done)
	c.mu.Unlock()
	for _, f := range fns {
		f() // fire the reap synchronously so the test is deterministic
	}
}

// beginShutdown models the teardown WINDOW: ctx cancelled + onClose consumed, but done NOT yet
// closed. A reap registered now is lost and Done() is still open — only Context().Done() reveals
// the death (session.ConnDead's second select).
func (c *fakeConn) beginShutdown() {
	c.mu.Lock()
	c.cancel()
	c.shuttingDwn = true
	c.onClose = nil
	c.mu.Unlock()
}

const (
	tnA = "tenant-a"
	tnB = "tenant-b"
	tok = "pump-1"
)

// live is a test helper: the live conn for (tenant, token) and whether it is reachable now.
func live(tbl *ConnTable, tenant, deviceToken string) (mux.Conn, bool) {
	c, r := tbl.Lookup(tenant, deviceToken)
	return c, r == ReachLive
}

// TestLookupReachStates pins the three-state reachability the dispatcher's metrics split on: a
// device this adapter never served (no index entry) vs a served device that is momentarily offline
// (parked conn) vs a live device.
func TestLookupReachStates(t *testing.T) {
	tbl := NewConnTable()

	_, r := tbl.Lookup(tnA, "never-served")
	assert.Equal(t, ReachNotServed, r, "no index entry ⇒ not served (ack-drop, no response)")

	c := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-1", 100, c)
	_, r = tbl.Lookup(tnA, tok)
	assert.Equal(t, ReachLive, r, "a bound live conn ⇒ live")

	c.close() // park
	_, r = tbl.Lookup(tnA, tok)
	assert.Equal(t, ReachOffline, r, "a served device with a parked conn ⇒ offline (rides TTL to TIMEOUT)")
}

func TestBindThenConnReturnsLiveConn(t *testing.T) {
	tbl := NewConnTable()
	c := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-1", 100, c)

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, c, got)
}

func TestConnUnknownDeviceNotServed(t *testing.T) {
	tbl := NewConnTable()
	_, ok := live(tbl, tnA, "never-bound")
	assert.False(t, ok)
}

// Tombstone guard: a Bind for a session at or below a recorded End must not resurrect a conn for
// a device presence already called DISCONNECTED.
func TestBindTombstonedSessionRefused(t *testing.T) {
	tbl := NewConnTable()
	tbl.End("id-1", 100) // session 100 ended (Deregister/expiry)
	tbl.Bind(tnA, tok, "id-1", 100, newFakeConn(1))

	_, ok := live(tbl, tnA, tok)
	assert.False(t, ok, "a Bind at the tombstoned epoch must not install")

	// A strictly-higher session (a real re-Register) is allowed to take over.
	c2 := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-1", 101, c2)
	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, c2, got)
}

func TestBindHigherEpochSupersedes(t *testing.T) {
	tbl := NewConnTable()
	tbl.Bind(tnA, tok, "id-1", 100, newFakeConn(1))
	c2 := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-1", 200, c2)

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, c2, got)
}

func TestBindLowerEpochLoses(t *testing.T) {
	tbl := NewConnTable()
	c2 := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-1", 200, c2)
	tbl.Bind(tnA, tok, "id-1", 100, newFakeConn(1)) // stale — loses

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, c2, got, "a lower-epoch Bind must not overwrite the live session")
}

// Equal-epoch ABA (positive): a re-handshaked sleeper's new LIVE conn at the same session wins.
func TestEqualEpochNewLiveConnWins(t *testing.T) {
	tbl := NewConnTable()
	tbl.Bind(tnA, tok, "id-1", 100, newFakeConn(1))
	c2 := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-1", 100, c2)

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, c2, got)
}

// Equal-epoch ABA (negative): a LATE Bind arriving on an already-DEAD older conn at the same
// epoch must not evict the live successor. This is the `!session.ConnDead(newConn)` guard.
func TestEqualEpochLateBindOnDeadConnDoesNotEvict(t *testing.T) {
	tbl := NewConnTable()
	liveConn := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-1", 100, liveConn)

	dead := newFakeConn(3)
	dead.beginShutdown() // this conn is already dead when the late Bind lands
	tbl.Bind(tnA, tok, "id-1", 100, dead)

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, liveConn, got, "a late Bind on a dead conn must not evict the live successor")
}

// Park-on-close: a conn close nils the conn but KEEPS the entry + token index, so Conn reports
// not-connected while the index survives for a later Update to heal.
func TestParkOnCloseKeepsIndexConnGone(t *testing.T) {
	tbl := NewConnTable()
	c := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-1", 100, c)
	c.close() // fires the reap

	_, ok := live(tbl, tnA, tok)
	assert.False(t, ok, "a parked entry reports not-connected")
}

// The unconditional heal (S2): a re-handshaked sleeper that sends only an Update (not a Register)
// on a new conn must have its conn healed onto the new conn — regardless of any telemetry. Without
// this the device is CONNECTED but command-dark until its lifetime lapses.
func TestRefreshHealsParkedConn(t *testing.T) {
	tbl := NewConnTable()
	c1 := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-1", 100, c1)
	c1.close() // park

	c2 := newFakeConn(2)
	tbl.Refresh("id-1", c2)

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok, "the Update heal must restore command reachability")
	assert.Same(t, c2, got)
}

func TestRefreshNilEntryNoop(t *testing.T) {
	tbl := NewConnTable()
	tbl.Refresh("never-bound", newFakeConn(1)) // must not panic
	_, ok := live(tbl, tnA, tok)
	assert.False(t, ok)
}

// Credential-rotation overlap heal: two identities share a (tenant, token). When the sibling that
// last claimed the token index ends, the index is removed; the other identity's next Update must
// RECLAIM the index (it went missing) so its device is reachable again.
func TestRefreshReclaimsMissingIndex(t *testing.T) {
	tbl := NewConnTable()
	cA := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-A", 100, cA) // A claims the index
	cB := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-B", 200, cB) // B (a rotated credential) claims the index

	tbl.End("id-B", 200) // B deregisters — pointer-guarded removal drops the index (points at B)
	_, ok := live(tbl, tnA, tok)
	require.False(t, ok, "with the index gone the live A is momentarily unreachable")

	cA2 := newFakeConn(3)
	tbl.Refresh("id-A", cA2) // A's next keepalive Update reclaims the missing index
	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, cA2, got)
}

// BLOCKER-2 regression: the index reclaim must run on a SAME-CONN Update, not only when the conn
// changes. A rotation sibling's End clears the index while this identity stays live on its ORIGINAL
// conn; its periodic keepalive Update (same conn) must reclaim the index — else the live device is
// command-dark until it disconnects or its lifetime lapses (up to a day).
func TestRefreshReclaimsIndexOnSameConn(t *testing.T) {
	tbl := NewConnTable()
	cA := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-A", 100, cA) // A live, holds the index, on conn cA
	cB := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-B", 200, cB) // rotation sibling B claims the index

	tbl.End("id-B", 200) // B ends — index cleared (pointed at B)
	_, ok := live(tbl, tnA, tok)
	require.False(t, ok, "the index is momentarily gone")

	tbl.Refresh("id-A", cA) // A keepalives on its ORIGINAL, still-live conn — same conn, no heal needed
	got, ok := live(tbl, tnA, tok)
	require.True(t, ok, "a same-conn Update must reclaim the missing index")
	assert.Same(t, cA, got)
}

// Refresh must RECLAIM an unowned index but never STEAL one a live sibling holds — else two
// concurrently-live rotated credentials ping-pong the token index.
func TestRefreshDoesNotStealIndexFromLiveSibling(t *testing.T) {
	tbl := NewConnTable()
	cA := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-A", 100, cA)
	cB := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-B", 200, cB) // B holds the index now

	tbl.Refresh("id-A", newFakeConn(3)) // A Updates — must NOT steal the index from live B

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok)
	assert.Same(t, cB, got, "a live sibling keeps the token index")
}

// End must protect a strictly-higher successor: a slow expiry hook for an old session must not
// evict the conn a newer session already installed for the identity.
func TestEndProtectsHigherSuccessor(t *testing.T) {
	tbl := NewConnTable()
	tbl.Bind(tnA, tok, "id-1", 100, newFakeConn(1))
	c2 := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-1", 200, c2) // successor session

	tbl.End("id-1", 100) // late expiry of the OLD session

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok, "the higher successor must survive a stale End")
	assert.Same(t, c2, got)
}

// End's index removal is pointer-guarded: ending an identity that no longer holds the token index
// (a rotation sibling claimed it) must not strip the sibling's index and make it unreachable.
func TestEndDoesNotRemoveIndexHeldBySibling(t *testing.T) {
	tbl := NewConnTable()
	cA := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-A", 100, cA)
	cB := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-B", 200, cB) // B holds the index

	tbl.End("id-A", 100) // A's OWN session ends; it does not hold the index

	got, ok := live(tbl, tnA, tok)
	require.True(t, ok, "the sibling holding the index must stay reachable")
	assert.Same(t, cB, got)
}

// B2: device tokens are per-tenant unique, not global. Two tenants' same-token devices must not
// collide — each resolves to its own conn.
func TestTwoTenantsSameTokenNoCollision(t *testing.T) {
	tbl := NewConnTable()
	cA := newFakeConn(1)
	cB := newFakeConn(2)
	tbl.Bind(tnA, tok, "id-A", 100, cA)
	tbl.Bind(tnB, tok, "id-B", 100, cB) // SAME token, different tenant

	gotA, okA := live(tbl, tnA, tok)
	require.True(t, okA)
	assert.Same(t, cA, gotA)
	gotB, okB := live(tbl, tnB, tok)
	require.True(t, okB)
	assert.Same(t, cB, gotB, "same token in another tenant must not shadow the first")
}

// Conn must report not-connected when the entry's conn has died in the go-coap teardown window
// (ctx cancelled, done still open) even before the reap fires.
func TestConnReportsDeadConnInTeardownWindow(t *testing.T) {
	tbl := NewConnTable()
	c := newFakeConn(1)
	tbl.Bind(tnA, tok, "id-1", 100, c)
	c.beginShutdown() // ctx cancelled, done still open, reap NOT fired

	_, ok := live(tbl, tnA, tok)
	assert.False(t, ok, "a conn dead in the teardown window must read as not-connected")
}

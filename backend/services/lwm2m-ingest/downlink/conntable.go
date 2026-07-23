// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package downlink is the LwM2M command path (ADR-075, slice L4a): the first true downlink
// adapter in the platform. command-delivery publishes a command to a device's per-device
// JetStream subject; a CoAP device cannot subscribe to NATS, so this package consumes that
// stream on the device's behalf, maps the command to a CoAP Read/Write/Execute on the
// device's live DTLS session, and reports the outcome back on command-responses.
//
// ConnTable is the seam that makes dispatch possible: given the (tenant, deviceToken) a
// command names, it returns the device's CURRENT live CoAP conn — or "not connected." A
// conn is a per-DTLS-session thing keyed by the authenticated identity (ADR-075 D1); a
// command names a device token; so the table maintains (tenant, deviceToken) → identity →
// live conn across the same session lifecycle the presence registry drives:
//   - a fresh Register (a new, higher session epoch) Binds the conn and claims the token;
//   - a queue-mode sleeper that re-handshakes on a NEW conn and sends only an Update Refreshes
//     the conn onto the new conn — WITHOUT that heal a re-handshaked device is CONNECTED but
//     command-DARK until its lifetime lapses (the same hole observe's Reestablish closes for
//     telemetry); the heal is UNCONDITIONAL of any telemetry the device does or does not have;
//   - a conn close PARKS the entry (conn=nil, keep the token index) so the next Update heals it;
//     deleting would strand the token→identity mapping until a full re-Register;
//   - a session that truly ends (Deregister/expiry) is tombstoned so a late Bind cannot resurrect
//     a conn for a device presence already called DISCONNECTED.
//
// It deliberately mirrors observe's identity→conn lifecycle rather than sharing it: observe
// can be absent entirely (a presence-only / actuator deployment tracks no telemetry), and its
// conn commit is entangled with the Observe network I/O; this table's Bind is a synchronous,
// I/O-free CAS, so it carries the SAME hazards (epoch-CAS, the equal-epoch ABA, park-on-close,
// the go-coap connDead teardown window, the session-end tombstone) but not the harder
// commit-after-I/O dance. The one genuinely subtle primitive — session.ConnDead — is shared.
package downlink

import (
	"sync"

	"github.com/plgd-dev/go-coap/v3/mux"

	"github.com/devicechain-io/dc-lwm2m-ingest/session"
)

// deviceKey is the (tenant, deviceToken) a command addresses. The tenant is part of the key
// because device tokens are UNIQUE PER TENANT, not globally (ADR-042): two tenants can each
// have a device token "pump-1", and keying on the token alone would let the last-registered
// tenant's device shadow the other's — silently making a live device unreachable for commands.
// The command subject carries the tenant, so the dispatcher has it before every lookup.
type deviceKey struct {
	tenant      string
	deviceToken string
}

// connEntry is one identity's live-conn state. It is mutated only under ConnTable.mu. A PARKED
// entry (conn nil) survives a conn close so the identity's next Update re-heals it.
type connEntry struct {
	epoch       uint64
	conn        mux.Conn // nil when parked (the conn closed); an Update Refresh restores it
	tenant      string
	deviceToken string
}

// ConnTable maps a device's (tenant, deviceToken) to its current live CoAP conn, by way of the
// authenticated identity that owns the DTLS session. It is safe for concurrent use; mu is a
// leaf — no other lock is taken under it and no blocking I/O runs while it is held.
type ConnTable struct {
	mu         sync.Mutex
	byIdentity map[string]*connEntry // identity -> live or parked conn entry
	byDevice   map[deviceKey]string  // (tenant, deviceToken) -> identity
	// ended is the per-identity session-end tombstone (set only by End); it is never pruned, but its
	// keys are authenticated PSK identities, which are config-bounded (one per provisioned
	// credential), so it cannot grow without bound.
	ended map[string]uint64 // identity -> highest ENDED session epoch
	// onLive, when set, is invoked (outside mu) after a device becomes reachable on a FRESH conn — a
	// winning Bind (Register) or a Refresh that heals onto a new conn (a queue-mode sleeper's
	// re-handshake). These are exactly the LwM2M wake signals, so it drives the L4b wake-drain
	// (wired to Dispatcher.Drain). It is deliberately NOT fired on a keepalive no-op Refresh: a
	// device that stayed live got its commands via the live dispatch path, so re-draining every
	// keepalive would be pure query load. Set once before serving; read on handler goroutines.
	onLive func(tenant, deviceToken string)
}

// NewConnTable builds an empty table.
func NewConnTable() *ConnTable {
	return &ConnTable{
		byIdentity: map[string]*connEntry{},
		byDevice:   map[deviceKey]string{},
		ended:      map[string]uint64{},
	}
}

// SetOnLive installs the wake hook invoked when a device becomes reachable on a fresh conn (Register,
// or an Update that re-handshakes). It must be called before serving begins (single-threaded wiring),
// since Bind/Refresh read it on handler goroutines with no lock.
func (t *ConnTable) SetOnLive(fn func(tenant, deviceToken string)) {
	t.onLive = fn
}

// Bind records an identity's live conn for a session (epoch) and claims its (tenant,
// deviceToken) for dispatch. The presence handler calls it on a fresh Register (RegisterOK),
// where the device token has just been resolved. It is a synchronous CAS under the lock:
//   - a tombstoned session (epoch at or below a recorded End) is refused — no conn after DISCONNECTED;
//   - a strictly-higher epoch always supersedes; an equal epoch wins only on a new LIVE conn or when
//     the current conn is dead (the equal-epoch ABA guard — a late Bind on an already-dead older conn
//     must not evict a live successor);
//   - a lower epoch loses.
//
// On a win it installs the entry, (re)points the token index at this identity, arms a park-on-close
// reap, and — closing the go-coap teardown window — reaps inline if the conn already died.
func (t *ConnTable) Bind(tenant, deviceToken, identity string, epoch uint64, conn mux.Conn) {
	t.mu.Lock()
	if !t.canWinLocked(identity, epoch, conn) {
		t.mu.Unlock()
		return
	}
	t.byIdentity[identity] = &connEntry{epoch: epoch, conn: conn, tenant: tenant, deviceToken: deviceToken}
	t.byDevice[deviceKey{tenant, deviceToken}] = identity
	t.mu.Unlock()

	// Register the reap AFTER the commit; a go-coap AddOnClose registered after the conn's
	// shutdown never fires, so if the conn already died in this window, reap inline.
	conn.AddOnClose(func() { t.reap(identity, epoch, conn) })
	if session.ConnDead(conn) {
		t.reap(identity, epoch, conn)
	}
	// A fresh registration is a wake signal: drain any commands held while the device was offline.
	// The drain re-checks liveness, so a bind that raced a conn death (reaped above) is a safe no-op.
	t.fireOnLive(tenant, deviceToken)
}

// Refresh heals an identity's conn onto a new one after an Update, reusing the entry's stored
// session epoch. It is the queue-mode heal: a sleeper that re-handshakes sends an Update (not a
// Register) on a new conn, and without this the entry keeps pointing at the dead conn and the
// device is command-dark. It is a cheap no-op when the conn already rides the entry. A nil entry
// (never bound — a 1.0-only presence-only device, or state lost to a term rebuild) is nothing to
// heal. It also re-claims the token index if it went missing (e.g. a credential-rotation overlap
// where a sibling identity's End removed it) — but never STEALS it from another identity that
// currently holds it, so two concurrently-live credentials do not ping-pong the index.
func (t *ConnTable) Refresh(identity string, conn mux.Conn) {
	t.mu.Lock()
	cur := t.byIdentity[identity]
	if cur == nil {
		t.mu.Unlock()
		return
	}
	// Reclaim the (tenant, deviceToken) index if it went missing — a credential-rotation sibling's
	// End cleared it while THIS identity stayed live. This MUST run on every Refresh of a live
	// entry, BEFORE the conn-CAS early-return below: a device that never disconnected keepalives on
	// the SAME live conn, so the ABA guard no-ops, yet its command reachability still has to heal.
	// It is steal-proof — reclaim only if unowned, never take the index from a live sibling.
	key := deviceKey{cur.tenant, cur.deviceToken}
	if _, claimed := t.byDevice[key]; !claimed {
		t.byDevice[key] = identity
	}
	// The conn heal is the equal-epoch ABA CAS: adopt a new live conn, or when the current conn is
	// dead; a late Update on an already-dead older conn must not evict a live successor. When the
	// conn is unchanged and live this is a no-op (the common keepalive) — but the index reclaim above
	// has already run.
	if !((cur.conn != conn && !session.ConnDead(conn)) || session.ConnDead(cur.conn)) {
		t.mu.Unlock()
		return
	}
	cur.conn = conn
	epoch := cur.epoch
	tenant, deviceToken := cur.tenant, cur.deviceToken
	t.mu.Unlock()

	conn.AddOnClose(func() { t.reap(identity, epoch, conn) })
	if session.ConnDead(conn) {
		t.reap(identity, epoch, conn)
	}
	// This Refresh HEALED onto a fresh conn — a queue-mode sleeper's re-handshake, i.e. a wake.
	// Drain any commands held while it was offline. (The common keepalive on an unchanged live conn
	// took the early return above and does NOT fire — it never went offline.)
	t.fireOnLive(tenant, deviceToken)
}

// fireOnLive invokes the wake hook (if set) outside t.mu. It must never be called under the lock —
// the hook enqueues a drain, and mu is a leaf.
func (t *ConnTable) fireOnLive(tenant, deviceToken string) {
	if t.onLive != nil {
		t.onLive(tenant, deviceToken)
	}
}

// Reach is a device's command reachability, so the dispatcher can tell a command it should not
// have received (a device on another protocol, or none) from one it received for a device it
// serves but that is momentarily offline — an operationally meaningful split (the latter is the
// interesting signal; the former is just a mirror of the whole instance's command traffic).
type Reach int

const (
	// ReachNotServed — no (tenant, deviceToken) index entry: this adapter does not serve the device
	// (it belongs to another protocol adapter, or nothing). The command is ack-dropped with no response.
	ReachNotServed Reach = iota
	// ReachOffline — a served device with no live conn right now (parked or dead). The live command
	// is ack-dropped; it stays held in command-delivery (SENT) and is drained to the device on its
	// next Register/Update wake (L4b), or reaches TIMEOUT at its TTL if it never wakes.
	ReachOffline
	// ReachLive — a served device with a live conn: dispatch.
	ReachLive
)

// Lookup returns the current live conn for a device (tenant, deviceToken) and its reachability.
// The conn is non-nil only when Reach is ReachLive. Keying on (tenant, deviceToken) is what keeps
// two tenants' identically-tokened devices from colliding (device tokens are per-tenant unique,
// ADR-042).
func (t *ConnTable) Lookup(tenant, deviceToken string) (mux.Conn, Reach) {
	t.mu.Lock()
	defer t.mu.Unlock()
	identity, ok := t.byDevice[deviceKey{tenant, deviceToken}]
	if !ok {
		return nil, ReachNotServed
	}
	cur := t.byIdentity[identity]
	if cur == nil || cur.conn == nil || session.ConnDead(cur.conn) {
		return nil, ReachOffline
	}
	return cur.conn, ReachLive
}

// End tears down an identity's session (epoch) — the presence registry calls it through
// OnSessionEnd on a Deregister or a lifetime-expiry. It tombstones the epoch (so a late Bind for
// it cannot resurrect a conn for a DISCONNECTED device) and removes the entry, but only if a
// strictly-higher successor session has not already taken over the identity (a slow expiry hook
// must not evict the successor). The token index is cleared only if it still points at THIS
// identity — a credential-rotation sibling that claimed the same token keeps it.
func (t *ConnTable) End(identity string, epoch uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.ended[identity]; !ok || epoch > e {
		t.ended[identity] = epoch
	}
	cur := t.byIdentity[identity]
	if cur == nil || cur.epoch > epoch {
		return // no entry, or a strictly-higher successor owns it — protect the successor
	}
	delete(t.byIdentity, identity)
	if t.byDevice[deviceKey{cur.tenant, cur.deviceToken}] == identity {
		delete(t.byDevice, deviceKey{cur.tenant, cur.deviceToken})
	}
}

// canWinLocked decides whether (identity, epoch, conn) may install/replace the identity's entry.
// The caller holds t.mu.
func (t *ConnTable) canWinLocked(identity string, epoch uint64, conn mux.Conn) bool {
	if e, ok := t.ended[identity]; ok && epoch <= e {
		return false // this session (at or below epoch) has ended — no conn after DISCONNECTED
	}
	cur := t.byIdentity[identity]
	if cur == nil {
		return true
	}
	if epoch > cur.epoch {
		return true // a fresh Register (higher session) always supersedes
	}
	if epoch == cur.epoch {
		return (cur.conn != conn && !session.ConnDead(conn)) || session.ConnDead(cur.conn)
	}
	return false
}

// reap PARKS an identity's entry when its conn closes (registered via AddOnClose): it drops the
// dead conn but KEEPS the entry and its token index, so the device's next Update re-heals it.
// Deleting here would strand the (tenant, deviceToken)→identity mapping until a full re-Register,
// leaving a re-handshaked sleeper command-dark. Guarded by epoch AND conn so a reap after a
// replace (a newer session, or a heal onto a newer conn) no-ops.
func (t *ConnTable) reap(identity string, epoch uint64, conn mux.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := t.byIdentity[identity]
	if cur == nil || cur.epoch != epoch || cur.conn != conn {
		return
	}
	cur.conn = nil
}

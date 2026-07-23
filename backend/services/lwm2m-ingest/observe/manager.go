// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package observe is the LwM2M telemetry lifecycle over the L0 transport (ADR-075, slice
// L2b): after a device Registers (registry, L1), the server issues an Observe (a downlink
// GET with Observe, Accept: SenML-JSON) against each of the device's telemetry object
// instances on the SAME authenticated DTLS conn the registration arrived on — CoAP is
// symmetric — and folds every resulting Notify onto the canonical measurement model through
// the shared ingest adapter (adapter.Ingester, the durable emit path Sparkplug also uses).
//
// It is deliberately BESIDE the registry, not inside it: the registry stays a pure presence
// machine (fake resolver/emitter, no transport), and this Manager owns ALL conn/observation
// state, keyed by the stable authenticated identity. The handler orchestrates the two — it
// calls Establish after a Register and Reestablish after an Update; the registry calls Cancel
// through an OnSessionEnd hook on Deregister/lifetime-expiry.
//
// The lifecycle is a compare-and-swap on a per-identity slot, guarded by the presence session
// epoch so the identity's observations always ride the newest live conn:
//   - a fresh Register (higher epoch) always wins and cancels the superseded session's obs;
//   - a queue-mode sleeper that re-handshakes on a NEW conn and sends Update (SAME epoch)
//     re-establishes on the new conn — the biggest functional hole this slice closes: without
//     it the device is CONNECTED but telemetry-dark until its lifetime expires (up to a day);
//   - conn close PARKS the slot (keeps epoch+paths, drops the dead conn) so the next Update
//     heals it — it does NOT delete the slot, which would strand the sleeper's paths;
//   - a session that truly ends (Deregister/expiry) is tombstoned so a late in-flight Establish
//     for that epoch cannot resurrect a zombie observation for a device we called DISCONNECTED.
//
// HA: single serving replica (ADR-075 / ADR-070). A restart loses every registration AND every
// observation; neither is reconstructable without a conn (L3 failover restores PRESENCE only,
// never observations), so recovery waits on device re-Register behaviour — device-controlled,
// up to the 86400s default lifetime. The one lever is clamping the maximum registration
// lifetime down (registry R5), trading presence-write volume for a bounded telemetry blackout.
//
// Named L2 boundaries (not bugs): a conformant LwM2M 1.0-only client answers the SenML Observe
// with 4.06 Not Acceptable (SenML is 1.1+), so it keeps L1 presence with ZERO telemetry until
// the TLV decode follow-up; boolean-primary IPSO objects yield zero numeric samples by ADR-016
// design; partial or transient establish failures are counted and KEPT (healed on re-Register,
// not mid-session); an Update that carries a new object list is ignored (re-Register observes it).
package observe

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/decode"
)

// Default lifecycle timeouts. Each downlink exchange is bounded so a queue-mode sleeper that
// never answers cannot pin the establishing goroutine, and a best-effort cancel to a sleeper
// cannot serialize other work.
const (
	// DefaultObserveTimeout bounds ONE Observe establish exchange (a CON GET+Observe to the
	// device). A device that does not answer within it fails that instance (counted, kept).
	DefaultObserveTimeout = 5 * time.Second
	// DefaultCancelTimeout bounds a best-effort deregister GET. Cancel does a LOCAL cleanup
	// FIRST (go-coap removes the token handler before the network GET), so a timed-out cancel
	// to a sleeper leaks nothing locally — the entry is gone regardless.
	DefaultCancelTimeout = 3 * time.Second
	// DefaultIngestTimeout bounds one Notify's resolve+emit through the shared ingester.
	DefaultIngestTimeout = 5 * time.Second
)

// errObserveNotSupported marks an Observe the device answered without an Observe option (a
// one-shot / non-observable resource) — go-coap hands back an already-cancelled handle we must
// not track. It is internal; the Establish loop just skips the instance.
var errObserveNotSupported = errors.New("lwm2m: resource is not observable (one-shot response)")

// Target is the tenancy correlation for a device's Notifies → ingest. It is fully derivable
// from the authenticated PSK binding (config.PskBinding), so a Notify needs no fresh resolve:
// the ingester resolves+caches the device token from (tenant, externalId, policy).
type Target struct {
	Tenant     string
	ExternalId string
	Policy     adapter.IngestPolicy
}

// ingester is the narrow slice of *adapter.Ingester the Manager needs; depending on the
// interface keeps the Manager unit-testable without a live device-management/NATS.
type ingester interface {
	Ingest(ctx context.Context, tenant string, policy adapter.IngestPolicy, externalId string, samples []adapter.Sample) error
}

// Metrics are the optional Prometheus instruments the Manager updates; any nil field is
// skipped so it is usable in tests without a registry. NONE is labeled by tenant/identity — a
// per-tenant label on a device-driven counter is the cardinality risk the governance lesson
// (ADR-023) warns against.
type Metrics struct {
	NotifiesReceived        prometheus.Counter // Notify messages delivered on an observed instance
	DecodeFailures          prometheus.Counter // a Notify body that could not be decoded (malformed / unreadable)
	UnknownContentFormat    prometheus.Counter // a Notify in a content format this slice does not decode (e.g. TLV)
	ObserveEstablishRefused prometheus.Counter // an Observe GET refused/failed (dominant cause: a 1.0-only client's 4.06)
	TerminalNotifications   prometheus.Counter // a non-2.05 notification that terminated an observation (RFC 7641)
	IngestDropped           prometheus.Counter // a Notify dropped on a retryable ingest error (no retry in the callback)
	ActiveObservations      prometheus.Gauge   // live observations currently held across all sessions
}

// slot is one identity's observation state. It is mutated only under Manager.mu. A PARKED
// slot (conn nil, obs nil) survives conn-close so the next Update re-establishes its paths.
type slot struct {
	epoch  uint64
	conn   mux.Conn // nil when parked (the conn closed); a re-establish restores it
	target Target
	paths  []string                   // the observed instance paths, kept for Update re-establish
	obs    map[string]mux.Observation // path -> live observation
}

// Manager is the in-memory per-identity observation table and the lifecycle over it. It is
// safe for concurrent use; mu is a leaf (no other lock is taken under it, and no blocking
// network I/O runs while it is held).
type Manager struct {
	mu    sync.Mutex
	slots map[string]*slot  // identity -> live or parked slot
	ended map[string]uint64 // identity -> highest ENDED session epoch (tombstone; set only by Cancel)

	ingester ingester
	metrics  Metrics

	now            func() time.Time
	observeTimeout time.Duration
	cancelTimeout  time.Duration
	ingestTimeout  time.Duration
}

// Options configures a Manager. Zero-valued fields take their package defaults.
type Options struct {
	Now            func() time.Time // nil ⇒ time.Now (the Notify receipt clock for a relative/absent SenML time)
	ObserveTimeout time.Duration
	CancelTimeout  time.Duration
	IngestTimeout  time.Duration
}

// NewManager builds a Manager over the shared ingester.
func NewManager(ing ingester, metrics Metrics, opts Options) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ObserveTimeout <= 0 {
		opts.ObserveTimeout = DefaultObserveTimeout
	}
	if opts.CancelTimeout <= 0 {
		opts.CancelTimeout = DefaultCancelTimeout
	}
	if opts.IngestTimeout <= 0 {
		opts.IngestTimeout = DefaultIngestTimeout
	}
	return &Manager{
		slots:          map[string]*slot{},
		ended:          map[string]uint64{},
		ingester:       ing,
		metrics:        metrics,
		now:            opts.Now,
		observeTimeout: opts.ObserveTimeout,
		cancelTimeout:  opts.CancelTimeout,
		ingestTimeout:  opts.IngestTimeout,
	}
}

// Establish (re)establishes observations for an identity's session (epoch) on conn, observing
// each path. It is the goroutine body the handler spawns AFTER the Register response — go-coap
// writes the 2.01 only after the handler returns, so an in-handler synchronous Observe would
// GET a client still awaiting its 2.01. It returns whether this call installed the session's
// observations (false when a newer/equal session already owns the identity, or the session was
// tombstoned). A winning call with an EMPTY path set still commits (an empty slot) so it
// cancels the predecessor — a re-Register whose object list yields no telemetry must not leave
// the old session's observations streaming forever.
func (m *Manager) Establish(identity string, epoch uint64, conn mux.Conn, target Target, paths []string) bool {
	m.mu.Lock()
	cur := m.slots[identity]
	if !m.canWinLocked(epoch, cur, conn, identity) {
		m.mu.Unlock()
		return false // clearly cannot win — skip the (possibly slow) Observe I/O
	}
	m.mu.Unlock()

	// Establish each observation OUTSIDE the manager mutex: each is a downlink CON GET that
	// can block up to observeTimeout (to a queue-mode sleeper, a real wait). Partial failures
	// are counted (ObserveEstablishRefused) and KEPT — a 1.0-only client 4.06s every SenML
	// Observe; a transient failure heals on the device's next re-Register (a named boundary).
	newObs := make(map[string]mux.Observation, len(paths))
	for _, p := range paths {
		obs, err := m.observeOne(conn, identity, epoch, target, p)
		if err != nil {
			continue
		}
		newObs[p] = obs
	}

	// Commit under the lock, re-validating the CAS: a higher (or equal-on-a-newer-conn) session
	// may have installed while we were doing I/O — if so we lost, so cancel what we just made.
	m.mu.Lock()
	cur = m.slots[identity]
	if !m.canWinLocked(epoch, cur, conn, identity) {
		m.mu.Unlock()
		m.cancelAll(newObs)
		return false
	}
	old := cur
	m.slots[identity] = &slot{epoch: epoch, conn: conn, target: target, paths: paths, obs: newObs}
	m.recomputeGaugeLocked()
	m.mu.Unlock()

	// Conn-death is a first-class, free cancel path. Register the reap AFTER the commit; a
	// go-coap AddOnClose registered after the conn's shutdown never fires, so if the conn
	// already died in the I/O window, reap inline.
	conn.AddOnClose(func() { m.reap(identity, epoch, conn) })
	if connDead(conn) {
		m.reap(identity, epoch, conn)
	}

	if old != nil {
		// Cancel the superseded session's observations off the hot path — a best-effort GET to
		// a maybe-sleeping old conn must not delay this establish.
		go m.cancelAll(old.obs)
	}
	return true
}

// Reestablish heals an identity's observations onto conn after an Update, reusing the stored
// session epoch and paths. It is a cheap no-op when the observations already ride this live
// conn (the normal keepalive); it re-observes only when the device moved to a new conn (a
// queue-mode sleeper re-handshaked) or the slot was parked by a conn close. A nil slot (no
// observations tracked — a 1.0-only client, or state lost to a restart) is nothing to heal.
func (m *Manager) Reestablish(identity string, conn mux.Conn, target Target) {
	m.mu.Lock()
	cur := m.slots[identity]
	if cur == nil {
		m.mu.Unlock()
		return
	}
	epoch := cur.epoch
	paths := append([]string(nil), cur.paths...) // copy: do not hand the slot's slice to the I/O path
	m.mu.Unlock()

	if len(paths) == 0 {
		return
	}
	m.Establish(identity, epoch, conn, target, paths)
}

// Cancel ends an identity's session (epoch): the registry calls it through OnSessionEnd on a
// Deregister or a lifetime-expiry. It tombstones the epoch (so a late in-flight Establish for
// it cannot resurrect a zombie) and cancels the observations — but only if a strictly-higher
// successor session has not already taken over the identity (a slow expiry hook must not kill
// the successor's observations).
func (m *Manager) Cancel(identity string, epoch uint64) {
	m.mu.Lock()
	if e, ok := m.ended[identity]; !ok || epoch > e {
		m.ended[identity] = epoch
	}
	cur := m.slots[identity]
	if cur == nil || cur.epoch > epoch {
		m.mu.Unlock()
		return // no slot, or a strictly-higher successor owns it — protect the successor
	}
	delete(m.slots, identity)
	obs := cur.obs
	m.recomputeGaugeLocked()
	m.mu.Unlock()
	go m.cancelAll(obs) // best-effort GETs to a maybe-sleeping conn, off the caller's path
}

// canWinLocked decides whether a (session epoch, conn) may install/replace the identity's slot.
// The caller holds m.mu.
func (m *Manager) canWinLocked(epoch uint64, cur *slot, newConn mux.Conn, identity string) bool {
	if e, ok := m.ended[identity]; ok && epoch <= e {
		return false // this session (at or below epoch) has ended — no zombie after DISCONNECTED
	}
	if cur == nil {
		return true
	}
	if epoch > cur.epoch {
		return true // a fresh Register (higher session) always supersedes
	}
	if epoch == cur.epoch {
		// Same session: re-observe when the device moved to a new LIVE conn (a re-handshaked
		// sleeper, or a parked slot whose conn is nil), OR the current conn is dead. The
		// !connDead(newConn) qualifier stops a LATE establish arriving on an already-dead older
		// conn from evicting a live successor at the same epoch (equal-epoch ABA).
		return (cur.conn != newConn && !connDead(newConn)) || connDead(cur.conn)
	}
	return false
}

// observeOne issues one Observe (GET+Observe, Accept: SenML-JSON) and wires its Notify stream
// to onNotify. A refused/failed establish (dominantly a 1.0-only client's 4.06 Not Acceptable)
// is counted; the instance is simply not observed.
func (m *Manager) observeOne(conn mux.Conn, identity string, epoch uint64, target Target, path string) (mux.Observation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.observeTimeout)
	defer cancel()
	req, err := conn.NewObserveRequest(ctx, path)
	if err != nil {
		incr(m.metrics.ObserveEstablishRefused, 1)
		return nil, err
	}
	defer conn.ReleaseMessage(req)
	// Ask for SenML-JSON so a 1.1 client notifies in the format L2a decodes. A conformant
	// 1.0-only client cannot honour this and answers 4.06 (counted above via the DoObserve err).
	req.SetAccept(message.AppSenmlJSON)
	obs, err := conn.DoObserve(req, func(msg *pool.Message) {
		m.onNotify(identity, epoch, conn, path, target, msg)
	})
	if err != nil {
		incr(m.metrics.ObserveEstablishRefused, 1)
		return nil, err
	}
	// go-coap returns a non-nil Observation even when the device answered the GET+Observe with
	// a 2.05/2.03 that carried NO Observe option — the resource is not observable, a one-shot
	// read (net/observation/handler.go: it removes the token handler and returns an
	// already-cancelled handle). Storing it would count a DEAD observation as live and never
	// heal (a same-conn re-establish is a deliberate no-op). The initial response already
	// reached observeFunc (correct); we simply must not track the handle.
	if obs.Canceled() {
		incr(m.metrics.ObserveEstablishRefused, 1)
		return nil, errObserveNotSupported
	}
	return obs, nil
}

// onNotify handles one Notify. It fires on go-coap's per-conn read loop, so the blast radius
// of a synchronous decode+ingest is a single device — no head-of-line concern across devices.
func (m *Manager) onNotify(identity string, epoch uint64, conn mux.Conn, path string, target Target, msg *pool.Message) {
	incr(m.metrics.NotifiesReceived, 1)
	code := msg.Code()
	if code != codes.Content && code != codes.Valid {
		// A terminal notification (RFC 7641): e.g. 4.04 once the observed object instance is
		// deleted. go-coap does NOT auto-remove the handle on a non-2.05 in steady state, so we
		// drop it ourselves — OFF the read loop, so ordered per-conn notification processing is
		// not stalled behind Cancel's deregister GET (an in-callback Cancel does not deadlock,
		// but going off-loop keeps processing ordered and avoids reader-loop replacement).
		go m.dropObservation(identity, epoch, conn, path)
		return
	}
	cf, err := msg.ContentFormat()
	if err != nil {
		incr(m.metrics.UnknownContentFormat, 1) // no content format ⇒ nothing to decode against
		return
	}
	body, err := msg.ReadBody()
	if err != nil {
		incr(m.metrics.DecodeFailures, 1)
		return
	}
	samples, err := decode.Samples(cf, body, m.now)
	if err != nil {
		if errors.Is(err, decode.ErrUnsupportedContentFormat) {
			incr(m.metrics.UnknownContentFormat, 1)
		} else {
			incr(m.metrics.DecodeFailures, 1)
		}
		return
	}
	if len(samples) == 0 {
		return // a well-formed but non-numeric batch (e.g. a boolean IPSO object) — nothing to measure
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.ingestTimeout)
	defer cancel()
	if err := m.ingester.Ingest(ctx, target.Tenant, target.Policy, target.ExternalId, samples); err != nil {
		// A Notify is best-effort telemetry: on a retryable infra error (device-management/NATS
		// down) we count and drop; the next Notify supersedes. NO retry budget here — a 3s×N
		// retry would stall this conn's Updates behind one slow publish.
		incr(m.metrics.IngestDropped, 1)
	}
}

// dropObservation removes and cancels a single terminated observation. It is guarded by BOTH
// epoch AND conn: a stale terminal notification arriving on a superseded conn (an equal-epoch
// conn swap left the old conn's token handler live) must not remove the successor's handle.
func (m *Manager) dropObservation(identity string, epoch uint64, conn mux.Conn, path string) {
	m.mu.Lock()
	cur := m.slots[identity]
	if cur == nil || cur.epoch != epoch || cur.conn != conn {
		m.mu.Unlock()
		return // a successor session, or a different conn at the same epoch, owns this identity now
	}
	obs, ok := cur.obs[path]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(cur.obs, path)
	// Count only a real removal — a refused establish's own initial 4.06 also lands here but
	// removes nothing (the slot is not yet installed), so it must not inflate this counter.
	incr(m.metrics.TerminalNotifications, 1)
	m.recomputeGaugeLocked()
	m.mu.Unlock()
	m.cancelOne(obs)
}

// reap PARKS the identity's slot when its conn closes (registered via AddOnClose): it drops
// the dead conn and its observations but KEEPS the epoch, target, and paths, so the device's
// next Update re-establishes the same instances on the new conn. Deleting the slot here would
// strand a queue-mode sleeper's paths — the mistake that leaves it telemetry-dark. No network
// cancel: the conn is closed, its observations die with it (go-coap tears down the per-conn
// token map on close). Guarded by epoch AND conn so a reap after a replace no-ops.
func (m *Manager) reap(identity string, epoch uint64, conn mux.Conn) {
	m.mu.Lock()
	cur := m.slots[identity]
	if cur == nil || cur.epoch != epoch || cur.conn != conn {
		m.mu.Unlock()
		return
	}
	cur.conn = nil
	cur.obs = nil
	m.recomputeGaugeLocked()
	m.mu.Unlock()
}

// cancelAll cancels a set of observations best-effort (short-ctx deregister GETs).
func (m *Manager) cancelAll(obs map[string]mux.Observation) {
	for _, o := range obs {
		m.cancelOne(o)
	}
}

// cancelOne cancels one observation best-effort. Cancel does a LOCAL cleanup first, so even a
// timed-out GET to a sleeper leaves nothing dangling locally.
func (m *Manager) cancelOne(o mux.Observation) {
	ctx, cancel := context.WithTimeout(context.Background(), m.cancelTimeout)
	defer cancel()
	if err := o.Cancel(ctx); err != nil {
		log.Debug().Err(err).Msg("Best-effort cancel of an LwM2M observation failed (the entry is gone regardless).")
	}
}

// recomputeGaugeLocked sets ActiveObservations to the live observation count. Called under
// m.mu, only on install/drop/cancel/reap (never per-Notify), so its O(slots) cost is off the
// telemetry hot path.
func (m *Manager) recomputeGaugeLocked() {
	if m.metrics.ActiveObservations == nil {
		return
	}
	total := 0
	for _, s := range m.slots {
		total += len(s.obs)
	}
	m.metrics.ActiveObservations.Set(float64(total))
}

// connDead reports whether a conn is closed (or nil — a parked slot's conn). Non-blocking. It
// checks BOTH Done() and Context().Done(): go-coap tears a session down as Close()→cancel(ctx)
// FIRST, then runs the AddOnClose callbacks, then close(Done()) LAST (dtls/server/session.go).
// So there is a window — a conn dying between an Establish commit and its AddOnClose — where the
// onClose callback is registered into an already-consumed list (never fires) yet Done() is still
// open. Context().Done() has already closed by then, so checking it lets the inline reap catch
// that window instead of leaking a slot pinned to a dead conn until the next Update/session end.
func connDead(c mux.Conn) bool {
	if c == nil {
		return true
	}
	select {
	case <-c.Done():
		return true
	default:
	}
	select {
	case <-c.Context().Done():
		return true
	default:
		return false
	}
}

// incr adds n to a counter, tolerating a nil counter (tests) and n <= 0.
func incr(c prometheus.Counter, n int) {
	if c != nil && n > 0 {
		c.Add(float64(n))
	}
}

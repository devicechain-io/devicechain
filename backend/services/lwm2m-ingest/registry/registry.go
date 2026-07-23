// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package registry is the LwM2M semantic layer over the L0 transport (ADR-075, slice
// L1): the /rd registration interface and the device-presence it drives. An
// authenticated device that Registers becomes CONNECTED (ASSERTED, ADR-067); an Update
// keeps it alive; a Deregister or a lapsed lifetime makes it DISCONNECTED. Every
// transition is emitted as the same StateChange the Sparkplug adapter feeds, resolved
// and ordered by a shared session epoch (adapter.EpochSource).
//
// Tenancy is bound to the AUTHENTICATED DTLS PSK identity, never the device's own
// registration payload (ADR-075 D1) — the handler recovers the identity from the
// connection (identity.go) and resolves it to a (tenant, external id) binding. The
// Registry itself takes the resolved identity as a plain string so its logic is testable
// without a DTLS stack.
//
// HA: single serving replica (ADR-075 HA posture). The registration table is in-memory;
// a device's presence missed across a pod restart is closed by the L3 failover
// reconstruction (rebuild from device-state + lifetime-timer sweep), not here. L1 does
// read the epoch floor from device-state at startup so a restart across a wall-clock
// step-back cannot mint stale-rejected epochs (main.go wiring).
package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
)

const (
	// DefaultMinLifetime is the floor a registration lifetime is clamped up to (ADR-075
	// R5). `lt` is the LwM2M rate knob: a device (mis)configured with lt=1 would churn
	// expiry/re-register — each replace mints a fresh session and a distinct durable
	// presence write — so a too-short lifetime is raised, bounding that write rate. It is
	// the L1 stand-in for the per-tenant ingest gate (ADR-023), which lands with the
	// higher-volume Observe/Notify path (L2).
	DefaultMinLifetime = 60 * time.Second
	// DefaultLifetime is used when a Register omits `lt`. LwM2M's own default registration
	// lifetime is 86400s (one day).
	DefaultLifetime = 86400 * time.Second
	// DefaultExpiryGrace is slack added to a registration's lifetime before the timer
	// declares it lapsed, absorbing network/scheduling jitter so a device that refreshes
	// slightly late is not falsely DISCONNECTED.
	DefaultExpiryGrace = 30 * time.Second
	// DefaultReplaceBackoff collapses an abusive re-Register storm (ADR-075 R5): a
	// re-Register for an identity whose live registration is younger than this returns the
	// existing location WITHOUT minting a new session — so a Register loop cannot produce
	// unbounded distinct-dedup-id CONNECTED writes. A genuine reboot after the window
	// mints a fresh session normally.
	DefaultReplaceBackoff = 5 * time.Second
	// emitTimeout bounds one durable presence write; emitAttempts / retryBackoff bound the
	// retry. Register uses the SHORT budget (it gates the CoAP response); the async expiry
	// path uses a MORE generous one (it is the only actor that re-drives that DISCONNECT).
	emitTimeout        = 3 * time.Second
	registerAttempts   = 2
	disconnectAttempts = 5
	retryBackoff       = 750 * time.Millisecond
)

// deviceResolver resolves a source external id to a DeviceChain device token,
// auto-registering per the policy (ADR-044). *adapter.Registrar implements it; the
// Registry depends on the interface so its logic is testable without device-management.
type deviceResolver interface {
	Resolve(ctx context.Context, tenant, externalId string, policy adapter.IngestPolicy) (string, adapter.ResolveOutcome, error)
}

// presenceEmitter writes one presence StateChange durably (ADR-067). *adapter.Emitter
// implements it; the Registry depends on the interface so it is testable without NATS.
type presenceEmitter interface {
	EmitPresence(ctx context.Context, tenant, source, deviceToken string, ev adapter.PresenceEvent) error
}

// Metrics are the optional Prometheus instruments the registry updates; any nil field is
// skipped so the registry is usable in tests without a registry. None is labeled by
// tenant — a per-tenant label on a device-driven counter is the cardinality risk the
// governance lesson (ADR-023) warns against.
type Metrics struct {
	Registrations       prometheus.Counter // successful Registers (a CONNECTED was emitted)
	Updates             prometheus.Counter // lifetime refreshes
	Deregistrations     prometheus.Counter // explicit DELETE /rd/{id}
	Expiries            prometheus.Counter // lifetime lapses → DISCONNECTED
	ActiveRegistrations prometheus.Gauge   // live registrations held
	DevicesRegistered   prometheus.Counter // devices auto-created on first registration
	PresenceEmitted     prometheus.Counter // presence StateChange events durably written
	Dropped             prometheus.Counter // unknown device (auto-register off) OR emit budget exhausted
	AuthErrors          prometheus.Counter // identity un-recoverable / no binding (handler layer)
	BadRequests         prometheus.Counter // malformed /rd request (handler layer)
}

// entry is one live registration. It is mutated only under Registry.mu.
type entry struct {
	regId       string
	identity    string
	tenant      string
	externalId  string
	token       string
	epoch       uint64    // the session epoch minted at Register; the DISCONNECT reuses it
	connectedAt time.Time // the CONNECTED OccurredAt, so DISCONNECT can be stamped strictly after it
	lifetime    time.Duration
	// generation invalidates a stale lifetime-timer fire: Update bumps it and re-arms, so
	// a timer that already slipped past Stop sees the mismatch and does nothing.
	generation uint64
	timer      *time.Timer
}

// Registry is the in-memory active-registration table and the presence logic over it.
// It is safe for concurrent use.
type Registry struct {
	mu         sync.Mutex
	byRegId    map[string]*entry
	byIdentity map[string]*entry

	resolver deviceResolver
	emitter  presenceEmitter
	epoch    *adapter.EpochSource
	metrics  Metrics

	source         string
	minLifetime    time.Duration
	defaultLife    time.Duration
	grace          time.Duration
	replaceBackoff time.Duration

	now      func() time.Time
	newRegID func() string
}

// Options configures a Registry. Zero-valued durations take their package defaults, so a
// caller sets only what it overrides (tests pin the clock, regId, and short windows).
type Options struct {
	Source         string // the event Source stamp (ADR-067); L3 reconcile reads it back
	MinLifetime    time.Duration
	DefaultLife    time.Duration
	Grace          time.Duration
	ReplaceBackoff time.Duration
	Now            func() time.Time // nil ⇒ time.Now
	NewRegID       func() string    // nil ⇒ crypto-random hex
}

// New builds a Registry over the shared adapter's device resolver + presence emitter and
// the shared epoch source (adapter.EpochSource). resolver and emitter are the interfaces
// *adapter.Registrar / *adapter.Emitter satisfy.
func New(resolver deviceResolver, emitter presenceEmitter, epoch *adapter.EpochSource, metrics Metrics, opts Options) *Registry {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewRegID == nil {
		opts.NewRegID = randomRegID
	}
	if opts.Source == "" {
		opts.Source = SourceLwM2M
	}
	if opts.MinLifetime <= 0 {
		opts.MinLifetime = DefaultMinLifetime
	}
	if opts.DefaultLife <= 0 {
		opts.DefaultLife = DefaultLifetime
	}
	if opts.Grace <= 0 {
		opts.Grace = DefaultExpiryGrace
	}
	if opts.ReplaceBackoff <= 0 {
		opts.ReplaceBackoff = DefaultReplaceBackoff
	}
	return &Registry{
		byRegId:        map[string]*entry{},
		byIdentity:     map[string]*entry{},
		resolver:       resolver,
		emitter:        emitter,
		epoch:          epoch,
		metrics:        metrics,
		source:         opts.Source,
		minLifetime:    opts.MinLifetime,
		defaultLife:    opts.DefaultLife,
		grace:          opts.Grace,
		replaceBackoff: opts.ReplaceBackoff,
		now:            opts.Now,
		newRegID:       opts.NewRegID,
	}
}

// SourceLwM2M is the event Source stamped on every LwM2M-origin event (ADR-067). It is a
// single fixed value for the whole adapter — one shared socket is one source, unlike
// Sparkplug which derives a source per broker/hostId. It is STABLE by contract: the L3
// failover reconciliation reads it back (assertedActiveDeviceStates(source:)), so
// renaming it would strand every previously-asserted LwM2M row.
const SourceLwM2M = "lwm2m"

// RegisterResult is the outcome of a Register the handler maps to a CoAP reply.
type RegisterResult int

const (
	// RegisterOK — a device was resolved and CONNECTED emitted; RegID is its location.
	RegisterOK RegisterResult = iota
	// RegisterUnknownDevice — the device is not registered and this credential does not
	// auto-register (a definitive refusal, → 4.03, not a retry).
	RegisterUnknownDevice
	// RegisterUnavailable — a retryable failure (device-management/NATS unreachable),
	// → 5.03; the device's own LwM2M retry re-drives it.
	RegisterUnavailable
)

// Register handles a POST /rd for an already-authenticated identity + its resolved
// binding. ltSeconds is the client's requested lifetime (0 ⇒ absent ⇒ default). It
// resolves (auto-registering per the binding), emits CONNECTED, and installs the
// registration. Ordering discipline (ADR-067 + ADR-075 R1/R3/R4):
//   - Resolve + emit happen BEFORE the table lock (they are network calls); the emit is
//     synchronous so the reply reflects it (R3: never a 2.01 that emitted nothing).
//   - The epoch is minted from the monotonic shared source, and the entry is installed
//     under the lock with COMPARE-ON-EPOCH: the entry always holds the highest epoch ever
//     emitted CONNECTED for the identity. Otherwise two concurrent Registers could leave a
//     lower epoch stored, and every later DISCONNECT@lowerEpoch would be rejected by the
//     projection → the device stuck CONNECTED forever.
//   - A too-fast re-Register (younger than replaceBackoff) is collapsed: the existing
//     location is returned with no new session minted (R5 storm control).
func (r *Registry) Register(ctx context.Context, identity string, binding config.PskBinding, ltSeconds int) (RegisterResult, string) {
	// Storm control: if a live registration for this identity is younger than the replace
	// backoff, return it unchanged rather than minting another session (R5). It keeps the
	// existing lifetime — a re-Register this fast is treated as a duplicate, not a genuine
	// lifetime change; the backoff (seconds) is far below the minimum lifetime, so a real
	// lifetime update is never lost this way.
	if regId, ok := r.recentRegistration(identity); ok {
		return RegisterOK, regId
	}

	policy := adapter.IngestPolicy{
		Source:          r.source,
		DeviceTypeToken: binding.DeviceTypeToken,
		AutoRegister:    binding.AutoRegister,
	}
	token, outcome, err := r.resolver.Resolve(ctx, binding.Tenant, binding.ExternalId, policy)
	if err != nil {
		return RegisterUnavailable, "" // retryable — the device re-Registers
	}
	if outcome == adapter.ResolveDropped {
		incr(r.metrics.Dropped, 1)
		log.Debug().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).
			Msg("Refusing LwM2M registration for an unregistered device (auto-registration is off for this credential).")
		return RegisterUnknownDevice, ""
	}
	if outcome == adapter.ResolveCreated {
		incr(r.metrics.DevicesRegistered, 1)
		log.Info().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).Str("token", token).
			Msg("Auto-registered a device on its first LwM2M registration.")
	}

	lifetime := r.clampLifetime(ltSeconds)
	epoch := r.epoch.Next()
	now := r.now()
	ev := adapter.PresenceEvent{
		ExternalId: binding.ExternalId,
		Connected:  true,
		Reason:     "register",
		SessionId:  epoch,
		OccurredAt: now,
	}
	// Synchronous emit: the 2.01 must not claim a registration we failed to record as
	// present (R3/R4). A failure here → 5.03 and NO entry; the device retries.
	if err := r.emitPresence(ctx, binding.Tenant, token, ev, registerAttempts); err != nil {
		incr(r.metrics.Dropped, 1)
		log.Warn().Err(err).Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).
			Msg("Could not emit CONNECTED for an LwM2M registration; refusing it so the device re-Registers.")
		return RegisterUnavailable, ""
	}
	incr(r.metrics.PresenceEmitted, 1)

	regId := r.newRegID()
	r.mu.Lock()
	// Compare-on-epoch (R1): if a concurrent Register already installed an equal-or-higher
	// epoch, it won — discard this one. Our just-emitted CONNECTED@epoch is stale but
	// harmless (the projection holds the higher session). Hand back the winner's location.
	if cur := r.byIdentity[identity]; cur != nil && cur.epoch >= epoch {
		winner := cur.regId
		r.mu.Unlock()
		return RegisterOK, winner
	}
	r.installLocked(regId, identity, binding, token, epoch, now, lifetime)
	r.mu.Unlock()

	incr(r.metrics.Registrations, 1)
	log.Info().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).Str("regId", regId).
		Uint64("session", epoch).Dur("lifetime", lifetime).Msg("LwM2M device registered (CONNECTED).")
	return RegisterOK, regId
}

// installLocked replaces any existing registration for the identity with a fresh one and
// arms its lifetime timer. The caller holds r.mu.
func (r *Registry) installLocked(regId, identity string, binding config.PskBinding, token string, epoch uint64, now time.Time, lifetime time.Duration) {
	if cur := r.byIdentity[identity]; cur != nil {
		// A reboot re-registered without deregistering: drop the old registration (no
		// DISCONNECT — the device did not disconnect, it re-registered at a higher epoch
		// that supersedes the old session) and cancel its timer.
		if cur.timer != nil {
			cur.timer.Stop()
		}
		delete(r.byRegId, cur.regId)
	}
	e := &entry{
		regId: regId, identity: identity, tenant: binding.Tenant,
		externalId: binding.ExternalId, token: token, epoch: epoch,
		connectedAt: now, lifetime: lifetime, generation: 0,
	}
	e.timer = time.AfterFunc(lifetime+r.grace, func() { r.onExpiry(regId, 0) })
	r.byRegId[regId] = e
	r.byIdentity[identity] = e
	setGauge(r.metrics.ActiveRegistrations, len(r.byRegId))
}

// recentRegistration reports the live location for an identity whose registration is
// younger than the replace backoff (R5 storm control), so a rapid re-Register is
// idempotent rather than a fresh session.
func (r *Registry) recentRegistration(identity string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.byIdentity[identity]
	if cur == nil {
		return "", false
	}
	if r.now().Sub(cur.connectedAt) < r.replaceBackoff {
		return cur.regId, true
	}
	return "", false
}

// UpdateResult is the outcome of an Update the handler maps to a CoAP reply.
type UpdateResult int

const (
	UpdateOK      UpdateResult = iota // lifetime refreshed → 2.04 Changed
	UpdateUnknown                     // no such registration for this identity → 4.04 (client re-Registers)
)

// Update handles a POST /rd/{regId}: it refreshes the lifetime and re-arms the timer. It
// is authorized against the connection's authenticated identity — a registration is
// updatable only by the credential that created it, so device A cannot keep device B
// alive. An unknown regId, OR one owned by a different identity, returns UpdateUnknown
// (uniform, so a probe cannot tell a foreign registration from a missing one). No
// presence is emitted: the device is already CONNECTED for this session.
func (r *Registry) Update(identity, regId string, ltSeconds int) UpdateResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byRegId[regId]
	if e == nil || e.identity != identity {
		return UpdateUnknown
	}
	if ltSeconds > 0 {
		e.lifetime = r.clampLifetime(ltSeconds)
	}
	e.generation++
	gen := e.generation
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = time.AfterFunc(e.lifetime+r.grace, func() { r.onExpiry(regId, gen) })
	incr(r.metrics.Updates, 1)
	return UpdateOK
}

// Deregister handles a DELETE /rd/{regId}: it removes the registration and emits
// DISCONNECTED. Authorized against the authenticated identity, same uniform-not-found
// contract as Update. The removal is under the lock; the emit is synchronous afterwards on
// the SHORT budget (it gates the 2.02) but the 2.02 does not truly depend on it — the
// device has deregistered regardless. Returns whether the registration existed and was
// owned by the caller.
func (r *Registry) Deregister(identity, regId string) bool {
	r.mu.Lock()
	e := r.byRegId[regId]
	if e == nil || e.identity != identity {
		r.mu.Unlock()
		return false
	}
	r.removeLocked(e)
	tenant, token, externalId, epoch, connectedAt := e.tenant, e.token, e.externalId, e.epoch, e.connectedAt
	r.mu.Unlock()

	incr(r.metrics.Deregistrations, 1)
	r.emitDisconnect(tenant, token, externalId, epoch, r.disconnectStamp(connectedAt), "deregister", registerAttempts)
	return true
}

// onExpiry fires when a registration's lifetime + grace elapses with no Update: the
// device is presumed gone (LwM2M has no rebirth — a queue-mode sleeper is silent for
// hours by design, so this timer, not a probe, is the presence signal). It commits the
// expiry UNDER the lock (removing the entry) before emitting, so a concurrent Update
// cannot observe a live entry we are about to disconnect: an Update that ran first bumped
// the generation (this fire is then a no-op); one that runs after finds no entry (4.04 →
// the device re-Registers at a higher epoch that supersedes this DISCONNECT).
func (r *Registry) onExpiry(regId string, gen uint64) {
	r.mu.Lock()
	e := r.byRegId[regId]
	if e == nil || e.generation != gen {
		r.mu.Unlock()
		return // stale fire: already removed, or refreshed by an Update
	}
	r.removeLocked(e)
	tenant, token, externalId, epoch, connectedAt := e.tenant, e.token, e.externalId, e.epoch, e.connectedAt
	r.mu.Unlock()

	incr(r.metrics.Expiries, 1)
	log.Info().Str("tenant", tenant).Str("externalId", externalId).Uint64("session", epoch).
		Msg("LwM2M registration lifetime lapsed with no update (DISCONNECTED).")
	// This runs on a dedicated timer goroutine (it blocks no request), so it gets the
	// GENEROUS retry budget: it is the only actor that re-drives this DISCONNECT.
	r.emitDisconnect(tenant, token, externalId, epoch, r.disconnectStamp(connectedAt), "lifetime-expiry", disconnectAttempts)
}

// removeLocked drops an entry from both indexes, stops its timer, and updates the gauge.
// The caller holds r.mu.
func (r *Registry) removeLocked(e *entry) {
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(r.byRegId, e.regId)
	// Only clear the identity index if it still points at THIS entry (a replace may have
	// already repointed it at a newer registration).
	if r.byIdentity[e.identity] == e {
		delete(r.byIdentity, e.identity)
	}
	setGauge(r.metrics.ActiveRegistrations, len(r.byRegId))
}

// emitDisconnect writes a DISCONNECTED transition with a bounded retry (attempts). On
// exhaustion it counts the drop (bounded loss) and logs — the L3 failover reconstruction
// re-derives presence from device-state, so a lost DISCONNECT during a durable-store
// outage is a covered gap, not a silent forever-stuck device.
//
// It always emits under context.Background(), NEVER a request context: the entry is
// already removed, so this DISCONNECT is our own bookkeeping — a device that drops its
// connection right after DELETE must not cancel it mid-retry (which would strand the device
// CONNECTED). The dedup id keys on (tenant, device, session, state), so any re-drive is
// idempotent.
func (r *Registry) emitDisconnect(tenant, token, externalId string, epoch uint64, occurredAt time.Time, reason string, attempts int) {
	ev := adapter.PresenceEvent{
		ExternalId: externalId,
		Connected:  false,
		Reason:     reason,
		SessionId:  epoch,
		OccurredAt: occurredAt,
	}
	if err := r.emitPresence(context.Background(), tenant, token, ev, attempts); err != nil {
		incr(r.metrics.Dropped, 1)
		log.Warn().Err(err).Str("tenant", tenant).Str("externalId", externalId).Str("reason", reason).
			Msg("Could not emit DISCONNECTED after the retry budget; L3 reconstruction is the backstop.")
		return
	}
	incr(r.metrics.PresenceEmitted, 1)
}

// disconnectStamp keeps a DISCONNECTED's OccurredAt strictly after the session's
// CONNECTED, even if the wall clock stepped back within the session (ADR-075 R6): the
// projection orders same-session transitions by OccurredAt, so a DISCONNECT stamped
// before its own CONNECTED would be rejected — leaving the device stuck CONNECTED. A
// registration lifetime is up to a day, so a mid-session NTP correction is a real
// exposure here, not a theoretical one.
func (r *Registry) disconnectStamp(connectedAt time.Time) time.Time {
	now := r.now()
	if !now.After(connectedAt) {
		return connectedAt.Add(time.Nanosecond)
	}
	return now
}

// emitPresence writes one transition, retrying a transient failure within a bounded
// budget. Each attempt is bounded by emitTimeout; between attempts it backs off (aborting
// early if ctx is cancelled). Returns the last error, or nil on success.
func (r *Registry) emitPresence(ctx context.Context, tenant, token string, ev adapter.PresenceEvent, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		ectx, cancel := context.WithTimeout(ctx, emitTimeout)
		err = r.emitter.EmitPresence(ectx, tenant, r.source, token, ev)
		cancel()
		if err == nil {
			return nil
		}
		if i < attempts-1 && !sleepCtx(ctx, retryBackoff) {
			return err // ctx cancelled during backoff
		}
	}
	return err
}

// clampLifetime turns a client-requested lifetime (seconds; 0 ⇒ absent) into the enforced
// duration: the default when absent, otherwise raised to the minimum (R5).
func (r *Registry) clampLifetime(ltSeconds int) time.Duration {
	if ltSeconds <= 0 {
		return r.defaultLife
	}
	lt := time.Duration(ltSeconds) * time.Second
	if lt < r.minLifetime {
		return r.minLifetime
	}
	return lt
}

// Stop cancels every live registration's lifetime timer (best-effort, on shutdown). It
// emits no presence — a graceful shutdown of the single replica is not a device
// disconnect, and L3 reconstruction re-establishes presence on the next leader.
func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.byRegId {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
}

// randomRegID mints an opaque 8-byte registration location. Opaque (not derived from the
// device) so a location cannot be guessed to forge an Update/Deregister for another
// device — though the per-identity authz check is the real defense, this removes the
// guess entirely.
func randomRegID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// sleepCtx waits for d or ctx cancellation; false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// incr adds n to a counter, tolerating a nil counter (tests) and n <= 0.
func incr(c prometheus.Counter, n int) {
	if c != nil && n > 0 {
		c.Add(float64(n))
	}
}

// setGauge sets a gauge, tolerating a nil gauge (tests).
func setGauge(g prometheus.Gauge, v int) {
	if g != nil {
		g.Set(float64(v))
	}
}

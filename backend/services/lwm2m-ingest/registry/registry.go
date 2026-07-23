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
	// DefaultMaxLifetime is the ceiling a registration lifetime is clamped DOWN to, and the
	// lifetime a failover-reconstruction shadow timer is armed for (ADR-075 L3b / F2). It
	// equals DefaultLifetime so an out-of-the-box fleet using LwM2M's own default `lt` is
	// clamped to exactly that; the config knob (maxLifetimeSeconds) tightens it per fleet.
	DefaultMaxLifetime = 86400 * time.Second
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
	// reconstructRetryInterval is how long a reconstruction shadow whose expiry-DISCONNECT
	// could not be completed — device-management unreachable for the lazy token resolve, or
	// the durable emit budget exhausted — waits before retrying (ADR-075 L3b / F5-2). The live
	// path drops on exhaustion because "L3 reconstruction is the backstop"; the reconstruction
	// path IS that backstop, so it re-arms instead of dropping, or a genuinely-dead device
	// stays CONNECTED until the next failover.
	reconstructRetryInterval = 30 * time.Second
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
	Registrations        prometheus.Counter // successful Registers (a CONNECTED was emitted)
	Updates              prometheus.Counter // lifetime refreshes
	Deregistrations      prometheus.Counter // explicit DELETE /rd/{id}
	Expiries             prometheus.Counter // lifetime lapses → DISCONNECTED
	ActiveRegistrations  prometheus.Gauge   // registrations held (INCLUDES failover-reconstruction shadows, L3b)
	ShadowsReconstructed prometheus.Counter // shadow registrations rebuilt on a leadership acquisition (L3b) — a takeover marker
	DevicesRegistered    prometheus.Counter // devices auto-created on first registration
	PresenceEmitted      prometheus.Counter // presence StateChange events durably written
	Dropped              prometheus.Counter // unknown device (auto-register off) OR emit budget exhausted
	AuthErrors           prometheus.Counter // identity un-recoverable / no binding (handler layer)
	BadRequests          prometheus.Counter // malformed /rd request (handler layer)
	ObservationOverflow  prometheus.Counter // telemetry instances a registration declared beyond the per-registration observe cap (L2b)
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
	// shadow marks an entry rebuilt by failover reconstruction (ADR-075 L3b), not by a live
	// Register: it never had a device connection, its token is resolved LAZILY at expiry
	// (lookup-only), and its lifetime timer's job is to DISCONNECT a device that stayed silent
	// across the leadership handover. A live Register for the identity supersedes it (the fresh
	// higher epoch wins) before the timer ever fires, in the common case.
	shadow bool
}

// Registry is the in-memory active-registration table and the presence logic over it.
// It is safe for concurrent use.
type Registry struct {
	mu         sync.Mutex
	byRegId    map[string]*entry
	byIdentity map[string]*entry
	// stopped is set by Stop and is TERMINAL: once a leadership term ends (eviction/shutdown) its
	// registry is abandoned (each term builds a fresh one), so a Stopped registry must accept no
	// further work. It closes the eviction race where a Register that passed the handler's
	// leadership gate BEFORE the term was cancelled finishes its out-of-lock resolve+emit and would
	// otherwise install an entry — arming a lifetime timer that fires in a dead term on a standby.
	stopped bool

	resolver deviceResolver
	emitter  presenceEmitter
	epoch    *adapter.EpochSource
	metrics  Metrics

	source         string
	minLifetime    time.Duration
	defaultLife    time.Duration
	maxLife        time.Duration
	grace          time.Duration
	replaceBackoff time.Duration

	// onSessionEnd, when set, is invoked (outside r.mu) whenever a session ends by
	// Deregister or lifetime-expiry — the seam the observe manager (L2b) hooks to cancel
	// that session's observations. It is NOT fired on an install-replace (a reboot without
	// deregister): there the new session's Establish already supersedes the old observations.
	onSessionEnd func(identity string, epoch uint64)

	now      func() time.Time
	newRegID func() string
}

// Options configures a Registry. Zero-valued durations take their package defaults, so a
// caller sets only what it overrides (tests pin the clock, regId, and short windows).
type Options struct {
	Source      string // the event Source stamp (ADR-067); L3 reconcile reads it back
	MinLifetime time.Duration
	DefaultLife time.Duration
	// MaxLife is the ceiling a registration lifetime is clamped DOWN to, and identically the
	// lifetime a failover-reconstruction shadow timer is armed for (ADR-075 L3b / F2). Zero
	// takes the package default; a value below MinLifetime is raised to it (the ceiling can
	// never fall below the floor). Setting the shadow lifetime equal to the Register ceiling
	// is what makes a shadow-expiry DISCONNECT only ever late-true, never false.
	MaxLife        time.Duration
	Grace          time.Duration
	ReplaceBackoff time.Duration
	// OnSessionEnd, when set, is called (outside the table lock) when a session ends by
	// Deregister or lifetime-expiry. The service wires it to the observe manager's Cancel so
	// a device's observations are torn down when its presence ends (L2b).
	OnSessionEnd func(identity string, epoch uint64)
	Now          func() time.Time // nil ⇒ time.Now
	NewRegID     func() string    // nil ⇒ crypto-random hex
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
	if opts.MaxLife <= 0 {
		opts.MaxLife = DefaultMaxLifetime
	}
	// The ceiling can never fall below the floor: a MaxLife below MinLifetime would make
	// clampLifetime raise a short lt to the floor and then cap it back below the floor — a
	// self-contradictory window. Raise it to the floor (config.Validate refuses this at the
	// config layer; this keeps a directly-constructed registry coherent too).
	if opts.MaxLife < opts.MinLifetime {
		opts.MaxLife = opts.MinLifetime
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
		maxLife:        opts.MaxLife,
		grace:          opts.Grace,
		replaceBackoff: opts.ReplaceBackoff,
		onSessionEnd:   opts.OnSessionEnd,
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
//
// It returns the CoAP-mappable result, the registration location, the SESSION EPOCH, and
// whether the caller should (re)establish observations for this conn (L2b). establish is true
// for a real install AND a storm-collapse (the device may have rebooted onto a new conn within
// the backoff), and false for a compare-on-epoch loss (the winning Register establishes) or any
// non-OK result — so the handler only spends Observe I/O when it can win the manager's CAS.
func (r *Registry) Register(ctx context.Context, identity string, binding config.PskBinding, ltSeconds int) (result RegisterResult, regId string, epoch uint64, establish bool) {
	// A terminal Stop (leadership eviction) refuses new registrations before any work: 5.03, the
	// device retries toward the leader. The handler's leadership gate is the primary guard; this
	// backs it for a request that raced ahead of the term cancellation.
	if r.isStopped() {
		return RegisterUnavailable, "", 0, false
	}
	// Storm control: if a live registration for this identity is younger than the replace
	// backoff, return it unchanged rather than minting another session (R5). It keeps the
	// existing lifetime — a re-Register this fast is treated as a duplicate, not a genuine
	// lifetime change; the backoff (seconds) is far below the minimum lifetime, so a real
	// lifetime update is never lost this way. establish=true: a reboot within the backoff may
	// be on a NEW conn, and the observe manager's equal-epoch CAS re-establishes on it.
	if rid, ep, ok := r.recentRegistration(identity); ok {
		return RegisterOK, rid, ep, true
	}

	policy := adapter.IngestPolicy{
		Source:          r.source,
		DeviceTypeToken: binding.DeviceTypeToken,
		AutoRegister:    binding.AutoRegister,
	}
	token, outcome, err := r.resolver.Resolve(ctx, binding.Tenant, binding.ExternalId, policy)
	if err != nil {
		return RegisterUnavailable, "", 0, false // retryable — the device re-Registers
	}
	if outcome == adapter.ResolveDropped {
		incr(r.metrics.Dropped, 1)
		log.Debug().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).
			Msg("Refusing LwM2M registration for an unregistered device (auto-registration is off for this credential).")
		return RegisterUnknownDevice, "", 0, false
	}
	if outcome == adapter.ResolveCreated {
		incr(r.metrics.DevicesRegistered, 1)
		log.Info().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).Str("token", token).
			Msg("Auto-registered a device on its first LwM2M registration.")
	}

	lifetime := r.clampLifetime(ltSeconds)
	epoch = r.epoch.Next()
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
		return RegisterUnavailable, "", 0, false
	}
	incr(r.metrics.PresenceEmitted, 1)

	regId = r.newRegID()
	r.mu.Lock()
	// A terminal Stop may have landed while we resolved+emitted outside the lock: do NOT install
	// an entry (and arm a lifetime timer) in a term that no longer serves — that timer would fire
	// on a standby / dead term. The CONNECTED already emitted rides the L3b reconstruction gap.
	if r.stopped {
		r.mu.Unlock()
		return RegisterUnavailable, "", 0, false
	}
	// Compare-on-epoch (R1): if a concurrent REAL Register already installed an equal-or-higher
	// epoch, it won — discard this one. Our just-emitted CONNECTED@epoch is stale but
	// harmless (the projection holds the higher session). Hand back the winner's location and
	// its epoch; establish=false because the winning Register establishes the observations
	// (our conn is superseded — observing on it would only lose the manager's CAS).
	//
	// A SHADOW is never a peer here (ADR-075 L3b): a real Register proves the device is alive, so
	// it must supersede a reconstruction shadow UNCONDITIONALLY — even one whose pre-minted epoch
	// is HIGHER than ours. The in-term retry (F4) can install a shadow WHILE serving, and it mints
	// under the lock AFTER a racing Register minted (outside the lock), so cur.epoch >= epoch is
	// reachable with cur a shadow; treating it as a compare-on-epoch winner would hand this live
	// device the shadow's regId (an empty-token entry it would then keep alive by Update, whose
	// Deregister emits an empty-token DISCONNECT → stuck CONNECTED). Falling through to
	// installLocked drops the shadow (cancelling its timer) and installs this live entry; the
	// shadow never emitted a CONNECTED, so no DISCONNECT is owed.
	if cur := r.byIdentity[identity]; cur != nil && !cur.shadow && cur.epoch >= epoch {
		winner, winnerEpoch := cur.regId, cur.epoch
		r.mu.Unlock()
		return RegisterOK, winner, winnerEpoch, false
	}
	r.installLocked(regId, identity, binding, token, epoch, now, lifetime)
	r.mu.Unlock()

	incr(r.metrics.Registrations, 1)
	log.Info().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).Str("regId", regId).
		Uint64("session", epoch).Dur("lifetime", lifetime).Msg("LwM2M device registered (CONNECTED).")
	return RegisterOK, regId, epoch, true
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
func (r *Registry) recentRegistration(identity string) (string, uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.byIdentity[identity]
	if cur == nil {
		return "", 0, false
	}
	if r.now().Sub(cur.connectedAt) < r.replaceBackoff {
		return cur.regId, cur.epoch, true
	}
	return "", 0, false
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
	// A Stopped (evicted) term's table is cleared, so this is 4.04 anyway; the explicit check keeps
	// the contract obvious and refuses even if a clear ever raced an install.
	if r.stopped {
		return UpdateUnknown
	}
	e := r.byRegId[regId]
	// A shadow is never servable (ADR-075 L3b): its opaque regId is not one any device was ever
	// handed (the compare-on-epoch exemption guarantees a real Register supersedes a shadow rather
	// than returning its regId), so an Update naming a shadow's regId cannot be legitimate. Treat it
	// as unknown → 4.04 → re-Register — defense-in-depth so a shadow's day-long timer can never be
	// kept alive by an Update, nor its empty token ever reach a Deregister emit.
	if e == nil || e.identity != identity || e.shadow {
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
	if r.stopped {
		r.mu.Unlock()
		return false
	}
	e := r.byRegId[regId]
	// A shadow's regId is never servable (see Update): a Deregister naming one cannot be legitimate,
	// and honoring it would emit a DISCONNECT with the shadow's EMPTY token. Uniform not-found.
	if e == nil || e.identity != identity || e.shadow {
		r.mu.Unlock()
		return false
	}
	r.removeLocked(e)
	tenant, token, externalId, epoch, connectedAt := e.tenant, e.token, e.externalId, e.epoch, e.connectedAt
	r.mu.Unlock()

	incr(r.metrics.Deregistrations, 1)
	// Cancel the session's observations FIRST (fast: it tombstones + detaches under a leaf lock
	// and cancels asynchronously), then emit DISCONNECTED — so a device decided gone stops
	// producing telemetry before, not after, the (retrying) presence write.
	r.fireSessionEnd(identity, epoch)
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
	if e.shadow {
		r.mu.Unlock()
		r.onShadowExpiry(regId, gen) // reconstruction path: lazy-resolve the token, then DISCONNECT
		return
	}
	r.removeLocked(e)
	identity, tenant, token, externalId, epoch, connectedAt := e.identity, e.tenant, e.token, e.externalId, e.epoch, e.connectedAt
	r.mu.Unlock()

	incr(r.metrics.Expiries, 1)
	log.Info().Str("tenant", tenant).Str("externalId", externalId).Uint64("session", epoch).
		Msg("LwM2M registration lifetime lapsed with no update (DISCONNECTED).")
	// Cancel the lapsed session's observations FIRST — the generous DISCONNECT retry budget
	// below can span many seconds, and a queue-mode sleeper's conn may still be alive; tearing
	// its observations down before the emit stops it ingesting telemetry for a device we have
	// already decided is gone.
	r.fireSessionEnd(identity, epoch)
	// This runs on a dedicated timer goroutine (it blocks no request), so it gets the
	// GENEROUS retry budget: it is the only actor that re-drives this DISCONNECT.
	r.emitDisconnect(tenant, token, externalId, epoch, r.disconnectStamp(connectedAt), "lifetime-expiry", disconnectAttempts)
}

// ReconstructShadow installs a SHADOW registration for an asserted-active device recovered
// from the device-state projection on a leadership acquisition (ADR-075 L3b failover
// reconstruction). A shadow occupies both indexes under a fresh random regId, so a device
// that stayed silent across the handover is DISCONNECTED when its lifetime timer lapses,
// while a surviving device's Update to the (unknowable) regId 4.04s → re-Register → a fresh
// higher-epoch CONNECTED supersedes the shadow (installLocked drops it, its timer cancelled).
//
// It returns whether a shadow was installed. It SKIPS unconditionally when a live entry for
// the identity already exists (B1): a live registration is always fresher than the device-
// state snapshot, and the in-term retry (F4) can run this while the term already serves, so a
// device may have re-Registered between the failed first read and the retry.
//
// The epoch is PRE-MINTED here (F6) — above the floor the caller already raised from the same
// asserted set — and stored as the entry's epoch, so the eventual onExpiry → emitDisconnect
// carries a session number that exceeds every stored one and wins by session (immune to the
// wall-clock step-back that makes a same-session, OccurredAt-ordered DISCONNECT fragile).
func (r *Registry) ReconstructShadow(identity string, binding config.PskBinding) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	if _, live := r.byIdentity[identity]; live {
		return false // B1: never replace a live entry with a stale snapshot
	}
	epoch := r.epoch.Mint() // F6: above the floor; the DISCONNECT wins by session number
	regId := r.newRegID()
	now := r.now()
	// Backdate connectedAt past the replace backoff so a genuine re-Register is NOT collapsed
	// onto the shadow by storm control (B3): recentRegistration would otherwise hand back the
	// shadow's regId+epoch WITHOUT ever emitting a CONNECTED, leaving the device with no
	// presence at all. A shadow never emitted a CONNECT, so it must never satisfy storm control.
	e := &entry{
		regId: regId, identity: identity, tenant: binding.Tenant,
		externalId: binding.ExternalId, token: "", epoch: epoch,
		connectedAt: now.Add(-r.replaceBackoff - time.Second),
		lifetime:    r.maxLife, generation: 0, shadow: true,
	}
	// The shadow timer is armed for the MAX lifetime (F2), never a per-device `lt` (unknown —
	// device-state does not persist it). Equalling the Register ceiling is what guarantees the
	// timer outlasts any live registration's remaining lifetime, so its DISCONNECT is late-true.
	e.timer = time.AfterFunc(r.maxLife+r.grace, func() { r.onExpiry(regId, 0) })
	r.byRegId[regId] = e
	r.byIdentity[identity] = e
	setGauge(r.metrics.ActiveRegistrations, len(r.byRegId))
	incr(r.metrics.ShadowsReconstructed, 1)
	log.Info().Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).Str("regId", regId).
		Uint64("session", epoch).Msg("Reconstructed a shadow LwM2M registration on leadership acquisition (ADR-075 L3b).")
	return true
}

// ReconstructDisconnect DISCONNECTS an asserted-active device that has NO current tenancy
// binding (its PSK credential was decommissioned while the device was CONNECTED, B6). Such a
// device can never re-handshake, so a shadow with a lifetime timer would only delay the
// inevitable — the honest terminal state is DISCONNECTED now. It mints a fresh epoch above
// the floor (F6, so the transition wins by session), resolves the token LOOKUP-ONLY
// (AutoRegister:false — a device deleted during the blackout must not be re-created just to
// mark it offline, F5-1; not-found ⇒ nothing to disconnect), and emits with the generous
// budget. A re-added credential's later re-Register supersedes this at a higher epoch.
//
// It runs synchronously (the caller drives it off the acquisition path so a slow
// device-management never stalls serving) and is best-effort: on an unreachable resolve or an
// exhausted emit it returns without emitting; the row stays asserted and the next acquisition
// re-attempts. This is the rare partial-decommission edge, not the hot path.
func (r *Registry) ReconstructDisconnect(ctx context.Context, tenant, externalId string) {
	if r.isStopped() {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, emitTimeout)
	token, outcome, err := r.resolver.Resolve(rctx, tenant, externalId, adapter.IngestPolicy{Source: r.source, AutoRegister: false})
	cancel()
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Str("externalId", externalId).
			Msg("Could not resolve an orphaned asserted device to DISCONNECT it (device-management unreachable); retried on the next acquisition.")
		return
	}
	if outcome == adapter.ResolveDropped {
		return // the device no longer exists — nothing to disconnect
	}
	epoch := r.epoch.Mint()
	log.Info().Str("tenant", tenant).Str("externalId", externalId).Uint64("session", epoch).
		Msg("DISCONNECTING an asserted LwM2M device whose credential was decommissioned (ADR-075 L3b / B6).")
	r.emitDisconnect(tenant, token, externalId, epoch, r.now(), "reconstruct-orphan", disconnectAttempts)
}

// onShadowExpiry runs when a reconstruction shadow's lifetime timer lapses with no re-Register:
// the device stayed silent across the handover and is presumed gone. Unlike a live expiry it
// must LAZILY resolve the token (a shadow carries none — F5-1 lookup-only) before it can emit,
// so it re-validates the entry under the lock at each step, since a re-Register may have
// superseded the shadow while this ran (its fresh higher-epoch CONNECTED then wins regardless).
//
//   - resolve error (device-management unreachable): RE-ARM a retry timer, do NOT drop — the
//     reconstruction path is its own backstop (F5-2).
//   - not-found (device deleted during the blackout): drop the shadow silently, no emit.
//   - resolved: emit DISCONNECT@pre-minted-epoch; on an exhausted emit budget, re-arm (F5-2);
//     on success, remove the shadow.
func (r *Registry) onShadowExpiry(regId string, gen uint64) {
	r.mu.Lock()
	e := r.byRegId[regId]
	if e == nil || e.generation != gen || !e.shadow {
		r.mu.Unlock()
		return // superseded by a re-Register (or already handled)
	}
	tenant, externalId, epoch, connectedAt := e.tenant, e.externalId, e.epoch, e.connectedAt
	r.mu.Unlock()

	// Lazy LOOKUP-ONLY resolve (F5-1): AutoRegister:false so a device deleted during the
	// blackout is not re-created merely to mark it offline.
	rctx, cancel := context.WithTimeout(context.Background(), emitTimeout)
	token, outcome, err := r.resolver.Resolve(rctx, tenant, externalId, adapter.IngestPolicy{Source: r.source, AutoRegister: false})
	cancel()
	if err != nil {
		r.rearmShadow(regId, gen) // transient — retry later, never drop (F5-2)
		return
	}
	if outcome == adapter.ResolveDropped {
		r.dropShadow(regId, gen) // device gone — nothing to disconnect
		return
	}

	// Emit while the shadow is still installed, then remove on success. The only concurrent
	// mutation is a re-Register (a shadow has no Update path), and it is epoch-ordered: whether
	// it lands before or after this DISCONNECT, its higher-epoch CONNECTED wins in the projection.
	if !r.emitDisconnect(tenant, token, externalId, epoch, r.disconnectStamp(connectedAt), "reconstruct-expiry", disconnectAttempts) {
		r.rearmShadow(regId, gen) // durable store down — retry later, never drop (F5-2)
		return
	}
	incr(r.metrics.Expiries, 1)
	log.Info().Str("tenant", tenant).Str("externalId", externalId).Uint64("session", epoch).
		Msg("Reconstruction shadow lifetime lapsed with no re-Register (DISCONNECTED across a leadership handover).")
	// No fireSessionEnd: a shadow never had a live conn in THIS term (the observe manager is
	// rebuilt empty per leadership term), so there are no observations to cancel.
	r.dropShadow(regId, gen)
}

// rearmShadow re-arms a still-present shadow's lifetime timer after a failed expiry-DISCONNECT
// (F5-2), re-validating under the lock: a re-Register may have superseded the shadow while the
// resolve/emit ran, in which case there is nothing to re-arm (the device is back). The
// generation is unchanged (a shadow has no Update to bump it), so the re-armed timer's gen
// still matches.
func (r *Registry) rearmShadow(regId string, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byRegId[regId]
	if e == nil || e.generation != gen || !e.shadow {
		return
	}
	e.timer = time.AfterFunc(reconstructRetryInterval, func() { r.onExpiry(regId, gen) })
}

// dropShadow removes a shadow (device deleted during the blackout) with no DISCONNECT,
// re-validating under the lock so a shadow a re-Register already superseded is left untouched.
func (r *Registry) dropShadow(regId string, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byRegId[regId]
	if e == nil || e.generation != gen || !e.shadow {
		return
	}
	r.removeLocked(e)
}

// fireSessionEnd invokes the OnSessionEnd hook (if set) so the observe manager cancels the
// ended session's observations. Called outside r.mu on Deregister/expiry — never on an
// install-replace, where the successor's Establish already supersedes the old observations.
func (r *Registry) fireSessionEnd(identity string, epoch uint64) {
	if r.onSessionEnd != nil {
		r.onSessionEnd(identity, epoch)
	}
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

// emitDisconnect writes a DISCONNECTED transition with a bounded retry (attempts),
// returning whether it was durably emitted. On exhaustion the LIVE callers (Deregister,
// live expiry) count the drop (bounded loss) and move on — the L3 failover reconstruction
// re-derives presence from device-state, so a lost DISCONNECT during a durable-store outage
// is a covered gap there, not a silent forever-stuck device. The RECONSTRUCTION caller is
// that backstop and has none of its own, so it inspects the return and re-arms rather than
// dropping (F5-2).
//
// It always emits under context.Background(), NEVER a request context: the entry is
// already removed, so this DISCONNECT is our own bookkeeping — a device that drops its
// connection right after DELETE must not cancel it mid-retry (which would strand the device
// CONNECTED). The dedup id keys on (tenant, device, session, state), so any re-drive is
// idempotent.
func (r *Registry) emitDisconnect(tenant, token, externalId string, epoch uint64, occurredAt time.Time, reason string, attempts int) bool {
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
		return false
	}
	incr(r.metrics.PresenceEmitted, 1)
	return true
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
// duration: the default when absent, otherwise raised to the minimum floor (R5) and capped
// at the maximum ceiling (ADR-075 L3b / F2). The ceiling bounds the largest live lifetime so
// a failover-reconstruction shadow timer armed at the same ceiling can never be shorter than
// a live registration's remaining lifetime — which is what makes a shadow-expiry DISCONNECT
// only ever late-true. The absent-lt default is capped too, so a tightened ceiling below the
// default takes effect for a device that omits `lt`.
func (r *Registry) clampLifetime(ltSeconds int) time.Duration {
	var lt time.Duration
	if ltSeconds <= 0 {
		lt = r.defaultLife
	} else {
		lt = time.Duration(ltSeconds) * time.Second
	}
	if lt < r.minLifetime {
		lt = r.minLifetime
	}
	if lt > r.maxLife {
		lt = r.maxLife
	}
	return lt
}

// isStopped reports whether Stop has been called (a leadership eviction / shutdown).
func (r *Registry) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// Stop is TERMINAL: it marks the registry stopped (refusing all further Register/Update/
// Deregister), cancels every live registration's lifetime timer, clears the table, and zeroes the
// active-registrations gauge. It emits no presence — a leadership handover or graceful shutdown is
// not a device disconnect, and L3b reconstruction re-establishes presence on the next leader.
// Clearing the table + gauge matters because the evicted term's registry is abandoned but the
// process lives on as a standby: a lingering handler must not find a live entry to arm a timer
// against, and the standby must not advertise phantom registrations.
func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for _, e := range r.byRegId {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	r.byRegId = map[string]*entry{}
	r.byIdentity = map[string]*entry{}
	setGauge(r.metrics.ActiveRegistrations, 0)
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// TenantLister names every tenant on the instance. Reconciliation cannot discover
// tenants any other way: a tenant whose whole fleet dropped appears in neither the
// broker's inventory nor any recent traffic, and it is exactly that tenant whose
// devices are stuck asserted-online.
type TenantLister interface {
	TenantTokens(ctx context.Context) ([]string, error)
}

// StoredDevice is what the projection currently holds for one asserted device of this
// source. Active distinguishes the two halves of the diff, and SessionId is what makes a
// repair for an INACTIVE row applicable at all — see reconcileTenant.
type StoredDevice struct {
	Tenant      string
	DeviceToken string
	SessionId   uint64
	Active      bool
}

// ProjectionReader reads what the device-state projection currently believes about
// this source's devices, keyed by DeviceKey.
//
// 🔑 KEYED BY DEVICE TOKEN, NOT EXTERNAL ID. The existing adapter.Reconciler is
// external-id keyed and SKIPS every row without one (adapter/ingest.go), which is right
// for Sparkplug and LwM2M — their devices are addressed by a transport-native identity —
// and wrong for plain MQTT, where external id is optional and usually absent. Reused
// here it would report almost every connected device as "not asserted online", and the
// connect direction would re-emit a StateChange for the whole fleet on every pass: a
// durable event row per device per pass, through the resolver, the projection, DETECT
// and the historian.
//
// 🔴 IT READS THE INACTIVE ROWS TOO, and that is not for symmetry. The repair a device
// needs is the one for a row that reads OFFLINE, and such a repair has to carry a
// session id presence.Decide will accept. Without the stored id there is nothing to
// compare the broker's against, and a regressed session produces a repair that is
// rejected on this pass and on every pass after it.
type ProjectionReader interface {
	AssertedStates(ctx context.Context, tenant, source string) (map[string]StoredDevice, error)
}

// ReconcileMetrics are the repair pass's operator signals.
type ReconcileMetrics struct {
	// Runs counts completed passes, labelled by outcome ("complete", "partial",
	// "failed", "timeout", "cancelled"). A pass that could not prove the inventory
	// complete does only half its job, and that must be visible as a shape rather than
	// inferred from silence. The same rule is why the later outcomes exist: "timeout" is
	// a pass that ran out of its own budget and left tenants unvisited, "failed" is one
	// that visited them and could read none, and "cancelled" is the service stopping —
	// which is not a fault and must not sit on the series operators alert from.
	Runs *prometheus.CounterVec
	// Repaired counts repairs the projection was SEEN TO APPLY, labelled by direction
	// ("connect", "disconnect"). In a healthy instance this is near zero; a standing
	// rate means advisories are being lost.
	//
	// 🔴 APPLIED, NOT EMITTED, AND THAT IS WHY IT LAGS A PASS. Whether the projection
	// accepts a transition is decided asynchronously and downstream — the resolver can
	// refuse it, presence ordering can throw it away — so a successful publish says
	// nothing about whether the device was repaired. Counting the publish is what let a
	// repair refused on every pass (a disabled device's disconnect, once) read as a
	// success every time. So a repair is counted when the next pass reads its tenant and
	// finds the row showing what the repair said; see settleRepairs.
	Repaired *prometheus.CounterVec
	// Unapplied counts repairs the next pass found NOT applied, labelled by direction:
	// the row still reads exactly what it read when the repair was emitted. The repair
	// was refused or lost downstream, and is emitted again on that pass. A standing rate
	// means repairs are being emitted and not landing.
	Unapplied *prometheus.CounterVec
	// RegressedSessions is the number of devices this pass found LIVE on a session id
	// lower than the one the projection is holding — the trailing-clock population.
	//
	// 🔑 IT IS THE CONVERGENCE SIGNAL, AND IT IS WHY Repaired's "emitted, not applied"
	// gap stops mattering here. A repair that is emitted and then rejected leaves the
	// device regressed, so it is counted again on the next pass; a repair that lands
	// re-files the row onto the live session and the device drops out of this gauge. A
	// standing non-zero value therefore means repairs are NOT converging, which is the
	// thing an operator wants alerted on — strictly more useful than a per-repair
	// applied counter, which would have to be inferred from a later read anyway.
	//
	// It is a GAUGE, not a counter: it measures a population at a moment, and a device
	// that is still regressed on the next pass is the same device, not a new event.
	RegressedSessions prometheus.Gauge
	// SkippedDisconnects counts devices that WOULD have been marked offline had the
	// inventory been provably complete. It is the cost of the safety rule, and
	// without it an instance that never proves completeness looks identical to one
	// with nothing to repair.
	SkippedDisconnects prometheus.Counter
}

func (m ReconcileMetrics) run(outcome string) {
	if m.Runs != nil {
		m.Runs.WithLabelValues(outcome).Inc()
	}
}

func (m ReconcileMetrics) repaired(direction string, n int) {
	if m.Repaired != nil && n > 0 {
		m.Repaired.WithLabelValues(direction).Add(float64(n))
	}
}

func (m ReconcileMetrics) unapplied(direction string, n int) {
	if m.Unapplied != nil && n > 0 {
		m.Unapplied.WithLabelValues(direction).Add(float64(n))
	}
}

// sentRepair is a repair this process emitted, kept until the next read of its tenant says
// whether it landed. before is the row the repair was emitted against (hadRow false when
// there was none); sessionId and connected are what the repair asked the row to become.
type sentRepair struct {
	before    StoredDevice
	hadRow    bool
	sessionId uint64
	connected bool
}

// settle judges one sent repair against the row read on the following pass. A row showing
// what the repair said is applied. A row exactly as it was when the repair was emitted is
// NOT applied: nothing moved it, so the repair was refused or lost. Anything else — the
// row has moved to another session or state since — is neither: something newer than the
// repair decided the row, and whether the repair landed first cannot be told.
func (s sentRepair) settle(row StoredDevice, seen bool) (applied, unapplied bool) {
	if seen && row.SessionId == s.sessionId && row.Active == s.connected {
		return true, false
	}
	if seen != s.hadRow {
		return false, false
	}
	if !seen || (row.SessionId == s.before.SessionId && row.Active == s.before.Active) {
		return false, true
	}
	return false, false
}

// Reconciler repairs the difference between what the broker holds and what the
// projection believes.
//
// 🔴 IT IS NOT A BACKSTOP, IT IS THE ONLY ACCOUNT OF SOME DEATHS. A graceful broker
// shutdown emits NO disconnect advisories for the connections it was holding —
// measured against nats-server v2.14.4 — so every rolling broker upgrade leaves every
// connected device asserted-online. Nothing else corrects that: an asserted row is
// skipped by the inactivity sweep (the presence_source predicate in Api.SweepInactive),
// and data events cannot flip an asserted device's Active (the freshness branch in
// Api.MergeDeviceState guards on PresenceSourceAsserted) — both in
// device-state/model/api.go. Both existing asserted sources repair their own missed
// deaths for the same reason (sparkplug host.reconcileProbe, lwm2m
// registry.ReconstructShadow / ReconstructDisconnect); this one would have been the
// first that could not.
type Reconciler struct {
	tap      *Tap
	requests Requester
	tenants  TenantLister
	reads    ProjectionReader
	metrics  ReconcileMetrics
	gather   time.Duration
	now      func() time.Time
	// clusterHighWater is the largest cluster this process has ever been told about.
	//
	// 🔴 WITHOUT IT, A FULL PARTITION DECLARES ITSELF COMPLETE. Completeness compares
	// the servers that answered against the size they CLAIM, and a server that has lost
	// its routes reports itself alone — so in a partition every reachable server claims
	// a cluster of one, the subset we can reach satisfies its own claim, and the pass
	// happily marks the unreachable side's devices offline. That is the mass false death
	// the completeness rule exists to prevent, arriving through the rule itself.
	//
	// A high-water mark closes it because a cluster does not shrink in normal operation:
	// once three servers have been seen, two answering is a partition, not a fact about
	// the cluster. A DELIBERATE scale-down does leave this too high, and the consequence
	// is that deaths are withheld — counted and logged, never silent — until the process
	// restarts. Withholding is the safe direction, and paying for it with a pod restart
	// on a rare operator action is the right trade.
	clusterHighWater int
	// passTimeout bounds one pass. See Run.
	passTimeout time.Duration
	// resumeAt is the index into the tenant list where the next pass begins — nonzero
	// only after a pass ran out of time. See Run.
	resumeAt int
	// sent holds the repairs emitted for each tenant on the last pass that read it, keyed by
	// DeviceKey, until the next read of that tenant settles them. See settleRepairs.
	sent map[string]map[string]sentRepair
}

// 🔑 A REPAIR AND THE TRANSITION IT REPLACES SHARE A DEDUP ID, WHICH IS WANTED AND
// SURPRISING. stateChangeDedupID keys on (tenant, device, session, state), so two repair
// passes emitting the same synthetic transition inside the JetStream dedup window
// collapse to one write — idempotent, which is the point. The consequence to know before
// reading a test result: a repair issued moments after the transition it is replacing can
// be SUPPRESSED rather than applied, so an integration test that reconciles twice inside
// one dedup window sees a single event and can read as "reconciliation is broken".
//
// NewReconciler binds a repair pass. now may be nil (⇒ time.Now), and passTimeout may be
// zero (⇒ DefaultPassTimeout) — a zero here would otherwise mean "already expired", so
// every pass would end before its first tenant. A missing bound must land on the default,
// never on nothing and never on an instant expiry.
func NewReconciler(tap *Tap, requests Requester, tenants TenantLister, reads ProjectionReader,
	metrics ReconcileMetrics, gather time.Duration, now func() time.Time, passTimeout time.Duration) *Reconciler {
	if now == nil {
		now = time.Now
	}
	if passTimeout <= 0 {
		passTimeout = DefaultPassTimeout
	}
	return &Reconciler{tap: tap, requests: requests, tenants: tenants, reads: reads,
		metrics: metrics, gather: gather, now: now, passTimeout: passTimeout}
}

// DefaultPassTimeout bounds one reconcile pass when the caller names no bound.
//
// It is deliberately generous — a pass walks every tenant, and each tenant costs a paged
// projection read — because the cost of cutting a healthy pass short is a real outage of
// repairs, while the cost of a long bound is only that a wedge takes longer to clear. The
// per-request deadlines are the tight bounds; this one is the backstop that guarantees a
// pass ENDS.
const DefaultPassTimeout = 4 * time.Minute

// PassTimeoutFor sizes a pass's budget against the interval passes are scheduled on.
//
// 🔑 A BUDGET LONGER THAN THE INTERVAL MEANS PASSES NEVER STOP. The reconcile interval is
// operator-settable and the default budget is a constant, so an instance configured to
// reconcile every minute would otherwise run four-minute passes back to back: ticks
// coalesce, there is no idle moment, and a fleet that always times out puts continuous
// paged-read load on device-state and user-management. Leaving a slice of the interval
// unspent is what keeps a pass a pass rather than a permanent background scan.
//
// The reservation is a fraction rather than a fixed subtraction so it holds at both ends
// of the range — a 30-second interval and an hour-long one are both left with room.
func PassTimeoutFor(interval time.Duration) time.Duration {
	if interval <= 0 {
		return DefaultPassTimeout
	}
	return min(DefaultPassTimeout, interval*4/5)
}

// Run performs one repair pass.
//
// 🔴 THE PASS IS BOUNDED, AND WHERE IT STOPPED IS REMEMBERED. Every broker request now
// carries a deadline (connz.go), but a pass over many tenants is still a long walk, so it
// gets a deadline of its own. A deadline alone would have been worse than none: the loop
// below leaves on ctx.Err(), and a pass that stopped early used to fall straight through
// to outcome "complete" and a nil error — reporting health while skipping every tenant it
// never reached. Since the tenant list arrives in a stable order and the walk used to
// restart at the beginning every time, a fleet whose pass could not finish would have
// reconciled the same prefix forever and never once reached its tail, silently.
//
// So a cut-short pass says so — outcome "timeout", a returned error — and the NEXT pass
// resumes from where this one stopped. That turns "some tenants are late" into a fair
// rotation rather than a permanent exclusion.
func (r *Reconciler) Run(ctx context.Context) error {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, r.passTimeout)
	defer cancel()

	inv, err := FetchInventory(ctx, r.requests, r.tap.instanceId, r.gather)
	if err != nil {
		r.metrics.run("failed")
		return err
	}
	inv = r.applyClusterHighWater(inv)
	tenants, err := r.tenants.TenantTokens(ctx)
	if err != nil {
		r.metrics.run("failed")
		return err
	}

	var total passCounts
	visited, readFailures, cutShort := 0, 0, false
	// 🔑 THE ROTATION IS ONE INDEX, AND IT ONLY MATTERS WHEN THINGS GO WRONG. On a healthy
	// instance every pass reaches every tenant and startedAt returns to zero, so this walks
	// the list in its own order. It earns its place only when a pass cannot finish — which
	// is exactly when starting from the beginning again would mean the tenants at the end
	// of the list are never reconciled at all.
	//
	// 🔴 A REMEMBERED INDEX PAST THE END DESCRIBES A LIST THAT NO LONGER EXISTS. Stop at
	// index 8 of ten tenants, the instance drops to five, and 8 names nothing — so it falls
	// back to the top, and startedAt (not r.resumeAt) is what the next resume point is
	// computed from below. A tenant added or removed between passes still shifts positions,
	// so resuming can repeat or skip one tenant once; that is acceptable in a way permanent
	// starvation is not.
	startedAt := r.resumeAt
	if startedAt >= len(tenants) {
		startedAt = 0
	}
	for i := range tenants {
		if ctx.Err() != nil {
			cutShort = true
			break
		}
		tenant := tenants[(startedAt+i)%len(tenants)]
		visited++
		stored, err := r.reads.AssertedStates(ctx, tenant, r.tap.source)
		if err != nil {
			// One tenant's read failing must not abort the others: the remaining
			// tenants' devices are equally stuck. It does cost this tenant a pass.
			readFailures++
			log.Warn().Err(err).Str("tenant", tenant).
				Msg("Could not read presence state while reconciling; this tenant is skipped for this pass.")
			continue
		}
		total.add(r.reconcileTenant(ctx, tenant, inv, stored))
	}
	// A pass that finished starts the next one at the top; a pass cut short resumes at the
	// tenant it never reached, counted from where this pass genuinely began.
	r.resumeAt = 0
	if cutShort && len(tenants) > 0 {
		r.resumeAt = (startedAt + visited) % len(tenants)
	}
	// A tenant gone from the list is never read again, so its unsettled repairs never
	// would be. Only a pass that saw the whole list can say which tenants those are.
	if !cutShort {
		listed := make(map[string]bool, len(tenants))
		for _, tenant := range tenants {
			listed[tenant] = true
		}
		for tenant := range r.sent {
			if !listed[tenant] {
				delete(r.sent, tenant)
			}
		}
	}

	r.metrics.repaired("connect", total.ConnectsApplied)
	r.metrics.repaired("disconnect", total.DisconnectsApplied)
	r.metrics.unapplied("connect", total.ConnectsUnapplied)
	r.metrics.unapplied("disconnect", total.DisconnectsUnapplied)
	if total.Withheld > 0 && r.metrics.SkippedDisconnects != nil {
		r.metrics.SkippedDisconnects.Add(float64(total.Withheld))
	}
	// Set unconditionally, including to zero. A gauge only means anything if the healthy
	// value is written as often as the unhealthy one; skipping the zero would leave the
	// last bad reading standing after the problem cleared.
	if r.metrics.RegressedSessions != nil {
		r.metrics.RegressedSessions.Set(float64(total.Regressed))
	}
	if !inv.Complete {
		log.Warn().Int("serversAnswered", inv.Servers).Int("serversExpected", inv.Expected).
			Int("withheldDisconnects", total.Withheld).
			Msg("Broker connection inventory was incomplete; devices were only ever marked ONLINE this pass.")
	}

	// 🔴 THE ORDER OF THESE CASES IS THE PRECEDENCE, and it is written once so the metric
	// and the returned error cannot disagree about what happened. Each says something the
	// one below it would hide:
	//
	//   - cancelled — the service is stopping. Not a fault, and it must not land on the
	//     series operators alert from: the pass deadline is a child of the service's own
	//     context, so from inside the loop a shutdown is indistinguishable from running
	//     out of budget.
	//   - timeout   — the pass ran out of ITS OWN budget and left tenants unvisited. It
	//     outranks "partial" because the two are different subjects: "partial" is a fact
	//     about the broker inventory, and reporting it for a pass that never finished
	//     would describe an unfinished pass as a finished one with a caveat.
	//   - failed    — every tenant was visited and none could be read, so nothing was
	//     reconciled. Per-tenant read failures are a WARN-and-continue, which is right per
	//     tenant and wrong in aggregate: with device-state down, or during the mixed
	//     window of a rolling upgrade where the query's own shape is refused, EVERY read
	//     fails and the only alertable metric used to read healthy.
	//   - partial   — the pass ran, but not every broker node answered.
	allReadsFailed := visited > 0 && readFailures == visited
	outcome := "complete"
	switch {
	case cutShort && parent.Err() != nil:
		outcome = "cancelled"
	case cutShort:
		outcome = "timeout"
	case allReadsFailed:
		outcome = "failed"
	case !inv.Complete:
		outcome = "partial"
	}
	r.metrics.run(outcome)
	if total.Connects > 0 || total.Disconnects > 0 {
		log.Info().Int("connects", total.Connects).Int("disconnects", total.Disconnects).
			Int("regressedSessions", total.Regressed).
			Msg("Emitted presence repairs for devices whose presence differed from the broker's live connections.")
	}
	if unapplied := total.ConnectsUnapplied + total.DisconnectsUnapplied; unapplied > 0 {
		log.Warn().Int("connects", total.ConnectsUnapplied).Int("disconnects", total.DisconnectsUnapplied).
			Msg("Presence repairs emitted on the previous pass were not applied; they were refused or lost downstream.")
	}
	if cutShort {
		if parent.Err() != nil {
			return nil // shutting down; the caller already knows and is not to be alarmed
		}
		return fmt.Errorf("the reconcile pass ran out of time after %d of %d tenants; "+
			"the rest are reconciled first on the next pass", visited, len(tenants))
	}
	if visited > 0 && readFailures == visited {
		return fmt.Errorf("no tenant's presence state could be read (%d of %d failed); "+
			"reconciliation did nothing this pass", readFailures, visited)
	}
	return nil
}

// passCounts is one pass's tally, per tenant and summed. It is a struct rather than a
// widening tuple of ints because every field has the same type and none of them is
// distinguishable at a call site — a transposed pair would compile and quietly mislabel
// an operator's metrics.
//
// Connects and Disconnects are repairs EMITTED this pass. The Applied and Unapplied pairs
// are the previous pass's repairs, settled by this pass's read (settleRepairs).
type passCounts struct {
	Connects             int
	Disconnects          int
	ConnectsApplied      int
	DisconnectsApplied   int
	ConnectsUnapplied    int
	DisconnectsUnapplied int
	Withheld             int
	Regressed            int
}

func (c *passCounts) add(o passCounts) {
	c.Connects += o.Connects
	c.Disconnects += o.Disconnects
	c.ConnectsApplied += o.ConnectsApplied
	c.DisconnectsApplied += o.DisconnectsApplied
	c.ConnectsUnapplied += o.ConnectsUnapplied
	c.DisconnectsUnapplied += o.DisconnectsUnapplied
	c.Withheld += o.Withheld
	c.Regressed += o.Regressed
}

// settleRepairs judges the repairs emitted for tenant on the last pass that read it against
// this pass's read of the same tenant, and forgets them. It runs before this pass emits, so
// a repair found unapplied is counted once here and emitted again after it.
func (r *Reconciler) settleRepairs(tenant string, stored map[string]StoredDevice) passCounts {
	var counts passCounts
	for key, s := range r.sent[tenant] {
		row, seen := stored[key]
		applied, unapplied := s.settle(row, seen)
		switch {
		case applied && s.connected:
			counts.ConnectsApplied++
		case applied:
			counts.DisconnectsApplied++
		case unapplied && s.connected:
			counts.ConnectsUnapplied++
		case unapplied:
			counts.DisconnectsUnapplied++
		}
	}
	delete(r.sent, tenant)
	return counts
}

// remember records a repair that was emitted, for the next read of its tenant to settle.
func (r *Reconciler) remember(tenant, key string, before StoredDevice, hadRow bool, session uint64, connected bool) {
	if r.sent == nil {
		r.sent = make(map[string]map[string]sentRepair)
	}
	if r.sent[tenant] == nil {
		r.sent[tenant] = make(map[string]sentRepair)
	}
	r.sent[tenant][key] = sentRepair{before: before, hadRow: hadRow, sessionId: session, connected: connected}
}

// applyClusterHighWater raises the pass's expectation to the largest cluster ever seen,
// downgrading Complete when fewer servers answered than that. See clusterHighWater.
func (r *Reconciler) applyClusterHighWater(inv Inventory) Inventory {
	if inv.Expected > r.clusterHighWater {
		r.clusterHighWater = inv.Expected
	}
	// The count that ANSWERED is itself evidence of cluster size: three replies prove
	// three servers exist regardless of what any of them claims.
	if inv.Servers > r.clusterHighWater {
		r.clusterHighWater = inv.Servers
	}
	inv.Expected = r.clusterHighWater
	inv.Complete = inv.Servers > 0 && inv.Servers >= r.clusterHighWater && inv.Complete
	return inv
}

// reconcileTenant diffs one tenant both ways.
func (r *Reconciler) reconcileTenant(ctx context.Context, tenant string, inv Inventory,
	stored map[string]StoredDevice) passCounts {
	counts := r.settleRepairs(tenant, stored)

	// One stamp for the pass, used both as the transitions' OccurredAt and as their dedup
	// nonce, so every emission below is attributable to this pass and no two passes
	// collapse into one write. See PresenceEvent.DedupNonce.
	at := r.now().UTC()
	nonce := at.Format(time.RFC3339Nano)

	// Direction 1 — the broker holds a connection the projection does not know is
	// live. Positive evidence, so it is emitted whether or not the inventory is
	// complete: a missing server's reply costs a delayed repair, never a false claim.
	for key, live := range inv.Devices {
		if live.Tenant != tenant {
			continue
		}
		known, seen := stored[key]

		// 🔴 A SESSION ID THAT WENT BACKWARDS IS THE ONE CASE AN ACTIVE ROW STILL NEEDS
		// REPAIRING, WHICH IS WHY THIS IS NOT JUST `known.Active`. Session ids are minted
		// from the wall clock of whichever broker node the device landed on, so a reconnect
		// onto a trailing node carries a genuinely-current id LOWER than the stored one.
		// Such a device can sit ACTIVE while filed under a session it is no longer on —
		// left alone, every event about the live connection is rejected as stale, including
		// its eventual death, so the row reads online forever and its commands are
		// dispatched to a subscriber that is gone.
		//
		// Three reachable states land here, and all three were previously skipped: a row
		// pinned online under a dead session (the whole population upgrading from the
		// earlier pin-based repair), a real death that crossed a repair in flight, and a
		// rejected death followed by a reconnect onto another trailing node.
		regressed := seen && live.SessionId < known.SessionId
		if seen && known.Active && !regressed {
			continue
		}
		if regressed {
			counts.Regressed++
		}
		// 🔴 STAMPED AT NOW, NOT AT THE CONNECTION'S START, AND THE OBVIOUS CHOICE IS
		// THE BROKEN ONE. The start is the more truthful instant and it is what the lost
		// advisory would have carried — but presence.Decide takes a SAME-session
		// transition only when its time is newer than the stored one, and the projection
		// may hold this very session at a LATER time: after a synthetic death (which
		// reuses the stored session and stamps NOW), the row reads offline at a moment
		// well after the connection began. A repair carrying the start is then older
		// than the death, rejected, and rejected identically on every subsequent pass —
		// forever, silently, while the repair counter says it worked. That is the
		// failure that looks exactly like success, and it wedges a live device offline,
		// holding its commands.
		//
		// The session id is normally the CONNECTION's, so this remains the same session
		// the advisory would have named and the dedup id is unchanged. What is given up
		// is accuracy of LastConnectTime on the repair path alone: it records when the
		// platform re-established the truth rather than when the device connected. On a
		// path that exists because the accurate signal was already lost, that is the
		// right thing to trade.
		//
		// 🔴 WHEN THE CONNECTION'S OWN SESSION HAS GONE BACKWARDS, THE REPAIR CARRIES A
		// COMPARE-AND-SET INSTEAD OF LYING ABOUT WHICH SESSION IT IS. Decide takes a
		// different session only when it is HIGHER, so a repair carrying this connection's
		// genuinely-lower id is rejected on its own — on this pass and on every pass after
		// it, forever, while the repair counter reports success.
		//
		// The earlier fix reused the STORED id, which Decide accepts as
		// same-session-newer-time. It worked, and it left the projection filed under a DEAD
		// session: the live connection's eventual death was then rejected for the same
		// ordering reason, so the row read online while the device was gone and its commands
		// went to a subscriber that no longer existed. It converted a permanent false-OFFLINE
		// into a false-ONLINE — the safer direction, but still a fiction, and one that could
		// only be cleared by a provably-complete inventory.
		//
		// 🔑 THE REPAIR NOW REPORTS THE SESSION THE DEVICE IS ACTUALLY ON, and proves it is
		// entitled to by naming the session it observed. This pass READ the projection, so
		// it can say "I am reporting session S, and I saw you holding E": if the row still
		// holds E the report applies and the device is re-filed onto its live session; if
		// anything has moved it since — a higher session, a death, another replica's repair
		// — the precondition fails and nothing happens. That is strictly better than the
		// pin, because afterwards the ordinary advisory path works again: the connection's
		// own DISCONNECT is same-session-newer-time against the session now stored.
		var expected uint64
		if regressed {
			expected = known.SessionId
			log.Warn().Str("tenant", live.Tenant).Str("device", live.DeviceToken).
				Uint64("brokerSession", live.SessionId).Uint64("storedSession", known.SessionId).
				Msg("Re-filing a device whose broker session id is OLDER than the stored one; " +
					"a broker node's clock is trailing its peers.")
		}
		if r.tap.Apply(ctx, Transition{
			Tenant:      live.Tenant,
			DeviceToken: live.DeviceToken,
			Event: adapter.PresenceEvent{
				Connected: true,
				Reason:    "reconcile-connected",
				// The connection's OWN session, always. Equality with the stored id needs
				// no compare-and-set: that is the ordinary recovery from a synthetic death
				// — same connection, same session, the row simply reads offline at a later
				// instant than the connect — and Decide takes it as same-session-newer-time.
				SessionId:         live.SessionId,
				ExpectedSessionId: expected,
				OccurredAt:        at,
				DedupNonce:        nonce,
			},
		}) {
			counts.Connects++
			r.remember(tenant, key, known, seen, live.SessionId, true)
		}
	}

	// Direction 2 — the projection believes a device is online that the broker is not
	// holding. This is the direction that can LIE, so it is gated on completeness.
	for key, believed := range stored {
		if !believed.Active {
			continue
		}
		if _, live := inv.Devices[key]; live {
			continue
		}
		if !inv.Complete {
			// A device on a server that did not answer is absent from Devices and
			// indistinguishable from one that is genuinely gone. Marking it offline
			// would hold a live device's commands, so the pass withholds instead.
			counts.Withheld++
			continue
		}
		if r.tap.Apply(ctx, Transition{
			Tenant:      tenant,
			DeviceToken: believed.DeviceToken,
			Event: adapter.PresenceEvent{
				Connected: false,
				Reason:    "reconcile-not-connected",
				// 🔑 THE SESSION IS THE ONE THE PROJECTION ALREADY HOLDS, and this is
				// the difference between a repair that works and one that is silently
				// rejected forever. presence.Decide takes a DIFFERENT session only when
				// it is HIGHER; a synthetic death carrying some other id — the last
				// known connection's, or one minted here — would be lower than the
				// stored one and rejected on every pass, with nothing to say why.
				// Reusing the stored id makes this same-session-newer-time, which
				// Decide accepts and which is exactly what this event means: the
				// session the projection is tracking has ended.
				SessionId:  believed.SessionId,
				OccurredAt: at,
				DedupNonce: nonce,
			},
		}) {
			counts.Disconnects++
			r.remember(tenant, key, believed, true, believed.SessionId, false)
		}
	}
	return counts
}

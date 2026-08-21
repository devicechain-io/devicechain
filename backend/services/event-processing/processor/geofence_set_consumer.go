// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"io"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// signalFenceSet marshals one FROZEN fence set onto the single-writer loop, where applyFenceSet
// installs it in the loop-owned projection (ADR-078).
//
// 🔴 IT CARRIES THE VALUE, WHERE THE ROSTER AND ATTRIBUTE SIGNALS CARRY ONLY AN IDENTITY. Those
// two send a RECHECK because the thing they mirror is mutable: two consumers over reorderable
// streams could otherwise install a stale observation, so the loop re-reads the converged
// projection instead. A fence-set VERSION has no such hazard — version 7's snapshot is frozen at
// mint and nothing ever rewrites it — so there is no order in which two of these facts could
// arrive that makes either wrong, and Put is keyed on the version each one carries. Sending the
// value is therefore not a shortcut past the convergence discipline; it is what that discipline
// reduces to when the mirrored state is immutable.
//
// It is a no-op when the projection is disabled (nil fenceView, the scaffold path — the view is
// built only when a fence source is wired). Returns false ONLY on shutdown mid-send, in which case
// the caller leaves the fact unacked; there is no durable projection to fall back on here, so the
// fact redelivers next start and, failing that, the startup reconcile re-seeds the current version
// from device-management's archive.
func (rp *ResolvedEventsProcessor) signalFenceSet(tenant string, set *geofence.FenceSet) bool {
	if rp.fenceView == nil {
		return true
	}
	select {
	case rp.fenceUpdates <- fenceUpdate{tenant: tenant, set: set}:
		return true
	case <-rp.procCtx.Done():
		return false
	}
}

// seedFencesForPublishedRules seeds a tenant's CURRENT fence set when a published-rule fact
// brings in a rule that tests containment, so the rule can evaluate from its first event.
//
// 🔴 WITHOUT THIS, A TENANT'S FIRST FENCE RULE DOES NOT WORK UNTIL SOMEONE EDITS A FENCE. The
// startup reconcile seeds only the tenants that ALREADY held a fence rule when the process
// started — it cannot seed a tenant whose rule does not exist yet — and the only other thing that
// fills the view is a geofence-set fact, which is minted by a fence WRITE. So a tenant that draws
// its fences on Monday and publishes the rule on Tuesday has a view with nothing in it: every
// event's stamped version misses, containment answers ErrNoFenceSet, and each sample is skipped
// and counted as an eval error. Loud, but the rule is dead, and the fix — go and re-save a fence
// you did not want to change — is not one an author would ever guess.
//
// It seeds unconditionally rather than checking whether the tenant is already held. The check
// would have to read fenceView, which is owned by the single-writer loop and must not be touched
// from this consumer goroutine, so "is it already seeded" is not a question that can be answered
// here without a race. Doing the read anyway is affordable because the trigger is a human
// authoring action, not event volume, and Put is idempotent on a version it already holds.
//
// The fence set is signalled before the rules it is for, but that is a preference, NOT a
// guarantee: the two travel on separate channels the loop selects over, so with both ready it may
// apply either first. The residual window is the microseconds between two already-queued sends,
// during which an event for that tenant would report an eval error. Closing it properly would mean
// carrying the fence set inside the ruleUpdate, which couples two independent facts to buy
// nothing — the failure it would prevent is a handful of eval errors, against the unbounded one
// this function exists to fix.
//
// A read failure is logged and skipped, not fatal, and does not hold up the fact: the same trade
// the startup reconcile makes. Refusing to install a whole profile's rules because one
// cross-service read blipped would be a far larger outage than the one it prevents, and the
// unseeded case still reports itself loudly. Returns false only on shutdown mid-send.
func (rp *ResolvedEventsProcessor) seedFencesForPublishedRules(rules []runtime.ScopedRule) bool {
	if rp.fenceView == nil || rp.FenceSets == nil {
		return true
	}
	tenant := ""
	for _, sr := range rules {
		if sr.Compiled == nil || !sr.Compiled.RequiresPosition {
			continue
		}
		tenant = sr.Tenant
		break
	}
	if tenant == "" {
		return true
	}
	set, err := rp.FenceSets.CurrentFenceSet(rp.procCtx, tenant)
	if err != nil {
		log.Error().Err(err).Str("tenant", tenant).
			Msg("Unable to seed the DETECT geofence projection for a newly published fence rule; its containment calls report unresolvable until a fence edit.")
		return true
	}
	if set == nil {
		return true
	}
	return rp.signalFenceSet(tenant, set)
}

// startFenceReconcile launches one periodic re-seed of the geofence projection. It runs ON the
// single-writer loop (the ticker branch) and does no I/O itself — see fenceReconcileInterval for
// why the sweep exists at all.
//
// 🔴 THE SPLIT BETWEEN THIS AND runFenceReconcile IS NOT STYLE — IT IS THE ONLY ARRANGEMENT THAT
// IS RACE-FREE. The sweep needs three things, and they belong to two different goroutines:
//
//   - the tenant list comes from rp.registry, which carries NO LOCK because all of its mutation
//     (applyRuleUpdate) and all of its hot-path reads (RulesFor, Lookup) share the loop goroutine.
//     Reading it from a sweep goroutine would be a data race on a map, so it is snapshotted HERE.
//   - the archive reads are cross-service round trips. Taking them on the loop would stall every
//     tenant's event processing behind device-management, which is the exact thing fenceView's
//     design note forbids — so they happen THERE.
//   - the install is fenceView.Put, and fenceView is loop-owned too. So the sweep does not write
//     it; it hands each set back over fenceUpdates (signalFenceSet), the same door the fact
//     consumer already uses.
//
// Skipping while a sweep is in flight is deliberate and is why lastFenceReconcile can stamp the
// START of a sweep without risk: if one somehow outlives the interval, the next tick drops its
// sweep rather than stacking a second one onto a service that is evidently not answering quickly.
// A dropped sweep costs one interval of repair latency for a fault measured in restarts.
func (rp *ResolvedEventsProcessor) startFenceReconcile() {
	if rp.fenceView == nil || rp.FenceSets == nil {
		return
	}
	if !rp.fenceReconciling.CompareAndSwap(false, true) {
		return // a previous sweep is still running; let it finish rather than pile on
	}
	tenants := rp.registry.TenantsWithFenceRules()
	if len(tenants) == 0 {
		rp.fenceReconciling.Store(false)
		return
	}
	// Add from the loop goroutine, which itself holds a readerWG count until run() returns — so
	// this can never be the Add that races ExecuteStop's Wait up from zero.
	rp.readerWG.Add(1)
	go rp.runFenceReconcile(tenants)
}

// runFenceReconcile reads each tenant's CURRENT frozen fence set from device-management's archive
// and hands it to the single-writer loop. It is the off-loop half of startFenceReconcile.
//
// It re-seeds unconditionally rather than asking whether a tenant is already current, for the same
// reason seedFencesForPublishedRules does: the answer lives in fenceView, which this goroutine must
// not touch. Put is idempotent on a version already held (a repeat replaces in place and consumes
// no retention slot), so a sweep that finds nothing wrong changes nothing.
//
// Reading the CURRENT version is the right repair for this fault specifically. A lost publish means
// events resolved from now on are stamped with a version the view never received; seeding current
// makes the live path whole immediately. It does not retroactively make a replay whole, and that
// residual stays deliberate — reported as a loud counted eval error, resolved off-loop through
// FenceSetSource by the callers that can afford to block.
//
// A per-tenant read failure is logged and skipped rather than abandoning the sweep: one tenant's
// blip must not deny the repair to every other tenant, and the next interval retries anyway.
func (rp *ResolvedEventsProcessor) runFenceReconcile(tenants []string) {
	defer rp.readerWG.Done()
	defer rp.fenceReconciling.Store(false)

	reseeded := 0
	for _, tenant := range tenants {
		set, err := rp.FenceSets.CurrentFenceSet(rp.procCtx, tenant)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).
				Msg("Unable to re-seed the DETECT geofence projection for a tenant; its containment calls report unresolvable until the next sweep.")
			continue
		}
		if set == nil {
			continue
		}
		if !rp.signalFenceSet(tenant, set) {
			return // shutdown mid-send: the next start reconciles from the archive anyway
		}
		reseeded++
	}
	log.Debug().Int("tenants", len(tenants)).Int("reseeded", reseeded).
		Msg("Swept the DETECT geofence projection against device-management's frozen fence sets.")
}

// runFenceSetConsumer drains device-management's geofence-set fact stream (ADR-078): the FROZEN
// fence set of each newly-minted fence-set version. For each fact it hands the compiled set to the
// single-writer loop and then acks.
//
// It is the ONLY fact consumer here with no persist-before-ack step, and the asymmetry is
// deliberate rather than an omission. The other consumers ack against a durable projection they
// own because the fact stream's finite retention is not a system of record. For fence sets the
// system of record already exists — device-management stores every version's snapshot durably —
// so building a second copy here would be a duplicate archive to keep honest, and the restart
// path reads the original instead (reconcileFenceView). What survives a restart is therefore
// device-management's row, not a row of ours.
func (rp *ResolvedEventsProcessor) runFenceSetConsumer() {
	defer rp.readerWG.Done()
	for {
		msg, err := rp.FenceSetReader.ReadMessage(rp.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.FenceSetReader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
			continue
		}
		if !rp.handleFenceSetFact(msg) {
			return // shutdown mid-send: leave unacked; it redelivers next start
		}
	}
}

// handleFenceSetFact compiles one fence-set fact, installs it on the single-writer loop, and acks.
//
// It takes no "signal" flag, unlike the roster/entity-deleted handlers, because there is no
// pre-replay catch-up drain for this stream and there could not usefully be one: those drains exist
// to bring a DURABLE PROJECTION current before the gate reads it, and this consumer maintains no
// durable projection — the startup reconcile reads device-management's archive instead, which is
// strictly fresher than any backlog the stream still holds.
//
// It returns false ONLY on a shutdown mid-send (the fact is left unacked to redeliver); a poison or
// malformed fact is dropped-and-acked and returns true.
func (rp *ResolvedEventsProcessor) handleFenceSetFact(msg messaging.Message) bool {
	tenant, ev, ok := decodeFenceSetFact(rp, msg)
	if !ok {
		// Unparseable/poison: redelivery cannot fix it. Ack so it stops redelivering forever.
		rp.ackFact(msg, "geofence-set")
		return true
	}
	// A non-positive version is unusable: 0 is the reserved "the tenant had never created a
	// fence" stamp, which the view answers from knowledge without consulting the map, so a fact
	// claiming to BE version 0 could only overwrite that with something a producer invented.
	// Nothing legitimate mints one — mintGeoFenceSetVersion starts at 1 — so this is a forged or
	// buggy-producer fact, dropped-and-acked rather than filed.
	if ev.Version <= 0 {
		log.Warn().Int32("version", ev.Version).Str("subject", msg.Subject).
			Msg("Dropping malformed geofence-set fact (non-positive fence-set version).")
		rp.ackFact(msg, "geofence-set")
		return true
	}
	// A POINTER fact carries no fences and must be resolved from the archive. It is
	// checked BEFORE ev.Fences is read, because after decoding the two are identical:
	// a pointer fact and a genuinely-empty set both hold an empty list, and only this
	// flag separates "go and fetch it" from "this tenant has no fences", which is a
	// real and meaningful state (a tenant that deleted its last fence). Reading the
	// list first would install a hundred fences as none, and containment would then
	// answer "outside" forever — a healthy-looking rule that never fires.
	if ev.FencesOmitted {
		return rp.resolveOmittedFenceSet(msg, tenant, ev.Version)
	}
	fences := make([]geofence.SnapshotFence, 0, len(ev.Fences))
	for _, f := range ev.Fences {
		fences = append(fences, geofence.SnapshotFence{Token: f.Token, Geometry: f.Geometry})
	}
	// Compiled on the CONSUMER goroutine, not on the loop: compiling a hundred fences of a few
	// hundred vertices each is real work, and the loop is the one goroutine every tenant's event
	// processing shares. What crosses to the loop is the finished, immutable set. A fence whose
	// geometry will not compile is retained with its error rather than dropped, so one bad fence
	// cannot disable containment for the rest of the tenant's set.
	if !rp.signalFenceSet(tenant, geofence.NewFenceSet(ev.Version, fences)) {
		return false // shutdown mid-send: leave unacked; it redelivers next start
	}
	rp.ackFact(msg, "geofence-set")
	return true
}

// resolveOmittedFenceSet resolves a POINTER fact — one whose fence set was too large for a single
// broker message — by reading that version out of device-management's frozen archive, then
// installing it on the single-writer loop exactly as an ordinary fact's set is installed.
//
// 🔴 IT READS THE VERSION THE FACT NAMES, NEVER THE CURRENT ONE. Those differ the moment a second
// fence edit lands while this one is being resolved, and installing "current" under a fact that
// announced version 7 would leave the view holding 8 with 7 still missing — every event stamped 7
// would report unresolvable, which is the exact fault the pointer fact exists to prevent. The
// version-addressed read is also the only deterministic one: version 7's snapshot is frozen and
// cannot become something else between the fact and the fetch.
//
// The blocking read is legal HERE and would not be one line further in. This runs on the fact
// consumer's own goroutine, which already does the fence compile for the same reason; the
// single-writer loop — where every tenant's event processing is serialized — is on the far side of
// signalFenceSet and never waits on this.
//
// A failed read is logged and ACKED rather than left to redeliver. Redelivery would re-run the same
// read against the same unavailable peer on the broker's schedule, while the five-minute reconcile
// sweep already exists to repair precisely a version the view never received and will retry with
// backoff-shaped cadence forever. Acking also keeps a persistently unreadable version from parking
// the stream behind it. The residual — up to one sweep interval of eval errors for that tenant — is
// the same trade the sweep's other callers make, and it is reported, not silent.
func (rp *ResolvedEventsProcessor) resolveOmittedFenceSet(msg messaging.Message, tenant string, version int32) bool {
	if rp.fenceView == nil {
		rp.ackFact(msg, "geofence-set")
		return true
	}
	set, err := rp.fetchOmittedFenceSet(tenant, version)
	if err != nil {
		rp.metrics.recordFenceSetPointerUnresolved()
		log.Error().Err(err).Str("tenant", tenant).Int32("version", version).
			Msg("Unable to resolve a geofence-set fact that arrived without its fences; containment for this tenant reports unresolvable at this version until the next reconcile sweep.")
		rp.ackFact(msg, "geofence-set")
		return true
	}
	if !rp.signalFenceSet(tenant, set) {
		return false // shutdown mid-send: leave unacked; it redelivers next start
	}
	rp.ackFact(msg, "geofence-set")
	return true
}

// errNoFenceSetArchive reports that no archive seam is wired, so a pointer fact's fences are
// unreachable.
//
// 🔴 IT IS AN ERROR AND NOT A BRANCH OF ITS OWN, DELIBERATELY. Written as a separate early
// return with its own log line and its own metric increment, it was UNREACHABLE in production:
// it needs fenceView non-nil and VersionedFenceSets nil, but fenceView is built only when the
// seam exists and buildFenceSetSeam returns both halves of one client or two nils. A dead
// branch carrying the only report of the state it describes is worse than no branch — it reads
// as coverage. Folded in here it is genuinely reachable (any caller assembling a processor by
// hand can produce it), it goes through the same counted, logged path as every other failure to
// resolve, and the nil check still stands between this code and a nil-interface call.
var errNoFenceSetArchive = errors.New("no fence-set archive seam is configured; a fact that arrived without its fences cannot be resolved")

// errFenceSetArchiveEmpty reports an archive read that succeeded and returned nothing. The
// source's contract is an error or a set, never a silent nil, so this is a broken implementation
// rather than a missing version — but it is folded into the same reported failure because the
// consequence is identical and the alternative is installing nil as a fence set.
var errFenceSetArchiveEmpty = errors.New("the fence-set archive returned no set and no error")

// fetchOmittedFenceSet reads one version out of the archive, turning every way the read can
// fail to produce a usable set into an error the caller reports once.
func (rp *ResolvedEventsProcessor) fetchOmittedFenceSet(tenant string, version int32) (*geofence.FenceSet, error) {
	if rp.VersionedFenceSets == nil {
		return nil, errNoFenceSetArchive
	}
	set, err := rp.VersionedFenceSets.FenceSetAt(rp.procCtx, tenant, version)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, errFenceSetArchiveEmpty
	}
	return set, nil
}

// decodeFenceSetFact unmarshals one geofence-set fact into its owning tenant (from the per-tenant
// subject) and payload, with the same drop-not-fatal contract as decodeRosterFact. The tenant
// travels on the subject, never the payload — which is what makes one tenant's fences unreachable
// through another tenant's projection entry however the payload is shaped.
func decodeFenceSetFact(rp *ResolvedEventsProcessor, msg messaging.Message) (string, *dmmodel.GeoFenceSetMintedEvent, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping geofence-set fact with no parseable tenant in subject %q", msg.Subject)
		return "", nil, false
	}
	ev, err := dmmodel.UnmarshalGeoFenceSetMintedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping geofence-set fact that could not be parsed from subject %q", msg.Subject)
		return "", nil, false
	}
	return tenant, ev, true
}

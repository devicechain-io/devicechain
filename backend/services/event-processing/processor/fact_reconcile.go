// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
)

// factReconcileInterval is how often the leader compares its rule, active-version, roster and
// attribute projections with device-management and repairs what differs.
//
// 🔴 IT REPAIRS THE LOSS NO OTHER PATH CAN SEE: A FACT THAT NEVER REACHED THE STREAM. device-
// management announces a profile publish or rollback, a device create or re-type, and a threshold
// attribute change with ONE message each, sent after its transaction commits and best-effort — the
// change has committed and must not be failed by a wire problem. A broker disruption, or a restart
// between the commit and the send, therefore loses the announcement, and nothing redelivers a
// message that was never written. Before this sweep that silenced a whole profile (every device
// resolves the new version token and the registry held nothing under it), left a never-reporting
// device unwatched for silence, and kept a dynamic threshold at its old value — indefinitely, with
// the only remedy a republish. Like the geofence sweep, the trigger is the ABSENCE of an event,
// which is why it must be a timer.
//
// The cost per tenant is one read per page of each projection (see the Max…PageSize constants in
// device-management's model), plus one tenant listing per sweep; a tenant that diverges pays one
// conditional write per divergent row. Five minutes is the geofence sweep's trade.
const factReconcileInterval = 5 * time.Minute

// factSettle is how old device-management's instant for a change must be before the reconcile
// will act on a divergence it explains.
//
// The reconcile reads its own projection first and device-management second, so a change that
// commits between the two — or one whose fact is simply still in flight, published and not yet
// consumed — reads as a divergence without anything having been lost. Repairing it would be
// harmless (the fact, arriving later, finds the row already current) but COUNTING it would not: the
// repairs counter would read "notifications are being lost" on an instance that lost nothing, and
// an alert that fires on ordinary traffic is one operators learn to ignore. So a divergence whose
// device-management instant is newer than this is left for the next sweep. Two minutes is twice
// the fact consumers' AckWait: a fact that has not been consumed in that time is not in flight, it
// is lost or stuck, and repairing it is the point.
//
// A divergence that device-management cannot date — a row this service holds that device-
// management no longer has, i.e. a deletion — is instead confirmed across two consecutive sweeps
// (see absentSeen) before it is acted on.
const factSettle = 2 * time.Minute

// reconcileProjection names one projection the fact reconcile repairs, and is the label value of
// its two counters.
type reconcileProjection string

const (
	projectionTenants       reconcileProjection = "tenants"
	projectionRules         reconcileProjection = "rules"
	projectionProfileActive reconcileProjection = "profile_active"
	projectionRoster        reconcileProjection = "roster"
	projectionAttributes    reconcileProjection = "attributes"
)

// repairProjections are the label values of the repairs counter; failureProjections those of the
// failures counter (the active version is read in the same walk as the rules, so it fails with
// them, and the tenant listing can fail but repairs nothing).
var (
	repairProjections  = []reconcileProjection{projectionRules, projectionProfileActive, projectionRoster, projectionAttributes}
	failureProjections = []reconcileProjection{projectionTenants, projectionRules, projectionRoster, projectionAttributes}
)

// errReconcileShutdown reports that the term ended while the sweep was handing a repair to the
// loop. The repair itself is durable; the next term's startup reads the repaired projection.
var errReconcileShutdown = errors.New("fact reconcile interrupted by the end of the DETECT term")

// attributeKey identifies one threshold attribute.
type attributeKey struct {
	device, scope, key string
}

// absentSeen remembers, per projection and tenant, the rows this service held that device-
// management did NOT return on the previous sweep, exactly as they were observed. A row is
// tombstoned only when the next sweep finds it absent again AND unchanged: the deletion was then
// visible to two reads at least one interval apart, so it is not a fact merely in flight (device-
// management's own entity-deleted announcement is at-least-once and usually lands first).
//
// It is touched only by the sweep goroutine — one at a time, handed over by factReconciling — and
// by resetForTerm, after the previous term's sweep has been joined.
type absentSeen struct {
	roster     map[string]map[string]model.DeviceRoster
	attributes map[string]map[attributeKey]model.DeviceAttribute
}

func newAbsentSeen() *absentSeen {
	return &absentSeen{
		roster:     make(map[string]map[string]model.DeviceRoster),
		attributes: make(map[string]map[attributeKey]model.DeviceAttribute),
	}
}

// startFactReconcile launches one fact sweep. It runs ON the single-writer loop (the ticker
// branch) and does no I/O itself.
//
// The split from runFactReconcile is the geofence sweep's, for the same reasons: device-
// management and user-management round trips must not stall every tenant's event processing, and
// the registry, the dead-man armer and the attribute view are loop-owned, so the sweep repairs the
// durable projections off the loop and hands each repair back through the doors a delivered fact
// uses (ruleUpdates, signalArmRecheck, signalAttrRecheck). One sweep at a time: a tick that finds
// one still running drops its own rather than stacking reads onto a peer that is evidently slow.
func (rp *ResolvedEventsProcessor) startFactReconcile() {
	if rp.DeviceManagement == nil {
		return
	}
	if !rp.factReconciling.CompareAndSwap(false, true) {
		return // a previous sweep is still running; let it finish rather than pile on
	}
	// Add from the loop goroutine, which itself holds a readerWG count until run() returns — so
	// this can never be the Add that races ExecuteStop's Wait up from zero.
	rp.readerWG.Add(1)
	go rp.runFactReconcile()
}

// runFactReconcile is the off-loop half of startFactReconcile.
func (rp *ResolvedEventsProcessor) runFactReconcile() {
	defer rp.readerWG.Done()
	defer rp.factReconciling.Store(false)
	rp.reconcileFacts(rp.pctx())
}

// reconcileFacts compares every tenant's rule, active-version, roster and attribute projections
// with device-management and repairs what differs. It reports whether every tenant and projection
// completed.
//
// A failure is contained to the projection it happened in: logged with the tenant, counted, and
// the projection left exactly as it was — a failed or partial read never deletes anything, because
// only a COMPLETE answer from device-management can say a row is gone. A tenant being erased
// (rdb.ErrTenantPurged on a write) skips the rest of that tenant silently, as the fact consumers
// drop its facts. A failed tenant listing reconciles nothing at all.
func (rp *ResolvedEventsProcessor) reconcileFacts(ctx context.Context) bool {
	if rp.factAbsent == nil {
		rp.factAbsent = newAbsentSeen() // a processor assembled without its constructor
	}
	began := rp.clock.Now()
	settledBefore := began.Add(-factSettle)
	tenants, err := rp.DeviceManagement.Tenants(ctx)
	if err != nil {
		log.Error().Err(err).
			Msg("Unable to list tenants for the detection fact reconcile; nothing was compared with device-management this sweep.")
		rp.metrics.factReconcileFailed(projectionTenants)
		return false
	}
	complete := true
	repaired := make(map[reconcileProjection]int)
	failed := make(map[reconcileProjection]int)
	steps := []struct {
		projection reconcileProjection
		run        func(context.Context, string, time.Time) (map[reconcileProjection]int, error)
	}{
		{projectionRules, rp.reconcileTenantRules},
		{projectionRoster, rp.reconcileTenantRoster},
		{projectionAttributes, rp.reconcileTenantAttributes},
	}
tenantLoop:
	for _, tenant := range tenants {
		for _, step := range steps {
			if ctx.Err() != nil {
				return false
			}
			counts, err := step.run(ctx, tenant, settledBefore)
			for p, n := range counts {
				repaired[p] += n
				rp.metrics.factRepaired(p, n)
			}
			switch {
			case err == nil:
			case errors.Is(err, errReconcileShutdown), ctx.Err() != nil:
				// The term ended mid-sweep: not a failure of the comparison, and the next
				// term's first tick sweeps again.
				return false
			case errors.Is(err, rdb.ErrTenantPurged):
				log.Debug().Str("tenant", tenant).Msg("Skipping the fact reconcile for a tenant that is being erased.")
				continue tenantLoop
			default:
				log.Error().Err(err).Str("tenant", tenant).Str("projection", string(step.projection)).
					Msg("The detection fact reconcile could not compare a projection with device-management; it is left as it was and retried next sweep.")
				rp.metrics.factReconcileFailed(step.projection)
				failed[step.projection]++
				complete = false
			}
		}
	}
	ev := log.Debug()
	if len(repaired) > 0 || len(failed) > 0 {
		ev = log.Info()
	}
	ev.Int("tenants", len(tenants)).Interface("repaired", repaired).Interface("failed", failed).
		Dur("took", rp.clock.Now().Sub(began)).
		Msg("Compared the detection projections with device-management.")
	return complete
}

// reconcileTenantRules repairs one tenant's rule and active-version projections: for every
// published profile, the active version's rules and which version is active.
//
// Only a profile's ACTIVE version is reconciled. Superseded versions' rules are retained by design
// and a profile device-management no longer publishes is left as it is — the dead-man armer's
// documented residual — because neither changes what an event resolved now is evaluated against.
func (rp *ResolvedEventsProcessor) reconcileTenantRules(ctx context.Context, tenant string, settledBefore time.Time) (map[reconcileProjection]int, error) {
	counts := make(map[reconcileProjection]int)
	if rp.RuleStore == nil || rp.ProfileActiveStore == nil {
		return counts, nil
	}
	// This service's rows FIRST, device-management's second: every row read here was written from
	// a fact device-management sent after committing, so "held here, absent from a complete answer
	// read afterwards" means gone there.
	actives, err := rp.ProfileActiveStore.LoadTenant(ctx, tenant)
	if err != nil {
		return counts, err
	}
	heldRules, err := rp.RuleStore.LoadTenant(ctx, tenant)
	if err != nil {
		return counts, err
	}
	profiles, err := rp.DeviceManagement.ActiveProfiles(ctx, tenant)
	if err != nil {
		return counts, err
	}
	activeByProfile := make(map[string]model.ProfileActive, len(actives))
	for _, a := range actives {
		activeByProfile[a.ProfileToken] = a
	}
	rulesByVersion := make(map[string]map[string]model.DetectRule)
	for _, r := range heldRules {
		if rulesByVersion[r.ProfileVersionToken] == nil {
			rulesByVersion[r.ProfileVersionToken] = make(map[string]model.DetectRule)
		}
		rulesByVersion[r.ProfileVersionToken][r.RuleToken] = r
	}

	for _, p := range profiles {
		if p.ActiveSince.After(settledBefore) {
			continue // activated moments ago: its fact may be in flight (see factSettle)
		}
		// The rows and the active-version row are built by the SAME code a delivered fact is
		// persisted through, so a repair writes exactly what the lost fact would have.
		ev := &dmmodel.DetectionRulesPublishedEvent{ProfileVersionToken: p.VersionToken, Rules: p.Rules, PublishedAt: p.ActiveSince}
		wantActive, ok := profileActiveFromFact(tenant, ev)
		if !ok {
			log.Warn().Str("tenant", tenant).Str("version", p.VersionToken).
				Msg("Skipping a published profile whose version token is malformed.")
			continue
		}
		held := rulesByVersion[p.VersionToken]
		var changed []dmmodel.PublishedDetectionRule
		var removed []string
		wanted := make(map[string]bool)
		for _, want := range factRuleRows(tenant, ev) {
			wanted[want.RuleToken] = true
			observed, present := held[want.RuleToken]
			if present && sameRuleRow(observed, want) {
				continue
			}
			var applied bool
			if present {
				applied, err = rp.RuleStore.ReplaceIf(ctx, &want, observed)
			} else {
				applied, err = rp.RuleStore.InsertIfAbsent(ctx, &want)
			}
			if err != nil {
				return counts, err
			}
			if applied {
				changed = append(changed, publishedRuleOf(want))
			}
		}
		for token, observed := range held {
			if wanted[token] {
				continue
			}
			applied, err := rp.RuleStore.DeleteIf(ctx, observed)
			if err != nil {
				return counts, err
			}
			if applied {
				removed = append(removed, observed.RuleId)
			}
		}
		counts[projectionRules] += len(changed) + len(removed)

		activeChanged := false
		if observed, present := activeByProfile[wantActive.ProfileToken]; !present {
			activeChanged, err = rp.ProfileActiveStore.InsertIfAbsent(ctx, wantActive)
		} else if observed.ActiveVersionToken != wantActive.ActiveVersionToken {
			activeChanged, err = rp.ProfileActiveStore.ReplaceIf(ctx, wantActive, observed)
		}
		if err != nil {
			return counts, err
		}
		if activeChanged {
			counts[projectionProfileActive]++
		}
		if len(changed) > 0 || len(removed) > 0 || activeChanged {
			if err := rp.signalRuleRepair(ctx, tenant, wantActive.ProfileToken, p.VersionToken, changed, removed); err != nil {
				return counts, err
			}
		}
	}
	return counts, nil
}

// sameRuleRow reports whether a held rule row already says what device-management says: the same
// rule (dmmodel.SameRuleDefinition — never a byte comparison) under the same group scope.
func sameRuleRow(held, want model.DetectRule) bool {
	return dmmodel.SameRuleDefinition(held.Definition, want.Definition) &&
		held.EntityGroupToken == want.EntityGroupToken &&
		held.EntityGroupVersion == want.EntityGroupVersion
}

// publishedRuleOf turns a projection row back into the rule a fact carries.
func publishedRuleOf(row model.DetectRule) dmmodel.PublishedDetectionRule {
	return dmmodel.PublishedDetectionRule{
		Token:              row.RuleToken,
		Definition:         row.Definition,
		EntityGroupToken:   row.EntityGroupToken,
		EntityGroupVersion: row.EntityGroupVersion,
	}
}

// signalRuleRepair hands a repaired profile version to the loop exactly as handleRuleFact hands a
// delivered one: fences first, then the compiled rules and the authoritative active version in one
// ruleUpdate, so applyRuleUpdate installs the rules before it arms against them.
func (rp *ResolvedEventsProcessor) signalRuleRepair(ctx context.Context, tenant, profileToken, versionToken string,
	changed []dmmodel.PublishedDetectionRule, removed []string) error {
	compiled, _ := runtime.CompilePublishedRules(tenant, versionToken, changed)
	if !rp.seedFencesForPublishedRules(compiled) {
		return errReconcileShutdown
	}
	var active *runtime.ActiveEntry
	if rp.armer != nil {
		row, found, err := rp.ProfileActiveStore.Load(ctx, tenant, profileToken)
		if err != nil {
			return err
		}
		if found {
			active = &runtime.ActiveEntry{Tenant: row.Tenant, ProfileToken: row.ProfileToken,
				ActiveVersionToken: row.ActiveVersionToken, PublishedAt: row.PublishedAt}
		}
	}
	select {
	case rp.ruleUpdates <- ruleUpdate{upserts: compiled, removals: removed, active: active}:
		return nil
	case <-rp.pctx().Done():
		return errReconcileShutdown
	}
}

// reconcileTenantRoster repairs one tenant's device roster: a device device-management has and
// this service does not (or holds under another profile, or holds as deleted) is re-rostered; a
// device this service holds live and device-management no longer has is tombstoned once two sweeps
// agree it is gone. Each repaired device is re-checked on the loop, which re-reads the projection —
// so the dead-man armer arms or disarms it exactly as a delivered fact would.
//
// Identity, not the instant, decides a divergence: a row on the right profile is left alone even
// if its membership instant differs, because the armer's deadline only moves forward and a
// same-profile difference re-arms nothing. That is also what keeps an upgrade quiet — rows written
// before device-management stored the instant differ from it only in the instant.
func (rp *ResolvedEventsProcessor) reconcileTenantRoster(ctx context.Context, tenant string, settledBefore time.Time) (map[reconcileProjection]int, error) {
	counts := make(map[reconcileProjection]int)
	if rp.RosterStore == nil {
		return counts, nil
	}
	heldRows, err := rp.RosterStore.LoadTenant(ctx, tenant)
	if err != nil {
		return counts, err
	}
	entries, err := rp.DeviceManagement.Roster(ctx, tenant)
	if err != nil {
		return counts, err
	}
	held := make(map[string]model.DeviceRoster, len(heldRows))
	for _, r := range heldRows {
		held[r.DeviceToken] = r
	}
	seen := make(map[string]bool, len(entries))
	var changed []string
	for _, e := range entries {
		seen[e.DeviceToken] = true
		// The same drop rule the roster consumer applies to a fact: a row the live path would
		// refuse is not written by the repair path either.
		if e.DeviceToken == "" || !validRosterToken(e.DeviceToken) || !validRosterToken(e.ProfileToken) || e.ExpectedSince.IsZero() {
			log.Warn().Str("tenant", tenant).Str("device", e.DeviceToken).
				Msg("Skipping a device-management roster entry the roster consumer would drop (empty/over-long token or zero expected-since).")
			continue
		}
		observed, present := held[e.DeviceToken]
		if present && !observed.Deleted && observed.ProfileToken == e.ProfileToken {
			continue
		}
		if e.ExpectedSince.After(settledBefore) {
			continue // a membership that began moments ago: its fact may be in flight
		}
		want := &model.DeviceRoster{Tenant: tenant, DeviceToken: e.DeviceToken, ProfileToken: e.ProfileToken, ExpectedSince: e.ExpectedSince}
		var applied bool
		if present {
			applied, err = rp.RosterStore.ReplaceIf(ctx, want, observed)
		} else {
			applied, err = rp.RosterStore.InsertIfAbsent(ctx, want)
		}
		if err != nil {
			return counts, err
		}
		if applied {
			changed = append(changed, e.DeviceToken)
		}
	}
	previous := rp.factAbsent.roster[tenant]
	next := make(map[string]model.DeviceRoster)
	for token, observed := range held {
		if observed.Deleted || seen[token] {
			continue
		}
		if before, ok := previous[token]; !ok || !sameRosterRow(before, observed) {
			next[token] = observed // first sighting: confirm on the next sweep
			continue
		}
		applied, err := rp.RosterStore.TombstoneIf(ctx, observed)
		if err != nil {
			return counts, err
		}
		if applied {
			changed = append(changed, token)
		}
	}
	rp.factAbsent.roster[tenant] = next
	counts[projectionRoster] = len(changed)
	for _, token := range changed {
		if !rp.signalArmRecheck(tenant, token) {
			return counts, errReconcileShutdown
		}
	}
	return counts, nil
}

// sameRosterRow reports whether two reads of a roster row saw the same row.
func sameRosterRow(a, b model.DeviceRoster) bool {
	return a.Deleted == b.Deleted && a.ProfileToken == b.ProfileToken &&
		a.ExpectedSince.Equal(b.ExpectedSince) && a.LastEventAt.Equal(b.LastEventAt)
}

// reconcileTenantAttributes repairs one tenant's threshold-attribute projection, by the same rules
// as the roster: a value device-management has and this service does not (or holds as another
// value, or as removed) is written; a value this service holds live and device-management no
// longer has is tombstoned once two sweeps agree. Identity is the value — a row holding the right
// number is left alone whatever its instant. Each repaired device's view entry is re-read on the
// loop, as after a delivered fact.
func (rp *ResolvedEventsProcessor) reconcileTenantAttributes(ctx context.Context, tenant string, settledBefore time.Time) (map[reconcileProjection]int, error) {
	counts := make(map[reconcileProjection]int)
	if rp.AttributeStore == nil {
		return counts, nil
	}
	heldRows, err := rp.AttributeStore.LoadTenant(ctx, tenant)
	if err != nil {
		return counts, err
	}
	entries, err := rp.DeviceManagement.ThresholdAttributes(ctx, tenant)
	if err != nil {
		return counts, err
	}
	held := make(map[attributeKey]model.DeviceAttribute, len(heldRows))
	for _, r := range heldRows {
		held[attributeKey{r.DeviceToken, r.Scope, r.AttrKey}] = r
	}
	seen := make(map[attributeKey]bool, len(entries))
	changed := make(map[string]bool)
	for _, e := range entries {
		k := attributeKey{e.DeviceToken, e.Scope, e.AttrKey}
		seen[k] = true
		// The attribute consumer's own drop rule: what the live path refuses, the repair path
		// does not write.
		if reason, bad := attributeFactPoison(&dmmodel.DeviceAttributeEvent{DeviceToken: e.DeviceToken,
			AttrKey: e.AttrKey, Scope: e.Scope, Value: e.Value, UpdatedAt: e.UpdatedAt}); bad {
			log.Warn().Str("tenant", tenant).Str("device", e.DeviceToken).Str("key", e.AttrKey).Str("reason", reason).
				Msg("Skipping a device-management threshold attribute the attribute consumer would drop.")
			continue
		}
		observed, present := held[k]
		if present && !observed.Deleted && observed.Value == e.Value {
			continue
		}
		if e.UpdatedAt.After(settledBefore) {
			continue // written moments ago: its fact may be in flight
		}
		want := &model.DeviceAttribute{Tenant: tenant, DeviceToken: e.DeviceToken, Scope: e.Scope,
			AttrKey: e.AttrKey, Value: e.Value, LastEventAt: e.UpdatedAt}
		var applied bool
		if present {
			applied, err = rp.AttributeStore.ReplaceIf(ctx, want, observed)
		} else {
			applied, err = rp.AttributeStore.InsertIfAbsent(ctx, want)
		}
		if err != nil {
			return counts, err
		}
		if applied {
			counts[projectionAttributes]++
			changed[e.DeviceToken] = true
		}
	}
	previous := rp.factAbsent.attributes[tenant]
	next := make(map[attributeKey]model.DeviceAttribute)
	for k, observed := range held {
		if observed.Deleted || seen[k] {
			continue
		}
		if before, ok := previous[k]; !ok || !sameAttributeRow(before, observed) {
			next[k] = observed // first sighting: confirm on the next sweep
			continue
		}
		applied, err := rp.AttributeStore.TombstoneIf(ctx, observed)
		if err != nil {
			return counts, err
		}
		if applied {
			counts[projectionAttributes]++
			changed[k.device] = true
		}
	}
	rp.factAbsent.attributes[tenant] = next
	for device := range changed {
		if !rp.signalAttrRecheck(tenant, device) {
			return counts, errReconcileShutdown
		}
	}
	return counts, nil
}

// sameAttributeRow reports whether two reads of an attribute row saw the same row.
func sameAttributeRow(a, b model.DeviceAttribute) bool {
	return a.Deleted == b.Deleted && a.Value == b.Value && a.LastEventAt.Equal(b.LastEventAt)
}

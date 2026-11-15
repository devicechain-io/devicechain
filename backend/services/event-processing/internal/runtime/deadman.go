// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
)

// expectedArmer is the slice of the engine the dead-man armer drives: it arms, disarms, and
// enumerates dead-man absence timers (ADR-051 slice 4c-2b-2a). *core.Engine satisfies it; the
// narrow interface lets the armer's cross-reference logic be unit-tested against a fake.
type expectedArmer interface {
	CarryRaised(successor map[string]string, skip func(core.SeriesKey) bool) int
	IsRaised(core.SeriesKey) bool
	ClearRaised(core.SeriesKey)
	SetExpected(key core.SeriesKey, since time.Time)
	RemoveExpected(key core.SeriesKey)
	ExpectedKeys() []core.SeriesKey
	HeartbeatAbsenceKeys() []core.SeriesKey
}

// RosterEntry is one rostered device's membership — the tenant/token identity plus its STABLE
// profile token and the lifecycle time its membership began (device create time, or re-type/
// re-profile time). It is the plain shape the armer consumes so it never depends on the
// persistence model; the processor maps a DeviceRoster projection row (or a live device-roster
// fact, re-read authoritatively) onto it (slice 4c-2b-2b).
type RosterEntry struct {
	Tenant        string
	DeviceToken   string
	ProfileToken  string
	ExpectedSince time.Time
}

// ActiveEntry is one profile token's active published version and its publish time — the grace
// base half of the dead-man deadline. The processor maps a ProfileActive projection row (or the
// authoritative re-read after a published-rule fact) onto it.
type ActiveEntry struct {
	Tenant             string
	ProfileToken       string
	ActiveVersionToken string // "{profileToken}@{version}" — the scope the rule set is filed under
	PublishedAt        time.Time
}

// profKey / devKey scope the armer's in-memory views by tenant (the engine is one shared
// singleton across tenants, so a bare token is not unique).
type profKey struct{ tenant, profileToken string }
type devKey struct{ tenant, deviceToken string }

type activeState struct {
	version     string // active profileVersionToken
	publishedAt time.Time
}

type rosterMember struct {
	profileToken  string
	expectedSince time.Time
}

// DeadmanArmer arms a never-seen device's absence timer (the "dead-man"), the thing a heartbeat-
// armed timer cannot do because a device that never reports produces no heartbeat (ADR-051 slice
// 4c-2b). It maintains three loop-owned in-memory views cross-referenced to resolve WHICH
// (absence rule, device) pairs to arm and at WHAT grace base:
//
//   - active:    profile token → its active published version + publish time (the grace base).
//   - roster:    device → its current profile token + membership-since (so a delete/re-type can
//     disarm the device's OLD scope, and arming knows its grace base).
//   - byProfile: profile token → its device set, so a publish arms every rostered device at once.
//
// It runs ONLY on the processor's single-writer loop (and the startup reconcile that precedes it),
// the same goroutine that mutates the engine, so it needs no lock. Every arm/disarm goes through
// the engine's forward-only, once-per-epoch SetExpected/RemoveExpected (slice 4c-2b-2a), so a
// redelivered fact re-arms idempotently and a fired dead-man never re-fires at the same epoch.
//
// INVARIANT it maintains: a device is dead-man-armed ONLY under its profile's CURRENT active
// version's absence rules. A version change disarms the superseded version across the fleet before
// arming the new one; a re-type disarms the old profile before arming the new. That keeps disarm
// targetable (the armer always knows the exact keys a device holds) and avoids a stale superseded
// version firing a spurious fleet-wide absence on every re-publish.
//
// WIRING CONTRACT (slice 4c-2b-2b-ii): a rule's core MUST be installed in the ENGINE
// (engine.UpsertRule) BEFORE the arming that references it (ApplyActiveVersion / armDevice /
// Reconcile). SetExpected hard-gates on the engine holding the rule as Absence and does NOT retry,
// so arming before the engine install would silently drop every dead-man for the version. The
// registry (AbsenceRulesFor) and the engine must therefore be advanced together, engine-first, on
// the same single-writer message.
//
// RESIDUAL: the active map has no profile-deletion hook, so a deleted profile's active entry is
// retained (inert — its byProfile set empties, so it arms nothing). Growth is bounded by profile
// count (ADR-023 state-budget class), not event volume; a profile-teardown GC hook is deferred.
type DeadmanArmer struct {
	reg       *RuleRegistry
	engine    expectedArmer
	active    map[profKey]activeState
	roster    map[devKey]rosterMember
	byProfile map[profKey]map[string]struct{}
	// Only handoffs actually deferred for pending output may be retried. Late
	// historical latches created after a handoff are never retry candidates.
	deferredThresholds map[core.SeriesKey]core.SeriesKey
	// Inverse of the current threshold pairing, including every retained old
	// version. Built on membership changes, not on the per-detection hot path.
	thresholdPredecessors map[profKey]map[string][]string
	// Replay publications fence the once-only post-replay carry. These are
	// transient; the carry bookkeeping changes no snapshot or rolling-upgrade wire.
	replayThresholdEdges map[core.SeriesKey]core.EdgeKind
}

// NewDeadmanArmer builds an empty armer over the rule registry and the engine.
func NewDeadmanArmer(reg *RuleRegistry, engine expectedArmer) *DeadmanArmer {
	return &DeadmanArmer{
		reg:                   reg,
		engine:                engine,
		active:                map[profKey]activeState{},
		roster:                map[devKey]rosterMember{},
		byProfile:             map[profKey]map[string]struct{}{},
		thresholdPredecessors: map[profKey]map[string][]string{},
	}
}

// graceBase resolves a dead-man's grace base = max(device.expectedSince, rule.publishedAt), the
// device's silence being "overdue" only once BOTH it has been expected to report AND the rule has
// been active. ok is false when publishedAt is ZERO — a pre-4c-2a fact predating the grace-base
// field, where the rule's activation time is unknown so NO correct deadline can be computed; the
// caller then does NOT arm.
//
// F1 handling — why skip, not clamp-to-now. Falling back to expectedSince alone would burst an old
// fleet (its deadline is already elapsed); clamping the unknown publish time to the wall clock
// avoids the burst but is NON-DETERMINISTIC across a restart: reconcile recomputes a fresh, later
// Now(), and the engine's strictly-later-base rule reads that as a NEW epoch — clearing the
// once-fired latch and RE-FIRING the dead-man after every restart (and pushing an unfired one out
// perpetually under frequent restarts). Skipping is deterministic and fail-safe: such a device is
// simply not dead-man-monitored until a real re-publish records a publishedAt (post-4c-2a always
// does), so this only affects genuinely legacy facts, which pre-GA cutover does not carry.
func (d *DeadmanArmer) graceBase(expectedSince, publishedAt time.Time) (since time.Time, ok bool) {
	if publishedAt.IsZero() {
		return time.Time{}, false
	}
	if expectedSince.After(publishedAt) {
		return expectedSince, true
	}
	return publishedAt, true
}

// ApplyDeviceMembership applies one authoritative device-membership change (slice 4c-2b-2b feeds
// it the post-merge roster row after a device-roster or entity-deleted fact, so this method never
// re-implements the projection's monotonic ordering — it just reflects the resolved truth).
// present=false means the device is gone (deleted/tombstoned) and must be disarmed. A present
// device whose profile token differs from what the armer last knew is a re-type: the OLD profile's
// dead-men are disarmed before the new profile's are armed.
func (d *DeadmanArmer) ApplyDeviceMembership(e RosterEntry, present bool) {
	dk := devKey{e.Tenant, e.DeviceToken}
	prev, known := d.roster[dk]
	// Departed, or moved to a different profile: tear down the old scope first.
	if known && (!present || prev.profileToken != e.ProfileToken) {
		d.disarmDevice(e.Tenant, e.DeviceToken, prev.profileToken)
		delete(d.roster, dk)
		d.removeFromProfile(profKey{e.Tenant, prev.profileToken}, e.DeviceToken)
	}
	if !present {
		return
	}
	d.roster[dk] = rosterMember{profileToken: e.ProfileToken, expectedSince: e.ExpectedSince}
	d.addToProfile(profKey{e.Tenant, e.ProfileToken}, e.DeviceToken)
	d.armDevice(e)
}

// ApplyActiveVersion applies one authoritative active-version change (slice 4c-2b-2b feeds it the
// post-merge ProfileActive row after a published-rule fact, AFTER the fact's rules are installed
// in the registry so AbsenceRulesFor can see them). It records the active version, disarms a
// superseded version's dead-men across the profile's fleet, then arms every rostered device of the
// profile under the new active version. It returns the number of moved latches.
func (d *DeadmanArmer) ApplyActiveVersion(a ActiveEntry, skip func(core.SeriesKey) bool) int {
	pk := profKey{a.Tenant, a.ProfileToken}
	prev, had := d.active[pk]
	d.active[pk] = activeState{version: a.ActiveVersionToken, publishedAt: a.PublishedAt}
	pairs := d.successors(a.Tenant, a.ProfileToken)
	d.indexThresholdPredecessors(pk, pairs)
	// Redelivered active facts must not start a fresh handoff for historical
	// threshold latches created after the actual version transition.
	moved := 0
	if !had || prev.version != a.ActiveVersionToken {
		moved = d.carryHandoff(pairs, skip, nil)
	}
	if had && prev.version != a.ActiveVersionToken {
		for dev := range d.byProfile[pk] {
			for _, sr := range d.reg.AbsenceRulesFor(a.Tenant, prev.version) {
				d.engine.RemoveExpected(core.SeriesKey{Rule: sr.Compiled.ID, Series: dev})
			}
		}
	}
	abs := d.reg.AbsenceRulesFor(a.Tenant, a.ActiveVersionToken)
	if len(abs) == 0 {
		return moved
	}
	for dev := range d.byProfile[pk] {
		since, ok := d.graceBase(d.roster[devKey{a.Tenant, dev}].expectedSince, a.PublishedAt)
		if !ok {
			continue
		}
		for _, sr := range abs {
			d.engine.SetExpected(core.SeriesKey{Rule: sr.Compiled.ID, Series: dev}, since)
		}
	}
	return moved
}

// loadMembership (re)populates the armer's three membership views (active / roster / byProfile) from
// the authoritative projections WITHOUT arming — the read-only half of a reconcile, shared by the
// pre-replay gate load (LoadMembership) and the post-replay Reconcile. It does not reset first;
// callers that need a clean rebuild (Reconcile) reset() beforehand.
func (d *DeadmanArmer) loadMembership(rosters []RosterEntry, actives []ActiveEntry) {
	for _, a := range actives {
		d.active[profKey{a.Tenant, a.ProfileToken}] = activeState{version: a.ActiveVersionToken, publishedAt: a.PublishedAt}
	}
	for pk := range d.active {
		d.indexThresholdPredecessors(pk, d.successors(pk.tenant, pk.profileToken))
	}
	for _, e := range rosters {
		d.roster[devKey{e.Tenant, e.DeviceToken}] = rosterMember{profileToken: e.ProfileToken, expectedSince: e.ExpectedSince}
		d.addToProfile(profKey{e.Tenant, e.ProfileToken}, e.DeviceToken)
	}
}

// LoadMembership populates the membership views for the publish-time absence gate (AbsenceLive)
// BEFORE replay, without arming any dead-man — arming needs the final post-replay engine and happens
// in Reconcile. It mirrors the dynamic-threshold view's build-before-replay (startAttributeView): the
// gate, like that view, must be live DURING replay so a replayed watermark advance that fires a stale
// heartbeat absence timer (a device deleted or superseded while the process was down) is dropped
// rather than published. Idempotent with the Reconcile that follows, which resets and reloads.
func (d *DeadmanArmer) LoadMembership(rosters []RosterEntry, actives []ActiveEntry) {
	d.loadMembership(rosters, actives)
}

// BindEngine points the armer at the engine it arms. It is called after replay (which may have
// REPLACED the engine on a snapshot-ahead-of-head reset) and before Reconcile, so arming always
// targets the final engine even though the armer was constructed pre-replay for the gate.
func (d *DeadmanArmer) BindEngine(e expectedArmer) { d.engine = e }

// AbsenceLive reports whether an ABSENCE detection's (rule, device) key is still a live membership:
// the device is rostered AND its profile's active version is the exact one that owns the rule. It is
// the publish-time belt (processor.dropSupersededDetections) and the reconcile heartbeat-sweep predicate that
// stop a stale wheel timer emitting a false absence after the device left the rule's scope — deleted
// (roster row gone), re-typed (roster profile token differs), or version superseded (active version
// moved past the rule's). It resolves entirely from the loop-owned roster + active views + the
// registry with NO engine read, so it is valid during replay (before the engine is final) as well as
// live. A rule the registry no longer holds, an unrostered device, or a profile with no known active
// version all read as NOT live — fail-safe: a false drop is a missed absence, strictly better than a
// published false one, and the intended direction against a stale/frozen projection.
func (d *DeadmanArmer) AbsenceLive(key core.SeriesKey) bool {
	sr, ok := d.reg.Lookup(key.Rule)
	if !ok {
		return false
	}
	m, ok := d.roster[devKey{sr.Tenant, key.Series}]
	if !ok {
		return false
	}
	a, ok := d.active[profKey{sr.Tenant, m.profileToken}]
	if !ok {
		return false
	}
	return a.version == sr.ProfileVersionToken
}

// successors pairs Threshold and Absence rules in retained non-active versions
// with the active version by stable identity AND kind. Edits to predicates, actions or severity do not create
// a rising edge. Unknown identities are left alone, toward the previous duplicate.
func (d *DeadmanArmer) successors(tenant, profile string) map[string]string {
	out := map[string]string{}
	a, ok := d.active[profKey{tenant, profile}]
	if !ok {
		return out
	}
	type identity struct {
		stable string
		kind   core.RuleKind
	}
	active := map[identity]string{}
	for _, nr := range d.reg.RulesFor(tenant, a.version) {
		// Repeating and Correlation need their windows/member maps as well as the
		// latch. Carrying their evaluation state is a follow-up. All other kinds
		// retain their existing publish behaviour; Absence keeps its own handoff.
		if nr.Compiled.Core.Kind != core.Threshold && nr.Compiled.Core.Kind != core.Absence {
			continue
		}
		stable, ok := StableRuleKey(nr.Compiled.ID)
		p, _, parsed := ProfileAndRuleToken(nr.Compiled.ID)
		if ok && parsed && p == profile {
			active[identity{stable, nr.Compiled.Core.Kind}] = nr.Compiled.ID
		}
	}
	for _, id := range d.reg.IDs() {
		old, _ := d.reg.Lookup(id)
		if old.Tenant != tenant || old.ProfileVersionToken == a.version {
			continue
		}
		if stable, ok := StableRuleKey(id); ok {
			if next, ok := active[identity{stable, old.Compiled.Core.Kind}]; ok {
				out[id] = next
			}
		}
	}
	return out
}

func (d *DeadmanArmer) indexThresholdPredecessors(pk profKey, pairs map[string]string) {
	previous := map[string][]string{}
	for old, next := range pairs {
		if sr, ok := d.reg.Lookup(old); ok && sr.Compiled.Core.Kind == core.Threshold {
			previous[next] = append(previous[next], old)
		}
	}
	d.thresholdPredecessors[pk] = previous
}

// CarriedForward reports a same-kind successor in the device's current profile.
// Missing membership, registry or active-version information never confirms a carry.
func (d *DeadmanArmer) CarriedForward(key core.SeriesKey) bool {
	_, ok := d.CarriedSuccessor(key)
	return ok
}

// CarriedSuccessor pairs only device-series kinds. An anchor is not a roster device.
// Pairing does not establish whether the successor is currently raised.
func (d *DeadmanArmer) CarriedSuccessor(key core.SeriesKey) (core.SeriesKey, bool) {
	sr, ok := d.reg.Lookup(key.Rule)
	if !ok || (sr.Compiled.Core.Kind != core.Threshold && sr.Compiled.Core.Kind != core.Absence) {
		return core.SeriesKey{}, false
	}
	m, ok := d.roster[devKey{sr.Tenant, key.Series}]
	if !ok {
		return core.SeriesKey{}, false
	}
	a, ok := d.active[profKey{sr.Tenant, m.profileToken}]
	if !ok || a.version == sr.ProfileVersionToken {
		return core.SeriesKey{}, false
	}
	stable, ok := StableRuleKey(key.Rule)
	p, _, parsed := ProfileAndRuleToken(key.Rule)
	if !ok || !parsed || p != m.profileToken {
		return core.SeriesKey{}, false
	}
	for _, nr := range d.reg.RulesFor(sr.Tenant, a.version) {
		if k, ok := StableRuleKey(nr.Compiled.ID); ok && k == stable && nr.Compiled.Core.Kind == sr.Compiled.Core.Kind {
			return core.SeriesKey{Rule: nr.Compiled.ID, Series: key.Series}, true
		}
	}
	return core.SeriesKey{}, false
}

// AbsenceCarriedForward preserves the existing absence publish gate: a stable
// absence rule in the device's current active version makes an old disarm a
// handoff, rather than a clear. Missing membership or a removed rule must clear.
// This differs from VersionSuperseded, which is also true for a re-type.
func (d *DeadmanArmer) AbsenceCarriedForward(key core.SeriesKey) bool {
	sr, ok := d.reg.Lookup(key.Rule)
	if !ok {
		return false
	}
	m, ok := d.roster[devKey{sr.Tenant, key.Series}]
	if !ok {
		return false
	}
	a, ok := d.active[profKey{sr.Tenant, m.profileToken}]
	if !ok || a.version == sr.ProfileVersionToken {
		return false
	}
	stable, ok := StableRuleKey(key.Rule)
	if !ok {
		return false
	}
	for _, nr := range d.reg.AbsenceRulesFor(sr.Tenant, a.version) {
		if k, ok := StableRuleKey(nr.Compiled.ID); ok && k == stable {
			return true
		}
	}
	return false
}

// VersionSuperseded reports whether a detection's (rule, device) belongs to a CONFIRMED-superseded
// profile version: the device is rostered, its profile HAS a known active version, and that active
// version is NOT the one owning the rule. It is the publish-time gate that stops a superseded version's
// FRONTIER-triggered firing (a Duration/Session timer or an Aggregate pane-close riding the shared
// watermark — starvation silences event-driven kinds but not these) from contributing a false edge to
// the alarm object under the stable contributor id (ADR-057 D6): a stale unsatisfied pane-close would
// otherwise resolve the ACTIVE version's genuine raise at the same epoch-aligned timestamp (a fail-open
// false clear). Unlike AbsenceLive (which drops on ANY uncertainty — correct for absence, a
// membership-gated kind), this fails the OTHER way: an unknown rule, an unrostered device, or a profile
// with no known active version returns FALSE (keep the detection), so a real alarm is never dropped
// merely because a projection is momentarily incomplete — it drops ONLY on a confirmed version mismatch.
func (d *DeadmanArmer) VersionSuperseded(key core.SeriesKey) bool {
	sr, ok := d.reg.Lookup(key.Rule)
	if !ok {
		return false
	}
	m, ok := d.roster[devKey{sr.Tenant, key.Series}]
	if !ok {
		return false
	}
	a, ok := d.active[profKey{sr.Tenant, m.profileToken}]
	if !ok {
		return false
	}
	return a.version != sr.ProfileVersionToken
}

// reset clears the three membership views so Reconcile is a full rebuild even when LoadMembership
// pre-populated them for the gate (a device deleted/superseded during replay must not linger as a
// stale roster entry the gate would read as live).
func (d *DeadmanArmer) reset() {
	d.active = map[profKey]activeState{}
	d.roster = map[devKey]rosterMember{}
	d.byProfile = map[profKey]map[string]struct{}{}
	d.thresholdPredecessors = map[profKey]map[string][]string{}
}

// RemoveTenant drops one tenant's membership views and transient carry records, and reports how many entries it
// removed, for the ADR-077 erasure.
//
// It does NOT disarm through the engine device by device, and that is worth stating
// because the armer's every other mutation does. The eviction that calls this removes the
// tenant's rules and ALL of their keyed state — dead-man armings included — in one sweep,
// so a per-device disarm would be re-deleting what is about to be deleted wholesale, and
// would do it through SetExpected/RemoveExpected, whose gates are written for a live
// tenant whose rules still exist.
//
// The views are swept even though they are re-derivable from the durable projections at
// startup, because the projections are swept in the same purge: a retained view would be
// a copy of a deleted tenant's device tokens and profile tokens sitting in memory, and
// the reused-token hazard the whole epoch is about is precisely a successor inheriting
// something keyed on a name it now shares.
func (d *DeadmanArmer) RemoveTenant(tenant string) int {
	if tenant == "" {
		return 0
	}
	for pk := range d.thresholdPredecessors {
		if pk.tenant == tenant {
			delete(d.thresholdPredecessors, pk)
		}
	}
	for old := range d.deferredThresholds {
		if t, ok := RuleTenant(old.Rule); ok && t == tenant {
			delete(d.deferredThresholds, old)
		}
	}
	for key := range d.replayThresholdEdges {
		if t, ok := RuleTenant(key.Rule); ok && t == tenant {
			delete(d.replayThresholdEdges, key)
		}
	}
	n := 0
	for k := range d.active {
		if k.tenant == tenant {
			delete(d.active, k)
			n++
		}
	}
	for k := range d.roster {
		if k.tenant == tenant {
			delete(d.roster, k)
			n++
		}
	}
	for k := range d.byProfile {
		if k.tenant == tenant {
			n += len(d.byProfile[k])
			delete(d.byProfile, k)
		}
	}
	return n
}

// Reconcile rebuilds the armer's views and the engine's dead-man arming from the durable roster +
// active-version projections at startup (slice 4c-2b-2b), diffing against the snapshot the engine
// restored: the SNAPSHOT is authoritative for FIRED state (its once-per-epoch latches survive), the
// PROJECTIONS are authoritative for MEMBERSHIP. It first resets the views (they may hold the gate's
// pre-replay LoadMembership state) then reloads. Every still-rostered device is re-armed under its
// active version (SetExpected no-ops an already-fired epoch, so nothing double-fires); then every
// dead-man entry the engine holds that is NOT in the rebuilt desired set is removed — a device
// deleted or a version superseded while the process was down, or an orphaned key for a rule the
// restored rule set no longer holds — AND every heartbeat-armed absence timer whose device left the
// rule's scope is swept (HeartbeatAbsenceKeys, which the ExpectedKeys sweep above cannot see). It runs
// once, on the startup goroutine, before the live loop.
func (d *DeadmanArmer) Reconcile(rosters []RosterEntry, actives []ActiveEntry, skip func(core.SeriesKey) bool) {
	d.reset()
	d.loadMembership(rosters, actives)
	// Carry only POST-replay: a pre-replay move would discard a pre-handoff
	// resolve already published by the original run, leaving a cleared alarm latched.
	// Keep the replay fence as well as successor-edge cleanup: an old recovery
	// followed by an old re-raise must still prevent carrying that replay episode.
	d.deferredThresholds = nil
	for pk := range d.active {
		pairs := d.successors(pk.tenant, pk.profileToken)
		d.carryHandoff(pairs, skip, func(old, next core.SeriesKey) bool {
			// Replayed successor edges or an old recovery settle this episode;
			// neither is a pending-output deferral that may be retried later.
			_, successorPublished := d.replayThresholdEdges[next]
			return successorPublished || d.replayThresholdEdges[old] == core.EdgeResolved
		})
	}
	d.replayThresholdEdges = nil
	desired := make(map[core.SeriesKey]struct{})
	for _, e := range rosters {
		a, ok := d.active[profKey{e.Tenant, e.ProfileToken}]
		if !ok {
			continue
		}
		since, ok := d.graceBase(e.ExpectedSince, a.publishedAt)
		if !ok {
			continue
		}
		for _, sr := range d.reg.AbsenceRulesFor(e.Tenant, a.version) {
			key := core.SeriesKey{Rule: sr.Compiled.ID, Series: e.DeviceToken}
			d.engine.SetExpected(key, since)
			desired[key] = struct{}{}
		}
	}
	for _, key := range d.engine.ExpectedKeys() {
		if _, want := desired[key]; !want {
			d.engine.RemoveExpected(key)
		}
	}
	// Sweep heartbeat-armed absence timers whose device left the rule's scope while the process was
	// down (deleted / re-typed / version superseded). The dead-man sweep above only sees e.expected
	// (SetExpected) keys; a timer armed by a device's own heartbeat (apply, Absence) lives in the wheel
	// only — invisible there — so a departed device's last heartbeat timer would otherwise fire one
	// false absence off the watermark post-restart. AbsenceLive resolves each against the freshly
	// reconciled membership (a still-live current device's heartbeat timer stays untouched).
	for _, key := range d.engine.HeartbeatAbsenceKeys() {
		if !d.AbsenceLive(key) {
			d.engine.RemoveExpected(key)
		}
	}
}

// carryHandoff records the exact threshold pairs left behind by this handoff's
// pending-output fence. CarryRaised calls the predicate on latched old keys;
// do not enumerate retained latches again after publication.
func (d *DeadmanArmer) carryHandoff(pairs map[string]string, skip func(core.SeriesKey) bool, invalid func(core.SeriesKey, core.SeriesKey) bool) int {
	return d.engine.CarryRaised(pairs, func(key core.SeriesKey) bool {
		if id, ok := pairs[key.Rule]; ok {
			next := core.SeriesKey{Rule: id, Series: key.Series}
			sr, found := d.reg.Lookup(key.Rule)
			threshold := found && sr.Compiled.Core.Kind == core.Threshold
			if threshold && invalid != nil && invalid(key, next) {
				return true
			}
			if skip != nil && (skip(key) || skip(next)) {
				if threshold {
					if d.deferredThresholds == nil {
						d.deferredThresholds = map[core.SeriesKey]core.SeriesKey{}
					}
					d.deferredThresholds[key] = next
				}
				return true
			}
		}
		return skip != nil && skip(key)
	})
}

// ThresholdEmitted settles predecessor latches as soon as a successor speaks,
// even during replay when no deferred-pair record survives. Clearing emits no
// Resolved: the successor's own edges now represent the alarm. This runs on the
// processor loop; ConcurrentSharded.ClearRaised queues the drop in the owning
// shard, just like the processor's publication drops, before the snapshot barrier.
func (d *DeadmanArmer) ThresholdEmitted(det core.Detection) {
	if det.Kind != core.Threshold {
		return
	}
	key := core.SeriesKey{Rule: det.RuleID, Series: det.Series}
	if sr, ok := d.reg.Lookup(key.Rule); ok {
		if profile, _, parsed := ProfileAndRuleToken(key.Rule); parsed {
			for _, old := range d.thresholdPredecessors[profKey{sr.Tenant, profile}][key.Rule] {
				// Threshold ClearRaised is a no-op when already unlatched. Avoid
				// an IsRaised read/barrier for each predecessor on concurrent engines.
				d.engine.ClearRaised(core.SeriesKey{Rule: old, Series: key.Series})
			}
		}
	}
	for old, next := range d.deferredThresholds {
		if key == next {
			d.engine.ClearRaised(old)
			delete(d.deferredThresholds, old)
		} else if key == old && det.Edge == core.EdgeResolved {
			// The old key's own resolve has already unlatched it.
			delete(d.deferredThresholds, old)
		}
	}
}

// ThresholdPublished also fences Reconcile against successor edges published
// during replay. An old Resolved stays invalidating even if it later re-raises.
func (d *DeadmanArmer) ThresholdPublished(det core.Detection, replay bool) {
	d.ThresholdEmitted(det)
	if !replay || det.Kind != core.Threshold {
		return
	}
	if d.replayThresholdEdges == nil {
		d.replayThresholdEdges = map[core.SeriesKey]core.EdgeKind{}
	}
	key := core.SeriesKey{Rule: det.RuleID, Series: det.Series}
	if d.replayThresholdEdges[key] != core.EdgeResolved {
		d.replayThresholdEdges[key] = det.Edge
	}
}

// CarryReadyThresholds retries only recorded handoffs after output settlement.
// An already latched successor, an old recovery, or a changed pairing settles
// the record without moving any latch. Absence retains its arm/disarm handoff.
func (d *DeadmanArmer) CarryReadyThresholds(skip func(core.SeriesKey) bool) int {
	pairs := map[string]string{}
	allowed := map[core.SeriesKey]bool{}
	for old, next := range d.deferredThresholds {
		current, paired := d.CarriedSuccessor(old)
		if !paired || current != next || !d.engine.IsRaised(old) || d.engine.IsRaised(next) {
			delete(d.deferredThresholds, old)
			continue
		}
		if skip != nil && (skip(old) || skip(next)) {
			continue
		}
		pairs[old.Rule] = next.Rule
		allowed[old], allowed[next] = true, true
		delete(d.deferredThresholds, old)
	}
	if len(pairs) == 0 {
		return 0
	}
	return d.engine.CarryRaised(pairs, func(key core.SeriesKey) bool { return !allowed[key] })
}

// armDevice arms one device under its profile's active version's absence rules, if the active
// version is known yet (a device-roster fact that races ahead of the profile's first published-
// rule fact simply defers — the later ApplyActiveVersion arms it once the version is known).
func (d *DeadmanArmer) armDevice(e RosterEntry) {
	a, ok := d.active[profKey{e.Tenant, e.ProfileToken}]
	if !ok {
		return
	}
	since, ok := d.graceBase(e.ExpectedSince, a.publishedAt)
	if !ok {
		return
	}
	for _, sr := range d.reg.AbsenceRulesFor(e.Tenant, a.version) {
		d.engine.SetExpected(core.SeriesKey{Rule: sr.Compiled.ID, Series: e.DeviceToken}, since)
	}
}

// disarmDevice removes a device's dead-men under a profile's CURRENT active version — the exact
// scope the maintained invariant guarantees it holds. When the profile's active version is not yet
// known it returns without calling RemoveExpected: that is correct rather than a skipped duty
// because active-unknown means no absence rule for the profile has ever been published (Reconcile
// seeds active from ProfileActive, whose row a publish always writes), so the device can hold
// neither a dead-man NOR a heartbeat-armed absence timer to cancel. It is NOT a general "no-op when
// unarmed" — the early return matters, and the invariant, not RemoveExpected's own tolerance, is
// what makes skipping safe.
func (d *DeadmanArmer) disarmDevice(tenant, device, profileToken string) {
	a, ok := d.active[profKey{tenant, profileToken}]
	if !ok {
		return
	}
	for _, sr := range d.reg.AbsenceRulesFor(tenant, a.version) {
		d.engine.RemoveExpected(core.SeriesKey{Rule: sr.Compiled.ID, Series: device})
	}
}

func (d *DeadmanArmer) addToProfile(pk profKey, device string) {
	set := d.byProfile[pk]
	if set == nil {
		set = map[string]struct{}{}
		d.byProfile[pk] = set
	}
	set[device] = struct{}{}
}

func (d *DeadmanArmer) removeFromProfile(pk profKey, device string) {
	set := d.byProfile[pk]
	delete(set, device)
	if len(set) == 0 {
		delete(d.byProfile, pk)
	}
}

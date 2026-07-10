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
	SetExpected(key core.SeriesKey, since time.Time)
	RemoveExpected(key core.SeriesKey)
	ExpectedKeys() []core.SeriesKey
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
}

// NewDeadmanArmer builds an empty armer over the rule registry and the engine.
func NewDeadmanArmer(reg *RuleRegistry, engine expectedArmer) *DeadmanArmer {
	return &DeadmanArmer{
		reg:       reg,
		engine:    engine,
		active:    map[profKey]activeState{},
		roster:    map[devKey]rosterMember{},
		byProfile: map[profKey]map[string]struct{}{},
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
// profile under the new active version.
func (d *DeadmanArmer) ApplyActiveVersion(a ActiveEntry) {
	pk := profKey{a.Tenant, a.ProfileToken}
	prev, had := d.active[pk]
	d.active[pk] = activeState{version: a.ActiveVersionToken, publishedAt: a.PublishedAt}
	if had && prev.version != a.ActiveVersionToken {
		for dev := range d.byProfile[pk] {
			for _, sr := range d.reg.AbsenceRulesFor(a.Tenant, prev.version) {
				d.engine.RemoveExpected(core.SeriesKey{Rule: sr.Compiled.ID, Series: dev})
			}
		}
	}
	abs := d.reg.AbsenceRulesFor(a.Tenant, a.ActiveVersionToken)
	if len(abs) == 0 {
		return
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
}

// Reconcile rebuilds the armer's views and the engine's dead-man arming from the durable roster +
// active-version projections at startup (slice 4c-2b-2b), diffing against the snapshot the engine
// restored: the SNAPSHOT is authoritative for FIRED state (its once-per-epoch latches survive), the
// PROJECTIONS are authoritative for MEMBERSHIP. Every still-rostered device is re-armed under its
// active version (SetExpected no-ops an already-fired epoch, so nothing double-fires); then every
// dead-man entry the engine holds that is NOT in the rebuilt desired set is removed — a device
// deleted or a version superseded while the process was down, or an orphaned key for a rule the
// restored rule set no longer holds. It runs once, on the startup goroutine, before the live loop.
func (d *DeadmanArmer) Reconcile(rosters []RosterEntry, actives []ActiveEntry) {
	for _, a := range actives {
		d.active[profKey{a.Tenant, a.ProfileToken}] = activeState{version: a.ActiveVersionToken, publishedAt: a.PublishedAt}
	}
	desired := make(map[core.SeriesKey]struct{})
	for _, e := range rosters {
		d.roster[devKey{e.Tenant, e.DeviceToken}] = rosterMember{profileToken: e.ProfileToken, expectedSince: e.ExpectedSince}
		d.addToProfile(profKey{e.Tenant, e.ProfileToken}, e.DeviceToken)
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

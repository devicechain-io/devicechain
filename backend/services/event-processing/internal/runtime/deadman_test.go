// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// fakeArmer records SetExpected/RemoveExpected calls so a test can assert the exact (rule, series)
// keys the DeadmanArmer arms and disarms and the grace base it resolves, without a real engine. It
// mimics the engine's forward-only, once-per-epoch SetExpected just enough for ExpectedKeys to
// return the live armed set the reconcile sweep diffs against.
type fakeArmer struct {
	armed     map[core.SeriesKey]time.Time // key → grace base currently armed (removed on RemoveExpected)
	heartbeat []core.SeriesKey             // heartbeat-armed absence timers NOT in `armed` (finding-6 sweep input)
	sets      []armCall
	rms       []core.SeriesKey
}

type armCall struct {
	key   core.SeriesKey
	since time.Time
}

func newFakeArmer() *fakeArmer { return &fakeArmer{armed: map[core.SeriesKey]time.Time{}} }

func (f *fakeArmer) SetExpected(key core.SeriesKey, since time.Time) {
	f.sets = append(f.sets, armCall{key, since})
	if cur, ok := f.armed[key]; ok && !since.After(cur) {
		return // forward-only, mirrors the engine
	}
	f.armed[key] = since
}

func (f *fakeArmer) RemoveExpected(key core.SeriesKey) {
	f.rms = append(f.rms, key)
	delete(f.armed, key)
}

func (f *fakeArmer) ExpectedKeys() []core.SeriesKey {
	out := make([]core.SeriesKey, 0, len(f.armed))
	for k := range f.armed {
		out = append(out, k)
	}
	return out
}

// HeartbeatAbsenceKeys returns the injected heartbeat-armed absence timers (those NOT modeled by the
// dead-man `armed` set) so the Reconcile heartbeat sweep (finding 6) can be exercised against a fake.
func (f *fakeArmer) HeartbeatAbsenceKeys() []core.SeriesKey { return f.heartbeat }

func (f *fakeArmer) armedKeys() []core.SeriesKey {
	out := f.ExpectedKeys()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Series < out[j].Series
	})
	return out
}

// absScoped builds an absence ScopedRule filed under (tenant, profileVersionToken) with a
// composed id, so a test registry files it exactly like the live path.
func absScoped(tenant, pvt, ruleKey string) ScopedRule {
	id := ComposeRuleID(tenant, pvt+"/"+ruleKey)
	return ScopedRule{
		Tenant:              tenant,
		ProfileVersionToken: pvt,
		Compiled: &rules.CompiledRule{
			ID:   id,
			Type: rules.TypeAbsence,
			Core: core.Rule{ID: id, Kind: core.Absence, Timeout: 10 * time.Second},
		},
	}
}

// thrScoped builds a non-absence (threshold) ScopedRule so AbsenceRulesFor's filtering is tested.
func thrScoped(tenant, pvt, ruleKey string) ScopedRule {
	id := ComposeRuleID(tenant, pvt+"/"+ruleKey)
	return ScopedRule{
		Tenant:              tenant,
		ProfileVersionToken: pvt,
		Compiled: &rules.CompiledRule{
			ID:   id,
			Type: rules.TypeThreshold,
			Core: core.Rule{ID: id, Kind: core.Threshold},
		},
	}
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ts(sec int) time.Time { return epoch.Add(time.Duration(sec) * time.Second) }

// TestArmerArmsWhenRuleThenDeviceArrive: a device armed against its profile's active absence rule,
// at the grace base max(expectedSince, publishedAt). Rule fact first, then device.
func TestArmerArmsWhenRuleThenDeviceArrive(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1"), thrScoped("t1", "p1@v1", "r2")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)

	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)

	want := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "dev"}
	if got := fe.armedKeys(); len(got) != 1 || got[0] != want {
		t.Fatalf("armed set = %v, want only the absence rule %v", got, want)
	}
	// Grace base = max(expectedSince=50, publishedAt=100) = 100.
	if base := fe.armed[want]; !base.Equal(ts(100)) {
		t.Fatalf("grace base = %v, want max(50,100)=%v", base, ts(100))
	}
}

// TestArmerArmsWhenDeviceThenRuleArrive: reversed arrival — device first (deferred, active version
// unknown), then the rule fact arms the whole fleet. Proves the eventual-consistency intersection.
func TestArmerArmsWhenDeviceThenRuleArrive(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)

	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	if got := fe.armedKeys(); len(got) != 0 {
		t.Fatalf("device with unknown active version should defer arming, got %v", got)
	}
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(200)})
	want := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "dev"}
	if got := fe.armedKeys(); len(got) != 1 || got[0] != want || !fe.armed[want].Equal(ts(200)) {
		t.Fatalf("rule fact did not arm the deferred device at base 200: %v (%v)", got, fe.armed)
	}
}

// TestArmerDeleteDisarms: a deleted device is disarmed and dropped from the fleet index.
func TestArmerDeleteDisarms(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)

	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev"}, false)
	if got := fe.armedKeys(); len(got) != 0 {
		t.Fatalf("deleted device still armed: %v", got)
	}
	// A subsequent publish must not resurrect the deleted device (dropped from byProfile).
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(300)})
	if got := fe.armedKeys(); len(got) != 0 {
		t.Fatalf("publish re-armed a deleted device: %v", got)
	}
}

// TestArmerRetypeMovesProfile: a device re-typed onto a new profile is disarmed under the old
// profile's absence rule and armed under the new one.
func TestArmerRetypeMovesProfile(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1"), absScoped("t1", "p2@v1", "rX")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p2", ActiveVersionToken: "p2@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)

	// Re-type to p2 (membership began now); old p1 arming removed, new p2 arming added.
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p2", ExpectedSince: ts(500)}, true)
	want := core.SeriesKey{Rule: ComposeRuleID("t1", "p2@v1/rX"), Series: "dev"}
	if got := fe.armedKeys(); len(got) != 1 || got[0] != want || !fe.armed[want].Equal(ts(500)) {
		t.Fatalf("re-type did not move the device to p2 at base 500: %v (%v)", got, fe.armed)
	}
}

// TestArmerVersionTransitionDisarmsSuperseded: publishing a new active version disarms the
// superseded version's dead-men across the fleet and arms the new one — no stale-version arming
// lingers (the maintained invariant).
func TestArmerVersionTransitionDisarmsSuperseded(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1"), absScoped("t1", "p1@v2", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: ts(10)}, true)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "devB", ProfileToken: "p1", ExpectedSince: ts(10)}, true)

	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: ts(400)})
	got := fe.armedKeys()
	wantA := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v2/r1"), Series: "devA"}
	wantB := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v2/r1"), Series: "devB"}
	if len(got) != 2 || got[0] != wantA || got[1] != wantB {
		t.Fatalf("version transition left stale/missing arming: %v", got)
	}
	if !fe.armed[wantA].Equal(ts(400)) {
		t.Fatalf("new-version grace base = %v, want the fresh publishedAt 400", fe.armed[wantA])
	}
}

// TestArmerZeroPublishedAtSkips: a zero publishedAt (a pre-4c-2a fact with no known activation
// time) is NOT armed — neither bursting the fleet (falling back to the ancient expectedSince) nor
// clamping to a non-deterministic wall clock (which would re-fire on every restart). Skipping is
// the deterministic, fail-safe choice; a real re-publish (which always carries a publishedAt) arms
// the device later. Verified through BOTH arming paths (device-driven and fleet-driven).
func TestArmerZeroPublishedAtSkips(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1"}) // zero PublishedAt
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(10)}, true)
	if len(fe.sets) != 0 {
		t.Fatalf("zero publishedAt should skip arming, got %v", fe.sets)
	}
	// A subsequent real publish (non-zero) then arms the device.
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(500)})
	key := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "dev"}
	if got := fe.armedKeys(); len(got) != 1 || got[0] != key || !fe.armed[key].Equal(ts(500)) {
		t.Fatalf("real publish did not arm the previously-skipped device at base 500: %v (%v)", got, fe.armed)
	}
}

// TestArmerVersionTransitionToRulelessVersionDisarms: a publish whose new active version has NO
// absence rule still disarms the superseded version's dead-men across the fleet (the disarm runs
// before the empty-abs early return) — a profile that drops its absence rule stops firing.
func TestArmerVersionTransitionToRulelessVersionDisarms(t *testing.T) {
	// v2 exists in the registry but carries no absence rule (only a threshold), so AbsenceRulesFor
	// for p1@v2 is empty.
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1"), thrScoped("t1", "p1@v2", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(10)}, true)
	if len(fe.armedKeys()) != 1 {
		t.Fatalf("setup: device should be armed under v1")
	}
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: ts(400)})
	if got := fe.armedKeys(); len(got) != 0 {
		t.Fatalf("transition to a ruleless version left the v1 dead-man armed: %v", got)
	}
}

// TestArmerFleetArmUsesPerDeviceExpectedSince: on a fleet arm (publish), the grace base is
// max(expectedSince, publishedAt) PER DEVICE — a device whose expectedSince exceeds publishedAt
// gets its own later base, not the publish time. Covers the expectedSince-dominant side of the
// fleet path (the device-driven path's max side is covered by the re-type test).
func TestArmerFleetArmUsesPerDeviceExpectedSince(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "young", ProfileToken: "p1", ExpectedSince: ts(900)}, true)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "old", ProfileToken: "p1", ExpectedSince: ts(10)}, true)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	young := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "young"}
	old := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "old"}
	if !fe.armed[young].Equal(ts(900)) { // expectedSince 900 > publishedAt 100
		t.Fatalf("young device grace base = %v, want max(900,100)=900", fe.armed[young])
	}
	if !fe.armed[old].Equal(ts(100)) { // publishedAt 100 > expectedSince 10
		t.Fatalf("old device grace base = %v, want max(10,100)=100", fe.armed[old])
	}
}

// TestArmerRetypeToUnknownProfileThenPublish: a device re-typed onto a profile with no active
// version yet defers, and the profile's later publish arms it (defer-then-fleet-arm across a
// re-type). Guards the byProfile membership that the deferred fleet arm depends on.
func TestArmerRetypeToUnknownProfileThenPublish(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1"), absScoped("t1", "p2@v1", "rX")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	// Re-type to p2 whose active version is not yet known: old p1 arming removed, new deferred.
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p2", ExpectedSince: ts(500)}, true)
	if got := fe.armedKeys(); len(got) != 0 {
		t.Fatalf("re-type to an unknown-active profile should defer, got %v", got)
	}
	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p2", ActiveVersionToken: "p2@v1", PublishedAt: ts(600)})
	want := core.SeriesKey{Rule: ComposeRuleID("t1", "p2@v1/rX"), Series: "dev"}
	if got := fe.armedKeys(); len(got) != 1 || got[0] != want || !fe.armed[want].Equal(ts(600)) {
		t.Fatalf("p2 publish did not arm the re-typed deferred device at base 600: %v (%v)", got, fe.armed)
	}
}

// TestArmerAgainstRealEngine drives the armer against a REAL core.Engine (not the fake) to prove
// an arm actually reaches the engine and fires — closing the gap that the fake accepts every
// SetExpected unconditionally. It exercises the WIRING CONTRACT: the rule core is installed in the
// engine BEFORE the arm. A never-seen device's dead-man then fires on a watermark advance.
func TestArmerAgainstRealEngine(t *testing.T) {
	id := ComposeRuleID("t1", "p1@v1/r1")
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	eng := core.NewEngine(reg.Cores(), 0) // engine holds the absence rule (contract: engine-first)
	d := NewDeadmanArmer(reg, eng)

	d.ApplyActiveVersion(ActiveEntry{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	// Grace base = max(50,100)=100, Timeout 10 → deadline 110. Advance past it.
	eng.Advance(ts(111))
	got := eng.Drain()
	if len(got) != 1 || got[0].RuleID != id || got[0].Series != "dev" || !got[0].At.Equal(ts(110)) {
		t.Fatalf("real engine did not fire the armed dead-man at 110: %+v", got)
	}
	// A deleted device disarms in the real engine: no second fire.
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev2", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev2"}, false)
	eng.Advance(ts(200))
	if got := eng.Drain(); len(got) != 0 {
		t.Fatalf("deleted device fired in the real engine: %v", got)
	}
}

// TestArmerReconcileRebuildsAndSweeps: startup reconcile arms every rostered device from the
// projections AND sweeps engine dead-man keys whose membership is gone — a device deleted while
// down (in the engine snapshot but not the roster) and an orphaned unknown-rule key.
func TestArmerReconcileRebuildsAndSweeps(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	// Pre-seed the engine's armed set with two stale keys the snapshot restored: a device that was
	// deleted while down, and a key for a rule the rule set no longer holds (orphan).
	staleDeleted := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "goneDev"}
	orphanRule := core.SeriesKey{Rule: ComposeRuleID("t1", "pX@v9/r9"), Series: "dev"}
	fe.armed[staleDeleted] = ts(0)
	fe.armed[orphanRule] = ts(0)

	d := NewDeadmanArmer(reg, fe)
	d.Reconcile(
		[]RosterEntry{{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}},
		[]ActiveEntry{{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)}},
	)
	live := core.SeriesKey{Rule: ComposeRuleID("t1", "p1@v1/r1"), Series: "dev"}
	if got := fe.armedKeys(); len(got) != 1 || got[0] != live {
		t.Fatalf("reconcile did not converge to exactly the live device: %v", got)
	}
	// Both stale keys were swept.
	swept := map[core.SeriesKey]bool{}
	for _, k := range fe.rms {
		swept[k] = true
	}
	if !swept[staleDeleted] || !swept[orphanRule] {
		t.Fatalf("reconcile did not sweep stale keys: removed=%v", fe.rms)
	}
}

// TestArmerUnknownActiveDefers: a device whose profile has no active version yet arms nothing and
// records no phantom key — it waits for the profile's first publish.
func TestArmerUnknownActiveDefers(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	if len(fe.sets) != 0 {
		t.Fatalf("armed against an unknown active version: %v", fe.sets)
	}
}

// TestArmerAbsenceLive covers the publish-time / reconcile membership gate: an absence detection's
// (rule, device) is live only when the device is rostered AND its profile's active version owns the
// rule. Everything else — unrostered, re-typed, superseded version, unknown rule — reads NOT live.
func TestArmerAbsenceLive(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1"), absScoped("t1", "p1@v2", "r1"), absScoped("t1", "p2@v1", "rX")})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.LoadMembership(
		[]RosterEntry{{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: ts(50)}},
		[]ActiveEntry{{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)}},
	)
	v1 := ComposeRuleID("t1", "p1@v1/r1")
	v2 := ComposeRuleID("t1", "p1@v2/r1")
	other := ComposeRuleID("t1", "p2@v1/rX")
	live := func(rule, dev string) bool { return d.AbsenceLive(core.SeriesKey{Rule: rule, Series: dev}) }

	if !live(v1, "dev") {
		t.Fatal("rostered device under the active version should be live")
	}
	if live(v2, "dev") {
		t.Fatal("superseded version should not be live")
	}
	if live(v1, "ghost") {
		t.Fatal("unrostered device should not be live")
	}
	if live(other, "dev") {
		t.Fatal("rule for a profile the device is not on should not be live")
	}
	if live("t1/nope@v1/r1", "dev") {
		t.Fatal("unknown rule should not be live")
	}

	// Re-type the device onto p2 (different active version): the p1 rule is no longer live, the p2 one is.
	d.LoadMembership(
		[]RosterEntry{{Tenant: "t1", DeviceToken: "dev", ProfileToken: "p2", ExpectedSince: ts(50)}},
		[]ActiveEntry{{Tenant: "t1", ProfileToken: "p2", ActiveVersionToken: "p2@v1", PublishedAt: ts(100)}},
	)
	if live(v1, "dev") {
		t.Fatal("after re-type, the old profile's rule should not be live")
	}
	if !live(other, "dev") {
		t.Fatal("after re-type, the new profile's rule should be live")
	}
}

// TestReconcileSweepsHeartbeatAbsenceTimers: Reconcile cancels a heartbeat-armed absence timer whose
// device left the rule's scope (finding 6 — invisible to the ExpectedKeys/dead-man sweep), while
// leaving a still-live device's heartbeat timer untouched.
func TestReconcileSweepsHeartbeatAbsenceTimers(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{absScoped("t1", "p1@v1", "r1")})
	fe := newFakeArmer()
	id := ComposeRuleID("t1", "p1@v1/r1")
	// A live device (rostered, active version matches) and a departed device (not rostered), each
	// holding a heartbeat-armed absence timer the ExpectedKeys/dead-man sweep cannot see.
	fe.heartbeat = []core.SeriesKey{{Rule: id, Series: "live"}, {Rule: id, Series: "gone"}}
	d := NewDeadmanArmer(reg, fe)
	d.Reconcile(
		[]RosterEntry{{Tenant: "t1", DeviceToken: "live", ProfileToken: "p1", ExpectedSince: ts(50)}},
		[]ActiveEntry{{Tenant: "t1", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)}},
	)
	var goneSwept, liveSwept bool
	for _, k := range fe.rms {
		switch k.Series {
		case "gone":
			goneSwept = true
		case "live":
			liveSwept = true
		}
	}
	if !goneSwept {
		t.Fatal("departed device's heartbeat absence timer was not swept")
	}
	if liveSwept {
		t.Fatal("live device's heartbeat absence timer was wrongly swept")
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
)

// Tenant eviction of the three loop-owned runtime views (ADR-077 erasure).
//
// The engine's keyed state is swept by core.RemoveMatching; these three views are everything
// ELSE the process holds that names a tenant — the rule set itself (ids, profile tokens, rule
// tokens), the dead-man armer's membership (device tokens, profile tokens), and the attribute
// view's per-device thresholds. All three are re-derivable from durable projections at startup,
// which is exactly why they are swept HERE: the relational purge empties those projections in
// the same pass, so a view left standing is a copy of a deleted tenant's identifiers with
// nothing left to reconcile it against — and the reused-token hazard the epoch exists for is a
// successor inheriting something keyed on a name it now shares.
//
// Two properties recur through the file and are worth stating once:
//
//   - The BYSTANDER is asserted in every case. An eviction that also took a neighbouring
//     tenant's rules or membership does not fail loudly — that tenant simply stops alarming,
//     with nothing in any log connecting it to the purge of a differently-named tenant.
//   - A second pass must report ZERO. The purge coordinator treats a non-zero count as "work
//     was still being found" and restarts its settle window, so a method that always reported
//     a number would stop a purge ever completing.

// idSet reduces RemoveTenant's returned ids to a set. byID is a map and RemoveTenant walks it,
// so the order is deliberately unspecified; an assertion names the ids it wants instead.
func idSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// TestRegistryRemoveTenantMatchesEitherAxis pins the UNION match, in both directions.
//
// A rule carries the tenant twice: in its Tenant field and in the prefix of its compiled id.
// They agree on everything the publish path mints, but the registry deliberately ADMITS a rule
// where they diverge — a mis-minted id is contained at the PUBLISH boundary (its detections are
// refused before any tenant subject is written), not refused at admission, so such a rule is
// live in byID and byScope. Matching on one axis only would therefore leave the tenant's
// identifier behind: either a rule filed under the purged tenant, or engine state keyed on a
// rule id that names it. An erasure that leaves the tenant's name in memory has not erased the
// tenant, so both directions must be swept.
func TestRegistryRemoveTenantMatchesEitherAxis(t *testing.T) {
	// Named for which axis carries the victim's token.
	fieldOnly := scopedThreshold("acme", "p@1", "globex/p@1/mis-minted")
	idOnly := scopedThreshold("globex", "p@1", "acme/p@1/mis-filed")
	neither := scopedThreshold("globex", "p@1", "globex/p@1/ok")

	reg := NewRuleRegistry([]ScopedRule{fieldOnly, idOnly, neither})
	if reg.Count() != 3 {
		t.Fatalf("test bug: registry admitted %d of 3 rules, so the eviction below asserts less "+
			"than it reads", reg.Count())
	}

	got := idSet(reg.RemoveTenant("acme"))

	if !got["globex/p@1/mis-minted"] {
		t.Fatalf("a rule OWNED by the purged tenant survived because its id names another — "+
			"the Tenant-field axis is not being matched: removed=%v", got)
	}
	if !got["acme/p@1/mis-filed"] {
		t.Fatalf("a rule whose ID names the purged tenant survived because it is filed under "+
			"another — the id-prefix axis is not being matched, leaving the tenant's name on a "+
			"live rule id: removed=%v", got)
	}
	if got["globex/p@1/ok"] {
		t.Fatalf("the eviction took a rule that names the purged tenant on neither axis: removed=%v", got)
	}
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1 (only the unrelated rule)", reg.Count())
	}
	if _, ok := reg.Lookup("globex/p@1/ok"); !ok {
		t.Fatal("the surviving rule is no longer resolvable by id")
	}
}

// TestRegistryRemoveTenantIsAnchoredOnTheWholeTenantToken is the highest-value test here: it
// pins the match against the two ways a tenant test goes wrong, an unanchored prefix and a
// substring.
//
// 🔴 Neither failure announces itself. Purging "acme" and taking "acme-2" with it, or taking
// "other/acme@1/hot" with it, leaves a LIVE tenant with no rules — it just stops alarming, and
// nothing connects that to a purge of a differently-named tenant. The id axis must therefore
// resolve the tenant through RuleTenant (a cut on the separator, which the ADR-042 token
// grammar excludes from every token), never a strings.HasPrefix on the bare token and never a
// strings.Contains.
func TestRegistryRemoveTenantIsAnchoredOnTheWholeTenantToken(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		scopedThreshold("acme", "p@1", "acme/p@1/hot"),
		// Shares the victim's token as a PREFIX but is a different tenant.
		scopedThreshold("acme-2", "p@1", "acme-2/p@1/hot"),
		// CONTAINS the victim's token, in the PROFILE position, but is owned by another.
		scopedThreshold("other", "acme@1", "other/acme@1/hot"),
	})

	ids := reg.RemoveTenant("acme")
	if len(ids) != 1 || ids[0] != "acme/p@1/hot" {
		t.Fatalf("evicting %q removed %v — want exactly the victim's own rule; a longer list "+
			"means the match is an unanchored prefix or a substring, and it has just silenced a "+
			"live tenant", "acme", ids)
	}
	// Asserted through the scope index, not merely Lookup: a survivor still present in byID but
	// detached from byScope is a rule that no longer fires, which is the same outage.
	if got := reg.RulesFor("acme-2", "p@1"); len(got) != 1 || got[0].Compiled.ID != "acme-2/p@1/hot" {
		t.Fatalf("the tenant whose token merely STARTS with the victim's lost its rules: %+v", got)
	}
	if got := reg.RulesFor("other", "acme@1"); len(got) != 1 || got[0].Compiled.ID != "other/acme@1/hot" {
		t.Fatalf("the tenant whose PROFILE token happens to be the victim's name lost its rules: %+v", got)
	}
	if reg.Count() != 2 {
		t.Fatalf("count = %d, want 2 survivors", reg.Count())
	}
}

// TestRegistryRemoveTenantCleansTheScopeIndex proves the eviction goes through the registry's
// own removal rather than deleting from byID alone.
//
// byID and byScope hold the SAME *ScopedRule, and byScope is what the hot path reads: RulesFor
// selects the rules a resolved event feeds. A sweep of byID only would leave those pointers in
// their scope buckets, so the purged tenant's rules would keep matching events and keep firing
// detections — invisible to Lookup, Count and IDs, all of which read byID. The victim's rules
// are spread over two scopes, and one of them is filed under ANOTHER tenant's scope (the
// mis-minted id case), so the cleanup is exercised on a bucket the victim's own scope key does
// not name.
func TestRegistryRemoveTenantCleansTheScopeIndex(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		scopedThreshold("acme", "p@1", "acme/p@1/r1"),
		scopedThreshold("acme", "p@1", "acme/p@1/r2"),
		scopedThreshold("acme", "p@2", "acme/p@2/r1"),
		// Matched on the id axis, but filed in globex's scope bucket alongside a real globex rule.
		scopedThreshold("globex", "p@1", "acme/p@1/stray"),
		scopedThreshold("globex", "p@1", "globex/p@1/keep"),
	})
	// Non-vacuity: the buckets must actually be populated, or "nothing selects afterwards" is a
	// statement about a registry that never held the rules.
	if len(reg.RulesFor("acme", "p@1")) != 2 || len(reg.RulesFor("acme", "p@2")) != 1 ||
		len(reg.RulesFor("globex", "p@1")) != 2 {
		t.Fatal("test bug: the scope index was not populated as the test assumes")
	}

	reg.RemoveTenant("acme")

	if got := reg.RulesFor("acme", "p@1"); len(got) != 0 {
		t.Fatalf("RulesFor(acme,p@1) still selects %d rule(s) after the purge — the scope index "+
			"was not cleaned, so the evicted tenant's rules keep firing on every event", len(got))
	}
	if got := reg.RulesFor("acme", "p@2"); len(got) != 0 {
		t.Fatalf("RulesFor(acme,p@2) still selects %d rule(s) after the purge", len(got))
	}
	got := reg.RulesFor("globex", "p@1")
	if len(got) != 1 || got[0].Compiled.ID != "globex/p@1/keep" {
		t.Fatalf("the surviving tenant's scope bucket is wrong after the purge: %+v — either the "+
			"mis-minted rule was left dangling in it, or the sweep took the neighbour with it", got)
	}
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1", reg.Count())
	}
	if _, ok := reg.Lookup("acme/p@1/r1"); ok {
		t.Fatal("Lookup still resolves an evicted rule")
	}
}

// TestRegistryRemoveTenantRefusesTheEmptyTenant: there is no purge of the empty tenant, and an
// unguarded sweep for it is not a no-op — it is an instance-wide one.
//
// The fixture makes the guard load-bearing rather than decorative: an UNPREFIXED rule id is a
// shape the registry admits (the backstop refuses its detections at publish, not its admission),
// and RuleTenant cannot parse a tenant out of it, so it reports "". An empty-tenant sweep would
// match that rule on the id axis and evict a live tenant's rule while claiming to have purged
// nobody.
func TestRegistryRemoveTenantRefusesTheEmptyTenant(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		scopedThreshold("acme", "p@1", "acme/p@1/r1"),
		scopedThreshold("acme", "p@1", "unprefixed"),
	})

	if ids := reg.RemoveTenant(""); ids != nil {
		t.Fatalf("RemoveTenant(\"\") returned %v; want nil — no purge names the empty tenant", ids)
	}
	if reg.Count() != 2 {
		t.Fatalf("count = %d, want 2: an empty-tenant sweep removed a live tenant's rules", reg.Count())
	}
	if got := reg.RulesFor("acme", "p@1"); len(got) != 2 {
		t.Fatalf("RulesFor after an empty-tenant sweep = %d, want 2", len(got))
	}
}

// evictionArmer builds a DeadmanArmer holding live membership for two tenants, populated through
// the armer's REAL public path (ApplyActiveVersion + ApplyDeviceMembership) rather than by
// writing active/roster/byProfile directly — the maps are an implementation detail of those
// methods, and a fixture that writes them itself measures the fixture's idea of the shape rather
// than the one the processor actually produces.
//
// Both tenants use the device token "devA", so a sweep keyed on the token rather than the tenant
// half of devKey shows up as the bystander losing its device.
func evictionArmer(t *testing.T) (*DeadmanArmer, *fakeArmer) {
	t.Helper()
	reg := NewRuleRegistry([]ScopedRule{
		absScoped("acme", "p1@v1", "r1"),
		absScoped("acme", "p1@v2", "r1"), // the version a SUCCESSOR of the token would publish
		absScoped("globex", "p1@v1", "r1"),
	})
	fe := newFakeArmer()
	d := NewDeadmanArmer(reg, fe)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyActiveVersion(ActiveEntry{Tenant: "globex", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "devB", ProfileToken: "p1", ExpectedSince: ts(50)}, true)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "globex", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: ts(50)}, true)

	// Non-vacuity: every assertion below is "this is no longer true", which asserts nothing
	// unless it was true to begin with.
	if !d.AbsenceLive(core.SeriesKey{Rule: ComposeRuleID("acme", "p1@v1/r1"), Series: "devA"}) {
		t.Fatal("test bug: the victim's membership is not live before the eviction")
	}
	if !d.AbsenceLive(core.SeriesKey{Rule: ComposeRuleID("globex", "p1@v1/r1"), Series: "devA"}) {
		t.Fatal("test bug: the bystander's membership is not live before the eviction")
	}
	if len(fe.armedKeys()) != 3 {
		t.Fatalf("test bug: expected three armed dead-men before the eviction, got %v", fe.armedKeys())
	}
	return d, fe
}

// TestArmerRemoveTenantSweepsTheRoster probes the roster through the exact scenario the whole
// epoch exists for: the token is RE-USED, and a successor tenant publishes under it.
//
// A direct "is the device still live" assertion cannot isolate this view — AbsenceLive needs
// the roster AND the active version, so with active swept it reads false whether or not the
// roster entry survived, and the roster's own failure hides behind its healthy neighbour. Giving
// the successor a published version restores the active half legitimately, and then a surviving
// roster row is exactly the inheritance the purge is meant to prevent: the predecessor's device
// token read back as a live membership of the tenant that now owns the name.
func TestArmerRemoveTenantSweepsTheRoster(t *testing.T) {
	d, _ := evictionArmer(t)
	d.RemoveTenant("acme")

	// The token is re-registered and its new owner publishes a profile version.
	d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: ts(1000)})

	for _, dev := range []string{"devA", "devB"} {
		if d.AbsenceLive(core.SeriesKey{Rule: ComposeRuleID("acme", "p1@v2/r1"), Series: dev}) {
			t.Fatalf("the purged tenant's device %q is a live membership of the SUCCESSOR that "+
				"reused the token — the roster view was not swept, so the new tenant inherited "+
				"the old one's fleet", dev)
		}
	}
}

// TestArmerRemoveTenantSweepsTheActiveVersions probes the active map through the arming path
// that reads it: a device membership arms only if its profile's active version is known.
//
// After the purge that version is not known, so re-applying a membership for the purged tenant
// must arm nothing. A surviving active entry would arm the device — publishing dead-man timers
// for a tenant whose erasure has already been reported, off a profile publish time that no
// longer exists anywhere durable.
func TestArmerRemoveTenantSweepsTheActiveVersions(t *testing.T) {
	d, fe := evictionArmer(t)
	d.RemoveTenant("acme")
	setsBefore := len(fe.sets)

	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: ts(50)}, true)

	if len(fe.sets) != setsBefore {
		t.Fatalf("a membership for the purged tenant armed %d dead-man timer(s) after the purge: "+
			"%v — the active-version view survived, so the profile's publish time is still in "+
			"memory and still arming", len(fe.sets)-setsBefore, fe.sets[setsBefore:])
	}
}

// TestArmerRemoveTenantSweepsTheProfileFleetIndex probes byProfile through the fleet arm: a
// publish arms every device the index holds for the profile.
//
// This is the view a "sweep the two obvious maps" mistake misses, and it is the one holding the
// device-token SET. After the purge a publish for the same profile token must arm nobody; a
// surviving index would re-arm the purged tenant's whole fleet from a single publish.
func TestArmerRemoveTenantSweepsTheProfileFleetIndex(t *testing.T) {
	d, fe := evictionArmer(t)
	d.RemoveTenant("acme")
	setsBefore := len(fe.sets)

	d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(1000)})

	if len(fe.sets) != setsBefore {
		t.Fatalf("a publish re-armed %d device(s) of the purged tenant: %v — the profile→fleet "+
			"index still holds its device tokens", len(fe.sets)-setsBefore, fe.sets[setsBefore:])
	}
}

// TestArmerRemoveTenantSparesTheOtherTenant asserts the bystander from all three sides, since a
// view swept too widely is silent: the neighbour's devices simply stop being monitored.
//
// It also pins the documented decision NOT to disarm through the engine device by device: the
// eviction that calls this removes the tenant's rules and all of their keyed state in one sweep,
// so a per-device RemoveExpected would be re-deleting what is about to be deleted wholesale,
// through gates written for a tenant whose rules still exist.
func TestArmerRemoveTenantSparesTheOtherTenant(t *testing.T) {
	d, fe := evictionArmer(t)
	bystander := core.SeriesKey{Rule: ComposeRuleID("globex", "p1@v1/r1"), Series: "devA"}

	d.RemoveTenant("acme")

	if len(fe.rms) != 0 {
		t.Fatalf("RemoveTenant disarmed %v through the engine; it is documented not to, and the "+
			"engine's own eviction is what drops this state", fe.rms)
	}
	if !fe.armed[bystander].Equal(ts(100)) {
		t.Fatalf("the surviving tenant's dead-man is no longer armed at its grace base: %v", fe.armed)
	}
	// roster + active both intact: the gate resolves the bystander's membership as live.
	if !d.AbsenceLive(bystander) {
		t.Fatal("the surviving tenant's membership is no longer live — the sweep took its roster " +
			"or its active version, so its devices have silently stopped being monitored")
	}
	// byProfile intact: a publish still reaches its fleet.
	setsBefore := len(fe.sets)
	d.ApplyActiveVersion(ActiveEntry{Tenant: "globex", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(1000)})
	if len(fe.sets) == setsBefore {
		t.Fatal("a publish for the surviving tenant armed nothing — the sweep took its profile→fleet index")
	}
}

// TestArmerRemoveTenantCountsEveryViewItSweeps pins the reported count, which is the signal the
// purge coordinator's settle window runs on: 1 active version + 2 roster rows + the 2 members of
// the profile's fleet index = 5. A sweep that skipped a view would still return a plausible
// non-zero number, so the figure is asserted exactly rather than merely as "> 0".
func TestArmerRemoveTenantCountsEveryViewItSweeps(t *testing.T) {
	d, _ := evictionArmer(t)
	if n := d.RemoveTenant("acme"); n != 5 {
		t.Fatalf("RemoveTenant reported %d entries; want 5 = 1 active version + 2 roster rows + "+
			"2 fleet-index members. A short count means one of the three views was not swept", n)
	}
}

// TestViewRemoveTenantSweepsOneTenantsDevices covers the attribute view, populated through BOTH
// of its real write paths (the startup Reconcile and a live recheck's ReplaceDevice).
//
// These are the platform-set thresholds a dynamic rule compares against — the tenant's
// configuration, keyed on (tenant, device token). The bystander deliberately reuses the device
// token "d1", so a sweep keyed on the token half of devKey removes the wrong entry.
func TestViewRemoveTenantSweepsOneTenantsDevices(t *testing.T) {
	v := NewDeviceAttributeView()
	v.Reconcile([]AttrEntry{
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 50},
		{Tenant: "acme", DeviceToken: "d2", Scope: AttrScopeShared, Key: "lim", Value: 70},
		{Tenant: "globex", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 7},
	})
	v.ReplaceDevice("acme", "d3", []AttrEntry{{Tenant: "acme", DeviceToken: "d3", Scope: AttrScopeServer, Key: "lim", Value: 90}})
	if v.For("acme", "d1") == nil || v.For("acme", "d3") == nil || v.For("globex", "d1") == nil {
		t.Fatal("test bug: the view was not populated as the test assumes")
	}

	if n := v.RemoveTenant("acme"); n != 3 {
		t.Fatalf("RemoveTenant reported %d entries; want 3 (d1, d2, d3)", n)
	}

	for _, dev := range []string{"d1", "d2", "d3"} {
		if m := v.For("acme", dev); m != nil {
			t.Fatalf("the purged tenant's device %q still has thresholds in memory: %v", dev, m)
		}
	}
	if got := v.For("globex", "d1"); got == nil || got["lim"] != 7 {
		t.Fatalf("the surviving tenant's entry for the SAME device token was changed by the "+
			"purge: %v, want lim=7", got)
	}
}

// TestArmerAndViewRemoveTenantRefuseTheEmptyTenant: as with the registry, the empty tenant is not
// a purge, and both views are keyed on a tenant string that CAN be empty — a malformed projection
// row reaches them (neither validates the token; the registry is the only one that refuses it),
// so an unguarded sweep would find real entries to delete.
//
// The armer's survival is asserted on its maps rather than through AbsenceLive, which is the one
// place in this file that reaches inside: the gate resolves the tenant from a REGISTRY rule, and
// the registry refuses an empty-tenant rule outright, so no rule exists to look the entries up
// with. The alternative is not a better assertion, it is only the return value.
func TestArmerAndViewRemoveTenantRefuseTheEmptyTenant(t *testing.T) {
	d := NewDeadmanArmer(NewRuleRegistry(nil), newFakeArmer())
	d.ApplyActiveVersion(ActiveEntry{Tenant: "", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: ts(100)})
	d.ApplyDeviceMembership(RosterEntry{Tenant: "", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: ts(50)}, true)

	if n := d.RemoveTenant(""); n != 0 {
		t.Fatalf("armer RemoveTenant(\"\") reported %d entries; want 0 — and a non-zero count "+
			"also restarts the coordinator's settle window", n)
	}
	if len(d.active) != 1 || len(d.roster) != 1 || len(d.byProfile) != 1 {
		t.Fatalf("an empty-tenant sweep deleted membership: active=%d roster=%d byProfile=%d",
			len(d.active), len(d.roster), len(d.byProfile))
	}

	v := NewDeviceAttributeView()
	v.ReplaceDevice("", "devA", []AttrEntry{{DeviceToken: "devA", Scope: AttrScopeServer, Key: "lim", Value: 50}})
	if n := v.RemoveTenant(""); n != 0 {
		t.Fatalf("view RemoveTenant(\"\") reported %d entries; want 0", n)
	}
	if got := v.For("", "devA"); got == nil || got["lim"] != 50 {
		t.Fatalf("an empty-tenant sweep deleted an attribute entry: %v", got)
	}
}

// TestRemoveTenantIsIdempotentAndReportsZeroOnASecondPass covers all three together, because it
// is one property of the same caller: the purge coordinator calls each RemoveTenant on EVERY
// pass, and reads the count as "work was still being found". A non-zero return restarts its
// settle window, so a method that reported a number unconditionally — or that built its removal
// list without actually removing anything — would keep the window open forever and no purge
// would ever complete.
func TestRemoveTenantIsIdempotentAndReportsZeroOnASecondPass(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		scopedThreshold("acme", "p@1", "acme/p@1/r1"),
		scopedThreshold("globex", "p@1", "globex/p@1/r1"),
	})
	if ids := reg.RemoveTenant("acme"); len(ids) == 0 {
		t.Fatal("test bug: the first registry pass found nothing, so the second asserts nothing")
	}
	if ids := reg.RemoveTenant("acme"); len(ids) != 0 {
		t.Fatalf("a repeated registry pass returned %v — the first pass did not actually remove "+
			"the rules, so the settle window restarts on every pass", ids)
	}
	if reg.Count() != 1 {
		t.Fatalf("the repeated pass disturbed the surviving tenant: count = %d, want 1", reg.Count())
	}

	d, _ := evictionArmer(t)
	if n := d.RemoveTenant("acme"); n == 0 {
		t.Fatal("test bug: the first armer pass found nothing")
	}
	if n := d.RemoveTenant("acme"); n != 0 {
		t.Fatalf("a repeated armer pass reported %d entries removed", n)
	}
	if n := d.RemoveTenant("never-registered"); n != 0 {
		t.Fatalf("a pass for a tenant the armer never held reported %d entries removed", n)
	}

	v := NewDeviceAttributeView()
	v.Reconcile([]AttrEntry{
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 50},
		{Tenant: "globex", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 7},
	})
	if n := v.RemoveTenant("acme"); n == 0 {
		t.Fatal("test bug: the first attribute-view pass found nothing")
	}
	if n := v.RemoveTenant("acme"); n != 0 {
		t.Fatalf("a repeated attribute-view pass reported %d entries removed", n)
	}
	if n := v.RemoveTenant("never-registered"); n != 0 {
		t.Fatalf("a pass for a tenant the view never held reported %d entries removed", n)
	}
	if got := v.For("globex", "d1"); got == nil || got["lim"] != 7 {
		t.Fatalf("the repeated passes disturbed the surviving tenant: %v", got)
	}
}

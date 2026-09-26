// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// These cover the fault no delivered fact can repair: a rule, roster or attribute fact that NEVER
// reached its stream. device-management's announcements are one message each, sent best-effort
// after its transaction commits; a lost one is not redelivered, because it was never written.

// A lost publish used to silence the WHOLE profile: every device resolves to the new version token
// and nothing here is filed under it. The sweep repairs the rules and the active version from
// device-management, and the same event then fires.
func TestALostRulePublishIsRepairedBySweepingDeviceManagement(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")
	dm.publish("p")
	dm.publish("p") // the fact for p@2 is lost
	rig := newReconcileRig(t, dm)
	rig.deliverRules(0)

	// CONTROL: the state a lost publish leaves — the engine thinks p@1 is active and an event
	// stamped p@2 (which is what device-management now resolves) matches nothing.
	active, _, err := rig.rp.ProfileActiveStore.Load(context.Background(), "acme", "p")
	if err != nil || active.ActiveVersionToken != "p@1" {
		t.Fatalf("control: active version %q (err %v), want p@1", active.ActiveVersionToken, err)
	}
	if n := rig.measure("d1", "p@2", "90"); n != 0 {
		t.Fatalf("control: an event on the unannounced version published %d derived events, want 0", n)
	}

	rig.sweep()

	active, _, err = rig.rp.ProfileActiveStore.Load(context.Background(), "acme", "p")
	if err != nil || active.ActiveVersionToken != "p@2" {
		t.Fatalf("after the sweep the active version is %q (err %v), want p@2", active.ActiveVersionToken, err)
	}
	if want := dm.activeSince("p"); !active.PublishedAt.Equal(want) {
		t.Fatalf("the repaired activation instant is %s, want device-management's stored %s", active.PublishedAt, want)
	}
	if _, found, err := rig.rp.RuleStore.LoadByID(dccore.WithTenant(context.Background(), "acme"), runtime.PublishedRuleID("acme", "p@2", "hot")); err != nil || !found {
		t.Fatalf("the p@2 rule row was not repaired (found=%v err=%v)", found, err)
	}
	if n := rig.measure("d1", "p@2", "90"); n != 1 {
		t.Fatalf("after the sweep the same event published %d derived events, want 1", n)
	}
	if got := rig.repairs(projectionRules); got != 1 {
		t.Fatalf("repairs{rules} = %v, want 1", got)
	}
	if got := rig.repairs(projectionProfileActive); got != 1 {
		t.Fatalf("repairs{profile_active} = %v, want 1", got)
	}
}

// A lost rollback is repaired to the STORED reactivation instant — the value the rollback fact
// would have carried — and once repaired, the next sweep finds nothing to do.
func TestALostRollbackIsRepairedWithTheStoredActivationInstant(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.publish("p")
	dm.publish("p")
	dm.rollback("p", 1) // the rollback fact is lost
	rig := newReconcileRig(t, dm)
	rig.deliverRules(0, 1)

	rig.sweep()
	active, _, err := rig.rp.ProfileActiveStore.Load(context.Background(), "acme", "p")
	if err != nil || active.ActiveVersionToken != "p@1" {
		t.Fatalf("active version %q (err %v), want p@1", active.ActiveVersionToken, err)
	}
	if want := dm.activeSince("p"); !active.PublishedAt.Equal(want) {
		t.Fatalf("the repaired reactivation instant is %s, want the stored %s", active.PublishedAt, want)
	}

	before, writes := rig.counters(), rig.writes.Load()
	rig.sweep()
	if after := rig.counters(); !reflect.DeepEqual(before, after) {
		t.Fatalf("a second sweep over a repaired projection moved the counters:\n before %v\n after  %v", before, after)
	}
	if w := rig.writes.Load() - writes; w != 0 {
		t.Fatalf("a second sweep over a repaired projection wrote %d rows, want 0", w)
	}
}

// A device whose roster fact was lost is never watched for silence — the device a dead-man
// exists to catch. The sweep rosters it and the armer arms it.
func TestALostRosterFactIsRepairedAndArmsTheDeadMan(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"dead": deadRule})
	dm.deviceType("sensor", "p")
	dm.publish("p")
	d1 := dm.device("d1", "sensor") // its roster fact is lost
	rig := newReconcileRig(t, dm)
	rig.deliverRules(0)

	if _, live, _ := rig.rp.RosterStore.Load(context.Background(), "acme", "d1"); live {
		t.Fatal("control: the device is already rostered; the fixture did not lose its fact")
	}

	rig.sweep()

	row, live, err := rig.rp.RosterStore.Load(context.Background(), "acme", "d1")
	if err != nil || !live || row.ProfileToken != "p" {
		t.Fatalf("after the sweep the roster row is %+v live=%v err=%v, want live on p", row, live, err)
	}
	if !row.ExpectedSince.Equal(d1.CreatedAt) {
		t.Fatalf("roster expected-since %s, want device-management's %s", row.ExpectedSince, d1.CreatedAt)
	}
	since := d1.CreatedAt
	if a := dm.activeSince("p"); a.After(since) {
		since = a
	}
	id := runtime.PublishedRuleID("acme", "p@1", "dead")
	if !deadmanFires(rig.rp.engine, id, "d1", since.Add(11*time.Second)) {
		t.Fatal("the repaired device was rostered but its dead-man was never armed")
	}
	if got := rig.repairs(projectionRoster); got != 1 {
		t.Fatalf("repairs{roster} = %v, want 1", got)
	}
}

// A lost attribute fact left a dynamic threshold on its old value indefinitely.
func TestALostAttributeFactIsRepaired(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": dynamicRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")
	dm.publish("p")
	dm.setAttr("d1", "SHARED", "tempLimit", "50")
	dm.setAttr("d1", "SHARED", "tempLimit", "80") // this fact is lost
	rig := newReconcileRig(t, dm)
	rig.deliverRules(0)
	rig.deliverRoster(0)
	rig.deliverAttrs(0)

	if got := rig.rp.attrView.For("acme", "d1")["tempLimit"]; got != 50 {
		t.Fatalf("control: the view holds %v, want the stale 50", got)
	}

	rig.sweep()

	if got := rig.rp.attrView.For("acme", "d1")["tempLimit"]; got != 80 {
		t.Fatalf("after the sweep the view holds %v, want 80", got)
	}
	if n := rig.measure("d1", "p@1", "60"); n != 0 {
		t.Fatalf("60 against the repaired limit of 80 published %d derived events, want 0", n)
	}
	if n := rig.measure("d1", "p@1", "90"); n != 1 {
		t.Fatalf("90 against the repaired limit of 80 published %d derived events, want 1", n)
	}
	if got := rig.repairs(projectionAttributes); got != 1 {
		t.Fatalf("repairs{attributes} = %v, want 1", got)
	}
}

// Every kind of divergence converges on device-management's state, each repaired row is counted
// once, and a projection that agrees is left untouched — the counters CAN read zero.
func TestADivergentProjectionConverges(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	d1 := dm.device("d1", "sensor")
	d2 := dm.device("d2", "sensor")
	dm.publish("p")
	dm.setAttr("d1", "SHARED", "tempLimit", "7")
	rig := newReconcileRig(t, dm)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)

	// Seed this service's projections with one of each divergence.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(rig.rp.RuleStore.Upsert(ctx, []model.DetectRule{
		{RuleId: runtime.PublishedRuleID("acme", "p@1", "hot"), Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "hot",
			Definition: `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","threshold":99}}`}, // a stale body
		{RuleId: runtime.PublishedRuleID("acme", "p@1", "gone"), Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "gone",
			Definition: hotRule}, // a rule the version does not hold
	}))
	must(rig.rp.ProfileActiveStore.Upsert(ctx, &model.ProfileActive{Tenant: "acme", ProfileToken: "p",
		ActiveVersionToken: "p@9", PublishedAt: old})) // the wrong version
	must(rig.rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "d1", ProfileToken: "wrong", ExpectedSince: old}))
	must(rig.rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "d2", ProfileToken: "p", ExpectedSince: old}))
	must(rig.rp.RosterStore.Delete(ctx, "acme", "d2", old.Add(time.Hour))) // tombstoned, but device-management has it
	must(rig.rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "d3", ProfileToken: "p", ExpectedSince: old}))
	must(rig.rp.AttributeStore.Upsert(ctx, &model.DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED",
		AttrKey: "tempLimit", Value: 5, LastEventAt: old})) // the wrong value
	must(rig.rp.AttributeStore.Upsert(ctx, &model.DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SERVER",
		AttrKey: "other", Value: 1, LastEventAt: old})) // an attribute device-management does not have

	// Two sweeps: a row device-management no longer has is acted on only once two sweeps agree.
	rig.sweep()
	rig.sweep()

	var attr dmmodel.EntityAttribute
	must(dm.api.RDB.DB(dm.ctx).Where("attr_key = ?", "tempLimit").First(&attr).Error)
	stored := dm.activeSince("p")
	want := projectionDump{
		Rules: []model.DetectRule{{RuleId: runtime.PublishedRuleID("acme", "p@1", "hot"), Tenant: "acme",
			ProfileVersionToken: "p@1", RuleToken: "hot", Definition: hotRule}},
		Actives: []model.ProfileActive{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: stored.UTC()}},
		Roster: []model.DeviceRoster{
			{Tenant: "acme", DeviceToken: "d1", ProfileToken: "p", ExpectedSince: d1.CreatedAt.UTC(), LastEventAt: d1.CreatedAt.UTC()},
			{Tenant: "acme", DeviceToken: "d2", ProfileToken: "p", ExpectedSince: d2.CreatedAt.UTC(), LastEventAt: d2.CreatedAt.UTC()},
			{Tenant: "acme", DeviceToken: "d3", ProfileToken: "p", ExpectedSince: old, Deleted: true, LastEventAt: old},
		},
		Attributes: []model.DeviceAttribute{
			{Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", AttrKey: "other", Value: 1, Deleted: true, LastEventAt: old},
			{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "tempLimit", Value: 7, LastEventAt: attr.LastUpdated.UTC()},
		},
	}
	got := rig.dump()
	sortDump(&got)
	sortDump(&want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the projection did not converge on device-management:\n got  %+v\n want %+v", got, want)
	}
	wantCounts := map[string]float64{
		"repairs/rules": 2, "repairs/profile_active": 1, "repairs/roster": 3, "repairs/attributes": 2,
		"failures/tenants": 0, "failures/rules": 0, "failures/roster": 0, "failures/attributes": 0,
	}
	if counts := rig.counters(); !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("counters %v, want %v", counts, wantCounts)
	}

	// A converged projection is left alone: nothing written, nothing counted, nothing signalled.
	writes := rig.writes.Load()
	rig.rp.startFactReconcile()
	rig.rp.readerWG.Wait()
	if n := len(rig.rp.ruleUpdates) + len(rig.rp.armUpdates) + len(rig.rp.attrUpdates); n != 0 {
		t.Fatalf("a sweep over a converged projection signalled the loop %d times, want 0", n)
	}
	if w := rig.writes.Load() - writes; w != 0 {
		t.Fatalf("a sweep over a converged projection wrote %d rows, want 0", w)
	}
	if counts := rig.counters(); !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("a sweep over a converged projection moved the counters to %v", counts)
	}
}

// sortDump orders a dump's rows by key so two dumps compare regardless of read order.
func sortDump(d *projectionDump) {
	sortBy(d.Rules, func(r model.DetectRule) string { return r.RuleId })
	sortBy(d.Actives, func(r model.ProfileActive) string { return r.ProfileToken })
	sortBy(d.Roster, func(r model.DeviceRoster) string { return r.DeviceToken })
	sortBy(d.Attributes, func(r model.DeviceAttribute) string { return r.DeviceToken + "/" + r.Scope + "/" + r.AttrKey })
}

func sortBy[T any](s []T, key func(T) string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && key(s[j]) < key(s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// NEGATIVE CONTROL: a failed or partial read of device-management changes NOTHING. A row this
// service holds that is absent from an incomplete answer is not gone — it is unread.
func TestTheSweepNeverDeletesOnAFailedRead(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	// More devices than one roster page holds, so the second page can fail after the first
	// succeeded — a partial walk.
	reqs := make([]*dmmodel.DeviceCreateRequest, 0, dmmodel.MaxRosterPageSize+1)
	for i := 0; i <= dmmodel.MaxRosterPageSize; i++ {
		reqs = append(reqs, &dmmodel.DeviceCreateRequest{Token: "dev-" + itoa(i), DeviceTypeToken: "sensor"})
	}
	for start := 0; start < len(reqs); start += 500 {
		end := min(start+500, len(reqs))
		if _, err := dm.api.CreateDevices(dm.ctx, reqs[start:end]); err != nil {
			t.Fatal(err)
		}
	}
	dm.publish("p")
	rig := newReconcileRig(t, dm)
	rig.deliverRules(0)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour).UTC()
	// Rows device-management does not have: a complete read would (eventually) tombstone them.
	if err := rig.rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "orphan", ProfileToken: "p", ExpectedSince: old}); err != nil {
		t.Fatal(err)
	}
	if err := rig.rp.AttributeStore.Upsert(ctx, &model.DeviceAttribute{Tenant: "acme", DeviceToken: "orphan", Scope: "SHARED",
		AttrKey: "k", Value: 1, LastEventAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := rig.rp.RuleStore.Upsert(ctx, []model.DetectRule{{RuleId: runtime.PublishedRuleID("acme", "p@1", "extra"),
		Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "extra", Definition: hotRule}}); err != nil {
		t.Fatal(err)
	}
	outage := errors.New("device-management: connection reset")
	rig.src.failOn = func(door string, call int) error {
		switch {
		case door == "deviceRosterPage" && call%2 == 0: // every walk's SECOND page
			return outage
		case door == "deviceThresholdAttributePage", door == "activeProfileRules":
			return outage
		}
		return nil
	}
	before := rig.dump()

	rig.sweep()
	rig.sweep()
	rig.sweep()

	if after := rig.dump(); !reflect.DeepEqual(before, after) {
		t.Fatalf("a failed read changed the projection:\n before %+v\n after  %+v", before, after)
	}
	for _, p := range []reconcileProjection{projectionRules, projectionRoster, projectionAttributes} {
		if got := rig.failures(p); got != 3 {
			t.Errorf("failures{%s} = %v, want 3 (one per sweep)", p, got)
		}
	}
	for _, p := range repairProjections {
		if got := rig.repairs(p); got != 0 {
			t.Errorf("repairs{%s} = %v, want 0", p, got)
		}
	}
	if rig.src.callsTo("deviceRosterPage") != 6 {
		t.Fatalf("the roster walk made %d requests, want 6 — the harness did not reach the second page",
			rig.src.callsTo("deviceRosterPage"))
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{digits[i%10]}, b...)
	}
	return string(b)
}

// NEGATIVE CONTROL: with no tenant list there is nothing to compare, and nothing is read from
// device-management at all.
func TestATenantListFailureReconcilesNothing(t *testing.T) {
	dm := newDmWorld(t)
	rig := newReconcileRig(t, dm)
	rig.src.failOn = func(door string, _ int) error {
		if door == "tenantTokens" {
			return errors.New("user-management unavailable")
		}
		return nil
	}
	if rig.rp.reconcileFacts(context.Background()) {
		t.Fatal("a sweep with no tenant list reported itself complete")
	}
	for _, door := range []string{"activeProfileRules", "deviceRosterPage", "deviceThresholdAttributePage"} {
		if n := rig.src.callsTo(door); n != 0 {
			t.Fatalf("%s was queried %d times with no tenant list", door, n)
		}
	}
	if got := rig.failures(projectionTenants); got != 1 {
		t.Fatalf("failures{tenants} = %v, want 1", got)
	}
}

// NEGATIVE CONTROL for the race the conditional writes exist for: a live fact that lands between
// the sweep's read of this service's row and its repair write wins. The sweep's write names the row
// it compared, finds it changed, and applies nothing.
func TestALiveFactThatLandsMidSweepWins(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")
	rig := newReconcileRig(t, dm)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour).UTC()
	if err := rig.rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "d1", ProfileToken: "stale", ExpectedSince: old}); err != nil {
		t.Fatal(err)
	}
	// While the sweep reads device-management — after it has read this service's rows — a newer
	// roster fact for d1 arrives through the live consumer.
	live := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Microsecond)
	rig.src.failOn = func(door string, call int) error {
		if door == "deviceRosterPage" && call == 1 {
			b, err := dmproto.MarshalDeviceRosterEvent(&dmmodel.DeviceRosterEvent{DeviceToken: "d1", ProfileToken: "p2", ExpectedSince: live})
			if err != nil {
				t.Fatal(err)
			}
			if !rig.rp.handleRosterFact(messaging.NewConsumedMessage("dc.acme."+streams.DeviceRoster, b, 0, nil, &fakeAck{}), false) {
				t.Fatal("the live fact was not consumed")
			}
		}
		return nil
	}

	rig.sweep()

	row, liveRow, err := rig.rp.RosterStore.Load(ctx, "acme", "d1")
	if err != nil || !liveRow || row.ProfileToken != "p2" || !row.ExpectedSince.Equal(live) {
		t.Fatalf("the row is %+v (live=%v err=%v); the live fact (p2, %s) must win over the sweep's older answer",
			row, liveRow, err, live)
	}
	if got := rig.repairs(projectionRoster); got != 0 {
		t.Fatalf("repairs{roster} = %v, want 0 — a write that lost the race is not a repair", got)
	}
}

// A device tombstoned by the sweep and then re-created (token reuse) is live again: the tombstone
// keeps the lifecycle instant it observed rather than inventing one, so the re-create's later
// instant still applies.
func TestAReCreatedDeviceSurvivesATombstone(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")
	rig := newReconcileRig(t, dm)
	rig.deliverRoster(0)
	dm.deleteDevice("d1") // its entity-deleted fact never arrives

	rig.sweep()
	if _, live, _ := rig.rp.RosterStore.Load(context.Background(), "acme", "d1"); !live {
		t.Fatal("a device missing from ONE sweep was tombstoned; a deletion must be seen by two")
	}
	rig.sweep()
	if _, live, _ := rig.rp.RosterStore.Load(context.Background(), "acme", "d1"); live {
		t.Fatal("a device missing from two sweeps was not tombstoned")
	}

	recreated := dm.device("d1", "sensor")
	rig.deliverRoster(len(dm.roster.payloads) - 1)
	row, live, err := rig.rp.RosterStore.Load(context.Background(), "acme", "d1")
	if err != nil || !live || !row.ExpectedSince.Equal(recreated.CreatedAt) {
		t.Fatalf("the re-created device is %+v (live=%v err=%v), want live since %s", row, live, err, recreated.CreatedAt)
	}
}

// NEGATIVE CONTROL for the counter's meaning: a change whose fact is merely still in flight is not
// a lost fact. A divergence younger than factSettle is left for the next sweep, and not counted.
func TestAFactStillInFlightIsNotCountedAsLost(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")
	dm.publish("p")
	dm.setAttr("d1", "SHARED", "tempLimit", "50")
	rig := newReconcileRig(t, dm)
	rig.clock.Set(time.Now()) // the changes above happened moments ago

	rig.sweep()
	for _, p := range repairProjections {
		if got := rig.repairs(p); got != 0 {
			t.Fatalf("repairs{%s} = %v for changes whose facts are still in flight, want 0", p, got)
		}
	}
	if d := rig.dump(); len(d.Rules)+len(d.Actives)+len(d.Roster)+len(d.Attributes) != 0 {
		t.Fatalf("the sweep wrote rows for in-flight changes: %+v", d)
	}

	// Once they have had time to arrive and did not, they are lost, and repaired.
	rig.clock.Set(time.Now().Add(factSettle + time.Second))
	rig.sweep()
	for _, p := range repairProjections {
		if got := rig.repairs(p); got != 1 {
			t.Errorf("repairs{%s} = %v once the changes had settled, want 1", p, got)
		}
	}
}

// NEGATIVE CONTROL for the harness: with every fact delivered, a sweep repairs nothing, writes
// nothing and signals nothing — the counters and the write count CAN read zero.
func TestTheSweepRepairsNothingWhenNothingDiverged(t *testing.T) {
	dm := newDmWorld(t)
	dm.profile("p", map[string]string{"hot": hotRule, "dead": deadRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")
	dm.publish("p")
	dm.setAttr("d1", "SHARED", "tempLimit", "50")
	dm.rollback("p", 1)
	rig := newReconcileRig(t, dm)
	rig.deliverRules(0, 1)
	rig.deliverRoster(0)
	rig.deliverAttrs(0)

	writes := rig.writes.Load()
	rig.rp.startFactReconcile()
	rig.rp.readerWG.Wait()
	if n := len(rig.rp.ruleUpdates) + len(rig.rp.armUpdates) + len(rig.rp.attrUpdates); n != 0 {
		t.Fatalf("a sweep over a current projection signalled the loop %d times", n)
	}
	if w := rig.writes.Load() - writes; w != 0 {
		t.Fatalf("a sweep over a current projection wrote %d rows", w)
	}
	for _, p := range repairProjections {
		if got := rig.repairs(p); got != 0 {
			t.Fatalf("repairs{%s} = %v over a current projection", p, got)
		}
	}
	if rig.src.sweeps.Load() != 1 || rig.src.callsTo("deviceRosterPage") != 1 {
		t.Fatal("the sweep did not actually read device-management; this control proved nothing")
	}
}

// The first tick of every leadership term sweeps: a term build is when a fact lost while this
// replica stood by needs repairing.
func TestEveryTermStartsWithAFactSweepDue(t *testing.T) {
	dm := newDmWorld(t)
	rig := newReconcileRig(t, dm)
	rig.rp.lastFactReconcile = time.Now()
	rig.rp.factAbsent.roster["acme"] = map[string]model.DeviceRoster{"d1": {}}
	rig.rp.resetForTerm()
	if !rig.rp.lastFactReconcile.IsZero() {
		t.Fatal("a new term inherited the previous term's sweep time, so its first tick does not sweep")
	}
	if len(rig.rp.factAbsent.roster) != 0 {
		t.Fatal("a new term inherited the previous term's unconfirmed deletions")
	}
}

// The loop's ticker runs the sweep — the half no direct call can prove.
func TestTheLoopTickerRunsTheFactSweep(t *testing.T) {
	dm := newDmWorld(t)
	rig := newReconcileRig(t, dm)
	loopCtx, cancel := context.WithCancel(context.Background())
	rig.rp.procCtx, rig.rp.procCancel = loopCtx, cancel
	rig.rp.ResolvedEventsReader = &fakeReader{}
	rig.rp.cfg.TickInterval = 5 * time.Millisecond

	rig.rp.readerWG.Add(1)
	go rig.rp.run()
	defer func() { cancel(); rig.rp.readerWG.Wait() }()

	deadline := time.Now().Add(2 * time.Second)
	for rig.src.sweeps.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the loop's ticker never ran the fact sweep")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// And only once inside the interval: many ticks, one sweep.
	time.Sleep(100 * time.Millisecond)
	if n := rig.src.sweeps.Load(); n != 1 {
		t.Fatalf("the ticker ran %d sweeps inside one interval, want 1", n)
	}
}

// Only one sweep runs at a time: a tick that finds one in flight drops its own.
func TestAHeldFactSweepIsNotStacked(t *testing.T) {
	dm := newDmWorld(t)
	rig := newReconcileRig(t, dm)
	rig.src.hold = make(chan struct{})
	rig.rp.startFactReconcile()
	rig.rp.startFactReconcile()
	rig.rp.startFactReconcile()
	close(rig.src.hold)
	rig.rp.readerWG.Wait()
	if n := rig.src.callsTo("tenantTokens"); n != 1 {
		t.Fatalf("%d sweeps ran; a sweep in flight must not be stacked", n)
	}
}

// A rollback on Postgres re-announces a version's rules in their jsonb rendering, byte-different
// from the compact bytes its publish carried. That is the same rule, so the running rule keeps its
// state — a byte comparison would drop its holds, windows and absence timers.
func TestTheSameRuleInAnotherByteFormKeepsItsState(t *testing.T) {
	compact := runtime.ScopedRule{Definition: `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","threshold":30}}`}
	rendered := runtime.ScopedRule{Definition: `{"name": "hot", "type": "threshold", "when": {"op": "gt", "metric": "temp", "threshold": 30}}`}
	if rendered.DiffersFrom(&compact) {
		t.Fatal("the jsonb rendering of an unchanged rule reads as a changed rule; its running state would be dropped")
	}
	changed := runtime.ScopedRule{Definition: `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","threshold":31}}`}
	if !changed.DiffersFrom(&compact) {
		t.Fatal("a changed threshold reads as the same rule")
	}
}

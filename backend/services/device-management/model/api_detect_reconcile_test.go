// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The tests in this file pin the two halves of the detection reconcile contract that live in
// device-management: every rule, roster and attribute fact carries a value this service STORES
// (so a repair can reproduce it), and each reconcile door answers exactly what the facts say
// (because both are built from one definition).

// newReconcileDoorApi builds an in-memory Api over every table the three doors and the fact
// emits read, with capturing publishers wired.
func newReconcileDoorApi(t *testing.T) (*Api, context.Context, *capturePublishedRules, *captureRoster, *captureAttr) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{},
		&EntityAttribute{}, &EntityGroupMembership{}, &EntityGroupFacetRef{}))
	api := NewApi(&rdb.RdbManager{Database: db})
	rules, roster, attrs := &capturePublishedRules{}, &captureRoster{}, &captureAttr{}
	api.DetectionRulesPublishedPublisher = rules
	api.DeviceRosterPublisher = roster
	api.DeviceAttributePublisher = attrs
	return api, core.WithTenant(context.Background(), "acme"), rules, roster, attrs
}

// storedActiveSince re-reads a profile's stored activation instant.
func storedActiveSince(t *testing.T, api *Api, ctx context.Context, token string) time.Time {
	t.Helper()
	p, err := api.deviceProfileByToken(ctx, token)
	require.NoError(t, err)
	require.True(t, p.ActiveSince.Valid, "profile %q has no stored activation instant", token)
	return p.ActiveSince.Time
}

// allActiveProfileRules walks the rules door to the end.
func allActiveProfileRules(t *testing.T, api *Api, ctx context.Context, limit int) []ActiveProfileRules {
	t.Helper()
	var out []ActiveProfileRules
	var after uint64
	for {
		page, err := api.ActiveProfileRules(ctx, after, limit)
		require.NoError(t, err)
		out = append(out, page.Entries...)
		if page.NextCursor == nil {
			return out
		}
		after = *page.NextCursor
	}
}

// A publish stores ONE activation instant and every reader sees it: the version row's creation
// time, the profile's active_since, the fact's PublishedAt and the reconcile door's answer.
func TestPublishStoresAndEmitsOneActivationInstant(t *testing.T) {
	api, ctx, facts, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)

	version, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	stored := storedActiveSince(t, api, ctx, "prof")

	require.Len(t, facts.events, 1)
	assert.True(t, facts.events[0].PublishedAt.Equal(stored), "fact %s, stored %s", facts.events[0].PublishedAt, stored)
	assert.True(t, version.CreatedAt.Equal(stored), "version created %s, stored %s", version.CreatedAt, stored)
	assert.Equal(t, stored, stored.Truncate(time.Microsecond), "the instant is minted at the precision the database keeps")

	door := allActiveProfileRules(t, api, ctx, MaxActiveProfileRulesPageSize)
	require.Len(t, door, 1)
	assert.Equal(t, "prof@1", door[0].VersionToken)
	assert.True(t, door[0].ActiveSince.Equal(stored), "door %s, stored %s", door[0].ActiveSince, stored)
	assert.Equal(t, facts.events[0].Rules, door[0].Rules, "the door answers the rules the fact carried")
}

// A rollback's fact carries the STORED reactivation instant — the value the door returns —
// rather than a clock read at send time that nothing could ever reproduce.
func TestRollbackEmitsTheStoredActivationInstant(t *testing.T) {
	api, ctx, facts, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	v2Since := storedActiveSince(t, api, ctx, "prof")

	_, err = api.RollbackDeviceProfile(ctx, "prof", 1)
	require.NoError(t, err)
	stored := storedActiveSince(t, api, ctx, "prof")

	require.Len(t, facts.events, 3)
	rb := facts.events[2]
	assert.Equal(t, "prof@1", rb.ProfileVersionToken)
	assert.True(t, rb.PublishedAt.Equal(stored), "rollback fact %s, stored %s", rb.PublishedAt, stored)
	assert.True(t, stored.After(v2Since), "the reactivation is later than the version it superseded")

	door := allActiveProfileRules(t, api, ctx, MaxActiveProfileRulesPageSize)
	require.Len(t, door, 1)
	assert.Equal(t, "prof@1", door[0].VersionToken)
	assert.True(t, door[0].ActiveSince.Equal(stored), "door %s, stored %s", door[0].ActiveSince, stored)
	assert.Equal(t, facts.events[0].Rules, rb.Rules, "a rollback re-emits the bodies the publish carried")
}

// A replica whose clock runs behind the one that made the last change still mints a LATER
// activation — the detection engine refuses an older one — so the stored instant never moves
// backwards.
func TestActivationInstantNeverMovesBackwards(t *testing.T) {
	api, ctx, facts, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)

	// Another replica's clock was an hour ahead when it made the last change.
	ahead := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceProfile{}).Where("token = ?", "prof").
		UpdateColumn("active_since", ahead).Error)

	_, err = api.RollbackDeviceProfile(ctx, "prof", 1)
	require.NoError(t, err)
	want := ahead.Add(time.Microsecond)
	assert.True(t, facts.events[2].PublishedAt.Equal(want), "got %s, want %s", facts.events[2].PublishedAt, want)
	assert.True(t, storedActiveSince(t, api, ctx, "prof").Equal(want))
}

// A profile whose pointer was last moved without storing an instant — before the column existed,
// or by an older replica during a rolling upgrade — is answered from its version rows: the newest
// version's publish when it is the active one, a microsecond after it when an older one is active.
func TestActiveSinceResolvesFromTheVersionRowsWhenNotStored(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	v2, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	clear := func(active int32) {
		require.NoError(t, api.RDB.DB(ctx).Model(&DeviceProfile{}).Where("token = ?", "prof").
			UpdateColumns(map[string]any{"active_since": nil, "active_version": active}).Error)
	}

	clear(2)
	door := allActiveProfileRules(t, api, ctx, MaxActiveProfileRulesPageSize)
	require.Len(t, door, 1)
	assert.True(t, door[0].ActiveSince.Equal(v2.CreatedAt), "active = newest: got %s, want %s", door[0].ActiveSince, v2.CreatedAt)

	clear(1)
	door = allActiveProfileRules(t, api, ctx, MaxActiveProfileRulesPageSize)
	require.Len(t, door, 1)
	want := v2.CreatedAt.Add(time.Microsecond)
	assert.True(t, door[0].ActiveSince.Equal(want), "rolled back without an instant: got %s, want %s", door[0].ActiveSince, want)

	// The next activation is floored on that same resolved value.
	_, err = api.RollbackDeviceProfile(ctx, "prof", 2)
	require.NoError(t, err)
	assert.True(t, storedActiveSince(t, api, ctx, "prof").After(want))
}

// A STORED instant older than what the version rows imply is not believed either: it is what an
// older replica leaves behind when it moves the pointer during a rolling upgrade without writing
// active_since. Believing it would answer an activation earlier than the active version's own
// publish, and would lower the floor the next activation is minted above.
func TestAStaleStoredActiveSinceLosesToTheVersionRows(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	v2, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	stale := v2.CreatedAt.Add(-time.Hour)
	setStale := func(active int32) {
		require.NoError(t, api.RDB.DB(ctx).Model(&DeviceProfile{}).Where("token = ?", "prof").
			UpdateColumns(map[string]any{"active_since": stale, "active_version": active}).Error)
	}

	setStale(2)
	door := allActiveProfileRules(t, api, ctx, MaxActiveProfileRulesPageSize)
	require.Len(t, door, 1)
	assert.True(t, door[0].ActiveSince.Equal(v2.CreatedAt), "active = newest: got %s, want %s", door[0].ActiveSince, v2.CreatedAt)

	setStale(1)
	door = allActiveProfileRules(t, api, ctx, MaxActiveProfileRulesPageSize)
	require.Len(t, door, 1)
	want := v2.CreatedAt.Add(time.Microsecond)
	assert.True(t, door[0].ActiveSince.Equal(want), "rolled back by an older replica: got %s, want %s", door[0].ActiveSince, want)
}

// A draft edit must not write the activation instant back from the value it loaded, any more than
// the version pointer beside it (the Omit in UpdateDeviceProfile). As in
// TestAssetTypeUpdateDoesNotWriteBackTheVersionPointer, the interleaving is the test: a gorm
// callback moves the pointer and its instant — the way a concurrent rollback would — after
// UpdateDeviceProfile's own read and before its own Save.
func TestProfileUpdateDoesNotWriteBackTheActivationInstant(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	before := storedActiveSince(t, api, ctx, "prof")
	rolledBackAt := before.Add(time.Minute).Truncate(time.Microsecond)

	fired := false
	db := api.RDB.Database
	require.NoError(t, db.Callback().Update().Before("gorm:update").
		Register("test:concurrent_rollback", func(tx *gorm.DB) {
			if fired || tx.Statement.Table != "device_profiles" {
				return
			}
			fired = true
			// On the statement's own connection: each connection to an in-memory SQLite database
			// is a separate, empty database.
			if err := tx.Session(&gorm.Session{NewDB: true}).Exec(`UPDATE device_profiles SET active_version = 1, active_since = ? WHERE token = ?`,
				rolledBackAt, "prof").Error; err != nil {
				t.Errorf("simulated concurrent rollback failed: %v", err)
			}
		}), "register the concurrent-rollback callback")
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:concurrent_rollback") })

	_, err = api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)
	require.True(t, fired, "the callback never ran; this test would pass vacuously")

	p, err := api.deviceProfileByToken(ctx, "prof")
	require.NoError(t, err)
	assert.EqualValues(t, 1, p.ActiveVersion.Int32, "an edit reverted the version pointer")
	require.True(t, p.ActiveSince.Valid)
	assert.True(t, p.ActiveSince.Time.Equal(rolledBackAt),
		"an edit wrote back the stale activation instant %s over the rollback's %s", p.ActiveSince.Time, rolledBackAt)
	assert.Equal(t, "Renamed", p.Name.String, "the edit itself still landed")
}

// The same race for a device: an edit that is not a re-type must not write back the membership
// instant a concurrent re-point of its type stamped.
func TestDeviceUpdateDoesNotWriteBackTheMembershipInstant(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "sensor", "sensor-profile")
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	repointedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)

	fired := false
	db := api.RDB.Database
	require.NoError(t, db.Callback().Update().Before("gorm:update").
		Register("test:concurrent_repoint", func(tx *gorm.DB) {
			if fired || tx.Statement.Table != "devices" {
				return
			}
			fired = true
			if err := tx.Session(&gorm.Session{NewDB: true}).Exec(`UPDATE devices SET expected_since = ? WHERE token = ?`, repointedAt, "d1").Error; err != nil {
				t.Errorf("simulated concurrent re-point failed: %v", err)
			}
		}), "register the concurrent-repoint callback")
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:concurrent_repoint") })

	_, err = api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)
	require.True(t, fired, "the callback never ran; this test would pass vacuously")

	var d Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "d1").First(&d).Error)
	require.True(t, d.ExpectedSince.Valid, "an edit wrote back the NULL membership instant it loaded")
	assert.True(t, d.ExpectedSince.Time.Equal(repointedAt), "got %s, want %s", d.ExpectedSince.Time, repointedAt)
	assert.Equal(t, "Renamed", d.Name.String, "the edit itself still landed")
}

// rosterTriple is a roster entry without its row id, for set comparison.
type rosterTriple struct {
	device, profile string
	since           time.Time
}

func rosterTriples[T any](in []T, f func(T) rosterTriple) []rosterTriple {
	out := make([]rosterTriple, 0, len(in))
	for _, e := range in {
		out = append(out, f(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].device < out[j].device })
	return out
}

// The roster facts and the roster page are one definition: for devices on a profiled type, an
// unprofiled type, a re-typed device and a type re-pointed to another profile, the latest fact per
// device equals the page's entry — token, profile and instant.
func TestRosterFactsAndRosterPageAgree(t *testing.T) {
	api, ctx, _, facts, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "sensor", "sensor-profile")
	seedType(t, api, ctx, "bare", "")
	seedType(t, api, ctx, "meter", "meter-profile")
	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "other-profile"})
	require.NoError(t, err)

	for _, d := range []struct{ tok, typ string }{{"d1", "sensor"}, {"d2", "bare"}, {"d3", "sensor"}, {"d4", "meter"}} {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: d.tok, DeviceTypeToken: d.typ})
		require.NoError(t, err)
	}
	_, err = api.CreateDevices(ctx, []*DeviceCreateRequest{{Token: "d5", DeviceTypeToken: "bare"}, {Token: "d6", DeviceTypeToken: "meter"}})
	require.NoError(t, err)
	_, err = api.UpdateDevice(ctx, "d3", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalString{Set: true, Value: strp("meter")}})
	require.NoError(t, err)
	_, err = api.UpdateDeviceType(ctx, "meter", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalString{Set: true, Value: strp("other-profile")}})
	require.NoError(t, err)

	latest := make(map[string]*DeviceRosterEvent)
	for _, e := range facts.events {
		latest[e.DeviceToken] = e
	}
	var fromFacts []*DeviceRosterEvent
	for _, e := range latest {
		fromFacts = append(fromFacts, e)
	}
	page, err := api.DeviceRosterPage(ctx, 0, MaxRosterPageSize)
	require.NoError(t, err)
	assert.Nil(t, page.NextCursor)

	gotFacts := rosterTriples(fromFacts, func(e *DeviceRosterEvent) rosterTriple {
		return rosterTriple{e.DeviceToken, e.ProfileToken, e.ExpectedSince.UTC()}
	})
	gotPage := rosterTriples(page.Entries, func(e DeviceRosterEntry) rosterTriple {
		return rosterTriple{e.DeviceToken, e.ProfileToken, e.ExpectedSince.UTC()}
	})
	require.Len(t, gotPage, 6, "every device is on the roster, including those whose type adopts no profile")
	assert.Equal(t, gotFacts, gotPage)
	byDevice := make(map[string]rosterTriple)
	for _, r := range gotPage {
		byDevice[r.device] = r
	}
	assert.Equal(t, "", byDevice["d2"].profile)
	assert.Equal(t, "other-profile", byDevice["d3"].profile, "re-typed onto meter, whose profile was then re-pointed")
	assert.Equal(t, "other-profile", byDevice["d6"].profile)
	assert.Equal(t, "sensor-profile", byDevice["d1"].profile)
}

// A re-type and a re-point STORE the instant membership began, and it is that stored value the
// fact carries — later than the device's creation, so a moved device gets a fresh grace window.
func TestRetypeAndRepointStoreTheMembershipInstant(t *testing.T) {
	api, ctx, _, facts, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "sensor", "sensor-profile")
	seedType(t, api, ctx, "meter", "meter-profile")
	created, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	_, err = api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d2", DeviceTypeToken: "meter"})
	require.NoError(t, err)
	stored := func(token string) time.Time {
		var d Device
		require.NoError(t, api.RDB.DB(ctx).Where("token = ?", token).First(&d).Error)
		require.True(t, d.ExpectedSince.Valid, "device %q has no stored membership instant", token)
		return d.ExpectedSince.Time
	}

	_, err = api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalString{Set: true, Value: strp("meter")}})
	require.NoError(t, err)
	retype := facts.events[len(facts.events)-1]
	assert.Equal(t, "d1", retype.DeviceToken)
	assert.True(t, retype.ExpectedSince.Equal(stored("d1")), "re-type fact %s, stored %s", retype.ExpectedSince, stored("d1"))
	assert.True(t, retype.ExpectedSince.After(created.CreatedAt))

	_, err = api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "other-profile"})
	require.NoError(t, err)
	before := len(facts.events)
	_, err = api.UpdateDeviceType(ctx, "meter", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalString{Set: true, Value: strp("other-profile")}})
	require.NoError(t, err)
	require.Len(t, facts.events, before+2, "a re-point re-rosters every device of the type")
	for _, e := range facts.events[before:] {
		assert.Equal(t, "other-profile", e.ProfileToken)
		assert.True(t, e.ExpectedSince.Equal(stored(e.DeviceToken)), "re-point fact %s for %s, stored %s",
			e.ExpectedSince, e.DeviceToken, stored(e.DeviceToken))
	}
}

// The threshold page answers exactly the attributes whose writes emit a VALUE fact: numeric
// SHARED and SERVER device attributes — not CLIENT, not a non-numeric one, not a deleted one.
func TestThresholdAttributePageAgreesWithEmittedFacts(t *testing.T) {
	api, ctx, _, _, facts := newReconcileDoorApi(t)
	seedType(t, api, ctx, "sensor", "")
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	set := func(scope, key, vt, v string) {
		_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: entity.TypeDevice.String(),
			Entity: "d1", Scope: scope, AttrKey: key, ValueType: vt, Value: strp(v)})
		require.NoError(t, err)
	}
	set(string(AttributeScopeShared), "limit", string(AttributeValueDouble), "72.5")
	set(string(AttributeScopeServer), "limit", string(AttributeValueLong), "80")
	set(string(AttributeScopeClient), "limit", string(AttributeValueDouble), "1")
	set(string(AttributeScopeServer), "label", string(AttributeValueString), "hot")
	set(string(AttributeScopeShared), "gone", string(AttributeValueDouble), "3")
	_, err = api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d1", string(AttributeScopeShared), "gone")
	require.NoError(t, err)

	type kv struct {
		scope, key string
		value      float64
		at         time.Time
	}
	latest := make(map[string]*DeviceAttributeEvent)
	for _, e := range facts.events {
		latest[e.Scope+"/"+e.AttrKey] = e
	}
	var fromFacts []kv
	for _, e := range latest {
		if !e.Removed {
			fromFacts = append(fromFacts, kv{e.Scope, e.AttrKey, e.Value, e.UpdatedAt.UTC()})
		}
	}
	page, err := api.DeviceThresholdAttributePage(ctx, 0, MaxThresholdAttributePageSize)
	require.NoError(t, err)
	var fromPage []kv
	for _, e := range page.Entries {
		assert.Equal(t, "d1", e.DeviceToken)
		fromPage = append(fromPage, kv{e.Scope, e.AttrKey, e.Value, e.UpdatedAt.UTC()})
	}
	less := func(s []kv) func(i, j int) bool {
		return func(i, j int) bool { return s[i].scope+s[i].key < s[j].scope+s[j].key }
	}
	sort.Slice(fromFacts, less(fromFacts))
	sort.Slice(fromPage, less(fromPage))
	require.Len(t, fromPage, 2)
	assert.Equal(t, fromFacts, fromPage)
	assert.Equal(t, kv{"SERVER", "limit", 80, fromPage[0].at}, fromPage[0])
	assert.Equal(t, kv{"SHARED", "limit", 72.5, fromPage[1].at}, fromPage[1])
}

// A frozen snapshot that does not parse fails the rules door — it must never read as "this
// version has no rules", which the reconcile would act on by deleting every rule it holds.
func TestActiveProfileRulesRefuseACorruptSnapshot(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceProfileVersion{}).Where("version = ?", 1).
		UpdateColumn("snapshot", datatypes.JSON(`{"rules": "not a list"}`)).Error)

	page, err := api.ActiveProfileRules(ctx, 0, MaxActiveProfileRulesPageSize)
	assert.Error(t, err)
	assert.Nil(t, page)
}

// Each door is a keyset walk that visits every row exactly once and ends with a nil cursor.
func TestReconcilePagesAreKeysetAndComplete(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "sensor", "")
	for _, tok := range []string{"a", "b", "c", "d", "e"} {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tok, DeviceTypeToken: "sensor"})
		require.NoError(t, err)
	}
	var tokens []string
	var after uint64
	pages := 0
	for {
		page, err := api.DeviceRosterPage(ctx, after, 2)
		require.NoError(t, err)
		pages++
		for _, e := range page.Entries {
			tokens = append(tokens, e.DeviceToken)
		}
		if page.NextCursor == nil {
			break
		}
		after = *page.NextCursor
	}
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, tokens)
	assert.Equal(t, 3, pages)

	for _, p := range []string{"p1", "p2", "p3"} {
		seedProfileWithRule(t, api, ctx, p, "r-"+p, true)
		_, err := api.PublishDeviceProfile(ctx, p, nil, nil, "tester")
		require.NoError(t, err)
	}
	var versions []string
	for _, e := range allActiveProfileRules(t, api, ctx, 2) {
		versions = append(versions, e.VersionToken)
	}
	assert.Equal(t, []string{"p1@1", "p2@1", "p3@1"}, versions)
}

// A page size outside 1..the door's maximum is refused, not clamped.
func TestReconcileDoorsRefuseAnOutOfRangePageSize(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	for _, n := range []int{0, -1, MaxActiveProfileRulesPageSize + 1} {
		_, err := api.ActiveProfileRules(ctx, 0, n)
		assert.Error(t, err, "activeProfileRules limit %d", n)
	}
	for _, n := range []int{0, MaxRosterPageSize + 1} {
		_, err := api.DeviceRosterPage(ctx, 0, n)
		assert.Error(t, err, "deviceRosterPage limit %d", n)
	}
	for _, n := range []int{0, MaxThresholdAttributePageSize + 1} {
		_, err := api.DeviceThresholdAttributePage(ctx, 0, n)
		assert.Error(t, err, "deviceThresholdAttributePage limit %d", n)
	}
}

// SameRuleDefinition compares documents, not bytes: the jsonb rendering of a rule is the same
// rule as its compact form, and a changed value is a changed rule.
func TestSameRuleDefinition(t *testing.T) {
	compact := `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","value":30}}`
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"identical", compact, compact, true},
		{"jsonb rendering", compact, `{"name": "hot", "type": "threshold", "when": {"op": "gt", "value": 30, "metric": "temp"}}`, true},
		{"number spelling", `{"v":1}`, `{"v":1.0}`, true},
		{"exponent spelling", `{"v":100}`, `{"v":1e2}`, true},
		{"unicode escape", `{"s":"\u00e9"}`, `{"s":"é"}`, true},
		{"changed value", compact, `{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","value":31}}`, false},
		{"big integers stay exact", `{"v":12345678901234567890}`, `{"v":12345678901234567891}`, false},
		{"array order matters", `{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{"added key", `{"a":1}`, `{"a":1,"b":null}`, false},
		{"not json compares bytes", `not json`, `not  json`, false},
		{"trailing content is not a document", `{"a":1} {}`, `{"a":1}`, false},
		{"huge exponent compares by spelling", `{"v":1e999999999}`, `{"v":1E999999999}`, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.same, SameRuleDefinition(c.a, c.b), c.name)
		assert.Equal(t, c.same, SameRuleDefinition(c.b, c.a), c.name+" (reversed)")
	}
}

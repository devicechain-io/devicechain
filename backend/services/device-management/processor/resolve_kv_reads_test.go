// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	nats "github.com/nats-io/nats.go"
	"gorm.io/gorm"
)

// These tests measure what one resolved event costs in cache round trips, through the
// REAL cached decorator over a real (SQLite) Api. The cache stores are counting in-memory
// doubles, so a Get here is exactly one key-value round trip in production.
//
// 🔑 THE CACHES ARE FOUND BY REFLECTION rather than named, so the rig does not care which
// cache fields model.Caches declares. That is what lets these tests run against a tree
// whose cache set differs from the one they were written on, and report the difference as
// a COUNT rather than failing to compile.

// resolveRig is a CachedApi over SQLite with every cache backed by a counting store.
type resolveRig struct {
	t      testing.TB
	ctx    context.Context
	db     *gorm.DB
	api    *model.Api
	capi   *model.CachedApi
	rez    *EventResolver
	stores map[string]*msgtest.MemoryKV

	mu  sync.Mutex
	sql []string
}

// newCountingResolveRig seeds one device of a type whose profile is PUBLISHED with one
// declared metric ("temp", unit Cel), tracked to one area. scoped seeds one rule-scoped
// group reference so the tenant's membership reads are not short-circuited.
//
// wrap, when non-nil, may replace the store a cache field is built over; it is how the
// version-straddle test hooks a read.
func newCountingResolveRig(t *testing.T, scoped bool,
	wrap func(field string, kv *msgtest.MemoryKV) *messaging.Cache) *resolveRig {
	t.Helper()
	return buildResolveRig(t, putest.NewSQLiteDB(t, resolveRigTables...), scoped, 0, wrap)
}

// resolveRigTables are the tables the resolve path reads.
var resolveRigTables = []any{&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
	&model.DeviceProfileVersion{}, &model.MetricDefinition{}, &model.CommandDefinition{},
	&model.DetectionRule{}, &model.DetectionRuleScopeRef{}, &model.EntityRelationship{},
	&model.EntityRelationshipType{}, &model.GeoFenceSetVersion{}, &model.EntityGroupFacetRef{},
	&model.EntityGroupMembership{}}

// buildResolveRig seeds the rig into db. extraMetrics declares that many more metrics on
// the profile beside "temp", to size the published version the per-type entry carries.
func buildResolveRig(t testing.TB, db *gorm.DB, scoped bool, extraMetrics int,
	wrap func(field string, kv *msgtest.MemoryKV) *messaging.Cache) *resolveRig {
	t.Helper()
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")

	rig := &resolveRig{t: t, ctx: ctx, db: db, api: api, stores: map[string]*msgtest.MemoryKV{}}

	profile := &model.DeviceProfile{}
	profile.Token = "p"
	mustCreate(t, db.WithContext(ctx), profile, "profile")

	unit := "Cel"
	if _, err := api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{
		Token: "temp-def", DeviceProfileToken: "p", MetricKey: "temp", DataType: "DOUBLE", Unit: &unit,
	}); err != nil {
		t.Fatalf("seed metric definition: %v", err)
	}
	for i := 0; i < extraMetrics; i++ {
		name, desc := fmt.Sprintf("Metric %d", i), "a declared metric sizing the published version"
		if _, err := api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{
			Token: fmt.Sprintf("m-%d", i), DeviceProfileToken: "p", MetricKey: fmt.Sprintf("m%d", i),
			DataType: "DOUBLE", Unit: &unit, Name: &name, Description: &desc,
		}); err != nil {
			t.Fatalf("seed metric definition %d: %v", i, err)
		}
	}
	if _, err := api.PublishDeviceProfile(ctx, "p", nil, nil, "t"); err != nil {
		t.Fatalf("publish profile: %v", err)
	}

	dtype := &model.DeviceType{ProfileId: &profile.ID}
	dtype.Token = "dt"
	mustCreate(t, db.WithContext(ctx), dtype, "device type")

	device := &model.Device{DeviceTypeId: dtype.ID}
	device.Token = "dev"
	mustCreate(t, db.WithContext(ctx), device, "device")

	relType := &model.EntityRelationshipType{Tracked: true}
	relType.Token = "tracks"
	mustCreate(t, db.WithContext(ctx), relType, "relationship type")

	rel := &model.EntityRelationship{
		SourceType:         string(entity.TypeDevice),
		SourceId:           device.ID,
		TargetType:         string(entity.TypeArea),
		TargetId:           1,
		TargetToken:        "area-1",
		RelationshipTypeId: relType.ID,
	}
	rel.Token = "rel1"
	mustCreate(t, db.WithContext(ctx), rel, "relationship")

	if scoped {
		ref := &model.EntityGroupFacetRef{FacetKey: "zone", MemberType: string(entity.TypeDevice),
			GroupId: 1, SelectorVersion: 1, GroupToken: "g"}
		mustCreate(t, db.WithContext(ctx), ref, "facet ref")
	}

	caches := &model.Caches{}
	cv := reflect.ValueOf(caches).Elem()
	cacheType := reflect.TypeOf((*messaging.Cache)(nil))
	for i := 0; i < cv.NumField(); i++ {
		f := cv.Type().Field(i)
		if f.Type != cacheType {
			continue
		}
		kv := msgtest.NewMemoryKV()
		rig.stores[f.Name] = kv
		cache := kv.NewCache()
		if wrap != nil {
			if w := wrap(f.Name, kv); w != nil {
				cache = w
			}
		}
		cv.Field(i).Set(reflect.ValueOf(cache))
	}
	if len(rig.stores) == 0 {
		t.Fatal("found no cache fields on model.Caches; the rig would count nothing")
	}
	rig.capi = model.NewCachedApi(api, caches)
	rig.rez = NewEventResolver(1, rig.capi, config.AuthModeDisabled, EventTimePolicy{},
		nil, nil, nil, nil, nil, nil)

	if err := db.Callback().Query().After("gorm:query").Register("test:count-sql", func(tx *gorm.DB) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.sql = append(rig.sql, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register sql counter: %v", err)
	}
	return rig
}

func mustCreate(t testing.TB, db *gorm.DB, row any, what string) {
	t.Helper()
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed %s: %v", what, err)
	}
}

// gets snapshots the Get count of every cache store, keyed by its Caches field name.
func (r *resolveRig) gets() map[string]int {
	out := make(map[string]int, len(r.stores))
	for name, kv := range r.stores {
		out[name] = kv.Gets
	}
	return out
}

// profileReads sums the Gets of every per-device-type cache: the reads of a device type's
// published profile, whatever the caches holding it are called.
func (r *resolveRig) profileReads() int {
	n := 0
	for name, kv := range r.stores {
		if strings.HasSuffix(name, "ByType") {
			n += kv.Gets
		}
	}
	return n
}

func (r *resolveRig) totalGets() int {
	n := 0
	for _, kv := range r.stores {
		n += kv.Gets
	}
	return n
}

func (r *resolveRig) resetCounters() {
	for _, kv := range r.stores {
		kv.Gets, kv.Puts, kv.Deletes = 0, 0, 0
	}
	r.mu.Lock()
	r.sql = nil
	r.mu.Unlock()
}

func (r *resolveRig) statements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sql...)
}

func (r *resolveRig) resolve(event *esmodel.UnresolvedEvent) *model.ResolvedEvent {
	r.t.Helper()
	results, reason, err := r.rez.ResolveEvent(r.ctx, event)
	if err != nil || reason != 0 {
		r.t.Fatalf("resolve %s: reason %d, err %v", event.EventType.String(), reason, err)
	}
	if len(results) != 1 {
		r.t.Fatalf("resolve %s: %d results, want 1", event.EventType.String(), len(results))
	}
	return results[0].Resolved
}

func tempEvent(value string) *esmodel.UnresolvedEvent {
	return &esmodel.UnresolvedEvent{
		Device:    "dev",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: map[string]string{"temp": value}}},
		},
	}
}

func devLocationEvent() *esmodel.UnresolvedEvent {
	lat, lon := "33.7490", "-84.3880"
	return &esmodel.UnresolvedEvent{
		Device:    "dev",
		EventType: esmodel.Location,
		Payload: &esmodel.UnresolvedLocationsPayload{
			Entries: []esmodel.UnresolvedLocationEntry{{Latitude: &lat, Longitude: &lon}},
		},
	}
}

// stampedUnit returns the unit stamped on the event's single measurement, or "" if none.
func stampedUnit(t testing.TB, resolved *model.ResolvedEvent) string {
	t.Helper()
	payload, ok := resolved.Payload.(*model.ResolvedMeasurementsPayload)
	if !ok || len(payload.Entries) != 1 || len(payload.Entries[0].Entries) != 1 {
		t.Fatalf("resolved payload is not one measurement: %#v", resolved.Payload)
	}
	if u := payload.Entries[0].Entries[0].Unit; u != nil {
		return *u
	}
	return ""
}

// A warm measurement event reads a device type's published profile from the cache ONCE,
// and costs four key-value reads in all: device, profile, tracked relationships and the
// tenant's scoped-groups gate.
//
// Before, the same event read the profile three times — its metric definitions once to
// validate and once to stamp classifiers, and its rule scope from a second cache — which
// was six reads per event and let the three reads straddle a publish.
func TestAWarmMeasurementEventCostsFourKvReads(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	resolved := rig.resolve(tempEvent("21"))

	// The guard that the counts below describe a WORKING warm read, not two empty ones:
	// the declared unit and the published version both came through.
	if got := stampedUnit(t, resolved); got != "Cel" {
		t.Fatalf("stamped unit = %q, want Cel: the warm read did not serve the metric definitions", got)
	}
	if resolved.ProfileVersionToken != "p@1" {
		t.Fatalf("profile version token = %q, want p@1", resolved.ProfileVersionToken)
	}

	if got := rig.profileReads(); got != 1 {
		t.Errorf("per-device-type profile reads = %d, want 1 (gets by cache: %v)", got, rig.gets())
	}
	if got := rig.totalGets(); got != 4 {
		t.Errorf("key-value reads per warm measurement event = %d, want 4 (gets by cache: %v)", got, rig.gets())
	}
	want := map[string]int{"DeviceByToken": 1, "ProfileResolutionByType": 1, "RelationshipsBySource": 1,
		"ScopedGroupsExist": 1, "MembershipsByEntity": 0}
	if got := rig.gets(); !reflect.DeepEqual(got, want) {
		t.Errorf("gets by cache = %v, want %v", got, want)
	}
	for name, kv := range rig.stores {
		if kv.Puts != 0 {
			t.Errorf("cache %s was written %d times on a warm event", name, kv.Puts)
		}
	}
	if stmts := rig.statements(); len(stmts) != 0 {
		t.Errorf("a warm event reached the database %d times: %v", len(stmts), stmts)
	}
}

// In a tenant with rule-scoped groups, the membership reads add one per target — the
// device plus each tracked anchor — on top of the same single profile read. Pinned so a
// later change cannot hide inside the +1+N term.
func TestAWarmMeasurementEventInARuleScopedTenantAddsOneReadPerTarget(t *testing.T) {
	rig := newCountingResolveRig(t, true, nil)
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	resolved := rig.resolve(tempEvent("21"))
	if got := stampedUnit(t, resolved); got != "Cel" {
		t.Fatalf("stamped unit = %q, want Cel", got)
	}

	if got := rig.stores["MembershipsByEntity"].Gets; got != 2 {
		t.Errorf("membership reads = %d, want 2 (the device and its one anchor)", got)
	}
	if got := rig.profileReads(); got != 1 {
		t.Errorf("per-device-type profile reads = %d, want 1 (gets by cache: %v)", got, rig.gets())
	}
	if got := rig.totalGets(); got != 6 {
		t.Errorf("key-value reads per warm scoped measurement event = %d, want 6 (gets by cache: %v)",
			got, rig.gets())
	}
}

// A location event never read metric definitions, and it still costs four reads: folding
// the definitions into the per-type entry does not add a read to the events that do not
// use them. It is a non-regression row, so it passes on either side of the fold.
func TestAWarmLocationEventStillCostsFourKvReads(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	rig.resolve(devLocationEvent())
	rig.resetCounters()

	resolved := rig.resolve(devLocationEvent())
	if resolved.ProfileVersionToken != "p@1" {
		t.Fatalf("profile version token = %q, want p@1", resolved.ProfileVersionToken)
	}
	if got := rig.profileReads(); got != 1 {
		t.Errorf("per-device-type profile reads = %d, want 1 (gets by cache: %v)", got, rig.gets())
	}
	if got := rig.totalGets(); got != 4 {
		t.Errorf("key-value reads per warm location event = %d, want 4 (gets by cache: %v)", got, rig.gets())
	}
}

// A COLD measurement event reads the profile's active-version pointer once. Two reads of
// it — one for the metric definitions, one for the rule scope — could straddle a publish
// on the database side exactly as two cache reads could, and give one event two versions.
func TestAColdMeasurementEventReadsTheProfileChainOnce(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	rig.resetCounters()

	resolved := rig.resolve(tempEvent("21"))
	if got := stampedUnit(t, resolved); got != "Cel" {
		t.Fatalf("stamped unit = %q, want Cel", got)
	}

	var profiles, versions, fences int
	for _, s := range rig.statements() {
		switch {
		case strings.Contains(s, "device_profile_versions"):
			versions++
		case strings.Contains(s, "device_profiles"):
			profiles++
		}
		if strings.Contains(s, "geo_fence_set_versions") {
			fences++
		}
	}
	if profiles != 1 {
		t.Errorf("reads of device_profiles on a cold event = %d, want 1: the active-version "+
			"pointer must be read once (statements: %v)", profiles, rig.statements())
	}
	if versions != 1 {
		t.Errorf("reads of device_profile_versions on a cold event = %d, want 1", versions)
	}
	if fences != 1 {
		t.Errorf("reads of geo_fence_set_versions on a cold event = %d, want 1", fences)
	}
}

// hookedKV runs a function after each Get of the store it wraps, and only after Get: a
// hook on Put would also fire during the unarmed warm-up, when the cache is populated, and
// shift every version the test expects.
type hookedKV struct {
	*msgtest.MemoryKV
	after func()
}

func (h *hookedKV) Get(key string) (nats.KeyValueEntry, error) {
	entry, err := h.MemoryKV.Get(key)
	h.after()
	return entry, err
}

// One event is validated, stamped and labelled from ONE published profile version, even
// when a publish lands between the resolver's reads.
//
// The hook publishes a new version, with a different unit on the metric, after every
// read of a per-device-type cache. If the resolver reads the profile more than once, the
// later reads see a later version, and the event carries one version's token with
// another's unit.
func TestAMeasurementIsValidatedAndStampedFromOneProfileVersion(t *testing.T) {
	units := []string{"Cel", "K", "[degF]", "W", "kW", "Pa"}
	var (
		armed, firing bool
		k             int
		rig           *resolveRig
	)
	hook := func() {
		if !armed || firing {
			return
		}
		firing = true
		defer func() { firing = false }()
		k++
		if k >= len(units) {
			rig.t.Fatalf("hook fired %d times; the resolver read the profile more often than any tree ever has", k)
		}
		if err := rig.db.WithContext(rig.ctx).Model(&model.MetricDefinition{}).
			Where("token = ?", "temp-def").
			Update("unit", sql.NullString{String: units[k], Valid: true}).Error; err != nil {
			rig.t.Fatalf("edit draft unit: %v", err)
		}
		if _, err := rig.capi.PublishDeviceProfile(rig.ctx, "p", nil, nil, "t"); err != nil {
			rig.t.Fatalf("publish from hook: %v", err)
		}
	}
	rig = newCountingResolveRig(t, false, func(field string, kv *msgtest.MemoryKV) *messaging.Cache {
		if !strings.HasSuffix(field, "ByType") {
			return nil
		}
		return messaging.NewCacheOver(&hookedKV{MemoryKV: kv, after: hook})
	})

	warm := rig.resolve(tempEvent("21"))
	if got := stampedUnit(t, warm); got != "Cel" || warm.ProfileVersionToken != "p@1" {
		t.Fatalf("warm-up resolved unit %q at %q, want Cel at p@1", got, warm.ProfileVersionToken)
	}

	armed = true
	resolved := rig.resolve(tempEvent("21"))
	armed = false

	if k < 1 {
		t.Fatal("the hook never fired, so no publish landed mid-resolution and the test proves nothing")
	}
	var profile model.DeviceProfile
	if err := rig.db.WithContext(rig.ctx).Where("token = ?", "p").First(&profile).Error; err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if int(profile.ActiveVersion.Int32) != 1+k {
		t.Fatalf("active version = %d after %d hooked publishes, want %d", profile.ActiveVersion.Int32, k, 1+k)
	}

	unit := stampedUnit(t, resolved)
	if resolved.ProfileVersionToken != "p@1" || unit != "Cel" {
		t.Errorf("the event carries unit %q with version token %q; want Cel with p@1 — the "+
			"version it was labelled with must be the version it was stamped from", unit, resolved.ProfileVersionToken)
	}
}

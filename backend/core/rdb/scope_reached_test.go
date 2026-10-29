// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// The fixture models of ReachedFromDevice: an area table beside the miniature graph of
// scope_ownership_test.go. The device model is grpDevice (scope_group_members_test.go), so
// the rule's nested device-visibility test exercises the group branch too.

type reachArea struct {
	gorm.Model
	TenantScoped
	Token string
}

var reachEdges = OwnershipEdges{
	Edge: &ownEdge{}, Type: &ownRelType{},
	SourceType: "source_type", SourceID: "source_id", SourceKind: "device", Row: "id",
	TargetType: "target_type", TargetToken: "target_token",
	EdgeType: "relationship_type_id", TypeID: "id", Tracked: "tracked",
	Groups: &GroupMembers{
		Kind: "group", Member: &grpMember{}, Group: &grpGroup{},
		MemberType: "entity_type", MemberID: "entity_id", MemberKind: "device",
		MemberGroup: "group_id", MemberVersion: "selector_version",
		GroupID: "id", GroupToken: "token", GroupVersion: "active_version", GroupEnabled: "enabled_at",
	},
}

func (reachArea) ScopedBy() ScopeRule {
	return ReachedFromDevice(reachEdges, &grpDevice{}, "area", "token")
}

type reachFixture struct{ db *gorm.DB }

// newReachFixture seeds tenant A. Devices: acme-pump (tracked edge to customer acme),
// globex-pump (to globex), north-pump (a member of the enabled group north), idle-pump.
// Areas and the edges that do or do not reach them:
//
//	shared      - tracked edges from acme-pump AND globex-pump
//	acme-only   - tracked, from acme-pump         globex-only - tracked, from globex-pump
//	untracked   - UNTRACKED edge from acme-pump
//	idle        - no edge at all
//	gone-edge   - acme-pump's tracked edge, soft-deleted
//	gone-device - a tracked edge from a soft-deleted acme device
//	via-asset   - a tracked edge from ASSET row 1 (the source type is not a device)
//	kind-mix    - a tracked edge from acme-pump to the ASSET "kind-mix" (target kind is not area)
//	forged-edge - an edge row in TENANT B naming acme-pump (only the edge's tenant clause)
//	crossed     - an edge in A from acme-pump whose type id is TENANT B's tracked type
//	foreign-dev - A's edges from a tenant-B DEVICE that A's edges also call acme's
//	by-group    - a tracked edge from north-pump (reached by a group scope)
//	second-hop  - a tracked edge from an asset that is itself assigned to customer acme
//
// and tenant B with the area "shared" (same token, another tenant), reached from a B device.
func newReachFixture(t *testing.T) reachFixture {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&grpDevice{}, &grpGroup{}, &grpMember{}, &ownRelType{}, &ownEdge{}, &reachArea{}); err != nil {
		t.Fatal(err)
	}
	ctxOf := func(tenant string) context.Context { return core.WithTenant(context.Background(), tenant) }
	create := func(tenant string, row any) {
		t.Helper()
		if err := db.WithContext(ctxOf(tenant)).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	device := func(tenant, token string) uint {
		d := &grpDevice{Token: token}
		create(tenant, d)
		return d.ID
	}
	types := map[string]uint{}
	typeOf := func(tenant, token string, tracked bool) uint {
		key := tenant + "/" + token
		if id, ok := types[key]; ok {
			return id
		}
		rt := &ownRelType{Token: token, Tracked: tracked}
		create(tenant, rt)
		types[key] = rt.ID
		return rt.ID
	}
	edge := func(tenant, source string, id uint, relType string, tracked bool, targetType, target string) *ownEdge {
		e := &ownEdge{SourceType: source, SourceId: id, TargetType: targetType, TargetToken: target,
			RelationshipTypeId: typeOf(tenant, relType, tracked)}
		create(tenant, e)
		return e
	}
	for _, a := range []string{"shared", "acme-only", "globex-only", "untracked", "idle", "gone-edge", "gone-device",
		"via-asset", "kind-mix", "forged-edge", "crossed", "foreign-dev", "by-group", "second-hop"} {
		create("A", &reachArea{Token: a})
	}
	create("B", &reachArea{Token: "shared"})

	acme, globex := device("A", "acme-pump"), device("A", "globex-pump")
	north := device("A", "north-pump")
	device("A", "idle-pump")
	edge("A", "device", acme, "assigned", true, "customer", "acme")
	edge("A", "device", globex, "assigned", true, "customer", "globex")
	edge("A", "device", acme, "assigned", true, "area", "shared")
	edge("A", "device", globex, "assigned", true, "area", "shared")
	edge("A", "device", acme, "assigned", true, "area", "acme-only")
	edge("A", "device", globex, "assigned", true, "area", "globex-only")
	edge("A", "device", acme, "monitors", false, "area", "untracked")
	deleted := edge("A", "device", acme, "assigned", true, "area", "gone-edge")
	if err := db.WithContext(ctxOf("A")).Delete(deleted).Error; err != nil {
		t.Fatal(err)
	}
	goneDev := device("A", "gone-pump")
	edge("A", "device", goneDev, "assigned", true, "customer", "acme")
	edge("A", "device", goneDev, "assigned", true, "area", "gone-device")
	if err := db.WithContext(ctxOf("A")).Delete(&grpDevice{Model: gorm.Model{ID: goneDev}}).Error; err != nil {
		t.Fatal(err)
	}
	// An asset's row id equal to acme-pump's: ids are shared across entity families, so only
	// the source-type clause keeps an asset's ownership from being read as the device's.
	edge("A", "asset", acme, "assigned", true, "customer", "acme")
	edge("A", "asset", acme, "assigned", true, "area", "via-asset")
	edge("A", "asset", acme, "assigned", true, "area", "second-hop")
	edge("A", "device", acme, "assigned", true, "asset", "kind-mix")
	edge("B", "device", acme, "assigned", true, "area", "forged-edge")
	bType := typeOf("B", "assigned", true)
	create("A", &ownEdge{SourceType: "device", SourceId: acme, TargetType: "area", TargetToken: "crossed", RelationshipTypeId: bType})
	foreign := device("B", "b-pump")
	edge("A", "device", foreign, "assigned", true, "customer", "acme")
	edge("A", "device", foreign, "assigned", true, "area", "foreign-dev")
	edge("B", "device", foreign, "assigned", true, "customer", "acme")
	edge("B", "device", foreign, "assigned", true, "area", "shared")

	g := &grpGroup{Token: "north"}
	g.ActiveVersion.Int32, g.ActiveVersion.Valid = 1, true
	g.EnabledAt.Time, g.EnabledAt.Valid = time.Now(), true
	create("A", g)
	create("A", &grpMember{EntityType: "device", EntityId: north, GroupId: g.ID, SelectorVersion: 1})
	edge("A", "device", north, "assigned", true, "area", "by-group")
	return reachFixture{db: db}
}

func reachNames(t *testing.T, db *gorm.DB, ctx context.Context) []string {
	t.Helper()
	var found []reachArea
	if err := db.WithContext(ctx).Find(&found).Error; err != nil {
		t.Fatalf("read areas: %v", err)
	}
	names := make([]string, 0, len(found))
	for _, a := range found {
		names = append(names, a.Token)
	}
	sort.Strings(names)
	return names
}

// 🔴 The rule, case by case. Each fixture area fails exactly one of the conditions, so
// each conjunct of the predicate has a case that goes red the moment it is dropped.
func TestAnAreaIsVisibleWhileADeviceTheCallerSeesHasATrackedEdgeToIt(t *testing.T) {
	f := newReachFixture(t)

	if got, want := reachNames(t, f.db, scopedCtx("A", "customer:acme")), []string{"acme-only", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's member saw %v, want %v", got, want)
	}
	if got, want := reachNames(t, f.db, scopedCtx("A", "customer:globex")), []string{"globex-only", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("globex's member saw %v, want %v", got, want)
	}
	// A group scope reaches through the Device rule's group branch, with no branch of its own.
	if got, want := reachNames(t, f.db, scopedCtx("A", "group:north")), []string{"by-group"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("north's member saw %v, want %v", got, want)
	}
	if got, want := reachNames(t, f.db, scopedCtx("A", "customer:acme", "group:north")),
		[]string{"acme-only", "by-group", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a two-anchor scope saw %v, want %v", got, want)
	}
	// Tenant B's acme sees tenant B's area, from B's own device.
	if got, want := reachNames(t, f.db, scopedCtx("B", "customer:acme")), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tenant B's member saw %v, want %v", got, want)
	}
	// No anchors is nothing, never everything; an anchor naming nobody is nothing.
	if got := reachNames(t, f.db, scopedCtx("A")); len(got) != 0 {
		t.Fatalf("an anchorless scope saw %v", got)
	}
	if got := reachNames(t, f.db, scopedCtx("A", "customer:nobody")); len(got) != 0 {
		t.Fatalf("an unknown customer's member saw %v", got)
	}
	// Positive control: unscoped, tenant A sees every area it has (and none of B's).
	if got := reachNames(t, f.db, unscopedCtx("A")); len(got) != 14 {
		t.Fatalf("the unscoped control saw %d areas: %v", len(got), got)
	}
	// A by-token read outside visibility finds nothing, as a missing row does.
	var a reachArea
	if err := f.db.WithContext(scopedCtx("A", "customer:acme")).Where("token = ?", "globex-only").First(&a).Error; err != gorm.ErrRecordNotFound {
		t.Fatalf("acme's by-token read of globex-only: %v", err)
	}
}

// Visibility is CURRENT: the next read after a device moves or is sold reflects it.
func TestAnAreaFollowsTheDeviceThatReachesIt(t *testing.T) {
	f := newReachFixture(t)
	acme := scopedCtx("A", "customer:acme")
	ctxA := core.WithTenant(context.Background(), "A")

	// The device leaves acme-only: the area disappears from acme's list at once.
	if err := f.db.WithContext(ctxA).Where("target_token = ? AND target_type = ?", "acme-only", "area").Delete(&ownEdge{}).Error; err != nil {
		t.Fatal(err)
	}
	if got, want := reachNames(t, f.db, acme), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the device left acme-only: %v, want %v", got, want)
	}
	// The device is sold: its customer edge now names globex, so acme no longer sees the
	// device, and with it every area that device was the only way to.
	if err := f.db.WithContext(ctxA).Model(&ownEdge{}).
		Where("source_type = ? AND target_type = ? AND target_token = ?", "device", "customer", "acme").
		Update("target_token", "globex").Error; err != nil {
		t.Fatal(err)
	}
	if got := reachNames(t, f.db, acme); len(got) != 0 {
		t.Fatalf("after the sale acme still saw %v", got)
	}
	if got, want := reachNames(t, f.db, scopedCtx("A", "customer:globex")), []string{"globex-only", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the sale globex saw %v, want %v", got, want)
	}
}

// 🔴 The predicate carries the tenant on the edge, the type AND the device table, beside
// the area's own. Rendered, so the clauses are seen and not inferred.
func TestTheReachedFromDevicePredicateCarriesTheTenantOnEveryTable(t *testing.T) {
	f := newReachFixture(t)
	stmt := f.db.Session(&gorm.Session{DryRun: true}).WithContext(scopedCtx("A", "customer:acme")).
		Find(&[]reachArea{}).Statement
	if stmt.Error != nil {
		t.Fatal(stmt.Error)
	}
	sql := stmt.SQL.String()
	for _, want := range []string{
		"`reach_areas`.`tenant_id` = ?",
		"`dc_reach_edge`.`tenant_id` = ?",
		"`dc_reach_type`.`tenant_id` = ?",
		"`dc_reach_device`.`tenant_id` = ?",
		"`dc_reach_type`.`tracked` = ?",
		"`dc_reach_edge`.`target_token` = `reach_areas`.`token`",
		"`dc_reach_device`.`id` = `dc_reach_edge`.`source_id`",
		"`dc_scope_edge`.`source_id` = `dc_reach_device`.`id`",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the predicate lacks %s: %s", want, sql)
		}
	}
}

// A scoped caller writes no area: create, update and delete are all refused.
func TestAReachedModelRefusesScopedWrites(t *testing.T) {
	f := newReachFixture(t)
	ctx := scopedCtx("A", "customer:acme")
	if err := f.db.WithContext(ctx).Create(&reachArea{Token: "new"}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped create: %v", err)
	}
	if err := f.db.WithContext(ctx).Model(&reachArea{}).Where("token = ?", "shared").Update("token", "x").Error; !isScopeRefusal(err) {
		t.Errorf("a scoped update: %v", err)
	}
	if err := f.db.WithContext(ctx).Where("token = ?", "shared").Delete(&reachArea{}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped delete: %v", err)
	}
	// Unscoped control: the same write succeeds.
	if err := f.db.WithContext(unscopedCtx("A")).Create(&reachArea{Token: "new"}).Error; err != nil {
		t.Errorf("the unscoped control create: %v", err)
	}
}

// A rule naming a model that is not tenant-scoped, or a column that is not there, refuses
// the read rather than running it unfiltered.
func TestAMisdeclaredReachedRuleRefusesTheRead(t *testing.T) {
	f := newReachFixture(t)
	if err := f.db.AutoMigrate(&reachBadToken{}, &reachNoKind{}); err != nil {
		t.Fatal(err)
	}
	var a []reachBadToken
	if err := f.db.WithContext(scopedCtx("A", "customer:acme")).Find(&a).Error; !isScopeRefusal(err) {
		t.Errorf("a rule naming no token column: %v", err)
	}
	var b []reachNoKind
	if err := f.db.WithContext(scopedCtx("A", "customer:acme")).Find(&b).Error; !isScopeRefusal(err) {
		t.Errorf("a rule with no kind: %v", err)
	}
}

type reachBadToken struct {
	gorm.Model
	TenantScoped
	Token string
}

func (reachBadToken) ScopedBy() ScopeRule {
	return ReachedFromDevice(reachEdges, &grpDevice{}, "area", "no_such_column")
}

type reachNoKind struct {
	gorm.Model
	TenantScoped
	Token string
}

func (reachNoKind) ScopedBy() ScopeRule {
	return ReachedFromDevice(reachEdges, &grpDevice{}, "", "token")
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ---- fixture models: a device reached through a tracked edge OR a group's members ------

type grpDevice struct {
	gorm.Model
	TenantScoped
	Token string
}

type grpGroup struct {
	gorm.Model
	TenantScoped
	Token         string
	ActiveVersion sql.NullInt32
	EnabledAt     sql.NullTime
}

type grpMember struct {
	gorm.Model
	TenantScoped
	EntityType      string
	EntityId        uint
	GroupId         uint
	SelectorVersion int32
}

func (grpDevice) ScopedBy() ScopeRule {
	return DeviceEdge(OwnershipEdges{
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
	})
}

type grpFixture struct {
	db      *gorm.DB
	devices map[string]uint
}

// newGroupMembersFixture seeds tenant A with the group "north" (enabled, active version 2),
// the group "draft" (NOT enabled, active version 1) and these devices:
//
//	north-pump    — a member of north@2
//	stale-pump    — a member of north@1 only (a version no longer in force)
//	draft-pump    — a member of draft@1 (a group that is not enabled)
//	gone-pump     — a member of north@2, the row soft-deleted
//	acme-pump     — a tracked edge to customer acme, in no group
//	area-row      — an AREA member of north@2 whose id is idle-pump's (only the member
//	                type keeps an area's membership from being read as the device's)
//	idle-pump     — nothing at all
//	retired-pump  — a member of "retired"@1, a group that is enabled but soft-deleted
//
// and tenant B with the group "north" (enabled, version 1) and b-pump in it, plus a forged
// tenant-B membership row naming tenant A's idle-pump in B's north.
func newGroupMembersFixture(t *testing.T) grpFixture {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&grpDevice{}, &grpGroup{}, &grpMember{}, &ownRelType{}, &ownEdge{}); err != nil {
		t.Fatal(err)
	}
	f := grpFixture{db: db, devices: map[string]uint{}}
	ctxOf := func(tenant string) context.Context { return core.WithTenant(context.Background(), tenant) }
	create := func(tenant string, row any) {
		t.Helper()
		if err := db.WithContext(ctxOf(tenant)).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	group := func(tenant, token string, version int32, enabled bool) uint {
		g := &grpGroup{Token: token, ActiveVersion: sql.NullInt32{Int32: version, Valid: true}}
		if enabled {
			g.EnabledAt = sql.NullTime{Time: time.Now(), Valid: true}
		}
		create(tenant, g)
		return g.ID
	}
	device := func(tenant, token string) uint {
		d := &grpDevice{Token: token}
		create(tenant, d)
		f.devices[token] = d.ID
		return d.ID
	}
	member := func(tenant, etype string, id, groupId uint, version int32) *grpMember {
		m := &grpMember{EntityType: etype, EntityId: id, GroupId: groupId, SelectorVersion: version}
		create(tenant, m)
		return m
	}

	north := group("A", "north", 2, true)
	draft := group("A", "draft", 1, false)
	member("A", "device", device("A", "north-pump"), north, 2)
	member("A", "device", device("A", "stale-pump"), north, 1)
	member("A", "device", device("A", "draft-pump"), draft, 1)
	gone := member("A", "device", device("A", "gone-pump"), north, 2)
	if err := db.WithContext(ctxOf("A")).Delete(gone).Error; err != nil {
		t.Fatal(err)
	}
	acme := device("A", "acme-pump")
	rt := &ownRelType{Token: "assigned", Tracked: true}
	create("A", rt)
	create("A", &ownEdge{SourceType: "device", SourceId: acme, TargetType: "customer", TargetToken: "acme", RelationshipTypeId: rt.ID})
	idle := device("A", "idle-pump")
	member("A", "area", idle, north, 2)
	retired := group("A", "retired", 1, true)
	member("A", "device", device("A", "retired-pump"), retired, 1)
	if err := db.WithContext(ctxOf("A")).Delete(&grpGroup{Model: gorm.Model{ID: retired}}).Error; err != nil {
		t.Fatal(err)
	}

	bNorth := group("B", "north", 1, true)
	member("B", "device", device("B", "b-pump"), bNorth, 1)
	// 🔴 FORGED: tenant B's membership row naming tenant A's idle-pump in B's north. Only
	// the member table's own tenant clause keeps it from reaching across.
	member("B", "device", idle, bNorth, 1)
	return f
}

func grpDeviceNames(t *testing.T, db *gorm.DB, ctx context.Context) []string {
	t.Helper()
	var found []grpDevice
	if err := db.WithContext(ctx).Order("token").Find(&found).Error; err != nil {
		t.Fatalf("read devices: %v", err)
	}
	names := make([]string, 0, len(found))
	for _, d := range found {
		names = append(names, d.Token)
	}
	return names
}

// TestADeviceIsVisibleThroughAGroupItIsAMemberOf pins the group branch: a live member row,
// of a device, in the caller's tenant (member AND group), of a group whose token the scope
// names, at the version the group holds now, while the group is enabled. Each fixture
// device fails exactly one of those conditions; the edge branch still reaches what it did.
func TestADeviceIsVisibleThroughAGroupItIsAMemberOf(t *testing.T) {
	f := newGroupMembersFixture(t)

	if got := grpDeviceNames(t, f.db, scopedCtx("A", "group:north")); !reflect.DeepEqual(got, []string{"north-pump"}) {
		t.Fatalf("north's member saw %v, want [north-pump]", got)
	}
	if got := grpDeviceNames(t, f.db, scopedCtx("A", "group:draft")); len(got) != 0 {
		t.Fatalf("a group that is not enabled reached %v", got)
	}
	// A deleted group confers nothing, whatever rows still name it (device-management deletes
	// hard, so this is the rule's own promise, not a path it takes today).
	if got := grpDeviceNames(t, f.db, scopedCtx("A", "group:retired")); len(got) != 0 {
		t.Fatalf("a deleted group reached %v", got)
	}
	// Both branches at once: a customer and a group.
	if got := grpDeviceNames(t, f.db, scopedCtx("A", "customer:acme", "group:north")); !reflect.DeepEqual(got,
		[]string{"acme-pump", "north-pump"}) {
		t.Fatalf("a customer-and-group scope saw %v", got)
	}
	// The group branch serves only its own kind: a customer anchor naming the group's
	// token reaches none of its members.
	if got := grpDeviceNames(t, f.db, scopedCtx("A", "customer:north")); len(got) != 0 {
		t.Fatalf("a customer anchor reached group members: %v", got)
	}
	if got := grpDeviceNames(t, f.db, scopedCtx("B", "group:north")); !reflect.DeepEqual(got, []string{"b-pump"}) {
		t.Fatalf("tenant B's north member saw %v", got)
	}
	// Negative control: unscoped, tenant A sees every one of its devices.
	if got := grpDeviceNames(t, f.db, unscopedCtx("A")); len(got) != 7 {
		t.Fatalf("the unscoped control saw %d devices: %v", len(got), got)
	}
	// A by-token read outside the scope finds nothing, as a missing one does.
	var d grpDevice
	if err := f.db.WithContext(scopedCtx("A", "group:north")).Where("token = ?", "stale-pump").First(&d).Error; err != gorm.ErrRecordNotFound {
		t.Fatalf("north's by-token read of stale-pump: %v", err)
	}
}

// The group branch carries the tenant on both of its tables, and binds every token.
func TestTheGroupBranchCarriesTheTenantOnBothTables(t *testing.T) {
	f := newGroupMembersFixture(t)
	stmt := f.db.WithContext(scopedCtx("A", "group:north'--")).Session(&gorm.Session{DryRun: true}).
		Find(&[]grpDevice{}).Statement
	sql := stmt.SQL.String()
	for _, want := range []string{"dc_scope_member", "dc_scope_group", "IS NOT NULL"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the predicate lacks %q: %s", want, sql)
		}
	}
	if strings.Contains(sql, "north'") {
		t.Fatalf("a group token was spliced rather than bound: %s", sql)
	}
	tenants := 0
	for _, v := range stmt.Vars {
		if v == "A" {
			tenants++
		}
	}
	// The statement's own tenant, the edge and type tables', and the member and group tables'.
	if tenants < 5 {
		t.Fatalf("the tenant is bound %d times, want at least 5: %s %v", tenants, sql, stmt.Vars)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ---- fixture models: a miniature relationship graph ----------------------------------

type ownDevice struct {
	gorm.Model
	TenantScoped
	Token string
}

type ownRelType struct {
	gorm.Model
	TenantScoped
	Token   string
	Tracked bool
}

type ownEdge struct {
	gorm.Model
	TenantScoped
	SourceType         string
	SourceId           uint
	TargetType         string
	TargetToken        string
	RelationshipTypeId uint
}

func (ownDevice) ScopedBy() ScopeRule {
	return DeviceEdge(OwnershipEdges{
		Edge: &ownEdge{}, Type: &ownRelType{},
		SourceType: "source_type", SourceID: "source_id", SourceKind: "device", Row: "id",
		TargetType: "target_type", TargetToken: "target_token",
		EdgeType: "relationship_type_id", TypeID: "id", Tracked: "tracked",
	})
}

// ownCustomer is the entity a customer anchor names.
type ownCustomer struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Token string
}

func (ownCustomer) ScopedBy() ScopeRule { return NamedAnchor("customer", "token") }

// ownAnchorRow is a row that IS an anchor (an event's anchor set row).
type ownAnchorRow struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	AnchorType  string
	AnchorToken string
}

func (ownAnchorRow) ScopedBy() ScopeRule { return OwnAnchor("anchor_type", "anchor_token") }

type ownFixture struct {
	db      *gorm.DB
	devices map[string]uint
}

// newOwnershipFixture seeds tenant A with:
//
//	acme-pump    — tracked "assigned" edge to customer acme
//	globex-pump  — tracked "assigned" edge to customer globex
//	leased-pump  — tracked "leased" edge (a caller-defined tracked type) to customer acme
//	member-pump  — UNTRACKED "member" edge to customer acme
//	gone-pump    — tracked edge to acme, soft-deleted
//	asset-pump   — tracked edge to an ASSET whose token is "acme"
//	idle-pump    — no edge at all
//
// and tenant B with b-pump, tracked edge to customer acme (same anchor, another tenant).
func newOwnershipFixture(t *testing.T) ownFixture {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&ownDevice{}, &ownRelType{}, &ownEdge{}, &ownCustomer{}, &ownAnchorRow{}); err != nil {
		t.Fatal(err)
	}
	f := ownFixture{db: db, devices: map[string]uint{}}
	types := map[string]uint{}
	typeOf := func(tenant, token string, tracked bool) uint {
		key := tenant + "/" + token
		if id, ok := types[key]; ok {
			return id
		}
		rt := &ownRelType{Token: token, Tracked: tracked}
		if err := db.WithContext(core.WithTenant(context.Background(), tenant)).Create(rt).Error; err != nil {
			t.Fatal(err)
		}
		types[key] = rt.ID
		return rt.ID
	}
	add := func(tenant, name string, edges ...func(ctx context.Context, id uint)) {
		ctx := core.WithTenant(context.Background(), tenant)
		d := &ownDevice{Token: name}
		if err := db.WithContext(ctx).Create(d).Error; err != nil {
			t.Fatal(err)
		}
		f.devices[name] = d.ID
		for _, e := range edges {
			e(ctx, d.ID)
		}
	}
	edge := func(relType, targetType, target string, tracked, deleted bool) func(context.Context, uint) {
		return func(ctx context.Context, id uint) {
			tenant, _ := core.TenantFromContext(ctx)
			e := &ownEdge{SourceType: "device", SourceId: id, TargetType: targetType, TargetToken: target,
				RelationshipTypeId: typeOf(tenant, relType, tracked)}
			if err := db.WithContext(ctx).Create(e).Error; err != nil {
				t.Fatal(err)
			}
			if deleted {
				if err := db.WithContext(ctx).Delete(e).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	add("A", "acme-pump", edge("assigned", "customer", "acme", true, false))
	add("A", "globex-pump", edge("assigned", "customer", "globex", true, false))
	add("A", "leased-pump", edge("leased", "customer", "acme", true, false))
	add("A", "member-pump", edge("member", "customer", "acme", false, false))
	add("A", "gone-pump", edge("assigned", "customer", "acme", true, true))
	add("A", "asset-pump", edge("assigned", "asset", "acme", true, false))
	add("A", "idle-pump")
	add("B", "b-pump", edge("assigned", "customer", "acme", true, false))

	// 🔴 FORGED: tenant B's edge table naming tenant A's globex-pump as owned by acme,
	// with tenant B's (tracked) type. Row ids are global, so an honest edge never names
	// another tenant's device; this stands in for a corrupt or hostile row, and only the
	// edge table's own tenant clause keeps it from handing acme's member globex's pump.
	edge("assigned", "customer", "acme", true, false)(core.WithTenant(context.Background(), "B"), f.devices["globex-pump"])
	// 🔴 FORGED, the other table: an edge in tenant A whose type id is TENANT B's tracked
	// type. Only the type table's own tenant clause keeps another tenant's "tracked" from
	// deciding ownership here.
	bType := typeOf("B", "assigned", true)
	ctxA := core.WithTenant(context.Background(), "A")
	add("A", "crossed-type-pump")
	if err := db.WithContext(ctxA).Create(&ownEdge{SourceType: "device", SourceId: f.devices["crossed-type-pump"],
		TargetType: "customer", TargetToken: "acme", RelationshipTypeId: bType}).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func ownDeviceNames(t *testing.T, db *gorm.DB, ctx context.Context) []string {
	t.Helper()
	var found []ownDevice
	if err := db.WithContext(ctx).Find(&found).Error; err != nil {
		t.Fatalf("read devices: %v", err)
	}
	names := make([]string, 0, len(found))
	for _, d := range found {
		names = append(names, d.Token)
	}
	sort.Strings(names)
	return names
}

// TestADeviceIsVisibleThroughATrackedEdgeToAScopeCustomer pins the ownership rule: an edge
// of a TRACKED type, live, in the caller's tenant (edge AND type), from the device, to an
// entity of the anchor's kind whose token the scope names. Each fixture device fails
// exactly one of those conditions.
func TestADeviceIsVisibleThroughATrackedEdgeToAScopeCustomer(t *testing.T) {
	f := newOwnershipFixture(t)

	got := ownDeviceNames(t, f.db, scopedCtx("A", "customer:acme"))
	if want := []string{"acme-pump", "leased-pump"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's member saw %v, want %v", got, want)
	}
	if got := ownDeviceNames(t, f.db, scopedCtx("A", "customer:globex")); !reflect.DeepEqual(got, []string{"globex-pump"}) {
		t.Fatalf("globex's member saw %v", got)
	}
	if got := ownDeviceNames(t, f.db, scopedCtx("A", "customer:acme", "customer:globex")); !reflect.DeepEqual(got,
		[]string{"acme-pump", "globex-pump", "leased-pump"}) {
		t.Fatalf("a two-customer scope saw %v", got)
	}
	if got := ownDeviceNames(t, f.db, scopedCtx("B", "customer:acme")); !reflect.DeepEqual(got, []string{"b-pump"}) {
		t.Fatalf("tenant B's acme member saw %v", got)
	}
	// A group anchor names no customer edge (group scope arrives with its own branch).
	if got := ownDeviceNames(t, f.db, scopedCtx("A", "group:acme")); len(got) != 0 {
		t.Fatalf("a group anchor matched customer edges: %v", got)
	}
	// 🔴 No anchors is nothing, never everything.
	if got := ownDeviceNames(t, f.db, scopedCtx("A")); len(got) != 0 {
		t.Fatalf("an anchorless scope saw %v", got)
	}
	// Negative control: unscoped, tenant A sees every one of its devices.
	if got := ownDeviceNames(t, f.db, unscopedCtx("A")); len(got) != 8 {
		t.Fatalf("the unscoped control saw %d devices: %v", len(got), got)
	}
	// A by-token read of a device outside the scope finds nothing, as a missing one does.
	var d ownDevice
	err := f.db.WithContext(scopedCtx("A", "customer:acme")).Where("token = ?", "globex-pump").First(&d).Error
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("acme's by-token read of globex-pump: %v", err)
	}
}

// 🔴 The predicate carries the tenant on the edge table AND on the type table, beside
// the row's own tenant predicate. Rendered, so the clauses are seen and not inferred.
func TestTheDeviceEdgePredicateCarriesTheTenantOnBothGraphTables(t *testing.T) {
	f := newOwnershipFixture(t)
	stmt := f.db.Session(&gorm.Session{DryRun: true}).WithContext(scopedCtx("A", "customer:acme")).
		Find(&[]ownDevice{}).Statement
	if stmt.Error != nil {
		t.Fatal(stmt.Error)
	}
	sql := stmt.SQL.String()
	for _, want := range []string{
		"`own_devices`.`tenant_id` = ?",
		"`dc_scope_edge`.`tenant_id` = ?",
		"`dc_scope_type`.`tenant_id` = ?",
		"`dc_scope_edge`.`deleted_at` IS NULL",
		"`dc_scope_type`.`tracked` = ?",
		"`dc_scope_edge`.`source_id` = `own_devices`.`id`",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the predicate lacks %s: %s", want, sql)
		}
	}
	tenants := 0
	for _, v := range stmt.Vars {
		if v == "A" {
			tenants++
		}
	}
	if tenants != 3 {
		t.Errorf("want the tenant bound three times (device, edge, type), got %d in %v", tenants, stmt.Vars)
	}
}

// A scoped caller writes no device: create, update and delete are all refused.
func TestADeviceEdgeModelRefusesScopedWrites(t *testing.T) {
	f := newOwnershipFixture(t)
	ctx := scopedCtx("A", "customer:acme")
	if err := f.db.WithContext(ctx).Create(&ownDevice{Token: "new"}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped create: %v", err)
	}
	if err := f.db.WithContext(ctx).Model(&ownDevice{}).Where("token = ?", "acme-pump").
		Update("token", "renamed").Error; !isScopeRefusal(err) {
		t.Errorf("a scoped update: %v", err)
	}
	if err := f.db.WithContext(ctx).Where("token = ?", "acme-pump").Delete(&ownDevice{}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped delete: %v", err)
	}
}

// NamedAnchor: a customer member reads its own customer record and no other.
func TestANamedAnchorModelShowsOnlyTheEntitiesTheScopeNames(t *testing.T) {
	f := newOwnershipFixture(t)
	for _, c := range []struct{ tenant, token string }{{"A", "acme"}, {"A", "globex"}, {"B", "acme"}} {
		if err := f.db.WithContext(core.WithTenant(context.Background(), c.tenant)).
			Create(&ownCustomer{Token: c.token}).Error; err != nil {
			t.Fatal(err)
		}
	}
	read := func(ctx context.Context) []string {
		var found []ownCustomer
		if err := f.db.WithContext(ctx).Find(&found).Error; err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, c := range found {
			out = append(out, c.TenantId+"/"+c.Token)
		}
		sort.Strings(out)
		return out
	}
	if got := read(scopedCtx("A", "customer:acme")); !reflect.DeepEqual(got, []string{"A/acme"}) {
		t.Fatalf("acme's member read customers %v", got)
	}
	// An anchor of another kind names no customer: nothing, never everything.
	if got := read(scopedCtx("A", "group:acme")); len(got) != 0 {
		t.Fatalf("a group anchor read customers %v", got)
	}
	if got := read(unscopedCtx("A")); !reflect.DeepEqual(got, []string{"A/acme", "A/globex"}) {
		t.Fatalf("the unscoped control read %v", got)
	}
	if err := f.db.WithContext(scopedCtx("A", "customer:acme")).Model(&ownCustomer{}).
		Where("token = ?", "acme").Update("token", "x").Error; !isScopeRefusal(err) {
		t.Fatalf("a scoped update of its own customer: %v", err)
	}
}

// OwnAnchor: reading an anchor set shows only the scope's own anchors, so a device two
// customers own does not tell either one about the other.
func TestAnOwnAnchorModelShowsOnlyTheScopesAnchors(t *testing.T) {
	f := newOwnershipFixture(t)
	ctxA := core.WithTenant(context.Background(), "A")
	for _, a := range [][2]string{{"customer", "acme"}, {"customer", "globex"}, {"asset", "acme"}, {"group", "acme"}} {
		if err := f.db.WithContext(ctxA).Create(&ownAnchorRow{AnchorType: a[0], AnchorToken: a[1]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	read := func(ctx context.Context) []string {
		var found []ownAnchorRow
		if err := f.db.WithContext(ctx).Find(&found).Error; err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range found {
			out = append(out, r.AnchorType+":"+r.AnchorToken)
		}
		sort.Strings(out)
		return out
	}
	if got := read(scopedCtx("A", "customer:acme")); !reflect.DeepEqual(got, []string{"customer:acme"}) {
		t.Fatalf("acme's member read anchors %v", got)
	}
	if got := read(scopedCtx("A", "customer:acme", "group:acme")); !reflect.DeepEqual(got, []string{"customer:acme", "group:acme"}) {
		t.Fatalf("a two-anchor scope read %v", got)
	}
	if got := read(scopedCtx("A")); len(got) != 0 {
		t.Fatalf("an anchorless scope read %v", got)
	}
	if got := read(unscopedCtx("A")); len(got) != 4 {
		t.Fatalf("the unscoped control read %v", got)
	}
}

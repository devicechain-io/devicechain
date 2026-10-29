// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ownEdges is the fixture graph (scope_ownership_test.go) with the type's token column
// named, which EdgeTo needs and DeviceEdge does not.
var ownEdges = OwnershipEdges{
	Edge: &ownEdge{}, Type: &ownRelType{},
	SourceType: "source_type", SourceID: "source_id", SourceKind: "device", Row: "id",
	TargetType: "target_type", TargetToken: "target_token",
	EdgeType: "relationship_type_id", TypeID: "id", Tracked: "tracked", TypeToken: "token",
}

func edgeToNames(t *testing.T, db *gorm.DB, ctx context.Context, target EdgeTarget) []string {
	t.Helper()
	var found []ownDevice
	if err := db.WithContext(ctx).Model(&ownDevice{}).Scopes(EdgeTo(ownEdges, target)).Find(&found).Error; err != nil {
		t.Fatalf("read devices by edge to %+v: %v", target, err)
	}
	names := make([]string, 0, len(found))
	for _, d := range found {
		names = append(names, d.Token)
	}
	sort.Strings(names)
	return names
}

// TestEdgeToMatchesOneTypeToOneTarget pins the filter against the ownership fixture, in
// which every device fails exactly one condition: an edge of THE named type (by token, in
// the tenant), live, from a DEVICE, to an entity of the named KIND and token.
func TestEdgeToMatchesOneTypeToOneTarget(t *testing.T) {
	f := newOwnershipFixture(t)
	a := unscopedCtx("A")
	for _, tc := range []struct {
		target EdgeTarget
		want   []string
	}{
		// Not leased-pump (another type), member-pump, gone-pump (deleted edge), asset-pump
		// (an asset named acme), crossed-type-pump (tenant B's type id), idle-pump (an
		// asset's edge on its row id) or b-pump (tenant B).
		{EdgeTarget{Kind: "customer", Token: "acme"}, []string{"acme-pump", "leased-pump", "member-pump"}},
		{EdgeTarget{Kind: "customer", Token: "acme", TrackedOnly: true}, []string{"acme-pump", "leased-pump"}},
		{EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned"}, []string{"acme-pump"}},
		{EdgeTarget{Kind: "customer", Token: "acme", Type: "leased"}, []string{"leased-pump"}},
		// An untracked type matches unless the read asks for ownership only.
		{EdgeTarget{Kind: "customer", Token: "acme", Type: "member"}, []string{"member-pump"}},
		{EdgeTarget{Kind: "customer", Token: "acme", Type: "member", TrackedOnly: true}, []string{}},
		{EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned", TrackedOnly: true}, []string{"acme-pump"}},
		{EdgeTarget{Kind: "customer", Token: "globex", Type: "assigned"}, []string{"globex-pump"}},
		{EdgeTarget{Kind: "asset", Token: "acme", Type: "assigned"}, []string{"asset-pump"}},
		{EdgeTarget{Kind: "customer", Token: "nobody", Type: "assigned"}, []string{}},
		{EdgeTarget{Kind: "customer", Token: "acme", Type: "no-such-type"}, []string{}},
	} {
		if got := edgeToNames(t, f.db, a, tc.target); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.target, got, tc.want)
		}
	}
	// Tenant B: its own acme pump, through its own type.
	if got := edgeToNames(t, f.db, unscopedCtx("B"), EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned"}); !reflect.DeepEqual(got, []string{"b-pump"}) {
		t.Errorf("tenant B: %v", got)
	}
}

// TestEdgeToNarrowsAScopedReadAndNeverWidensIt: the device's own scope rule still applies
// beside the filter, so a scoped caller filtering by a customer outside its scope reads
// nothing through it here (a device SHARED by two customers is why the resolver refuses
// that request before it reaches the store — see EdgeTo).
func TestEdgeToNarrowsAScopedReadAndNeverWidensIt(t *testing.T) {
	f := newOwnershipFixture(t)
	acme := scopedCtx("A", "customer:acme")
	if got := edgeToNames(t, f.db, acme, EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned", TrackedOnly: true}); !reflect.DeepEqual(got, []string{"acme-pump"}) {
		t.Errorf("acme's member, its own customer: %v", got)
	}
	if got := edgeToNames(t, f.db, acme, EdgeTarget{Kind: "customer", Token: "globex", Type: "assigned"}); len(got) != 0 {
		t.Errorf("acme's member read globex's devices through the filter: %v", got)
	}
}

// 🔴 The filter carries the tenant on the edge AND the type table, bound — rendered, so the
// clauses are seen rather than inferred — and the type token and the target are bound too.
func TestTheEdgeFilterCarriesTheTenantOnBothGraphTables(t *testing.T) {
	f := newOwnershipFixture(t)
	stmt := f.db.Session(&gorm.Session{DryRun: true}).WithContext(unscopedCtx("A")).Model(&ownDevice{}).
		Scopes(EdgeTo(ownEdges, EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned"})).
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
		"`dc_scope_type`.`token` = ?",
		"`dc_scope_edge`.`source_id` = `own_devices`.`id`",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the filter lacks %s: %s", want, sql)
		}
	}
	if strings.Contains(sql, "assigned") || strings.Contains(sql, "acme") {
		t.Errorf("a value was spliced rather than bound: %s", sql)
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

// A read with no tenant, or a filter missing a part, is refused rather than run unfiltered.
func TestEdgeToRefusesWhatItCannotBuild(t *testing.T) {
	f := newOwnershipFixture(t)
	var found []ownDevice
	err := f.db.WithContext(context.Background()).Model(&ownDevice{}).
		Scopes(EdgeTo(ownEdges, EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned"})).Find(&found).Error
	if !errors.Is(err, core.ErrNoTenant) {
		t.Errorf("no tenant: %v", err)
	}
	for _, target := range []EdgeTarget{
		{Token: "acme", Type: "assigned"},
		{Kind: "customer", Type: "assigned"},
	} {
		err := f.db.WithContext(unscopedCtx("A")).Model(&ownDevice{}).Scopes(EdgeTo(ownEdges, target)).Find(&found).Error
		if err == nil {
			t.Errorf("%+v: accepted", target)
		}
	}
	noToken := ownEdges
	noToken.TypeToken = ""
	if err := f.db.WithContext(unscopedCtx("A")).Model(&ownDevice{}).
		Scopes(EdgeTo(noToken, EdgeTarget{Kind: "customer", Token: "acme", Type: "assigned"})).Find(&found).Error; err == nil {
		t.Error("a graph naming no type-token column was accepted")
	}
}

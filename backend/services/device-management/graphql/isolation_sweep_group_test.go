// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newGroupScopeFixture is the scope fixture with one more way in: the access-scope group
// "north", a published dynamic group of devices selecting region == "north", which holds
// acme-pump and not globex-pump. A member limited to group:north must reach acme-pump
// through the group alone — it is limited to no customer.
func newGroupScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	// A group write opens a transaction while the pool already holds a connection, so every
	// connection must see the same tables: the shared named in-memory database.
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	f := seedScopeFixture(t, db, &model.EntityGroup{}, &model.IssuedGroupToken{}, &model.EntityGroupVersion{},
		&model.EntityGroupMembership{}, &model.EntityGroupFacetRef{}, &model.DetectionRuleScopeRef{},
		&model.DeviceConfigurationRevision{}, &model.DeviceConfigurationState{})
	ctx := f.operator()
	north := "north"
	if _, err := f.api.SetEntityAttribute(ctx, &model.EntityAttributeSetRequest{
		EntityType: "device", Entity: "acme-pump", Scope: string(model.AttributeScopeShared),
		AttrKey: "region", ValueType: string(model.AttributeValueString), Value: &north}); err != nil {
		t.Fatalf("region: %v", err)
	}
	dynamic, selector := "dynamic", `attr["region"] == "north"`
	if _, err := f.api.CreateEntityGroup(ctx, &model.EntityGroupCreateRequest{Token: "north",
		MemberType: "device", MembershipMode: &dynamic, Selector: &selector}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if _, err := f.api.PublishEntityGroup(ctx, "north", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := f.api.SetEntityGroupAccessScope(ctx, "north", true); err != nil {
		t.Fatalf("access scope: %v", err)
	}
	return f
}

// TestIsolationSweepForAGroupMember is the sweep for a member limited to a GROUP (ADR-081):
// every query, mutation and subscription device-management serves, as group:north's member,
// aimed at globex's device, which is in no group. Nothing may carry globex's data or tell
// its device apart from a missing one. The opted-in device read must still answer with data
// — acme-pump, reached through the group alone — or the sweep proves nothing.
func TestIsolationSweepForAGroupMember(t *testing.T) {
	f := newGroupScopeFixture(t)
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   f.member("group:north"),
		Unscoped: f.operator(),
		Probe:    "globex-pump",
		Hidden:   []string{"globex"},
		Literals: map[string]string{"originatorType": `"device"`, "state": `"ACTIVE"`, "severity": `"WARNING"`},
	})
	t.Logf("swept %d operations as group:north's member", len(out))
	if o, ok := isolationsweep.OutcomeOf(out, "query devices"); !ok || !o.Data {
		t.Errorf("query devices: %+v (reached=%v), want data for the group's member", o, ok)
	}
	// The access-scope switch is a write: a member never widens its own scope with it.
	if o, ok := isolationsweep.OutcomeOf(out, "mutation setEntityGroupAccessScope"); !ok || o.Data ||
		len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("mutation setEntityGroupAccessScope: %+v (reached=%v), want FORBIDDEN", o, ok)
	}
	// What "data" meant: the group's device and nothing else.
	got := serve(t, f.member("group:north"), `{ devices(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`, nil)
	if len(got.errs) > 0 {
		t.Fatalf("devices: %v", got.errs)
	}
	results := got.data["devices"].(map[string]any)["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["token"] != "acme-pump" {
		t.Fatalf("group:north's member saw %v, want only acme-pump", results)
	}
}

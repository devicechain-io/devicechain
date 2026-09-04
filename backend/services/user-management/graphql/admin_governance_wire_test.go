// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴🔴 THE SEAM THIS FILE GUARDS HAD NO TEST, AND A REVIEWER PROVED IT BY DELETING ONE LINE.
//
// CreateTenant and UpdateTenant hand-copy the GraphQL request struct into
// admin.GovernanceOverrides, field by field, in two nearly identical literals. Dropping
// `GeoFencePositionBudget: intPtr(args.Request.GeoFencePositionBudget)` from the UPDATE literal
// left every one of user-management's twelve packages green — while at runtime writing NULL into
// that column on every updateTenant, so an operator's cap never lands and an existing override is
// erased by any unrelated edit.
//
// That is exactly the failure this codebase has now shipped TWICE — heldCommandCeiling, then
// shedPriority for a further release — and the loop was believed closed. It is gated at every
// other link: console form → request by `Required<AdminTenantUpdateRequest>` and a derived-key
// test, service → row by TestUpdateTenantRoundTripsTheGeoFenceCaps, row → wire by the resolver
// tests. This one link, SDL args struct → GovernanceOverrides, had nothing.
//
// 🔑 SO THE GATE IS DERIVED, NOT LISTED. It reflects over admin.GovernanceOverrides and requires
// every field it declares to survive the round trip, which is what makes it a gate on the CLASS
// rather than on today's eleven fields: a governance override added tomorrow is covered the day it
// is declared, and a hand-written list here would be the same artifact as the bug — somebody's
// memory of which fields exist.

// newWireTestService builds the admin Service over an in-memory sqlite database, as the admin
// package's own tests do, so this exercises the real store rather than a stub. The point of the
// test is what reaches the ROW, and a fake service could not answer that.
func newWireTestService(t *testing.T) *admin.Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&iam.TenantTier{}, &iam.Tenant{}))
	return admin.NewService(iam.NewStore(&rdb.RdbManager{Database: db}), 300*time.Second, 12*time.Hour, nil)
}

// distinctOverrides sets every field admin.GovernanceOverrides declares to a DISTINCT value on
// the request struct at dst, and returns what it assigned, keyed by field name.
//
// 🔴 THE VALUES ARE ALL IN [base, base+n) WITH base >= 1 AND base+n <= 100, WHICH IS NOT
// ARBITRARY. Every override has its own validator and the assignment has to satisfy all of them
// at once: rates and bursts must be positive, shedPriority must be a point on the 1–100 band, and
// the three geofence caps must not exceed their platform maxima (1024 / 4000 / 125000). A window
// inside 1–100 is the intersection. Distinct matters more than large: a repeated value would let
// "the field arrived" be satisfied by a neighbour's value.
func distinctOverrides(t *testing.T, dst reflect.Value, base int) map[string]int {
	t.Helper()
	overrides := reflect.TypeOf(admin.GovernanceOverrides{})
	want := make(map[string]int, overrides.NumField())

	for i := 0; i < overrides.NumField(); i++ {
		name := overrides.Field(i).Name
		field := dst.FieldByName(name)
		require.Truef(t, field.IsValid(),
			"admin.GovernanceOverrides declares %s but the GraphQL request struct has no such field — "+
				"an override an operator cannot send is one that silently stays at its default", name)

		v := base + i
		want[name] = v

		// The request struct spells whole numbers as *int32 (a GraphQL Int) and rates as
		// *float64. Both are set from the same integer so the assertion can compare one way.
		elem := field.Type().Elem()
		switch elem.Kind() {
		case reflect.Int32, reflect.Int, reflect.Float64:
			p := reflect.New(elem)
			p.Elem().Set(reflect.ValueOf(v).Convert(elem))
			field.Set(p)
		default:
			t.Fatalf("%s is a *%s, which this test does not know how to fill", name, elem.Kind())
		}
	}
	require.NotEmpty(t, want, "reflection found no governance overrides — the gate is measuring nothing")
	return want
}

// assertStored requires every assigned override to have reached the tenant ROW. The resolver's M
// is the row: both mutations return s.iam.TenantByToken(...), a reload, not the struct they wrote.
func assertStored(t *testing.T, got *AdminTenantResolver, want map[string]int, where string) {
	t.Helper()
	row := reflect.ValueOf(got.M)
	for name, v := range want {
		field := row.FieldByName(name)
		require.Truef(t, field.IsValid(),
			"iam.Tenant has no field %s, which admin.GovernanceOverrides declares", name)
		require.Falsef(t, field.IsNil(),
			"%s: %s arrived as NULL. The wire→service copy in %s does not carry it, so an operator "+
				"setting it sees success and no effect — and any later edit erases one already set", where, name, where)
		require.Equalf(t, fmt.Sprint(v), fmt.Sprint(field.Elem().Interface()),
			"%s: %s stored the wrong value — the copy crossed two fields", where, name)
	}
}

// TestEveryGovernanceOverrideSurvivesTheAdminWireCopy drives the real resolvers against the real
// service and store, on BOTH mutations.
//
// Both are covered because they are separate hand-written literals that drift independently — the
// reviewer's deletion hit the update one only, and the create one would still have looked fine.
// Update is the more dangerous of the two (it is a full REPLACE, so a missing field does not fail
// to set, it CLEARS), but a create that silently drops an override is equally invisible.
func TestEveryGovernanceOverrideSurvivesTheAdminWireCopy(t *testing.T) {
	svc := newWireTestService(t)
	ctx := context.WithValue(adminCtx(string(auth.TenantWrite)), ContextAdminKey, svc)

	_, err := svc.CreateTenantTier(ctx, admin.TierInput{Token: iam.TierGoldToken, Name: "Gold"})
	require.NoError(t, err)

	r := &AdminResolver{}

	createIn := adminTenantCreateInput{Token: "seam", TierToken: iam.TierGoldToken}
	wantCreate := distinctOverrides(t, reflect.ValueOf(&createIn).Elem(), 11)
	created, err := r.CreateTenant(ctx, struct{ Request adminTenantCreateInput }{Request: createIn})
	require.NoError(t, err)
	assertStored(t, created, wantCreate, "CreateTenant")

	// Different values on update, so "the update landed" cannot be satisfied by the create's.
	updateIn := adminTenantUpdateInput{TierToken: iam.TierGoldToken}
	wantUpdate := distinctOverrides(t, reflect.ValueOf(&updateIn).Elem(), 31)
	updated, err := r.UpdateTenant(ctx, struct {
		Token   string
		Request adminTenantUpdateInput
	}{Token: "seam", Request: updateIn})
	require.NoError(t, err)
	assertStored(t, updated, wantUpdate, "UpdateTenant")

	// 🔴 THE HALF THAT MAKES THE OTHER HALF MEAN SOMETHING. updateTenant is a full replace, so an
	// omitted override must CLEAR the column rather than leave the old value — which is the
	// behaviour the console's round-trip depends on, and the direction in which a "carried over"
	// value would be indistinguishable from a working copy.
	empty := adminTenantUpdateInput{TierToken: iam.TierGoldToken}
	cleared, err := r.UpdateTenant(ctx, struct {
		Token   string
		Request adminTenantUpdateInput
	}{Token: "seam", Request: empty})
	require.NoError(t, err)
	row := reflect.ValueOf(cleared.M)
	overrides := reflect.TypeOf(admin.GovernanceOverrides{})
	for i := 0; i < overrides.NumField(); i++ {
		name := overrides.Field(i).Name
		require.Truef(t, row.FieldByName(name).IsNil(),
			"%s survived an update that omitted it — updateTenant is a full replace, so an omitted "+
				"override must revert the tenant to its tier, not silently keep the old bound", name)
	}
}

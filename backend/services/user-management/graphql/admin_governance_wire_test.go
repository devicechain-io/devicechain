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
//
// It fills BOTH request shapes, which is what keeps one derived gate over two mutations that no
// longer take the same kind of field: the CREATE input still spells an optional override as a
// plain pointer (a create has nothing to preserve, so two states are all it needs), while the
// UPDATE request spells it as a three-state dcgraphql.Optional*. Handling only one shape here
// would silently stop covering the other — and the update is the one the reviewer's deleted line
// was in.
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
		fillNumeric(t, field, name, v)
	}
	require.NotEmpty(t, want, "reflection found no governance overrides — the gate is measuring nothing")
	return want
}

// fillNumeric puts one numeric value into a request field, whichever of the two shapes it is.
//
// A create input's field is a *int32 or *float64. An update request's is an Optional* — a struct
// carrying `Set bool` and `Value *T` — and BOTH halves have to be written: a value assigned
// without Set is the ABSENT state, which means "leave it alone", so a version of this that only
// wrote Value would drive an update that changes nothing and then assert the row holds what it
// never sent.
func fillNumeric(t *testing.T, field reflect.Value, name string, v int) {
	t.Helper()
	if field.Kind() == reflect.Struct {
		set := field.FieldByName("Set")
		value := field.FieldByName("Value")
		require.Truef(t, set.IsValid() && value.IsValid(),
			"%s is a %s with no Set/Value pair, so it cannot tell an absent field from a null",
			name, field.Type())
		set.SetBool(true)
		elem := value.Type().Elem()
		p := reflect.New(elem)
		p.Elem().Set(reflect.ValueOf(v).Convert(elem))
		value.Set(p)
		return
	}
	require.Equalf(t, reflect.Ptr, field.Kind(), "%s is a %s, which this test cannot fill", name, field.Type())
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

// clearAllOverrides puts every override on an UPDATE request into the explicit-null state, which
// is what an operator sends to revert a tenant to its tier and then the platform default.
func clearAllOverrides(t *testing.T, dst reflect.Value) {
	t.Helper()
	overrides := reflect.TypeOf(admin.GovernanceOverrides{})
	for i := 0; i < overrides.NumField(); i++ {
		name := overrides.Field(i).Name
		field := dst.FieldByName(name)
		require.Truef(t, field.IsValid(), "the update request has no %s", name)
		require.Equalf(t, reflect.Struct, field.Kind(),
			"%s is a %s, so it has no explicit-null state to put it in", name, field.Type())
		field.FieldByName("Set").SetBool(true)
		value := field.FieldByName("Value")
		value.Set(reflect.Zero(value.Type()))
	}
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
// Both are covered because they used to be separate hand-written literals that drift
// independently — the reviewer's deletion hit the update one only, and the create one would still
// have looked fine. The update literal is GONE now: the resolver hands the service its own
// TenantUpdateRequest, so there is no field-by-field copy left to lose a field in, and what this
// gate measures on that side is the fold (governanceFor) rather than a copy. The create side still
// copies by hand, and is still the reason this is derived rather than listed.
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
	updateIn := admin.TenantUpdateRequest{}
	wantUpdate := distinctOverrides(t, reflect.ValueOf(&updateIn).Elem(), 31)
	updated, err := r.UpdateTenant(ctx, struct {
		Token   string
		Request admin.TenantUpdateRequest
	}{Token: "seam", Request: updateIn})
	require.NoError(t, err)
	assertStored(t, updated, wantUpdate, "UpdateTenant")

	// 🔴 THE HALF THAT MAKES THE OTHER HALF MEAN SOMETHING, AND IT HAS BEEN INVERTED.
	//
	// It used to assert that an update OMITTING an override CLEARED the column — correct for a
	// full replace, and precisely the defect this conversion removes. An omitted override must
	// now leave the stored bound exactly where it is, or renaming a tenant still silently
	// removes every ceiling an operator set.
	preserved, err := r.UpdateTenant(ctx, struct {
		Token   string
		Request admin.TenantUpdateRequest
	}{Token: "seam", Request: admin.TenantUpdateRequest{}})
	require.NoError(t, err)
	assertStored(t, preserved, wantUpdate,
		"UpdateTenant naming nothing (every override must survive an unrelated edit)")

	// And clearing is still reachable — it just has to be SAID. Without this the assertion above
	// would be satisfied by an update that ignores the overrides entirely, which is the same
	// fail-open one level over: an operator could no longer revert a tenant to its tier.
	clearIn := admin.TenantUpdateRequest{}
	clearAllOverrides(t, reflect.ValueOf(&clearIn).Elem())
	cleared, err := r.UpdateTenant(ctx, struct {
		Token   string
		Request admin.TenantUpdateRequest
	}{Token: "seam", Request: clearIn})
	require.NoError(t, err)
	row := reflect.ValueOf(cleared.M)
	overrides := reflect.TypeOf(admin.GovernanceOverrides{})
	for i := 0; i < overrides.NumField(); i++ {
		name := overrides.Field(i).Name
		require.Truef(t, row.FieldByName(name).IsNil(),
			"%s survived an explicit null — clearing an override is how an operator reverts a "+
				"tenant to its tier, and a null that does nothing removes that capability", name)
	}
}

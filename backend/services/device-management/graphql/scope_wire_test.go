// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The scoped-caller surface of device-management (ADR-081), served end to end: the parsed
// schema, the resolvers, the API and the store, with the scope taken from claims exactly
// as the transports attach them. Tenant "t1" holds two customers' devices:
//
//	acme-pump   — "assigned" (tracked) to customer acme
//	globex-pump — "assigned" (tracked) to customer globex
//
// Acme's member must see acme-pump and its own customer record, and nothing of Globex's.
// Every test also runs the same operation UNSCOPED and requires Globex's rows to be there:
// a fixture that held no Globex row would make every scoped assertion vacuous.

const scopeTenant = "t1"

type scopeFixture struct {
	db  *gorm.DB
	api *model.Api
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// ONE connection: every connection to ":memory:" is a database of its own, and a query
	// whose field resolvers run concurrently (a relationship's source and target) would
	// otherwise open a second, empty one.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	return seedScopeFixture(t, db)
}

// seedScopeFixture migrates and seeds the scope fixture on db, which has tenant scoping
// registered.
func seedScopeFixture(t *testing.T, db *gorm.DB, extra ...any) scopeFixture {
	t.Helper()
	if err := db.AutoMigrate(append([]any{&model.DeviceType{}, &model.Device{}, &model.DeviceProfile{},
		&model.CustomerType{}, &model.Customer{}, &model.IssuedCustomerToken{}, &model.Area{}, &model.EntityRelationshipType{},
		&model.EntityRelationship{}, &model.PendingOwnershipFact{}, &model.DeviceClaim{}, &model.DeviceProfileVersion{}, &model.DeviceProfileActivation{},
		&model.MetricDefinition{}, &model.CommandDefinition{}, &model.Alarm{}, &model.EntityAttribute{}}, extra...)...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	f := scopeFixture{db: db, api: model.NewApi(&rdb.RdbManager{Database: db})}
	ctx := f.operator()
	if _, err := f.api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "pump"}); err != nil {
		t.Fatalf("device type: %v", err)
	}
	ct, err := f.api.CreateCustomerType(ctx, &model.CustomerTypeCreateRequest{Token: "retail"})
	if err != nil {
		t.Fatalf("customer type: %v", err)
	}
	for _, c := range []string{"acme", "globex"} {
		if _, err := f.api.CreateCustomer(ctx, &model.CustomerCreateRequest{Token: c, CustomerTypeToken: ct.Token}); err != nil {
			t.Fatalf("customer %s: %v", c, err)
		}
		f.addDevice(t, c+"-pump", c)
	}
	return f
}

// operator is an unscoped caller holding every tenant authority the fixture needs.
func (f scopeFixture) operator() context.Context {
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{"*"}})
}

// member is a scoped caller: the viewer baseline plus alarm acknowledgement, limited to anchors.
func (f scopeFixture) member(anchors ...string) context.Context {
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.AlarmRead), string(auth.AlarmWrite),
			string(auth.EventRead)},
		ScopeAnchors: anchors})
}

func (f scopeFixture) withClaims(c *auth.Claims) context.Context {
	ctx := auth.WithClaims(core.WithTenant(context.Background(), c.Tenant), c)
	return context.WithValue(ctx, gqlcore.ContextApiKey, f.api)
}

// addDevice creates a device assigned (tracked) to customer.
func (f scopeFixture) addDevice(t *testing.T, token, customer string) {
	t.Helper()
	ctx := f.operator()
	if _, err := f.api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: token, DeviceTypeToken: "pump"}); err != nil {
		t.Fatalf("device %s: %v", token, err)
	}
	assigned, err := f.api.EnsureAssignmentType(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.CreateEntityRelationship(ctx, &model.EntityRelationshipCreateRequest{
		Token: token + "-to-" + customer, SourceType: "device", Source: token,
		TargetType: "customer", Target: customer, RelationshipType: assigned.Token,
	}); err != nil {
		t.Fatalf("assign %s to %s: %v", token, customer, err)
	}
}

type served struct {
	data map[string]any
	errs []*gqlerrors.QueryError
}

func serve(t *testing.T, ctx context.Context, query string, vars map[string]any) served {
	t.Helper()
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, query, "", vars)
	out := served{errs: resp.Errors}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &out.data); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return out
}

// tokensAt collects every "token" string under the path's list.
func tokensAt(t *testing.T, v any, path ...string) []string {
	t.Helper()
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("no object at %q in %v", p, v)
		}
		v = m[p]
	}
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("no list at %v: %v", path, v)
	}
	out := []string{}
	for _, e := range list {
		out = append(out, e.(map[string]any)["token"].(string))
	}
	sort.Strings(out)
	return out
}

func requireServed(t *testing.T, s served, what string) {
	t.Helper()
	if len(s.errs) > 0 {
		t.Fatalf("%s: %v", what, s.errs)
	}
}

// TestScopedDevicesReturnsOnlyAcme: the device list, the by-token, by-id and by-external-id
// reads, with the device type each row carries (a catalogue model, read unfiltered).
func TestScopedDevicesReturnsOnlyAcme(t *testing.T) {
	f := newScopeFixture(t)
	const list = `{ devices(criteria: {pageNumber: 1, pageSize: 50}) { results { token deviceType { token } } } }`

	acme := serve(t, f.member("customer:acme"), list, nil)
	requireServed(t, acme, "acme's member listing devices")
	if got := tokensAt(t, acme.data, "devices", "results"); !reflect.DeepEqual(got, []string{"acme-pump"}) {
		t.Fatalf("acme's member listed %v", got)
	}
	// The device type rides along: a catalogue read, not refused.
	dt := acme.data["devices"].(map[string]any)["results"].([]any)[0].(map[string]any)["deviceType"]
	if dt == nil || dt.(map[string]any)["token"] != "pump" {
		t.Fatalf("the device type did not resolve for a scoped caller: %v", dt)
	}
	// Negative control: the operator sees Globex's device in the same fixture.
	all := serve(t, f.operator(), list, nil)
	requireServed(t, all, "the operator listing devices")
	if got := tokensAt(t, all.data, "devices", "results"); !reflect.DeepEqual(got, []string{"acme-pump", "globex-pump"}) {
		t.Fatalf("the unscoped control listed %v", got)
	}

	byToken := serve(t, f.member("customer:acme"),
		`{ devicesByToken(tokens: ["acme-pump", "globex-pump"]) { token } }`, nil)
	requireServed(t, byToken, "by token")
	if got := tokensAt(t, byToken.data, "devicesByToken"); !reflect.DeepEqual(got, []string{"acme-pump"}) {
		t.Fatalf("acme's member read by token %v", got)
	}
	// Globex's member, symmetrically.
	globex := serve(t, f.member("customer:globex"), list, nil)
	requireServed(t, globex, "globex's member")
	if got := tokensAt(t, globex.data, "devices", "results"); !reflect.DeepEqual(got, []string{"globex-pump"}) {
		t.Fatalf("globex's member listed %v", got)
	}
}

// TestDeviceOutsideScopeIsNotFound: a device outside the scope is answered exactly as a
// device that does not exist — omitted from a by-token list, NOT_FOUND where a single
// device is named — never FORBIDDEN, which would confirm it exists.
func TestDeviceOutsideScopeIsNotFound(t *testing.T) {
	f := newScopeFixture(t)
	acme := f.member("customer:acme")

	for _, tc := range []struct{ name, query string }{
		{"by token", `{ devicesByToken(tokens: ["globex-pump"]) { token } }`},
		{"by external id", `{ devicesByExternalId(externalIds: ["globex-pump"]) { token } }`},
	} {
		s := serve(t, acme, tc.query, nil)
		requireServed(t, s, tc.name)
		for _, v := range s.data {
			if l, _ := v.([]any); len(l) != 0 {
				t.Fatalf("%s: acme's member read %v", tc.name, l)
			}
		}
	}

	// The live alarm stream names a device: outside the scope it is the store's miss, the
	// same sentinel a device that never existed produces (the wire answers NOT_FOUND).
	stream := func(ctx context.Context, token string) error {
		kind := "device"
		_, err := (&SchemaResolver{}).AlarmStream(ctx, struct {
			OriginatorType *string
			Originator     *string
			State          *string
			Severity       *string
			AlarmKey       *string
		}{OriginatorType: &kind, Originator: &token})
		return err
	}
	outside, missing := stream(acme, "globex-pump"), stream(acme, "no-such-pump")
	if outside == nil || missing == nil {
		t.Fatalf("a stream on a device outside the scope (%v) or missing (%v) was accepted", outside, missing)
	}
	if !strings.Contains(outside.Error(), "record not found") || strings.Replace(outside.Error(), "globex-pump", "no-such-pump", 1) != missing.Error() {
		t.Fatalf("a device outside the scope is told apart from a missing one: %q vs %q", outside, missing)
	}
}

// A customer member reads its own customer record and no other, with the customer type
// (catalogue) it carries.
func TestScopedCustomersReturnsOnlyItsOwnRecord(t *testing.T) {
	f := newScopeFixture(t)
	const q = `{ customers(criteria: {pageNumber: 1, pageSize: 50}) { results { token customerType { token } } } }`
	acme := serve(t, f.member("customer:acme"), q, nil)
	requireServed(t, acme, "acme's member listing customers")
	if got := tokensAt(t, acme.data, "customers", "results"); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("acme's member listed customers %v", got)
	}
	all := serve(t, f.operator(), q, nil)
	requireServed(t, all, "the operator")
	if got := tokensAt(t, all.data, "customers", "results"); !reflect.DeepEqual(got, []string{"acme", "globex"}) {
		t.Fatalf("the unscoped control listed %v", got)
	}
	byToken := serve(t, f.member("customer:acme"), `{ customersByToken(tokens: ["globex"]) { token } }`, nil)
	requireServed(t, byToken, "by token")
	if l := byToken.data["customersByToken"].([]any); len(l) != 0 {
		t.Fatalf("acme's member read globex's record: %v", l)
	}
}

// Everything that has not opted in still answers FORBIDDEN with the reason, so the
// relationship graph — which would name every customer's devices — stays shut.
func TestUnoptedReadsStayForbiddenToAScopedCaller(t *testing.T) {
	f := newScopeFixture(t)
	for _, q := range []string{
		`{ entityRelationships(criteria: {pageNumber: 1, pageSize: 50}) { results { token } } }`,
		`{ existingEntityRefs(refs: [{type: "device", token: "globex-pump"}]) { token } }`,
		`{ entityAttributes(criteria: {pageNumber: 1, pageSize: 10, entityType: "device", entity: "globex-pump"}) { results { attrKey } } }`,
	} {
		s := serve(t, f.member("customer:acme"), q, nil)
		if len(s.errs) == 0 {
			t.Fatalf("a scoped caller was served %s: %v", q, s.data)
		}
		if s.errs[0].Extensions["code"] != "FORBIDDEN" || s.errs[0].Extensions["reason"] != auth.ReasonScopeUnsupported {
			t.Fatalf("%s: extensions %v (%q)", q, s.errs[0].Extensions, s.errs[0].Message)
		}
	}
}

// The catalogue opens to a scoped caller unfiltered: device types and profiles are
// configuration, not device-derived.
func TestTheCatalogueIsReadableByAScopedCaller(t *testing.T) {
	f := newScopeFixture(t)
	s := serve(t, f.member("customer:acme"), `{ deviceTypes(criteria: {pageNumber: 1, pageSize: 50}) { results { token } } }`, nil)
	requireServed(t, s, "device types")
	if got := tokensAt(t, s.data, "deviceTypes", "results"); !reflect.DeepEqual(got, []string{"pump"}) {
		t.Fatalf("a scoped caller read device types %v", got)
	}
	// And still refuses a write to it.
	w := serve(t, f.member("customer:acme"), wireCreateType, map[string]any{"request": map[string]any{"token": "x"}})
	if len(w.errs) == 0 || w.errs[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("a scoped caller created a device type: %v %v", w.data, w.errs)
	}
}

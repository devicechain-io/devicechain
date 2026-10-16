// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// customerDevices is the read a dashboard widget bound to a customer streams from. The
// fixture adds to the scope fixture's two pumps:
//
//	shared-pump  — "assigned" (tracked) to acme AND to globex
//	acme-pump    — also "monitors" (UNTRACKED) to acme
//	globex-pump  — also "monitors" (untracked) to acme: related to acme, owned by globex
//	customer globex — "monitors" to acme: an edge whose SOURCE is not a device
func newCustomerDevicesFixture(t *testing.T) scopeFixture {
	t.Helper()
	f := newScopeFixture(t)
	ctx := f.operator()
	f.addDevice(t, "shared-pump", "acme")
	assigned, err := f.api.EnsureAssignmentType(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.CreateEntityRelationshipType(ctx, &model.EntityRelationshipTypeCreateRequest{Token: "monitors"}); err != nil {
		t.Fatalf("untracked type: %v", err)
	}
	for _, e := range []model.EntityRelationshipCreateRequest{
		{Token: "shared-to-globex", SourceType: "device", Source: "shared-pump", TargetType: "customer", Target: "globex", RelationshipType: assigned.Token},
		{Token: "acme-monitors", SourceType: "device", Source: "acme-pump", TargetType: "customer", Target: "acme", RelationshipType: "monitors"},
		{Token: "globex-monitors", SourceType: "device", Source: "globex-pump", TargetType: "customer", Target: "acme", RelationshipType: "monitors"},
		{Token: "customer-monitors", SourceType: "customer", Source: "globex", TargetType: "customer", Target: "acme", RelationshipType: "monitors"},
	} {
		e := e
		if _, err := f.api.CreateEntityRelationship(ctx, &e); err != nil {
			t.Fatalf("edge %s: %v", e.Token, err)
		}
	}
	return f
}

const customerDevicesQuery = `query($customer: String!, $type: String!) {
  customerDevices(criteria: {pageNumber: 1, pageSize: 50, customer: $customer, relationshipType: $type}) {
    results { token name } pagination { totalRecords } } }`

type customerDevicesAnswer struct {
	tokens []string
	total  float64
	code   string
	msg    string
}

func askCustomerDevices(t *testing.T, ctx context.Context, customer, relType string) customerDevicesAnswer {
	t.Helper()
	s := serve(t, ctx, customerDevicesQuery, map[string]any{"customer": customer, "type": relType})
	if len(s.errs) > 0 {
		code, _ := s.errs[0].Extensions["code"].(string)
		if s.data != nil && s.data["customerDevices"] != nil {
			t.Fatalf("customerDevices answered data beside an error: %v", s.data)
		}
		return customerDevicesAnswer{code: code, msg: s.errs[0].Message}
	}
	page := s.data["customerDevices"].(map[string]any)
	return customerDevicesAnswer{
		tokens: tokensAt(t, s.data, "customerDevices", "results"),
		total:  page["pagination"].(map[string]any)["totalRecords"].(float64),
	}
}

// A customer member is answered about its own customer, with the devices it owns there —
// the shared pump included, since acme owns it too.
func TestCustomerDevicesAnswersAScopedMemberAboutItsOwnCustomer(t *testing.T) {
	f := newCustomerDevicesFixture(t)
	got := askCustomerDevices(t, f.member("customer:acme"), "acme", model.AssignmentRelationshipType)
	if got.code != "" || !reflect.DeepEqual(got.tokens, []string{"acme-pump", "shared-pump"}) || got.total != 2 {
		t.Fatalf("acme's member asking about acme: %+v", got)
	}
	// A two-customer scope may ask about either.
	both := f.member("customer:acme", "customer:globex")
	if got := askCustomerDevices(t, both, "globex", model.AssignmentRelationshipType); got.code != "" ||
		!reflect.DeepEqual(got.tokens, []string{"globex-pump", "shared-pump"}) {
		t.Fatalf("a two-customer member asking about globex: %+v", got)
	}
}

// 🔴 A customer outside the scope is refused — and refused from the claims alone, so a
// customer that exists and one that does not answer identically. Without the refusal,
// acme's member asking about globex would be handed shared-pump: a device it can see,
// which would tell it globex holds that device too.
func TestCustomerDevicesRefusesACustomerOutsideTheScopeAsItRefusesAMissingOne(t *testing.T) {
	f := newCustomerDevicesFixture(t)
	acme := f.member("customer:acme")
	outside := askCustomerDevices(t, acme, "globex", model.AssignmentRelationshipType)
	missing := askCustomerDevices(t, acme, "no-such-customer", model.AssignmentRelationshipType)
	if outside.code != "FORBIDDEN" {
		t.Fatalf("acme's member asking about globex: %+v, want FORBIDDEN", outside)
	}
	if !reflect.DeepEqual(outside, missing) {
		t.Fatalf("a customer outside the scope is told apart from a missing one: %+v vs %+v", outside, missing)
	}
	// A scope naming only a group names no customer.
	if got := askCustomerDevices(t, f.member("group:acme"), "acme", model.AssignmentRelationshipType); got.code != "FORBIDDEN" {
		t.Fatalf("a group-scoped member asking about acme: %+v", got)
	}
	// Negative control: the operator is answered about globex, shared pump and all.
	if got := askCustomerDevices(t, f.operator(), "globex", model.AssignmentRelationshipType); got.code != "" ||
		!reflect.DeepEqual(got.tokens, []string{"globex-pump", "shared-pump"}) {
		t.Fatalf("the unscoped control asking about globex: %+v", got)
	}
}

// For a scoped member only ownership matches. An untracked edge is not something that
// member could otherwise read (the relationship graph is closed to it), so an untracked
// type matches nothing — not acme's own pump's "monitors" edge, and not globex's pump,
// which acme does not own.
func TestCustomerDevicesMatchesOnlyOwnershipForAScopedMember(t *testing.T) {
	f := newCustomerDevicesFixture(t)
	if got := askCustomerDevices(t, f.member("customer:acme"), "acme", "monitors"); got.code != "" || len(got.tokens) != 0 || got.total != 0 {
		t.Fatalf("acme's member through an untracked type: %+v", got)
	}
	if got := askCustomerDevices(t, f.member("customer:acme"), "acme", "no-such-type"); got.code != "" || len(got.tokens) != 0 {
		t.Fatalf("acme's member through an unknown type: %+v", got)
	}
	// An empty type (a half-authored binding) matches nothing, for every caller.
	for _, ctx := range []context.Context{f.member("customer:acme"), f.operator()} {
		if got := askCustomerDevices(t, ctx, "acme", ""); got.code != "" || len(got.tokens) != 0 {
			t.Fatalf("an empty relationship type: %+v", got)
		}
	}
	if got := askCustomerDevices(t, f.operator(), "", model.AssignmentRelationshipType); got.code != "" || len(got.tokens) != 0 {
		t.Fatalf("an empty customer: %+v", got)
	}
	// Negative control: unscoped, the untracked type answers both pumps related to acme.
	if got := askCustomerDevices(t, f.operator(), "acme", "monitors"); !reflect.DeepEqual(got.tokens, []string{"acme-pump", "globex-pump"}) {
		t.Fatalf("the unscoped control through the untracked type: %+v", got)
	}
}

// 🔑 For an unscoped caller customerDevices answers what the relationship search answers
// for (source device → target customer, type) — the read the dashboard resolver made for
// every customer anchor before — so the resolver can take the one path for every viewer.
// Every (customer, type) the fixture can tell apart is compared, as sets of device tokens
// and as totals.
func TestCustomerDevicesMatchesTheRelationshipSearchForAnUnscopedCaller(t *testing.T) {
	f := newCustomerDevicesFixture(t)
	ctx := f.operator()
	const rels = `query($customer: String!, $type: String!) {
  entityRelationships(criteria: {pageNumber: 1, pageSize: 50, sourceType: "device", targetType: "customer",
    target: $customer, relationshipType: $type}) { results { source { token } } pagination { totalRecords } } }`
	compared := 0
	for _, customer := range []string{"acme", "globex"} {
		for _, relType := range []string{model.AssignmentRelationshipType, "monitors", "no-such-type"} {
			s := serve(t, ctx, rels, map[string]any{"customer": customer, "type": relType})
			requireServed(t, s, "entityRelationships "+customer+"/"+relType)
			page := s.data["entityRelationships"].(map[string]any)
			want := []string{}
			for _, r := range page["results"].([]any) {
				want = append(want, r.(map[string]any)["source"].(map[string]any)["token"].(string))
			}
			sort.Strings(want)
			wantTotal := page["pagination"].(map[string]any)["totalRecords"].(float64)

			got := askCustomerDevices(t, ctx, customer, relType)
			if got.code != "" || !reflect.DeepEqual(got.tokens, want) || got.total != wantTotal {
				t.Errorf("%s/%s: customerDevices %+v, the relationship search %v (total %v)", customer, relType, got, want, wantTotal)
			}
			if len(want) > 0 {
				compared++
			}
		}
	}
	// The comparison is only worth something if both reads found members somewhere.
	if compared < 3 {
		t.Fatalf("only %d of the compared reads found any member; the fixture proves little", compared)
	}
}

// The by-token twin the sweep cannot build: it fills every String with the probe device's
// token, so its customerDevices call names no customer at all.
func TestCustomerDevicesIsInTheSweepAndRefusedThere(t *testing.T) {
	f := newScopeFixture(t)
	s := serve(t, f.member("customer:acme"), customerDevicesQuery, map[string]any{"customer": "globex-pump", "type": "globex-pump"})
	if len(s.errs) == 0 || s.errs[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("customerDevices aimed at the probe: %v %v", s.data, s.errs)
	}
	if reason := s.errs[0].Extensions["reason"]; reason == auth.ReasonScopeUnsupported {
		t.Fatalf("customerDevices is opted in; its refusal must not read as SCOPE_UNSUPPORTED: %v", s.errs[0].Extensions)
	}
}

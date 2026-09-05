// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// THE WIRE HALF of updateDashboard's partial-update guarantee.
//
// The guarantees split across three layers, and no one of them is sufficient:
//
//   - THE SHAPE OF THE INPUT is only observable here, against the real schema: that
//     `token` is not a member of DashboardUpdateRequest, that `request` is required, and
//     — the one this conversion turns on — that `definition` is NULLABLE in the SDL. A
//     model test cannot see any of the three, because all three are decisions the schema
//     makes before a resolver runs.
//   - THE THREE STATES REACHING STORAGE live in the model harness
//     (model/partial_update_families_test.go), which drives the real Api against a real
//     database.
//   - THAT THE THREE STATES SURVIVE THE WIRE AT ALL is proved once, generically, in
//     core's graphql.TestOptionalStringCarriesThreeStates. Every field on this input is
//     an OptionalString, so that proof carries here by construction rather than by
//     repetition.
//
// 🔴 WHY `definition: String` AND NOT `String!`, WHEN THE COLUMN IS NOT NULL. A non-null
// input field with no default is REQUIRED by validation, which would make the ABSENT
// state — the one that means "leave the definition alone while I rename this" —
// unrepresentable. So the schema admits the null and the MODEL refuses it, which is a
// refusal naming the field rather than a validation error about the document's type. The
// two tests at the bottom are what keep that split from silently reverting: one asserts
// the null reaches the resolver, the other that the resolver refuses it.

const wireDefinition = `{"schemaVersion":1,"widgets":[]}`

// newWireFixture builds a real Api over a throwaway database and a context carrying it
// plus dashboard:write, and returns both so a test can seed and read back around a real
// schema.Exec.
//
// 🔴 THE DSN IS A NAMED SHARED-CACHE DATABASE AND THE POOL IS CLOSED, for the reasons
// core's putest.NewSQLiteDB spells out: a bare ":memory:" gives every pooled connection
// its own database, and a shared-cache one outlives the test that made it unless the pool
// is closed, so the next test with the same name inherits its rows. The test name is
// sanitized first because a URI reads the "#" Go appends to a repeated name as the start
// of a FRAGMENT, silently truncating the name and falling back to an on-disk file.
func newWireFixture(t *testing.T) (*model.Api, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+wireDSNName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, cerr := db.DB()
		if cerr != nil {
			t.Errorf("reach the underlying pool to close it: %v", cerr)
			return
		}
		if cerr := sqlDB.Close(); cerr != nil {
			t.Errorf("close the fixture's pool: %v — the shared-cache database outlives this "+
				"test and the next one to reuse its name inherits these rows", cerr)
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&model.Dashboard{}, &model.DashboardVersion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx := withAuthorities(core.WithTenant(context.Background(), "acme"),
		auth.DashboardRead, auth.DashboardWrite)
	return api, context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

func wireDSNName(t *testing.T) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, t.Name())
}

func seedWireDashboard(t *testing.T, api *model.Api, ctx context.Context, token string) {
	t.Helper()
	name, description := "Original name", "Original description"
	if _, err := api.CreateDashboard(ctx, &model.DashboardCreateRequest{
		Token: token, Name: &name, Description: &description, Definition: wireDefinition,
	}); err != nil {
		t.Fatalf("seed dashboard: %v", err)
	}
}

const updateDashboardDoc = `mutation($token: String!, $request: DashboardUpdateRequest!) {
  updateDashboard(token: $token, request: $request) { token name description definition }
}`

// execUpdate runs the mutation with the given request map and reports the request errors
// (a nil Path) and resolver errors (a non-nil Path) separately. The distinction is the
// whole point of every test below: "there was an error" is satisfied by a not-found
// whether or not the schema refused anything.
func execUpdate(t *testing.T, ctx context.Context, token string, request map[string]any) (requestErrs, resolverErrs []string) {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, updateDashboardDoc, "", map[string]any{
		"token": token, "request": request,
	})
	for _, e := range res.Errors {
		if e.Path == nil {
			requestErrs = append(requestErrs, e.Message)
		} else {
			resolverErrs = append(resolverErrs, e.Message)
		}
	}
	return requestErrs, resolverErrs
}

// The token is the mutation's own argument and is deliberately not a member of the update
// input, so naming a second dashboard is UNREPRESENTABLE rather than refused. It also
// closes the defect this conversion was built on: the payload token used to be written
// onto the row, and an empty one — legal, since `token: String!` admits "" — blanked the
// dashboard's token and left it addressable by nothing.
//
// The rejection arrives from the unknown-input-field guard, which makes this a check on
// that guard too: a silently dropped field would tell the caller their update succeeded.
func TestUpdateDashboardInputCannotCarryAToken(t *testing.T) {
	_, ctx := newWireFixture(t)
	reqErrs, _ := execUpdate(t, ctx, "whatever", map[string]any{"token": "moved"})
	if len(reqErrs) == 0 {
		t.Fatal("a `token` on DashboardUpdateRequest reached the resolver instead of being " +
			"refused by the schema — either the field was re-added to the input, or an " +
			"undeclared field is being silently dropped again")
	}
}

// `request` is non-null, so a caller who sends nothing gets a request error rather than a
// silently successful no-op that returns the dashboard unchanged.
func TestUpdateDashboardRequiresARequest(t *testing.T) {
	_, ctx := newWireFixture(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `mutation($token: String!) {
	  updateDashboard(token: $token, request: null) { token }
	}`, "", map[string]any{"token": "whatever"})
	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("a null request reached the resolver instead of being refused by the schema "+
		"(errors: %v)", res.Errors)
}

// THE COUNTERWEIGHT, and it is what makes the two rejections above mean anything: they
// are only safe while a well-formed partial update still parses, reaches the resolver AND
// lands. Without it, renaming the input or mistyping a field would make both pass for
// exactly the wrong reason — every request rejected, the guarantee "held" vacuously.
//
// It also closes the seam between the wire and storage that neither layer can see alone:
// the request names ONE field, and the two it does not name are read back from the
// DATABASE unchanged. Under the full-replace shape this replaces, that same request
// erased both and returned 200.
func TestUpdateDashboardAppliesAWellFormedPartialRequest(t *testing.T) {
	api, ctx := newWireFixture(t)
	seedWireDashboard(t, api, ctx, "dash-a")

	reqErrs, resErrs := execUpdate(t, ctx, "dash-a", map[string]any{"name": "Renamed"})
	if len(reqErrs) > 0 {
		t.Fatalf("a well-formed partial update was rejected before the resolver: %v", reqErrs)
	}
	if len(resErrs) > 0 {
		t.Fatalf("a well-formed partial update was refused by the resolver: %v", resErrs)
	}

	found, err := api.DashboardsByToken(ctx, []string{"dash-a"})
	if err != nil || len(found) != 1 {
		t.Fatalf("reload: %v (%d rows)", err, len(found))
	}
	if got := found[0].Name.String; got != "Renamed" {
		t.Errorf("name = %q, want %q — the field the caller SENT was not written", got, "Renamed")
	}
	if got := found[0].Description.String; got != "Original description" {
		t.Errorf("description = %q, want %q — an update naming only `name` erased it, which "+
			"is the full replace this conversion removes", got, "Original description")
	}
	if got := string(found[0].Definition); got != wireDefinition {
		t.Errorf("definition = %q, want %q — an update naming only `name` overwrote the "+
			"dashboard's entire content", got, wireDefinition)
	}
}

// 🔴 `definition` IS NULLABLE IN THE SDL, SO AN EXPLICIT NULL REACHES THE RESOLVER.
//
// This is the assertion that keeps someone from "fixing" the input by declaring
// `definition: String!` — which reads like the right thing (the column IS NOT NULL) and
// would silently delete the ABSENT state along with the null, making every rename resend
// the whole document. The refusal belongs to the model, and the test below is its other
// half.
func TestUpdateDashboardAdmitsAnExplicitNullDefinitionToTheResolver(t *testing.T) {
	api, ctx := newWireFixture(t)
	seedWireDashboard(t, api, ctx, "dash-a")

	reqErrs, _ := execUpdate(t, ctx, "dash-a", map[string]any{"definition": nil})
	if len(reqErrs) > 0 {
		t.Fatalf("an explicit null on `definition` was refused by the schema (%v) — the field "+
			"has been declared non-null, which deletes the ABSENT state with it and makes "+
			"every rename resend the whole document", reqErrs)
	}
}

// …and the resolver REFUSES it, naming the field, leaving the row alone. A dashboard with
// no definition is not a thing; folding the null to an empty document would store
// something nothing can render and report success.
func TestUpdateDashboardRefusesAnExplicitNullDefinition(t *testing.T) {
	api, ctx := newWireFixture(t)
	seedWireDashboard(t, api, ctx, "dash-a")

	_, resErrs := execUpdate(t, ctx, "dash-a", map[string]any{
		"name": "Renamed", "definition": nil,
	})
	if len(resErrs) == 0 {
		t.Fatal("an explicit null on `definition` was accepted, leaving a dashboard with no content")
	}
	if !strings.Contains(strings.Join(resErrs, " "), "definition") {
		t.Errorf("the refusal does not name the field the caller can act on: %v", resErrs)
	}

	found, err := api.DashboardsByToken(ctx, []string{"dash-a"})
	if err != nil || len(found) != 1 {
		t.Fatalf("reload: %v (%d rows)", err, len(found))
	}
	if got := found[0].Name.String; got != "Original name" {
		t.Errorf("the refused update still applied the rename (name = %q) — a caller who "+
			"retries starts from a half-applied first attempt", got)
	}
}

// 🔴 THE SCHEMA'S UPDATE SURFACE IS DERIVED, NOT LISTED. Everything above drives ONE
// mutation by name, so a second update mutation added to this service tomorrow would be
// covered by nothing here and the file would stay green. The set is taken from the
// server's own introspection — the same rule the server enforces rather than an
// approximation of it — and anything not covered fails on the day it is added.
func TestEveryUpdateMutationIsCoveredByTheWireTests(t *testing.T) {
	covered := map[string]bool{"updateDashboard": true}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	mutationType := schema.Inspect().MutationType()
	if mutationType == nil {
		t.Fatal("the schema declares no Mutation type; this guard is reading nothing")
	}
	fields := mutationType.Fields(&struct{ IncludeDeprecated bool }{})
	if fields == nil || len(*fields) == 0 {
		t.Fatal("the Mutation type reports no fields; this guard is reading nothing")
	}

	found := 0
	for _, f := range *fields {
		if !strings.HasPrefix(f.Name(), "update") {
			continue
		}
		found++
		if !covered[f.Name()] {
			t.Errorf("%s is an update mutation the schema serves, but nothing in this file "+
				"sends it a token, a null request or a well-formed partial one", f.Name())
		}
	}
	// The anti-vacuity control. An introspection call that returned an empty or
	// prefix-less field set would certify everything.
	if found < 1 {
		t.Fatal("no update* mutation was found in the schema; this guard has stopped seeing " +
			"the surface it certifies")
	}
}

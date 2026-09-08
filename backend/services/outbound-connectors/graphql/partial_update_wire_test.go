// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// THE WIRE HALF of the connector's partial-update guarantee.
//
// The SHAPE of the input is only observable here, against the real schema: that `token`
// is not a member of ConnectorUpdateRequest, and that `request` is required. Both are
// rejections the schema performs before any resolver runs, so the model harness cannot
// see them. What reaches storage is the model harness's half
// (model/partial_update_suite_test.go), and that the three states survive the packer at
// all is proved once, generically, in core's graphql/optional_test.go.

const testWireRootKey = "0123456789abcdef0123456789abcdef"

func connectorWireCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+wireDSNName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 🔴 A shared-cache in-memory database lives while a connection to it is open, and
	// gorm closes none — so without this the name survives the test that made it and the
	// next test to reuse it inherits these rows.
	t.Cleanup(func() {
		sqlDB, cerr := db.DB()
		if cerr != nil {
			t.Errorf("reach the underlying pool to close it: %v", cerr)
			return
		}
		if cerr := sqlDB.Close(); cerr != nil {
			t.Errorf("close the fixture's pool: %v", cerr)
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&model.Connector{}, &model.ConnectorVersion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := secrets.NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate the secret store: %v", err)
	}
	kek, err := secrets.NewInstanceKeyProvider([]byte(testWireRootKey))
	if err != nil {
		t.Fatalf("build the instance key provider: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.ConnectorWrite)}})
	api := model.NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// wireDSNName turns a test's name into something safe to sit in a SQLite URI. Subtest
// names carry "/" and Go appends "#01" whenever one name repeats within a run — and a
// URI reads "#" as the start of a FRAGMENT, so the name silently truncates and sqlite
// falls back to an ON-DISK FILE in the working directory.
func wireDSNName(t *testing.T) string {
	out := []rune(t.Name())
	for i, r := range out {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			out[i] = '_'
		}
	}
	return string(out)
}

const updateConnectorDoc = `
mutation ($token: String!, $request: ConnectorUpdateRequest!) {
  updateConnector(token: $token, request: $request) { token }
}`

// 🔴 THE REJECTION HAS TO BE A REQUEST ERROR, AND CHECKING ONLY "there was an error" IS
// A FAIL-OPEN: every request below is addressed to a token that names no connector, so
// the RESOLVER also errors, and "there was an error" would be satisfied by the not-found
// whether or not the schema refused anything. A request error — a validation failure,
// which is what an undeclared field produces — arrives with a nil Path.
func assertRefusedBeforeTheResolver(t *testing.T, request map[string]any) {
	t.Helper()
	ctx := connectorWireCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, updateConnectorDoc, "", map[string]any{
		"token": "whatever", "request": request,
	})
	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("%v reached the resolver instead of being refused by the schema (errors: %v)",
		request, res.Errors)
}

// The token is the mutation's own argument and is deliberately not a member of the
// update input, so moving a connector's token through an update is UNREPRESENTABLE
// rather than merely refused. The rejection arrives from the unknown-input-field guard,
// which makes this a check on that guard too: a silently dropped field would tell the
// caller their rename succeeded.
func TestConnectorUpdateInputCannotCarryAToken(t *testing.T) {
	assertRefusedBeforeTheResolver(t, map[string]any{"token": "moved"})
}

// `request` is non-null, so a caller who sends nothing gets a request error rather than
// a silently successful no-op that returns the connector unchanged.
func TestConnectorUpdateRequiresARequest(t *testing.T) {
	ctx := connectorWireCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `mutation ($token: String!) {
	  updateConnector(token: $token, request: null) { token }
	}`, "", map[string]any{"token": "whatever"})
	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("a null request reached the resolver instead of being refused by the schema "+
		"(errors: %v)", res.Errors)
}

// THE COUNTERWEIGHT, and the reason the two rejections above mean anything: they are
// only safe while a well-formed partial update still parses and reaches the resolver.
// Without this, renaming the input or mistyping a field would make both tests above
// pass for exactly the wrong reason — every request rejected, the guarantee held
// vacuously.
func TestConnectorUpdateAcceptsAWellFormedPartialRequest(t *testing.T) {
	ctx := connectorWireCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, updateConnectorDoc, "", map[string]any{
		"token": "whatever", "request": map[string]any{"name": "Renamed"},
	})
	for _, e := range res.Errors {
		if e.Path == nil {
			t.Fatalf("a well-formed partial update was rejected before the resolver: %v", e)
		}
	}
}

// 🔴 THE THREE STATES OF THE WRITE-ONLY SECRET, DRIVEN OVER THE WIRE. This is the only
// place the ABSENT and NULL spellings can be told apart: a model test builds Go values
// directly and never asks the packer whether the distinction survives a request.
func TestConnectorSecretThreeStatesOverTheWire(t *testing.T) {
	seed := func(t *testing.T) (context.Context, *gql.Schema) {
		t.Helper()
		ctx := connectorWireCtx(t)
		schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
		res := schema.Exec(ctx, `
mutation ($request: ConnectorCreateRequest!) {
  createConnector(request: $request) { token }
}`, "", map[string]any{
			"request": map[string]any{
				"token": "pager", "type": "mqtt", "secret": "s3cret",
				"config": `{"urls":["tcp://broker:1883"],"topic":"alerts"}`,
			},
		})
		if len(res.Errors) > 0 {
			t.Fatalf("seed: %v", res.Errors)
		}
		return ctx, schema
	}
	stored := func(t *testing.T, ctx context.Context) string {
		t.Helper()
		api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
		found, err := api.ConnectorsByToken(ctx, []string{"pager"})
		if err != nil || len(found) != 1 {
			t.Fatalf("reload the connector: %v (%d rows)", err, len(found))
		}
		ref, err := model.ConnectorSecretRef(ctx, found[0].ID)
		if err != nil {
			t.Fatalf("secret ref: %v", err)
		}
		value, err := api.Secrets.Resolve(ctx, ref)
		if err != nil {
			return ""
		}
		return string(value)
	}
	update := func(t *testing.T, ctx context.Context, schema *gql.Schema, request map[string]any) {
		t.Helper()
		if res := schema.Exec(ctx, updateConnectorDoc, "", map[string]any{
			"token": "pager", "request": request,
		}); len(res.Errors) > 0 {
			t.Fatalf("update: %v", res.Errors)
		}
	}

	t.Run("omitted preserves", func(t *testing.T) {
		ctx, schema := seed(t)
		update(t, ctx, schema, map[string]any{"name": "Renamed"})
		if got := stored(t, ctx); got != "s3cret" {
			t.Fatalf("secret = %q after an update that never mentioned it, want it preserved", got)
		}
	})

	t.Run("a value rotates", func(t *testing.T) {
		ctx, schema := seed(t)
		update(t, ctx, schema, map[string]any{"secret": "rotated"})
		if got := stored(t, ctx); got != "rotated" {
			t.Fatalf("secret = %q, want %q", got, "rotated")
		}
	})

	t.Run("an explicit null deletes", func(t *testing.T) {
		ctx, schema := seed(t)
		update(t, ctx, schema, map[string]any{"secret": nil})
		if got := stored(t, ctx); got != "" {
			t.Fatalf("secret = %q after an explicit null, want it deleted", got)
		}
	})
}

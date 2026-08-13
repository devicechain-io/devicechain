// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// This file exercises the multi-status search filter and the dispatch claim
// THROUGH THE SCHEMA, executing real GraphQL against a real resolver over a real
// (sqlite) database.
//
// 🔴 It sends the criteria as a VARIABLE, never as a query literal, because that
// is the path every real client takes — the console, the SDKs, dcctl, codegen and
// the LwM2M drain all send variables — and it is the path where this library
// historically diverged from the spec by silently discarding input-object entries
// the schema does not define. A test that inlined the criteria as a literal would
// exercise the one code path no caller uses, and would keep passing while the
// wire contract broke underneath it.
//
// Calling the resolver methods directly in Go would prove even less: it would
// skip schema parsing, input coercion and the struct binding, which is precisely
// where a field name or a nullability mismatch turns into a filter that is
// silently ignored.

// withServiceAuthorities attaches verified SERVICE-token claims granting exactly
// the given authorities. Claims are the only thing auth.Authorize consults, and
// the context key is unexported precisely so a resolver cannot forge them — a test
// that wants to exercise the real gate has to go through WithClaims too.
//
// A service token because the claim mutation is service-plane only, and the point
// of these tests is to drive the resolvers the way their real caller does. Tests
// that need to be REFUSED build their own access-token claims inline.
func withServiceAuthorities(ctx context.Context, authorities ...auth.Authority) context.Context {
	held := make([]string, 0, len(authorities))
	for _, a := range authorities {
		held = append(held, string(a))
	}
	return auth.WithClaims(ctx, &auth.Claims{
		Authorities: held,
		TokenType:   auth.TokenTypeService,
	})
}

func newWireTestCtx(t *testing.T) (context.Context, *model.Api) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("failed to register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&model.Command{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	if err := rdb.CreateTenantTokenIndex(db, &model.Command{}); err != nil {
		t.Fatalf("failed to create tenant token index: %v", err)
	}
	mgr := &rdb.RdbManager{Database: db}
	api := model.NewApi(mgr)

	ctx := core.WithTenant(context.Background(), "A")
	ctx = withServiceAuthorities(ctx, auth.CommandRead, auth.CommandWrite)
	ctx = context.WithValue(ctx, gqlcore.ContextRdbKey, mgr)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return ctx, api
}

// seedCommand creates a command and drives it to the requested state. HELD has no
// public transition yet (the presence gate is a later slice), so it is written
// directly rather than through an API call that does not exist.
func seedCommand(t *testing.T, ctx context.Context, api *model.Api, token string, status model.CommandStatus) {
	t.Helper()
	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: token, DeviceToken: "device-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand %s failed: %v", token, err)
	}
	if status == model.CommandQueued {
		return
	}
	if err := api.RDB.DB(ctx).Model(&model.Command{}).
		Where("id = ?", created.ID).Update("status", status.String()).Error; err != nil {
		t.Fatalf("forcing %s to %s failed: %v", token, status, err)
	}
}

// exec runs one operation and fails the test on any GraphQL error.
func exec(t *testing.T, ctx context.Context, query string, vars map[string]any) json.RawMessage {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, query, "", vars)
	if len(res.Errors) > 0 {
		t.Fatalf("GraphQL execution failed: %v", res.Errors)
	}
	return res.Data
}

// TestStatusesFilterBindsThroughAVariable is the wire test for the multi-status
// filter the LwM2M wake drain depends on: it asks for the two states a waking
// device's backlog lives in (HELD ∪ SENT) in a single query.
//
// The assertion is on the ROWS RETURNED, not on the criteria struct, because the
// failure this guards against is a filter that binds to nothing and therefore
// filters nothing — which returns MORE rows, not an error.
func TestStatusesFilterBindsThroughAVariable(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	seedCommand(t, ctx, api, "w-queued", model.CommandQueued)
	seedCommand(t, ctx, api, "w-held", model.CommandHeld)
	seedCommand(t, ctx, api, "w-sent", model.CommandSent)
	seedCommand(t, ctx, api, "w-successful", model.CommandSuccessful)

	const query = `query($criteria: CommandSearchCriteria!) {
	  commands(criteria: $criteria) { results { token status } }
	}`

	// The literal strings the drain puts on the wire. Written out rather than
	// referenced through model.CommandHeld so that renaming the constant cannot
	// quietly rename the wire value on both sides at once — a test comparing a
	// constant to itself would pin nothing.
	data := exec(t, ctx, query, map[string]any{
		"criteria": map[string]any{
			"pageNumber": 1,
			"pageSize":   100,
			"statuses":   []any{"HELD", "SENT"},
		},
	})

	got := decodeTokens(t, data)
	if len(got) != 2 || !got["w-held"] || !got["w-sent"] {
		t.Fatalf("statuses:[HELD,SENT] returned %v; want exactly w-held and w-sent. "+
			"Extra rows mean the filter bound to nothing and was silently ignored", got)
	}
}

// TestUnknownCriteriaFieldIsRejected is the counterweight: the filter above is
// only trustworthy while a MISNAMED field still fails loudly. This repo carries a
// patched fork of graphql-go for exactly this reason — upstream discards
// input-object entries the schema does not define when they arrive through a
// variable, which turns a typo into a successful call that quietly ignores the
// filter.
func TestUnknownCriteriaFieldIsRejected(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	seedCommand(t, ctx, api, "u-held", model.CommandHeld)

	const query = `query($criteria: CommandSearchCriteria!) {
	  commands(criteria: $criteria) { results { token } }
	}`
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, query, "", map[string]any{
		"criteria": map[string]any{
			"pageNumber": 1,
			"pageSize":   100,
			// Singular, and wrong. Must be a request error, not a silent no-filter.
			"status_es": []any{"HELD"},
		},
	})
	if len(res.Errors) == 0 {
		t.Fatal("a misnamed criteria field sent through a variable must be REJECTED; " +
			"accepting it returns an unfiltered result that looks like a successful query")
	}
}

// TestMarkCommandSentClaimsThroughTheSchema exercises the claim a dispatcher makes
// before it actuates hardware, over the wire, including the losing second claim.
//
// The boolean is the whole point: a caller decides whether to move physical
// equipment based on it, so it must reflect whether the conditional UPDATE
// matched — not a status read back afterwards, which cannot tell "I claimed it"
// from "someone else did a millisecond ago".
func TestMarkCommandSentClaimsThroughTheSchema(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	seedCommand(t, ctx, api, "wire-claim", model.CommandHeld)

	const mutation = `mutation($token: String!) { markCommandSent(token: $token) }`

	var first struct {
		MarkCommandSent bool `json:"markCommandSent"`
	}
	if err := json.Unmarshal(exec(t, ctx, mutation, map[string]any{"token": "wire-claim"}), &first); err != nil {
		t.Fatalf("decoding the first claim failed: %v", err)
	}
	if !first.MarkCommandSent {
		t.Fatal("the first claim on a HELD command must win")
	}

	var second struct {
		MarkCommandSent bool `json:"markCommandSent"`
	}
	if err := json.Unmarshal(exec(t, ctx, mutation, map[string]any{"token": "wire-claim"}), &second); err != nil {
		t.Fatalf("decoding the second claim failed: %v", err)
	}
	if second.MarkCommandSent {
		t.Fatal("a second claim must LOSE over the wire; winning it makes the caller actuate the device twice")
	}
}

// TestMarkCommandSentIsServicePlaneOnly pins BOTH halves of the claim's gate.
//
// 🔴 The second half is the one that matters and it is not expressible in the
// authority vocabulary: command:write is a TENANT-tier authority, so every tenant
// user who can issue or cancel a command holds it. A claim is not a harmless
// status edit — it removes a command from the dispatchable set WITHOUT publishing
// it, so the command is never delivered, sits in SENT, and dies as TIMEOUT, a
// terminal that blames the device for a delivery the platform suppressed. Cancel
// records the truth; this would manufacture a lie with nothing in the audit trail
// to contradict it. Only the machine caller that is about to put the command on a
// live session may claim.
func TestMarkCommandSentIsServicePlaneOnly(t *testing.T) {
	const mutation = `mutation($token: String!) { markCommandSent(token: $token) }`

	cases := []struct {
		name         string
		claims       *auth.Claims
		mustBeDenied string
	}{
		{
			name:         "a read-only caller",
			claims:       &auth.Claims{Authorities: []string{string(auth.CommandRead)}, TokenType: auth.TokenTypeAccess},
			mustBeDenied: "markCommandSent must require command:write",
		},
		{
			name: "a tenant user holding command:write",
			claims: &auth.Claims{
				Authorities: []string{string(auth.CommandRead), string(auth.CommandWrite)},
				TokenType:   auth.TokenTypeAccess,
			},
			mustBeDenied: "command:write is a TENANT-tier authority, so an ordinary user holds it; " +
				"letting them claim gives every tenant user a way to suppress a delivery and have the device blamed for it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, api := newWireTestCtx(t)
			seedCommand(t, ctx, api, "authz-claim", model.CommandHeld)

			denied := auth.WithClaims(ctx, tc.claims)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(denied, mutation, "", map[string]any{"token": "authz-claim"})
			if len(res.Errors) == 0 {
				t.Fatalf("%s (the claim was allowed)", tc.mustBeDenied)
			}

			// And the command must be untouched — a refused claim that still mutated
			// the row would be worse than one that succeeded, because the caller
			// believes it did not happen.
			matches, err := api.CommandsByToken(ctx, []string{"authz-claim"})
			if err != nil {
				t.Fatalf("lookup failed: %v", err)
			}
			if len(matches) != 1 || matches[0].Status != model.CommandHeld.String() {
				t.Fatalf("a refused claim must leave the command HELD, got %v", matches)
			}
		})
	}

	// The counterweight: the gate is only worth having while the ONE legitimate
	// caller still gets through. A denial test alone would pass if the mutation
	// were broken for everybody.
	t.Run("a service token is allowed", func(t *testing.T) {
		ctx, api := newWireTestCtx(t)
		seedCommand(t, ctx, api, "svc-claim", model.CommandHeld)

		svc := auth.WithClaims(ctx, &auth.Claims{
			Authorities: []string{string(auth.CommandRead), string(auth.CommandWrite)},
			TokenType:   auth.TokenTypeService,
		})
		var out struct {
			MarkCommandSent bool `json:"markCommandSent"`
		}
		if err := json.Unmarshal(exec(t, svc, mutation, map[string]any{"token": "svc-claim"}), &out); err != nil {
			t.Fatalf("decoding the service claim failed: %v", err)
		}
		if !out.MarkCommandSent {
			t.Fatal("a service token must be able to claim; the LwM2M wake drain is the only caller and it would be dead")
		}
		matches, err := api.CommandsByToken(ctx, []string{"svc-claim"})
		if err != nil {
			t.Fatalf("lookup failed: %v", err)
		}
		if matches[0].Status != model.CommandSent.String() {
			t.Fatalf("a won claim must move the command to SENT, got %s", matches[0].Status)
		}
	})
}

func decodeTokens(t *testing.T, data json.RawMessage) map[string]bool {
	t.Helper()
	var out struct {
		Commands struct {
			Results []struct {
				Token  string `json:"token"`
				Status string `json:"status"`
			} `json:"results"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding results failed: %v", err)
	}
	got := make(map[string]bool, len(out.Commands.Results))
	for _, r := range out.Commands.Results {
		got[r.Token] = true
	}
	return got
}

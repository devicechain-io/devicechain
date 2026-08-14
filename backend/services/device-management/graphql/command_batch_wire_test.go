// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// batchApiOf pulls the Api back out of a wire context for seeding.
func batchApiOf(t *testing.T, ctx context.Context) *model.Api {
	t.Helper()
	api, ok := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if !ok {
		t.Fatal("no api in context")
	}
	return api
}

// newBatchWireCtx builds a schema-executable context over an in-memory DB carrying the
// tables the batch target/validation queries touch.
func newBatchWireCtx(t *testing.T) context.Context {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.CommandDefinition{}, &model.MetricDefinition{},
		&model.DetectionRule{}, &model.EntityAttribute{}, &model.EntityGroup{},
		&model.EntityGroupVersion{}, &model.EntityGroupMembership{}, &model.EntityGroupFacetRef{},
		&model.DetectionRuleScopeRef{}, &model.EntityRelationship{},
		&model.EntityRelationshipType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, auth.DeviceRead)
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// TestTheBatchGateAnswersRefusalsOnTheWire drives the batch gate through the REAL schema.
//
// 🔴 A MODEL TEST CANNOT COVER THIS: the resolver is a separate layer that can be wired
// wrong while every model assertion stays green. A method that compiled, satisfied the
// schema parse and returned an empty list every time would leave the model perfectly
// correct and unreachable — command-delivery would see a healthy verdict for every device
// in a fleet, and enqueue commands the gate exists to refuse. This drives the real schema
// with variables, the path command-delivery's validator uses.
func TestTheBatchGateAnswersRefusalsOnTheWire(t *testing.T) {
	ctx := newBatchWireCtx(t)
	api := batchApiOf(t, ctx)
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{
		Token: "real-1", DeviceTypeToken: "sensor",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	// A variable, not a query literal — the path command-delivery's validator uses.
	const query = `query($deviceTokens: [String!]!, $commandKey: String!, $payload: String) {
	  validateCommandEnqueueBatch(deviceTokens: $deviceTokens, commandKey: $commandKey, payload: $payload) {
	    deviceToken
	    code
	    reason
	  }
	}`
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, query, "", map[string]any{
		"deviceTokens": []any{"real-1", "ghost-1", "ghost-2"},
		"commandKey":   "reboot",
		"payload":      nil,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("graphql errors: %v", res.Errors)
	}

	var out struct {
		Refusals []struct {
			DeviceToken string `json:"deviceToken"`
			Code        string `json:"code"`
			Reason      string `json:"reason"`
		} `json:"validateCommandEnqueueBatch"`
	}
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.Refusals) != 2 {
		t.Fatalf("expected exactly the two ghosts to be refused, got %d: %+v",
			len(out.Refusals), out.Refusals)
	}
	for _, r := range out.Refusals {
		if r.DeviceToken == "real-1" {
			t.Fatal("a device with an unconstrained profile was refused")
		}
		if r.Code != string(model.RejectDeviceNotFound) {
			t.Fatalf("%s refused as %q, want %q", r.DeviceToken, r.Code, model.RejectDeviceNotFound)
		}
		if r.Reason == "" {
			t.Fatalf("%s carries no reason — the field is non-null on the wire", r.DeviceToken)
		}
	}
}

// TestAHealthyFleetProducesNoRefusalsAtAll pins the whole-document shape for the case
// that dominates in production: nothing wrong with the fleet.
//
// ⚠️ IT DOES NOT TEST nil-VERSUS-EMPTY, AND AN EARLIER VERSION OF THIS COMMENT CLAIMED IT
// DID. That claim was false and was disproved by mutation: making the resolver return a
// nil slice leaves this test — and the whole package — passing, because graphql-go renders
// a nil Go slice as [] on a [X!]! field. Written as a defence against nil this was
// decoration; what it actually earns is the exact-JSON assertion below, which fails if the
// field is renamed or if a device that should pass silently acquires a refusal.
func TestAHealthyFleetProducesNoRefusalsAtAll(t *testing.T) {
	ctx := newBatchWireCtx(t)
	api := batchApiOf(t, ctx)
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{
		Token: "real-1", DeviceTypeToken: "sensor",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	const query = `query($deviceTokens: [String!]!) {
	  validateCommandEnqueueBatch(deviceTokens: $deviceTokens, commandKey: "reboot") { deviceToken }
	}`
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, query, "", map[string]any{"deviceTokens": []any{"real-1"}})
	if len(res.Errors) > 0 {
		t.Fatalf("graphql errors: %v", res.Errors)
	}
	if got := string(res.Data); got != `{"validateCommandEnqueueBatch":[]}` {
		t.Fatalf("a healthy fleet must marshal as an empty list, got %s", got)
	}
}

// 🔴 TestAMalformedCursorIsRefusedRatherThanRestartingTheWalk.
//
// This is a REGRESSION TEST FOR A LIVE BUG, not a hypothetical. parseCursor used
// fmt.Sscanf("%d"), which stops at the first non-digit and reports SUCCESS for whatever
// prefix it read — so " 12" and "12abc" both became 12, and "0x10" became 0 WITH NO ERROR.
// Zero means "begin", so that last one silently restarted the walk from the top: a fleet
// write that re-commands every device it has already commanded, and reports success while
// doing it. The comment above the function asserted this could not happen.
//
// The cases below are the ones Sscanf accepted. Each must now be refused.
func TestAMalformedCursorIsRefusedRatherThanRestartingTheWalk(t *testing.T) {
	for _, cursor := range []string{"12abc", "12.5", "0x10", " 12", "0", "-5", "", "999999999999999999999999"} {
		t.Run(cursor, func(t *testing.T) {
			if _, err := parseCursor(cursor); err == nil {
				t.Fatalf("cursor %q was accepted; a cursor this walk never issued must be "+
					"refused, not silently coerced into a page number", cursor)
			}
		})
	}
	// The counterweight: a cursor the resolver actually emits must still round-trip, or
	// the fix above would refuse every legitimate page-two request.
	id, err := parseCursor("4207")
	if err != nil {
		t.Fatalf("a well-formed cursor was refused: %v", err)
	}
	if id != 4207 {
		t.Fatalf("parseCursor(\"4207\") = %d", id)
	}
}

// 🔴 TestTheCursorSurvivesTheWireAsAString is the marshalling test the design depends on.
//
// The cursor is a row id, and GraphQL's Int is a signed 32-bit value: typed as Int, an id
// beyond 2^31 fails to marshal on the one field whose entire job is to not lose members.
// It crosses as a String and must come back parseable — this drives a real two-page walk
// through the schema rather than trusting the resolver's conversion.
func TestTheCursorSurvivesTheWireAsAString(t *testing.T) {
	ctx := newBatchWireCtx(t)
	api := batchApiOf(t, ctx)
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	for _, token := range []string{"d1", "d2", "d3"} {
		if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{
			Token: token, DeviceTypeToken: "sensor",
		}); err != nil {
			t.Fatalf("seed %s: %v", token, err)
		}
	}
	if _, err := api.CreateEntityGroup(ctx, &model.EntityGroupCreateRequest{
		Token: "fleet", MemberType: entity.TypeDevice.String(),
	}); err != nil {
		t.Fatalf("create static group: %v", err)
	}
	for i, token := range []string{"d1", "d2", "d3"} {
		if _, err := api.CreateEntityRelationships(ctx, []*model.EntityRelationshipCreateRequest{{
			Token:            "e" + string(rune('1'+i)),
			RelationshipType: model.MembershipRelationshipType,
			SourceType:       string(entity.TypeGroup), Source: "fleet",
			TargetType: entity.TypeDevice.String(), Target: token,
		}}); err != nil {
			t.Fatalf("add member edge for %s: %v", token, err)
		}
	}

	const query = `query($groupToken: String!, $afterId: String, $limit: Int!) {
	  resolveDeviceGroupTargets(groupToken: $groupToken, afterId: $afterId, limit: $limit) {
	    rejected
	    code
	    deviceTokens
	    nextCursor
	  }
	}`
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	exec := func(afterId any) (tokens []string, cursor *string) {
		t.Helper()
		res := schema.Exec(ctx, query, "", map[string]any{
			"groupToken": "fleet", "afterId": afterId, "limit": int32(2),
		})
		if len(res.Errors) > 0 {
			t.Fatalf("graphql errors: %v", res.Errors)
		}
		var out struct {
			Page struct {
				Rejected     bool     `json:"rejected"`
				Code         *string  `json:"code"`
				DeviceTokens []string `json:"deviceTokens"`
				NextCursor   *string  `json:"nextCursor"`
			} `json:"resolveDeviceGroupTargets"`
		}
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Page.Rejected {
			t.Fatalf("unexpected rejection: %v", out.Page.Code)
		}
		return out.Page.DeviceTokens, out.Page.NextCursor
	}

	first, cursor := exec(nil)
	if len(first) != 2 {
		t.Fatalf("first page = %v, want 2 members", first)
	}
	if cursor == nil {
		t.Fatal("a full page must carry a cursor on the wire")
	}

	// 🔑 THE CURSOR IS FED STRAIGHT BACK IN, exactly as a real caller would. If the
	// String/uint conversion were wrong in either direction this restarts the walk from
	// the beginning — and a fleet write that re-walks re-commands every device it has
	// already commanded, while reporting success.
	second, _ := exec(*cursor)
	if len(second) != 1 || second[0] == first[0] {
		t.Fatalf("second page = %v after cursor %q; the walk did not advance", second, *cursor)
	}
}

// TestAnUnusableGroupIsRejectedOnTheWire, carrying its code — not raised as a GraphQL
// error. command-delivery must be able to tell "your group is wrong" (relay it) from
// "device-management is unreachable" (fail closed, say nothing), and only a code on a
// successful response supports that.
func TestAnUnusableGroupIsRejectedOnTheWire(t *testing.T) {
	ctx := newBatchWireCtx(t)
	api := batchApiOf(t, ctx)
	if _, err := api.CreateEntityGroup(ctx, &model.EntityGroupCreateRequest{
		Token: "north-plant", MemberType: entity.TypeArea.String(),
	}); err != nil {
		t.Fatalf("create area group: %v", err)
	}

	const query = `query($groupToken: String!) {
	  resolveDeviceGroupTargets(groupToken: $groupToken, limit: 100) {
	    rejected
	    code
	    deviceTokens
	  }
	}`
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, query, "", map[string]any{"groupToken": "north-plant"})
	if len(res.Errors) > 0 {
		t.Fatalf("a decided refusal must not be a GraphQL error: %v", res.Errors)
	}

	var out struct {
		Page struct {
			Rejected     bool     `json:"rejected"`
			Code         *string  `json:"code"`
			DeviceTokens []string `json:"deviceTokens"`
		} `json:"resolveDeviceGroupTargets"`
	}
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Page.Rejected {
		t.Fatal("an area group was accepted as a command target on the wire")
	}
	if out.Page.Code == nil || *out.Page.Code != string(model.RejectGroupNotADeviceGroup) {
		t.Fatalf("code = %v, want %q", out.Page.Code, model.RejectGroupNotADeviceGroup)
	}
	// 🔑 The empty list on a rejection must not read as "the group has no members".
	// That is why `rejected` is its own field rather than being inferred from emptiness.
	if len(out.Page.DeviceTokens) != 0 {
		t.Fatalf("a rejected page carried tokens: %v", out.Page.DeviceTokens)
	}
}

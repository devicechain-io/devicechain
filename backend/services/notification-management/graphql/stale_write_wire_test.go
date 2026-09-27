// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// The precondition on updateNotificationPolicy, through the served schema as production
// parses it (gqlcore.MustParseSchema, not the bare library), with the variables a client
// sends.

const stalePolicyDoc = `mutation($token: String!, $request: NotificationPolicyUpdateRequest!, $expectedUpdatedAt: String) {
  updateNotificationPolicy(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {
    token
    name
    updatedAt
  }
}`

func execStalePolicy(t *testing.T, ctx context.Context, request map[string]any, expected any) *gql.Response {
	t.Helper()
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	return schema.Exec(ctx, stalePolicyDoc, "", map[string]any{
		"token": "ops-policy", "request": request, "expectedUpdatedAt": expected,
	})
}

func storedPolicyName(t *testing.T, ctx context.Context) (name, updatedAt string) {
	t.Helper()
	found, err := apiOf(t, ctx).NotificationPoliciesByToken(ctx, []string{"ops-policy"})
	if err != nil || len(found) != 1 {
		t.Fatalf("reload the policy: err=%v rows=%d", err, len(found))
	}
	return found[0].Name.String, *gqlcore.FormatTime(found[0].UpdatedAt)
}

// A stale precondition is refused on the wire with the sentence clients recognise, with no
// CONFLICT code, and nothing is written.
func TestUpdatePolicyStalePreconditionOnTheWire(t *testing.T) {
	ctx := newWireCtx(t)
	seedPolicyWithOneRule(t, ctx)

	res := execStalePolicy(t, ctx, map[string]any{"name": "Changed"}, "2000-01-01T00:00:00Z")
	if len(res.Errors) != 1 {
		t.Fatalf("want exactly one error, got %d: %v", len(res.Errors), res.Errors)
	}
	const want = "notification policy was modified by another writer; reload and try again"
	if got := res.Errors[0].Message; got != want {
		t.Fatalf("error message is %q, want %q", got, want)
	}
	if code, ok := res.Errors[0].Extensions["code"]; ok {
		t.Fatalf("a stale save carries extensions.code %v; it must carry none", code)
	}
	if name, _ := storedPolicyName(t, ctx); name != "Original" {
		t.Fatalf("stored name is %q after a refused save, want %q", name, "Original")
	}
}

// The counterweight: the version the caller read is accepted, and the response carries
// the new stored version to send next time.
func TestUpdatePolicyFreshPreconditionOnTheWire(t *testing.T) {
	ctx := newWireCtx(t)
	seedPolicyWithOneRule(t, ctx)
	_, current := storedPolicyName(t, ctx)

	res := execStalePolicy(t, ctx, map[string]any{"name": "Changed"}, current)
	requireNoErrors(t, res)
	name, stored := storedPolicyName(t, ctx)
	if name != "Changed" {
		t.Fatalf("stored name is %q, want %q", name, "Changed")
	}
	var data struct {
		UpdateNotificationPolicy struct {
			UpdatedAt string `json:"updatedAt"`
		} `json:"updateNotificationPolicy"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	if got := data.UpdateNotificationPolicy.UpdatedAt; got != stored || got == current {
		t.Fatalf("response updatedAt %q, want the new stored %q (was %q)", got, stored, current)
	}
}

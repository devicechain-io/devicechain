// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// Keep all three served coordinates, including the search input, deprecated from
// v1.10. The window guard discovers these directives in the release's tagged SDL.
func TestPolicyDeviceTypeCoordinatesAreDeprecated(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, name := range []string{
		"NotificationPolicy", "NotificationPolicyCreateRequest", "NotificationPolicySearchCriteria",
	} {
		t.Run(name, func(t *testing.T) {
			var directives ast.DirectiveList
			switch typ := schema.AST().Types[name].(type) {
			case *ast.ObjectTypeDefinition:
				field := typ.Fields.Get("deviceTypeToken")
				if field == nil || field.Type.String() != "String" {
					t.Fatal("deviceTypeToken must remain an optional String")
				}
				directives = field.Directives
			case *ast.InputObject:
				field := typ.Values.Get("deviceTypeToken")
				if field == nil || field.Type.String() != "String" {
					t.Fatal("deviceTypeToken must remain an optional String")
				}
				directives = field.Directives
			default:
				t.Fatalf("missing served type %s", name)
			}
			dep := directives.Get("deprecated")
			if dep == nil {
				t.Fatal("deviceTypeToken must be deprecated, not removed or re-enabled")
			}
			value, ok := dep.Arguments.Get("reason")
			if !ok {
				t.Fatal("deprecation must explain the unchanged behavior and future removal")
			}
			literal, ok := value.(*ast.PrimitiveValue)
			if !ok {
				t.Fatal("deprecation reason must be a string")
			}
			reason, err := strconv.Unquote(literal.Text)
			if err != nil {
				t.Fatal(err)
			}
			for _, phrase := range []string{"v1.10", "never applied", "future major", "anchor or group", "planned"} {
				if !strings.Contains(reason, phrase) {
					t.Errorf("deprecation reason %q must explain %q", reason, phrase)
				}
			}
		})
	}
}

// Deprecation is metadata only: real variable inputs still refuse a scope, accepted
// policies read null, and the equality filter still matches no API-created policy.
func TestDeprecatedPolicyDeviceTypeKeepsWireBehavior(t *testing.T) {
	ctx := newWireCtx(t)
	seedPolicyWithOneRule(t, ctx)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	for _, scope := range []string{"thermostat", "", "  "} {
		res := schema.Exec(ctx, `mutation($request: NotificationPolicyCreateRequest!) {
			createNotificationPolicy(request: $request) { token deviceTypeToken }
		}`, "", map[string]any{"request": map[string]any{
			"token": "candidate", "enabled": true, "deviceTypeToken": scope,
			"rules": []any{map[string]any{"severity": "*", "channelToken": "smtp-ops"}},
		}})
		if scope == "thermostat" {
			if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "deviceTypeToken") {
				t.Fatalf("nonblank scope must still be refused: data=%s errors=%v", res.Data, res.Errors)
			}
			found, err := apiOf(t, ctx).NotificationPoliciesByToken(ctx, []string{"candidate"})
			if err != nil || len(found) != 0 {
				t.Fatalf("refusal must leave no policy: rows=%d err=%v", len(found), err)
			}
			continue
		}
		if len(res.Errors) != 0 || string(res.Data) != `{"createNotificationPolicy":{"token":"candidate","deviceTypeToken":null}}` {
			t.Fatalf("blank scope must still create and return null: data=%s errors=%v", res.Data, res.Errors)
		}
		if _, err := apiOf(t, ctx).DeleteNotificationPolicy(ctx, "candidate"); err != nil {
			t.Fatal(err)
		}
	}

	for _, scope := range []any{nil, "thermostat", "", "  "} {
		criteria := map[string]any{"pageNumber": 1, "pageSize": 10}
		if scope != nil {
			criteria["deviceTypeToken"] = scope
		}
		res := schema.Exec(ctx, `query($criteria: NotificationPolicySearchCriteria!) {
			notificationPolicies(criteria: $criteria) { results { token deviceTypeToken } }
		}`, "", map[string]any{"criteria": criteria})
		if len(res.Errors) != 0 {
			t.Fatalf("filter %v: %v", scope, res.Errors)
		}
		var data struct {
			Policies struct {
				Results []map[string]json.RawMessage `json:"results"`
			} `json:"notificationPolicies"`
		}
		if err := json.Unmarshal(res.Data, &data); err != nil {
			t.Fatal(err)
		}
		if scope == nil {
			if len(data.Policies.Results) != 1 || string(data.Policies.Results[0]["token"]) != `"ops-policy"` ||
				string(data.Policies.Results[0]["deviceTypeToken"]) != "null" {
				t.Fatalf("unfiltered read must return the tenant-wide policy with null scope: %s", res.Data)
			}
		} else if len(data.Policies.Results) != 0 {
			t.Fatalf("filter %q must still match no tenant-wide policy: %s", scope, res.Data)
		}
	}
}

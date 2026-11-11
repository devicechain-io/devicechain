// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-outbound-connectors/model"
)

func TestConnectorUpdateStaleWriteOverTheWire(t *testing.T) {
	ctx := connectorWireCtx(t)
	ctx = auth.WithClaims(ctx, &auth.Claims{Authorities: []string{
		string(auth.ConnectorRead), string(auth.ConnectorWrite),
	}})
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if err := rdb.CreateTenantTokenIndex(api.RDB.Database, &model.Connector{}); err != nil {
		t.Fatalf("create connector token index: %v", err)
	}
	// Use the served boundary, which also translates duplicate-token driver errors.
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	create := `mutation ($token: String!) {
	  createConnector(request: {token: $token, name: "Original", description: "Original description",
	    type: "mqtt", config: "{\"urls\":[\"tcp://broker:1883\"],\"topic\":\"alerts\"}"}) { token }
	}`
	if res := schema.Exec(ctx, create, "", map[string]any{"token": "pager"}); len(res.Errors) != 0 {
		t.Fatalf("create: %v", res.Errors)
	}
	read := func() map[string]any {
		t.Helper()
		res := schema.Exec(ctx, `{connector(token: "pager") {
		  token name description type config hasSecret updatedAt
		}}`, "", nil)
		if len(res.Errors) != 0 {
			t.Fatalf("read: %v", res.Errors)
		}
		var data struct{ Connector map[string]any }
		if err := json.Unmarshal(res.Data, &data); err != nil || data.Connector == nil {
			t.Fatalf("decode connector: %v (%s)", err, res.Data)
		}
		return data.Connector
	}
	baseline := read()
	update := `mutation ($request: ConnectorUpdateRequest!, $expected: String!) {
	  updateConnector(token: "pager", request: $request, expectedUpdatedAt: $expected) { updatedAt }
	}`
	res := schema.Exec(ctx, update, "", map[string]any{
		"expected": baseline["updatedAt"], "request": map[string]any{"name": "Current"},
	})
	if len(res.Errors) != 0 {
		t.Fatalf("first update: %v", res.Errors)
	}
	current := read()
	if current["name"] != "Current" || current["updatedAt"] == baseline["updatedAt"] {
		t.Fatalf("first update did not advance the baseline: %v", current)
	}
	res = schema.Exec(ctx, update, "", map[string]any{
		"expected": baseline["updatedAt"],
		"request": map[string]any{"name": "Stale", "description": nil,
			"config": `{"urls":["tcp://other:1883"],"topic":"stale"}`, "secret": "stale-secret"},
	})
	if len(res.Errors) != 1 || res.Errors[0].Extensions["code"] != "STALE_WRITE" {
		t.Fatalf("want one STALE_WRITE error, got %v", res.Errors)
	}
	if res.Errors[0].Message != "connector was modified by another writer; reload and try again" {
		t.Fatalf("legacy message changed: %q", res.Errors[0].Message)
	}
	if after := read(); !reflect.DeepEqual(after, current) {
		t.Fatalf("stale write changed current values: before %v, after %v", current, after)
	}
	// A duplicate token keeps its existing meaning and code.
	res = schema.Exec(ctx, create, "", map[string]any{"token": "pager"})
	if len(res.Errors) != 1 || res.Errors[0].Extensions["code"] != "CONFLICT" {
		t.Fatalf("want duplicate-token CONFLICT, got %v", res.Errors)
	}
}

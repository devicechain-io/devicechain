// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func TestCustomerTypeExpectedUpdatedAt(t *testing.T) {
	ctx := newPartialUpdateWireCtx(t)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	api.RDB.Database.NowFunc = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	name := "original"
	_, err := api.CreateCustomerType(ctx, &model.CustomerTypeCreateRequest{Token: "org", Name: &name})
	require.NoError(t, err)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	const doc = `mutation($expected: String, $request: CustomerTypeUpdateRequest!) {
		updateCustomerType(token:"org", request:$request, expectedUpdatedAt:$expected) { name updatedAt }
	}`
	version := func() string {
		rows, err := api.CustomerTypesByToken(ctx, []string{"org"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0].UpdatedAt.Format(time.RFC3339Nano)
	}
	initial := version()
	response := schema.Exec(ctx, doc, "", map[string]any{"expected": initial, "request": map[string]any{"name": "winner"}})
	require.Empty(t, response.Errors)
	var data struct {
		UpdateCustomerType struct {
			Name      string
			UpdatedAt string
		}
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.Equal(t, "winner", data.UpdateCustomerType.Name)
	require.Equal(t, version(), data.UpdateCustomerType.UpdatedAt)
	require.NotEqual(t, initial, version())
	winnerVersion := version()
	for _, expected := range []string{initial, "", "invalid"} {
		for _, request := range []map[string]any{{"name": "loser"}, {}} {
			response = schema.Exec(ctx, doc, "", map[string]any{"expected": expected, "request": request})
			require.Len(t, response.Errors, 1)
			require.Contains(t, response.Errors[0].Message, "modified by another writer")
			require.Equal(t, winnerVersion, version())
		}
	}
	response = schema.Exec(ctx, `mutation { updateCustomerType(token:"org", request:{name:"legacy"}) { name } }`, "", nil)
	require.Empty(t, response.Errors)
	require.NotEqual(t, winnerVersion, version(), "legacy writes must invalidate old versions with a frozen clock")
	response = schema.Exec(ctx, doc, "", map[string]any{"expected": winnerVersion, "request": map[string]any{"name": "stale"}})
	require.Len(t, response.Errors, 1)
	response = schema.Exec(ctx, doc, "", map[string]any{"expected": version(), "request": map[string]any{"name": nil}})
	require.Empty(t, response.Errors)
	rows, err := api.CustomerTypesByToken(ctx, []string{"org"})
	require.NoError(t, err)
	require.False(t, rows[0].Name.Valid)
}

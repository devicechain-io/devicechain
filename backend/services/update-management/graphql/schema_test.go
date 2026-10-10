// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// TestSchemaParses validates that the GraphQL schema parses against the resolver —
// every schema field must have a matching resolver method.
func TestSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}

// The plane is explicitly unimplemented: executing its one query through the real
// schema must produce no data and an error carrying the NOT_IMPLEMENTED code. An empty
// string, or a null without an error, would read as a working API with nothing in it.
func TestEveryOperationFailsLoudlyWithItsCode(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(context.Background(), `{ updateManagementInfo }`, "", nil)

	require.Len(t, resp.Errors, 1, "an unimplemented operation must answer with an error")
	require.Equal(t, NotImplementedCode, resp.Errors[0].Extensions["code"])
	require.Contains(t, resp.Errors[0].Message, "not implemented")

	// The field is non-null, so the error must null the whole data object rather than
	// leaving a plausible empty value behind.
	require.JSONEq(t, `null`, string(resp.Data),
		"an unimplemented plane must not answer with data")
}

// Every field the schema's Query type declares must be one this test executes, so a field
// added later without its own failing-loudly check — or with a real implementation that
// should take this one's place — is noticed here.
func TestTheQueryTypeDeclaresOnlyTheProbe(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(context.Background(),
		`{ __schema { queryType { fields { name } } mutationType { name } subscriptionType { name } } }`,
		"", nil)
	require.Empty(t, resp.Errors)

	var got struct {
		Schema struct {
			QueryType struct {
				Fields []struct{ Name string }
			}
			MutationType     *struct{ Name string }
			SubscriptionType *struct{ Name string }
		} `json:"__schema"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &got))
	names := []string{}
	for _, f := range got.Schema.QueryType.Fields {
		names = append(names, f.Name)
	}
	require.Equal(t, []string{"updateManagementInfo"}, names)
	require.Nil(t, got.Schema.MutationType, "the unimplemented plane declares no mutations")
	require.Nil(t, got.Schema.SubscriptionType, "the unimplemented plane declares no subscriptions")
}

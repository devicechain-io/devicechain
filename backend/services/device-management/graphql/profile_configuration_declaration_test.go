// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

const setConfigDeclMutation = `
mutation ($token: String!, $keys: [ConfigurationKeyInput!]!) {
  setDeviceProfileConfigurationDeclaration(token: $token, keys: $keys) {
    token
    configurationDeclaration { key valueType description }
  }
}`

// The declaration is set and read back over the wire with real variables. The two keys
// carry different types and only one has a description, so a resolver that crossed
// fields, or dropped an absent description to "", would show.
func TestGraphQLSetsAndReadsConfigurationDeclaration(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "thermostat"},
	})

	data := execProfileGql(t, ctx, setConfigDeclMutation, map[string]any{
		"token": "thermostat",
		"keys": []any{
			map[string]any{"key": "sampleIntervalSeconds", "valueType": "LONG", "description": "how often"},
			map[string]any{"key": "mode", "valueType": "STRING"},
		},
	})
	var parsed struct {
		Set struct {
			Decl []struct {
				Key         string  `json:"key"`
				ValueType   string  `json:"valueType"`
				Description *string `json:"description"`
			} `json:"configurationDeclaration"`
		} `json:"setDeviceProfileConfigurationDeclaration"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	d := parsed.Set.Decl
	if len(d) != 2 || d[0].Key != "sampleIntervalSeconds" || d[0].ValueType != "LONG" ||
		d[0].Description == nil || *d[0].Description != "how often" ||
		d[1].Key != "mode" || d[1].ValueType != "STRING" || d[1].Description != nil {
		t.Fatalf("declaration did not round trip: %+v", d)
	}

	// A profile that never declared reads an empty list, not null.
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "plain"},
	})
	data = execProfileGql(t, ctx, `query { deviceProfilesByToken(tokens: ["plain"]) { configurationDeclaration { key } } }`, nil)
	if string(data) != `{"deviceProfilesByToken":[{"configurationDeclaration":[]}]}` {
		t.Fatalf("undeclared profile read %s", data)
	}
}

// An invalid key is refused as a whole, over the wire, and nothing is stored; an unknown
// input field is rejected by the schema (the forked library's job, shown here for the
// new input).
func TestGraphQLRefusesInvalidConfigurationDeclaration(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "thermostat"},
	})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	for name, keys := range map[string][]any{
		"bad grammar": {map[string]any{"key": "has space", "valueType": "LONG"}},
		"bad type":    {map[string]any{"key": "a", "valueType": "FLOAT"}},
		"valid then invalid": {
			map[string]any{"key": "ok", "valueType": "LONG"},
			map[string]any{"key": "../x", "valueType": "LONG"},
		},
		"misnamed field": {map[string]any{"key": "a", "type": "LONG"}},
	} {
		resp := schema.Exec(ctx, setConfigDeclMutation, "", map[string]any{"token": "thermostat", "keys": keys})
		if len(resp.Errors) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	data := execProfileGql(t, ctx, `query { deviceProfilesByToken(tokens: ["thermostat"]) { configurationDeclaration { key } } }`, nil)
	if string(data) != `{"deviceProfilesByToken":[{"configurationDeclaration":[]}]}` {
		t.Fatalf("a refused declaration left state behind: %s", data)
	}
}

// The declaration setter takes the same authority as every other profile edit. The
// read-only refusal and the device:write success are asserted together so neither can
// be bought by breaking the mutation for everyone.
func TestGraphQLSetConfigurationDeclarationRequiresDeviceWrite(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "tracker"},
	})
	vars := map[string]any{
		"token": "tracker",
		"keys":  []any{map[string]any{"key": "mode", "valueType": "STRING"}},
	}
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	readOnly := withAuthorities(ctx, viewerBaseline...)
	if resp := schema.Exec(readOnly, setConfigDeclMutation, "", vars); len(resp.Errors) == 0 {
		t.Fatal("a read-only caller was allowed to set a configuration declaration")
	}
	writer := withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	if resp := schema.Exec(writer, setConfigDeclMutation, "", vars); len(resp.Errors) > 0 {
		t.Fatalf("a device:write caller was refused: %v", resp.Errors)
	}
}

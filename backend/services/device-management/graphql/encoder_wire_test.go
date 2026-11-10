// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func TestEncoderAcceptedAndServedOnGraphQL(t *testing.T) {
	ctx := newPartialUpdateWireCtx(t)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	p := &model.DeviceProfile{}
	p.Token = "encoder-profile"
	require.NoError(t, api.RDB.DB(ctx).Create(p).Error)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	encoder := `{"fPort":10,"fields":[{"param":"level","type":"u8"}]}`
	res := schema.Exec(ctx, `mutation($request:CommandDefinitionCreateRequest!) { createCommandDefinition(request:$request) { token encoder } }`, "", map[string]any{"request": map[string]any{"token": "encoded", "deviceProfileToken": p.Token, "commandKey": "level", "encoder": encoder, "parameterSchema": `[{"name":"level","dataType":"INT"}]`}})
	require.Empty(t, res.Errors)
	var created struct {
		Command struct{ Encoder *string } `json:"createCommandDefinition"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &created))
	require.NotNil(t, created.Command.Encoder)
	require.JSONEq(t, encoder, *created.Command.Encoder)
	dt := &model.DeviceType{ProfileId: &p.ID}
	dt.Token = "encoder-type"
	require.NoError(t, api.RDB.DB(ctx).Create(dt).Error)
	d := &model.Device{DeviceTypeId: dt.ID}
	d.Token = "encoder-device"
	require.NoError(t, api.RDB.DB(ctx).Create(d).Error)
	_, err := api.PublishDeviceProfile(ctx, p.Token, nil, nil, "test")
	require.NoError(t, err)
	res = schema.Exec(ctx, `{ deviceCommandVocabulary(deviceToken:"encoder-device") { commands { commandKey encoder } } }`, "", nil)
	require.Empty(t, res.Errors)
	var published struct {
		Vocabulary struct{ Commands []struct{ Encoder *string } } `json:"deviceCommandVocabulary"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &published))
	require.Len(t, published.Vocabulary.Commands, 1)
	require.NotNil(t, published.Vocabulary.Commands[0].Encoder)
	require.JSONEq(t, encoder, *published.Vocabulary.Commands[0].Encoder)
	// Explicit null clears the draft; omission preserves it.
	for _, request := range []map[string]any{{"name": "renamed"}, {"encoder": nil}} {
		res = schema.Exec(ctx, `mutation($request:CommandDefinitionUpdateRequest!) { updateCommandDefinition(token:"encoded",request:$request) { encoder } }`, "", map[string]any{"request": request})
		require.Empty(t, res.Errors)
		var updated struct {
			Command struct{ Encoder *string } `json:"updateCommandDefinition"`
		}
		require.NoError(t, json.Unmarshal(res.Data, &updated))
		if _, cleared := request["encoder"]; cleared {
			require.Nil(t, updated.Command.Encoder)
		} else {
			require.NotNil(t, updated.Command.Encoder)
			require.JSONEq(t, encoder, *updated.Command.Encoder)
		}
	}
}

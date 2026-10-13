// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// A version that cannot be rendered is a field error over the wire, never a placeholder.
func TestGraphQLThingModelOfAnUnrenderableVersionIsAnError(t *testing.T) {
	ctx := deviceConfigurationCtx(t) // profile "prof", published once
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	enum := `{"not":"a list"}` // valid JSON, so the create path stores it
	if _, err := api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{Token: "t", DeviceProfileToken: "prof",
		MetricKey: "temp", DataType: "DOUBLE", Enum: &enum}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, `query { deviceProfileVersions(token: "prof", limit: 1) { version thingModel } }`, "", nil)
	if len(resp.Errors) == 0 {
		t.Fatalf("an unrenderable version was served without an error: %s", resp.Data)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

// A published version's Thing Model is served over the wire as JSON-LD text: a Thing Model
// titled by the profile, carrying the published metric as a property, and the same text
// the model package renders.
func TestGraphQLServesAProfileVersionAsAThingModel(t *testing.T) {
	ctx := deviceConfigurationCtx(t) // profile "prof", published once
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	unit := "Cel"
	if _, err := api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{Token: "t", DeviceProfileToken: "prof",
		MetricKey: "temp", DataType: "DOUBLE", Unit: &unit}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}

	data := execProfileGql(t, ctx, `query { deviceProfileVersions(token: "prof") { version thingModel } }`, nil)
	var out struct {
		Versions []struct {
			Version    int32  `json:"version"`
			ThingModel string `json:"thingModel"`
		} `json:"deviceProfileVersions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Versions) != 2 || out.Versions[0].Version != 2 {
		t.Fatalf("versions %s", data)
	}
	var tm map[string]any
	if err := json.Unmarshal([]byte(out.Versions[0].ThingModel), &tm); err != nil {
		t.Fatalf("thingModel is not JSON: %v: %s", err, out.Versions[0].ThingModel)
	}
	props, _ := tm["properties"].(map[string]any)
	if tm["@type"] != "tm:ThingModel" || tm["title"] != "prof" || props["temp"] == nil {
		t.Fatalf("thingModel %s", out.Versions[0].ThingModel)
	}
	// Version 1 was published before the metric existed, and its model says so.
	if old := out.Versions[1].ThingModel; old == out.Versions[0].ThingModel {
		t.Fatal("two different versions rendered the same model")
	}
}

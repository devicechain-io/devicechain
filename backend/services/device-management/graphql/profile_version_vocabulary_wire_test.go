// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

// A profile's ACTIVE version answers its metrics and commands by profile token, with no
// device in the tenant: the console builds a board for a device type that has none yet.
// The draft is edited after the publish, so a resolver that read the draft instead of the
// frozen snapshot would show the edit; the command's impact is the PUBLISHED one, and the
// calculated metric is marked.
func TestGraphQLReadsPublishedVocabularyByProfile(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{"request": map[string]any{"token": "meter"}})

	const createMetric = `mutation ($request: MetricDefinitionCreateRequest) {
	  createMetricDefinition(request: $request) { token }
	}`
	const createCommand = `mutation ($request: CommandDefinitionCreateRequest) {
	  createCommandDefinition(request: $request) { token }
	}`
	execProfileGql(t, ctx, createMetric, map[string]any{"request": map[string]any{
		"token": "md-v", "deviceProfileToken": "meter", "metricKey": "voltage", "dataType": "DOUBLE", "unit": "V"}})
	execProfileGql(t, ctx, createMetric, map[string]any{"request": map[string]any{
		"token": "md-p", "deviceProfileToken": "meter", "metricKey": "power", "dataType": "DOUBLE",
		"expression": "voltage * 2.0"}})
	execProfileGql(t, ctx, createCommand, map[string]any{"request": map[string]any{
		"token": "cd-r", "deviceProfileToken": "meter", "commandKey": "reboot", "impact": "DISRUPTIVE"}})

	const read = `query { deviceProfilesByToken(tokens: ["meter"]) {
	  activeProfileVersion { version
	    metrics { metricKey dataType unit calculated }
	    commands { commandKey impact } } } }`

	if got := string(execProfileGql(t, ctx, read, nil)); got !=
		`{"deviceProfilesByToken":[{"activeProfileVersion":null}]}` {
		t.Fatalf("unpublished profile read %s", got)
	}

	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	api.CalculatedMetricsCeiling = ceilingOf(5)
	if _, err := api.PublishDeviceProfile(ctx, "meter", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Draft edits after the publish: a new metric and a changed impact.
	execProfileGql(t, ctx, createMetric, map[string]any{"request": map[string]any{
		"token": "md-x", "deviceProfileToken": "meter", "metricKey": "draftOnly", "dataType": "INT"}})
	execProfileGql(t, ctx, `mutation { updateCommandDefinition(token: "cd-r", request: {impact: "NORMAL"}) { token } }`, nil)

	got := string(execProfileGql(t, ctx, read, nil))
	want := `{"deviceProfilesByToken":[{"activeProfileVersion":{"version":1,` +
		`"metrics":[{"metricKey":"voltage","dataType":"DOUBLE","unit":"V","calculated":false},` +
		`{"metricKey":"power","dataType":"DOUBLE","unit":null,"calculated":true}],` +
		`"commands":[{"commandKey":"reboot","impact":"DISRUPTIVE"}]}}]}`
	if got != want {
		t.Fatalf("published vocabulary did not read from the frozen version:\n got %s\nwant %s", got, want)
	}
}

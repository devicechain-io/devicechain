// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// The hold on a disabled device, and cancel-all, THROUGH THE SCHEMA with the device token sent
// as a variable, like every real client. This is what the console's "N commands held while this
// device is disabled" banner and its Cancel all action ride.
func TestDeviceCommandHoldAndCancelAllCrossTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	seedCommand(t, ctx, api, "h-queued", model.CommandQueued)
	seedCommand(t, ctx, api, "h-held", model.CommandHeld)
	seedCommand(t, ctx, api, "h-sent", model.CommandSent)
	if _, err := api.SetDeviceDisabled(ctx, "device-1", true); err != nil {
		t.Fatal(err)
	}

	const holdQuery = `query($d: String!) { deviceCommandHold(deviceToken: $d) { disabled heldCount } }`
	var hold struct {
		DeviceCommandHold struct {
			Disabled  bool `json:"disabled"`
			HeldCount int  `json:"heldCount"`
		} `json:"deviceCommandHold"`
	}
	if err := json.Unmarshal(exec(t, ctx, holdQuery, map[string]any{"d": "device-1"}), &hold); err != nil {
		t.Fatal(err)
	}
	if !hold.DeviceCommandHold.Disabled || hold.DeviceCommandHold.HeldCount != 2 {
		t.Fatalf("deviceCommandHold = %+v; want disabled with 2 held (the sent command is at the device)", hold.DeviceCommandHold)
	}

	const cancelMutation = `mutation($d: String!) { cancelDeviceCommands(deviceToken: $d) { cancelled } }`
	var cancel struct {
		CancelDeviceCommands struct {
			Cancelled int `json:"cancelled"`
		} `json:"cancelDeviceCommands"`
	}
	if err := json.Unmarshal(exec(t, ctx, cancelMutation, map[string]any{"d": "device-1"}), &cancel); err != nil {
		t.Fatal(err)
	}
	if cancel.CancelDeviceCommands.Cancelled != 2 {
		t.Fatalf("cancelDeviceCommands cancelled %d, want 2", cancel.CancelDeviceCommands.Cancelled)
	}
	if err := json.Unmarshal(exec(t, ctx, holdQuery, map[string]any{"d": "device-1"}), &hold); err != nil {
		t.Fatal(err)
	}
	if hold.DeviceCommandHold.HeldCount != 0 {
		t.Fatalf("held count after cancel-all = %d, want 0", hold.DeviceCommandHold.HeldCount)
	}
}

// Reading the hold needs command:read and cancelling needs command:write: a token with neither is
// refused both.
func TestDeviceHoldOperationsRequireTheirAuthorities(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	bare := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{}, TokenType: auth.TokenTypeService})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for name, query := range map[string]string{
		"read":  `query { deviceCommandHold(deviceToken: "device-1") { heldCount } }`,
		"write": `mutation { cancelDeviceCommands(deviceToken: "device-1") { cancelled } }`,
	} {
		if res := schema.Exec(bare, query, "", nil); len(res.Errors) == 0 {
			t.Errorf("%s: a token without the authority was answered", name)
		}
	}
}

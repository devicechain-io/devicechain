// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// The window's wire surface, driven through the real schema: the SDL parses with the Go
// resolvers (graphql-go refuses a schema whose resolver is missing or mistyped), the slots
// document round-trips canonically, and the two doors command delivery depends on answer in
// the shapes its decoder expects.
func TestMaintenanceWindowWireSurface(t *testing.T) {
	ctx := newPartialUpdateWireCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	exec := func(query string, vars map[string]any) map[string]any {
		t.Helper()
		res := schema.Exec(ctx, query, "", vars)
		if len(res.Errors) > 0 {
			t.Fatalf("%s: %v", query, res.Errors)
		}
		var out map[string]any
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	created := exec(`mutation($r: MaintenanceWindowCreateRequest!) {
	  createMaintenanceWindow(request: $r) { token timezone slots }
	}`, map[string]any{"r": map[string]any{
		"token": "plant", "timezone": "Europe/Berlin",
		// Whitespace and key order differ from the canonical form on purpose.
		"slots": `[ { "durationMinutes": 240, "start": "02:00", "day": "SUNDAY" } ]`,
	}})["createMaintenanceWindow"].(map[string]any)
	if got, want := created["slots"], `[{"day":"SUNDAY","start":"02:00","durationMinutes":240}]`; got != want {
		t.Fatalf("slots = %v, want the canonical form %s", got, want)
	}

	opening := exec(`query { maintenanceWindowOpening(token: "plant", at: "2026-01-14T12:00:00Z") {
	  found openNow dispatchAfter closes } }`, nil)["maintenanceWindowOpening"].(map[string]any)
	if opening["found"] != true || opening["openNow"] != false ||
		opening["dispatchAfter"] != "2026-01-18T01:00:00Z" || opening["closes"] != "2026-01-18T05:00:00Z" {
		t.Fatalf("opening = %v", opening)
	}

	gone := exec(`query { maintenanceWindowOpening(token: "gone") { found openNow } }`, nil)
	if gone["maintenanceWindowOpening"].(map[string]any)["found"] != false {
		t.Fatalf("a missing window must answer found=false, got %v", gone)
	}

	// An empty schedule is an EMPTY decision, and decodes as lists, not nulls: command
	// delivery reads an absent list as a broken response and fails closed.
	sched := exec(`query { commandDispatchSchedule(deviceTokens: ["nobody"], commandKey: "reboot", alreadyHeld: true) {
	  holds { deviceToken } refusals { deviceToken code } ungated undefined impacts { deviceToken impact } } }`, nil)["commandDispatchSchedule"].(map[string]any)
	if holds, ok := sched["holds"].([]any); !ok || len(holds) != 0 {
		t.Fatalf("holds = %v, want an empty list", sched["holds"])
	}
	if refusals, ok := sched["refusals"].([]any); !ok || len(refusals) != 0 {
		t.Fatalf("refusals = %v, want an empty list", sched["refusals"])
	}
	if ungated, ok := sched["ungated"].([]any); !ok || len(ungated) != 0 {
		t.Fatalf("ungated = %v, want an empty list", sched["ungated"])
	}
	if undefined, ok := sched["undefined"].([]any); !ok || len(undefined) != 0 {
		t.Fatalf("undefined = %v, want an empty list", sched["undefined"])
	}
	if impacts, ok := sched["impacts"].([]any); !ok || len(impacts) != 0 {
		t.Fatalf("impacts = %v, want an empty list, not null: an absent field reads as a broken response", sched["impacts"])
	}

	res := schema.Exec(ctx, `mutation($r: MaintenanceWindowCreateRequest!) {
	  createMaintenanceWindow(request: $r) { token } }`, "", map[string]any{"r": map[string]any{
		"token": "bad", "timezone": "Nowhere/Land", "slots": `[{"day":"SUNDAY","start":"02:00","durationMinutes":60}]`}})
	if len(res.Errors) == 0 {
		t.Fatal("an unknown time zone was accepted")
	}
}

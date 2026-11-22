// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apiclient

import (
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
)

func TestLegacyContracts(t *testing.T) {
	for _, lane := range []string{
		"user-management", "user-management/admin", "user-management/settings",
		"device-management", "device-state", "event-management", "event-management/admin",
		"event-processing", "command-delivery", "dashboard-management",
		"outbound-connectors", "notification-management", "update-management", "ai-inference/admin",
	} {
		t.Run(lane, func(t *testing.T) {
			sdl := LegacySchema("https://instance.example/api/" + lane + "/graphql")
			if sdl == "" {
				t.Fatal("missing legacy contract")
			}
			if _, err := graphql.ParseSchema(sdl, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, route := range []string{"/graphql", "/api/not-an-area/graphql", "/api/user-management/other/graphql", "/api/user-management/admin/graphql/more"} {
		if got := LegacySchema("https://instance.example" + route); got != "" {
			t.Fatalf("invented a contract for %s", route)
		}
	}
	if LegacySchema("https://instance.example/api/user-management/graphql") == LegacySchema("https://instance.example/api/user-management/admin/graphql") {
		t.Fatal("admin and tenant schemas were confused")
	}
}

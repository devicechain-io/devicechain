// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// The device-management SDL is outside this module and the Go cache does not
// track it. Run this test with -count=1.
func TestAlarmLookupDocumentsMatchServedDeviceManagementSDL(t *testing.T) {
	sdl, served, err := schemaplane.SDLAt(filepath.Join("..", "..", "device-management", "graphql"), schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("device-management SDL: served=%v err=%v", served, err)
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{"suppression": alarmSuppressionStatusDocument, "lifecycles": alarmLifecyclesDocument} {
		t.Run(name, func(t *testing.T) {
			if errs := schema.ValidateWithVariables(doc, map[string]any{"tokens": []string{"alarm"}}); len(errs) != 0 {
				t.Fatalf("%s: %v", doc, errs)
			}
		})
	}
}

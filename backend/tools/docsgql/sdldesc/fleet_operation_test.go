// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sdldesc_test

import (
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-docsgql/sdlcompat"
	"github.com/devicechain-io/dc-docsgql/sdldesc"
)

func TestFleetOperationReadinessIsPreviewAndAdditive(t *testing.T) {
	data, err := os.ReadFile("../../../services/device-management/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	sdl := string(data)
	base, _, found := strings.Cut(sdl, "# Fleet operation readiness (Preview)")
	if !found {
		t.Fatal("readiness types missing")
	}
	// The query field lives in the Query type; the "before" schema has neither it nor its
	// description.
	const field = "    \"\"\"Preview. Report this build's fleet operation capabilities and fresh database enforcement and tenant orphan facts. Requires fleet:operate; scoped members are refused. An expensive diagnostic (it counts rows): query it when you need the answer, do not poll it.\"\"\"\n    fleetOperationReadiness: FleetOperationReadiness!\n"
	if !strings.Contains(base, field) {
		t.Fatal("Query.fleetOperationReadiness and its description not found where expected")
	}
	base = strings.Replace(base, field, "", 1)
	elements, err := sdldesc.Parse(sdl)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range elements {
		if strings.HasPrefix(e.Coord, "FleetOperation") || e.Coord == "Query.fleetOperationReadiness" {
			count++
			if !strings.HasPrefix(e.Desc, "Preview.") {
				t.Errorf("%s description does not begin Preview.", e.Coord)
			}
		}
	}
	if count != 11 {
		t.Fatalf("found %d readiness coordinates, want 11", count)
	}
	old, err := sdlcompat.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := sdlcompat.Parse(sdl)
	if err != nil {
		t.Fatal(err)
	}
	changes := sdlcompat.Diff("device-management", old, cur)
	if len(changes) != 3 {
		t.Fatalf("expected two types and a query added, got %v", changes)
	}
	for _, c := range changes {
		if c.Kind != sdlcompat.TypeAdded && c.Kind != sdlcompat.FieldAdded {
			t.Errorf("readiness is not additive: %v", c)
		}
	}
}

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
	const description = "    \"\"\"Preview. Report this build's bulk change capabilities and fresh database enforcement and tenant orphan facts. Requires fleet:operate; scoped members are refused. An expensive diagnostic (it counts rows): query it when you need the answer, do not poll it.\"\"\"\n"
	const field = description + "    fleetOperationReadiness: FleetOperationReadiness! @deprecated(reason: \"Use bulkChangeReadiness.\")\n"
	const bulkField = description + "    bulkChangeReadiness: BulkChangeReadiness!\n"
	if !strings.Contains(base, field) {
		t.Fatal("Query.fleetOperationReadiness and its description not found where expected")
	}
	base = strings.Replace(base, field, "", 1)
	base = strings.Replace(base, bulkField, "", 1)
	elements, err := sdldesc.Parse(sdl)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range elements {
		if strings.HasPrefix(e.Coord, "FleetOperation") || strings.HasPrefix(e.Coord, "BulkChange") || e.Coord == "Query.fleetOperationReadiness" || e.Coord == "Query.bulkChangeReadiness" {
			count++
			if !strings.HasPrefix(e.Desc, "Preview.") {
				t.Errorf("%s description does not begin Preview.", e.Coord)
			}
		}
	}
	if count != 22 {
		t.Fatalf("found %d readiness coordinates, want 22", count)
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
	if len(changes) != 6 {
		t.Fatalf("expected four types and two queries added, got %v", changes)
	}
	for _, c := range changes {
		if c.Kind != sdlcompat.TypeAdded && c.Kind != sdlcompat.FieldAdded {
			t.Errorf("readiness is not additive: %v", c)
		}
	}
}

func TestBulkChangeRenamePreservesV19Contract(t *testing.T) {
	data, err := os.ReadFile("../../../services/device-management/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	sdl := string(data)
	base, _, found := strings.Cut(sdl, "# Bulk change readiness (Preview)")
	if !found {
		t.Fatal("bulk change types missing")
	}
	lines := strings.Split(base, "\n")
	for i, line := range lines {
		if strings.Contains(line, "bulkChangeReadiness: BulkChangeReadiness!") {
			lines[i-1], lines[i] = "", ""
		}
	}
	base = strings.ReplaceAll(strings.Join(lines, "\n"), ` @deprecated(reason: "Use bulkChangeReadiness.")`, "")
	old, err := sdlcompat.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := sdlcompat.Parse(sdl)
	if err != nil {
		t.Fatal(err)
	}
	changes := sdlcompat.Diff("device-management", old, cur)
	if len(changes) != 4 {
		t.Fatalf("want two types, one query and one deprecation added: %v", changes)
	}
	for _, change := range changes {
		if change.Kind != sdlcompat.TypeAdded && change.Kind != sdlcompat.FieldAdded && change.Kind != sdlcompat.DeprecationAdded {
			t.Errorf("v1.9 readiness contract changed: %v", change)
		}
	}
}

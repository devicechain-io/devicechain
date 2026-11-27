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
	sdl := preServeSDL(t)
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
	sdl := preServeSDL(t)
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

// servedBulkChangeOperations are the operations serving bulk changes added to Query and Mutation.
var servedBulkChangeOperations = []string{
	"bulkChange", "bulkChanges", "bulkChangeLines", "planBulkChange", "previewBulkChangeResume", "previewBulkChangeReversal",
	"createBulkChange", "startBulkChange", "pauseBulkChange", "resumeBulkChange", "abortBulkChange",
	"reverseBulkChange", "resolveAmbiguousBulkChangeLines",
}

const bulkChangeSectionMarker = "\n# Bulk changes (Preview)\n"

func servedSDL(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../../services/device-management/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// preServeSDL is the served schema with bulk changes taken back out: the section of their type
// definitions, and each operation's field with the description above it. It is the schema the
// readiness tests were written against, and the "before" of the served-flip diff.
func preServeSDL(t *testing.T) string {
	t.Helper()
	sdl, _, found := strings.Cut(servedSDL(t), bulkChangeSectionMarker)
	if !found {
		t.Fatal("the bulk-change section marker is missing")
	}
	lines := strings.Split(sdl, "\n")
	drop := map[int]bool{}
	for i, line := range lines {
		field := strings.TrimPrefix(line, "    ")
		if field == line || strings.HasPrefix(field, " ") {
			continue
		}
		for _, op := range servedBulkChangeOperations {
			if !strings.HasPrefix(field, op+"(") && !strings.HasPrefix(field, op+":") {
				continue
			}
			drop[i] = true
			if field == op+"(" { // arguments on lines of their own, up to the closing "    ): Type"
				for k := i + 1; k < len(lines); k++ {
					drop[k] = true
					if strings.HasPrefix(lines[k], "    )") {
						break
					}
				}
			}
			j := i - 1
			if d := strings.TrimSpace(lines[j]); d == `"""` {
				drop[j] = true
				for j--; j >= 0 && !strings.HasPrefix(strings.TrimSpace(lines[j]), `"""`); j-- {
					drop[j] = true
				}
				drop[j] = true
			} else if strings.HasPrefix(d, `"""`) && strings.HasSuffix(d, `"""`) && len(d) > 6 {
				drop[j] = true
			}
		}
	}
	kept := make([]string, 0, len(lines))
	for i, line := range lines {
		if !drop[i] {
			kept = append(kept, line)
		}
	}
	out := strings.Join(kept, "\n") + "\n"
	for _, op := range servedBulkChangeOperations {
		if strings.Contains(out, " "+op+"(") || strings.Contains(out, " "+op+":") {
			t.Fatalf("the pre-serve schema still declares %s", op)
		}
	}
	return out
}

// Serving bulk changes is additive: against the schema without them, every difference is an added
// type or field, the thirteen operations are among them, and every added description begins Preview.
func TestServedBulkChangesArePreviewAndAdditive(t *testing.T) {
	before, after := preServeSDL(t), servedSDL(t)
	old, err := sdlcompat.Parse(before)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := sdlcompat.Parse(after)
	if err != nil {
		t.Fatal(err)
	}
	added := map[string]bool{}
	for _, c := range sdlcompat.Diff("device-management", old, cur) {
		if c.Kind != sdlcompat.TypeAdded && c.Kind != sdlcompat.FieldAdded {
			t.Errorf("serving bulk changes is not additive: %v", c)
		}
		added[c.Coord] = true
	}
	for _, op := range servedBulkChangeOperations {
		if !added["Query."+op] && !added["Mutation."+op] {
			t.Errorf("%s was not added", op)
		}
	}
	if !added["BulkChange"] || !added["BulkChangeReversalRequest"] {
		t.Fatalf("the bulk-change types were not added: %v", added)
	}
	elements, err := sdldesc.Parse(before)
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]bool{}
	for _, e := range elements {
		existing[e.Coord] = true
	}
	elements, err = sdldesc.Parse(after)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range elements {
		if existing[e.Coord] {
			continue
		}
		count++
		if !strings.HasPrefix(e.Desc, "Preview.") {
			t.Errorf("%s description does not begin Preview.", e.Coord)
		}
	}
	if count < 100 {
		t.Fatalf("checked %d new descriptions; the bulk-change surface has far more", count)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// servedSDL concatenates one area's tenant-plane schema text the way loadServedSchema
// does. It is duplicated rather than shared so a test can parse the SAME text twice —
// once through the code under test and once through the library directly — without the
// two readings being the same call.
func servedSDL(t *testing.T, area string) string {
	t.Helper()
	var files []string
	for _, ext := range []string{"*.graphql", "*.gql"} {
		found, err := filepath.Glob(filepath.Join("..", "..", "services", area, "graphql", ext))
		if err != nil {
			t.Fatalf("glob %s: %v", area, err)
		}
		files = append(files, found...)
	}
	sort.Strings(files)
	var sb strings.Builder
	for _, f := range files {
		if strings.Contains(filepath.Base(f), "admin") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sb.Write(body)
		sb.WriteString("\n")
	}
	return sb.String()
}

// probeTokens is a seed's tokens as the sweep receives them: one per entity in the table.
func probeTokens() map[string]string {
	tokens := map[string]string{}
	for _, e := range entities {
		tokens[e.Name] = "probe-" + e.Name
	}
	return tokens
}

func planAll(t *testing.T) ([]sweepCall, []sweepSkip) {
	t.Helper()
	var calls []sweepCall
	var skips []sweepSkip
	for _, area := range probeAreas() {
		schema, err := loadServedSchema(filepath.Join("..", "..", "services"), area)
		if err != nil {
			t.Fatalf("load %s: %v", area, err)
		}
		if schema == nil {
			t.Fatalf("%s served no schema; every probe area serves one", area)
		}
		c, s := planReadSweep(area, schema, probeTokens())
		calls = append(calls, c...)
		skips = append(skips, s...)
	}
	return calls, skips
}

// 🔴 THE FAIL-OPEN GUARD, and the reason this file exists at all.
//
// A planner that quietly stopped supplying an argument would skip the field, sweep less,
// and still print a clean pass — the same shape of silence that let a fence-set snapshot
// go stale behind a green drill. So every query field the platform serves must be either
// PLANNED or EXEMPT BY NAME, with nothing in between. "Skipped because no value could be
// found" is a legitimate outcome of the planner and an ILLEGITIMATE one to ship: it means
// a door lost its coverage without anyone writing down that it did.
func TestReadSweepPlansTheWholeServedSurface(t *testing.T) {
	calls, skips := planAll(t)

	planned := map[string]bool{}
	for _, c := range calls {
		planned[c.Area+"."+c.Field] = true
	}
	for _, s := range skips {
		key := s.Area + "." + s.Field
		if _, declared := sweepExemptions[key]; declared {
			continue
		}
		if strings.HasPrefix(s.Field, "_") {
			continue // a schema placeholder; it resolves to nothing by construction
		}
		t.Errorf("%s is neither planned nor exempt: %s\n"+
			"    Either teach the planner to supply its arguments, or add it to sweepExemptions\n"+
			"    with the reason. A door that silently stops being swept is the defect this\n"+
			"    whole sweep exists to prevent.", key, s.Reason)
	}

	// A floor for the VACUOUS run, which is the one case the loop above cannot speak to.
	// That loop reports a door that is skipped without an exemption — so a planner that
	// stopped supplying arguments fails there, loudly. What it says nothing about is a run
	// with no doors AT ALL: no areas, no schemas, an empty table. Then there are no skips
	// to object to and the loop passes by saying nothing, which is the exact silence this
	// sweep exists to break.
	//
	// ⚠️ NO MUTATION KILLS THIS LINE, and that is recorded rather than fixed. Every mutation
	// that breaks the planner still produces SKIPS, so the loop above gets there first;
	// reaching the floor needs the table or the area list to be empty, which no edit to this
	// package's logic produces. It is a backstop, it is cheap, and its cost is one number.
	// The number is well below what the tree plans today (99) because a floor that has to be
	// raised on every schema addition gets raised without being read.
	if len(calls) < 60 {
		t.Errorf("planned only %d doors; the served surface is far larger, so the planner is broken", len(calls))
	}
	if len(probeAreas()) < 2 {
		t.Errorf("swept %d areas; the coverage table spans several", len(probeAreas()))
	}
}

// Every planned document is validated with the SERVER'S OWN validator, against the same
// schemas the services parse — the discipline documents_test.go established, for the same
// reason: these documents are strings, and without this the first thing to read one is a
// cluster, twenty minutes into a drill.
//
// ⚠️ Validation is about the DOCUMENT. A variable VALUE that the schema types correctly but
// the platform refuses is invisible here — ValidateWithVariables accepts a `pageNumber` of
// "one" against an Int!, which was checked rather than assumed. That residue is what the
// live run is for.
func TestReadSweepDocumentsAreValidAgainstTheServedSchemas(t *testing.T) {
	byArea := map[string]*graphql.Schema{}
	for _, area := range probeAreas() {
		parsed, err := graphql.ParseSchema(servedSDL(t, area), nil, graphql.UseFieldResolvers())
		if err != nil {
			t.Fatalf("parse %s: %v", area, err)
		}
		byArea[area] = parsed
	}
	calls, _ := planAll(t)
	for _, c := range calls {
		for _, err := range byArea[c.Area].ValidateWithVariables(c.Doc, c.Vars) {
			t.Errorf("%s.%s is not a valid document: %v\n    %s", c.Area, c.Field, err, c.Doc)
		}
	}
}

// A declaration that names something the schema no longer has is worse than no
// declaration: it reads as coverage, or as a considered exemption, and is neither.
func TestSweepDeclarationsNameThingsThatExist(t *testing.T) {
	fields := map[string]*ast.FieldDefinition{}
	for _, area := range probeAreas() {
		schema, err := loadServedSchema(filepath.Join("..", "..", "services"), area)
		if err != nil {
			t.Fatalf("load %s: %v", area, err)
		}
		query, ok := schema.Types["Query"].(*ast.ObjectTypeDefinition)
		if !ok {
			continue
		}
		for _, f := range query.Fields {
			fields[area+"."+f.Name] = f
		}
	}

	for key, reason := range sweepExemptions {
		if _, ok := fields[key]; !ok {
			t.Errorf("sweepExemptions names %q, which no area serves — a stale exemption reads as a decision", key)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("sweepExemptions[%q] carries no reason; an exemption without one is just a gap", key)
		}
	}

	byName := map[string]bool{}
	for _, e := range entities {
		byName[e.Name] = true
	}
	for key, entity := range sweepTokenArgs {
		cut := strings.LastIndex(key, ".")
		field, arg := key[:cut], key[cut+1:]
		def, ok := fields[field]
		if !ok {
			t.Errorf("sweepTokenArgs names %q, which no area serves", field)
			continue
		}
		found := false
		for _, a := range def.Arguments {
			if a.Name.Name == arg {
				found = true
			}
		}
		if !found {
			t.Errorf("sweepTokenArgs names argument %q, which %s does not take", arg, field)
		}
		if !byName[entity] {
			t.Errorf("sweepTokenArgs[%q] names entity %q, which the coverage table does not seed", key, entity)
		}
	}
}

// The version-listing doors are the reason this sweep was built, so assert they are
// actually reached rather than trusting the count. Each reads a stored, frozen artifact
// derived from a write — the class #838 belonged to and the class a read-back of one's
// own writes cannot see.
func TestReadSweepReachesTheDerivedDoors(t *testing.T) {
	calls, _ := planAll(t)
	planned := map[string]sweepCall{}
	for _, c := range calls {
		planned[c.Area+"."+c.Field] = c
	}
	for _, want := range []string{
		// The three the geofence archive broke. currentGeoFenceSet hydrates EAGERLY in
		// the top-level resolver, so it errors under #838 on any selection at all.
		"device-management.currentGeoFenceSet",
		"device-management.currentGeoFenceSetManifest",
		"device-management.currentFenceSetVersion",
		// The frozen snapshots. A profile version's is the one stored document in the
		// platform decoded straight back into live models.
		"device-management.deviceProfileVersions",
		"device-management.entityGroupVersions",
		"dashboard-management.dashboardVersions",
		"outbound-connectors.connectorVersions",
	} {
		call, ok := planned[want]
		if !ok {
			t.Errorf("%s is not planned; it is one of the doors this sweep was built for", want)
			continue
		}
		// A door returning an OBJECT must select something, or the server would reject the
		// document; a door returning a SCALAR must select nothing, for the same reason.
		// currentFenceSetVersion is the second kind — an Int! — and it still runs its
		// resolver, which is what this sweep is asking of it. An earlier draft of this test
		// required __typename of every door and failed on exactly that one, which is the
		// only reason the distinction is written down here.
		if strings.Contains(call.Doc, "{"+strings.SplitN(want, ".", 2)[1]+"{") {
			if !strings.Contains(call.Doc, "__typename") {
				t.Errorf("%s returns an object and selects nothing that forces the resolver to run: %s", want, call.Doc)
			}
		}
	}
}

// A token argument reaching a row that does not exist returns an EMPTY LIST, not an error
// — so the sweep would pass having read nothing at all. That makes "the right token was
// supplied" a property worth asserting directly rather than inferring from a green run.
func TestReadSweepSuppliesSeededTokens(t *testing.T) {
	calls, _ := planAll(t)
	for _, want := range []struct {
		field string
		token string
	}{
		{"device-management.deviceProfileVersions", "probe-device-profile"},
		{"device-management.deviceCommandVocabulary", "probe-device"},
		{"dashboard-management.dashboardVersions", "probe-dashboard"},
		{"outbound-connectors.connectorVersions", "probe-connector"},
		{"device-management.geoFencesByToken", "probe-geo-fence"},
	} {
		var found *sweepCall
		for i := range calls {
			if calls[i].Area+"."+calls[i].Field == want.field {
				found = &calls[i]
			}
		}
		if found == nil {
			t.Errorf("%s is not planned", want.field)
			continue
		}
		if !strings.Contains(flatten(found.Vars), want.token) {
			t.Errorf("%s was planned with %v, which does not carry the seeded token %q",
				want.field, found.Vars, want.token)
		}
	}
}

// The pagination values a search criteria is called with are a CHOICE, and the wrong one
// turns this sweep into a generator of false findings: a pageSize of 0 is refused by some
// criteria, and the refusal arrives looking exactly like an upgrade defect. Page one, one
// row is the smallest read that still reaches a row. Pinned because a mutation to zero
// changed no test and no output — the reasoning lived only in a comment.
func TestReadSweepAsksForTheSmallestRealPage(t *testing.T) {
	calls, _ := planAll(t)
	checked := 0
	for _, c := range calls {
		criteria, ok := c.Vars["criteria"].(map[string]any)
		if !ok {
			continue
		}
		checked++
		if criteria["pageNumber"] != 1 || criteria["pageSize"] != 1 {
			t.Errorf("%s.%s asks for %v; a search sweep wants page one, one row — and a "+
				"pageSize of 0 is refused by some criteria, which would arrive as a finding",
				c.Area, c.Field, criteria)
		}
	}
	// The search doors are most of what this sweep adds over the read-backs, so a run that
	// found none of them has measured nothing about pagination at all.
	if checked < 20 {
		t.Errorf("only %d doors take a search criteria; the planner is not reaching them", checked)
	}
}

func flatten(vars map[string]any) string {
	var sb strings.Builder
	for _, v := range vars {
		switch typed := v.(type) {
		case string:
			sb.WriteString(typed + " ")
		case []any:
			for _, e := range typed {
				if s, ok := e.(string); ok {
					sb.WriteString(s + " ")
				}
			}
		}
	}
	return sb.String()
}

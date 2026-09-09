// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schemaplane_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// 🔴 THE SERVICES TREE LIVES OUTSIDE THIS MODULE AND GO'S TEST CACHE DOES NOT TRACK
// IT, so a plain `go test` can serve a stale PASS over a renamed or edited schema.
// CI runs every module with -count=1, and so does the sweep in CLAUDE.md.
const servicesTree = "../../../services"

// The anti-vacuity floor under the tree scan, and it is DELIBERATELY WELL BELOW the
// tree — 11 areas carry a graphql directory and 14 schema artifacts live in them
// today. Its only job is to fail when the scan finds nothing, or nearly nothing: a
// walker that classified an empty set would satisfy every assertion below by saying
// nothing, which is the shape of pass this file exists to refuse.
//
// The EXACT inventory is reconciled somewhere else, in both directions, on every
// pull request: docs/scripts/schemas.manifest.mjs lists each schema by path and the
// generator fails on a file with no entry AND on an entry with no file. So pinning
// the count here would add no detection, and a floor that has to be raised every
// time a service gains a schema gets raised without being read.
const (
	minAreasWithSchemas = 5
	minSchemas          = 8
)

// scanTree classifies every area's schema directory, the way a tool reading a
// deployed release's services tree does.
func scanTree(t *testing.T) map[string][]schemaplane.Schema {
	t.Helper()
	entries, err := os.ReadDir(servicesTree)
	if err != nil {
		t.Fatalf("read %s: %v", servicesTree, err)
	}
	out := map[string][]schemaplane.Schema{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(servicesTree, e.Name(), "graphql")
		if _, serr := os.Stat(dir); serr != nil {
			continue
		}
		// 🔴 THE LINT OVER OUR OWN TREE, AND IT IS THE STRICT HALF. Dir reads any
		// release's spelling because apiprobe points it at older trees; Lint is what
		// holds THIS repository to .graphql, and it is only correct to call it here,
		// on directories that are ours to rename.
		if lerr := schemaplane.Lint(dir); lerr != nil {
			t.Errorf("%s: %v", dir, lerr)
			continue
		}
		found, cerr := schemaplane.Dir(dir)
		if cerr != nil {
			t.Fatalf("classify %s: %v", dir, cerr)
		}
		if len(found) == 0 {
			t.Errorf("%s exists but holds no schema artifact; a graphql directory that "+
				"serves nothing is either a rename this classifier did not follow or an "+
				"extension it refused to guess at", dir)
			continue
		}
		out[e.Name()] = found
	}
	return out
}

// Every schema in the tree classifies, and the scan is not vacuous.
func TestEverySchemaArtifactInTheTreeClassifies(t *testing.T) {
	byArea := scanTree(t)

	total := 0
	for _, v := range byArea {
		total += len(v)
	}
	if len(byArea) < minAreasWithSchemas {
		t.Fatalf("classified schemas in %d area(s), expected at least %d; a scan that found "+
			"almost nothing satisfies every other assertion here", len(byArea), minAreasWithSchemas)
	}
	if total < minSchemas {
		t.Fatalf("classified %d schema artifact(s), expected at least %d", total, minSchemas)
	}

	// Every artifact is .graphql. Stated as its own assertion rather than left to
	// Dir's refusal, because this is the property the SPDX header gate depends on:
	// addlicense has no handler for .gql and skips such a file in silence.
	for area, schemas := range byArea {
		for _, s := range schemas {
			if filepath.Ext(s.Path) != schemaplane.Ext {
				t.Errorf("%s (%s): extension %q, want %q", s.Path, area, filepath.Ext(s.Path), schemaplane.Ext)
			}
		}
	}
}

// user-management is the area the plane split was wrong for: three schemas, three
// mounts, two of them identity-token surfaces.
func TestUserManagementServesThreeMounts(t *testing.T) {
	dir := filepath.Join(servicesTree, "user-management", "graphql")
	got, err := schemaplane.Dir(dir)
	if err != nil {
		t.Fatalf("classify %s: %v", dir, err)
	}
	want := map[string]schemaplane.Plane{
		schemaplane.MountTenant:   schemaplane.PlaneTenant,
		schemaplane.MountAdmin:    schemaplane.PlaneIdentity,
		schemaplane.MountSettings: schemaplane.PlaneIdentity,
	}
	if len(got) != len(want) {
		t.Fatalf("classified %d schema(s) in %s, want %d", len(got), dir, len(want))
	}
	for _, s := range got {
		plane, known := want[s.Mount]
		if !known {
			t.Errorf("%s: unexpected mount %s", s.Path, s.Mount)
			continue
		}
		if s.Plane != plane {
			t.Errorf("%s: plane %q, want %q", s.Path, s.Plane, plane)
		}
		delete(want, s.Mount)
	}
	for mount := range want {
		t.Errorf("no user-management schema classified at mount %s", mount)
	}
}

// 🔴 THE GATE THIS PACKAGE WAS WRITTEN FOR. The tenant plane of user-management has
// to serve ping, me and login — login above all, because it is where every other
// call's token comes from. With the old filename-substring classifier the tools
// concatenated schema + settings_schema into one "tenant" SDL, graphql-go kept the
// LAST of the two `type Query` declarations without complaining, and all three of
// these documents were rejected with "Cannot query field" while the SDL was
// non-empty and every anti-vacuity floor stayed quiet.
//
// The settings case at the bottom is the counterweight: serving the three fields is
// only correct while the settings surface stays OFF this plane. A classifier that
// merged everything would pass the first three assertions.
func TestUserManagementTenantPlaneServesLoginPingAndMe(t *testing.T) {
	dir := filepath.Join(servicesTree, "user-management", "graphql")
	sdl, ok, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil {
		t.Fatalf("read tenant schema: %v", err)
	}
	if !ok {
		t.Fatal("user-management serves no schema at " + schemaplane.MountTenant)
	}

	// A nil resolver is enough: validation reads the schema, never a resolver.
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse tenant schema: %v", err)
	}

	for _, c := range []struct {
		name  string
		doc   string
		valid bool
	}{
		{"ping", `{ ping }`, true},
		{"me", `{ me { email } }`, true},
		{"login", `mutation { login(email: "a@b.c", password: "p") { identityToken } }`, true},
		{
			// Served at /settings/graphql under an identity token. On the tenant
			// plane it must not resolve at all.
			"settings is not on the tenant plane", `{ settings { key } }`, false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			errs := schema.ValidateWithVariables(c.doc, nil)
			switch {
			case c.valid && len(errs) > 0:
				t.Fatalf("%s does not resolve on the user-management tenant plane: %v", c.doc, errs)
			case !c.valid && len(errs) == 0:
				t.Fatalf("%s resolves on the user-management tenant plane, but it is served at %s "+
					"under an identity token", c.doc, schemaplane.MountSettings)
			}
		})
	}
}

func TestClassifyRefusesAnUnrecognizedFilename(t *testing.T) {
	if _, err := schemaplane.Classify("backend/services/x/graphql/internal_schema.graphql"); err == nil {
		t.Fatal("Classify accepted a filename no convention names; guessing the plane is the defect")
	}
}

func TestLintRefusesAnAlternateExtension(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.graphql"), "type Query { a: String }")
	write(t, filepath.Join(dir, "settings_schema.gql"), "type Query { b: String }")

	err := schemaplane.Lint(dir)
	if err == nil {
		t.Fatal("Lint accepted a .gql schema artifact")
	}
	if !strings.Contains(err.Error(), "settings_schema.gql") {
		t.Fatalf("the error must name the offending file, got: %v", err)
	}
}

// Lint is the whole naming rule, not only the extension: a file no convention names
// has no mount to be served at, and Lint has to say so rather than leave it to a
// consumer that will find out later.
func TestLintRefusesAnUnrecognizedFilename(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "internal_schema.graphql"), "type Query { a: String }")

	if err := schemaplane.Lint(dir); err == nil {
		t.Fatal("Lint accepted a schema filename no convention names")
	}
}

// The counterweight to both refusals: a correctly named directory passes. A lint
// that refused everything would satisfy the two assertions above.
func TestLintAcceptsACorrectlyNamedDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.graphql"), "type Query { a: String }")
	write(t, filepath.Join(dir, "admin_schema.graphql"), "type Query { b: String }")
	write(t, filepath.Join(dir, "resolvers.go"), "package graphql")

	if err := schemaplane.Lint(dir); err != nil {
		t.Fatalf("Lint refused a directory named exactly as this repository requires: %v", err)
	}
}

// 🔴 THE DUPLICATE-MOUNT REFUSAL, WHICH READING THROUGH THE EXTENSION MAKES
// REACHABLE FROM A DIRECTORY. One convention under two spellings is two files
// claiming one endpoint, and a tool that took either would serve a different schema
// depending on which the directory listing handed it first. It is an error, named.
func TestDirRefusesTwoSpellingsOfOneMount(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.graphql"), "type Query { a: String }")
	write(t, filepath.Join(dir, "schema.gql"), "type Query { b: String }")

	_, err := schemaplane.Dir(dir)
	if err == nil {
		t.Fatal("Dir accepted two files claiming " + schemaplane.MountTenant)
	}
	for _, want := range []string{"schema.graphql", "schema.gql", schemaplane.MountTenant} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q, got: %v", want, err)
		}
	}
}

func TestDirIgnoresNonSchemaFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.graphql"), "type Query { a: String }")
	write(t, filepath.Join(dir, "resolvers.go"), "package graphql")
	write(t, filepath.Join(dir, "README.md"), "notes")

	got, err := schemaplane.Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(got) != 1 || got[0].Mount != schemaplane.MountTenant {
		t.Fatalf("got %+v, want the one tenant schema", got)
	}
}

func TestSDLAtReportsAnAbsentMountRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.graphql"), "type Query { a: String }")

	_, ok, err := schemaplane.SDLAt(dir, schemaplane.MountAdmin)
	if err != nil {
		t.Fatalf("an area that serves no admin plane is normal, not an error: %v", err)
	}
	if ok {
		t.Fatal("SDLAt reported an admin schema that is not there")
	}
}

func TestSDLAtRefusesAnEmptySchema(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.graphql"), "\n  \n")

	if _, _, err := schemaplane.SDLAt(dir, schemaplane.MountTenant); err == nil {
		t.Fatal("an empty schema must be an error: a parser handed nothing validates everything")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// 🔴 THE REGRESSION THIS SEPARATION EXISTS FOR. Dir is not only a lint over this
// repository's own tree; it is also the scanner apiprobe points at a CHECKED-OUT
// RELEASE, and the upgrade drill checks out the previous release to seed the
// baseline install it then upgrades. In that tree user-management's three schemas
// are spelled .gql, because the rename is newer than the release. A scanner that
// refuses the spelling an older tree actually uses cannot read the trees it exists
// to read: the drill died before it upgraded anything, and reported a seeding
// failure that said nothing about the release under test.
func TestDirReadsAReleaseTreeThatSpelledItsSchemasGql(t *testing.T) {
	dir := t.TempDir()
	// The three names exactly as the previous release spells them.
	write(t, filepath.Join(dir, "schema.gql"), "type Query { a: String }")
	write(t, filepath.Join(dir, "admin_schema.gql"), "type Query { b: String }")
	write(t, filepath.Join(dir, "settings_schema.gql"), "type Query { c: String }")

	got, err := schemaplane.Dir(dir)
	if err != nil {
		t.Fatalf("Dir refused a release tree it has to be able to read: %v", err)
	}
	want := map[string]schemaplane.Plane{
		schemaplane.MountTenant:   schemaplane.PlaneTenant,
		schemaplane.MountAdmin:    schemaplane.PlaneIdentity,
		schemaplane.MountSettings: schemaplane.PlaneIdentity,
	}
	if len(got) != len(want) {
		t.Fatalf("classified %d schema(s), want %d: %+v", len(got), len(want), got)
	}
	for _, s := range got {
		plane, known := want[s.Mount]
		if !known {
			t.Errorf("%s: unexpected mount %s", s.Path, s.Mount)
			continue
		}
		if s.Plane != plane {
			t.Errorf("%s: plane %q, want %q", s.Path, s.Plane, plane)
		}
		delete(want, s.Mount)
	}
	for mount := range want {
		t.Errorf("no schema classified at mount %s", mount)
	}
}

// The reading half of the same story: SDLAt has to hand back an older tree's text,
// because that text is what apiprobe parses to plan the calls it makes.
func TestSDLAtReadsAnOlderTreesSpelling(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "schema.gql"), "type Query { a: String }")

	sdl, ok, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil {
		t.Fatalf("SDLAt: %v", err)
	}
	if !ok || !strings.Contains(sdl, "type Query") {
		t.Fatalf("SDLAt(%s) = %q, %v; want the file's text", schemaplane.MountTenant, sdl, ok)
	}
}

// 🔴 THE ARM THAT CATCHES A NORMALIZATION THAT MOVES AN ENDPOINT. Every schema
// artifact in this tree still classifies to the mount and plane it classified to
// before the scanner learned to read an older release's spelling. The expected
// answers are written out here as LITERAL strings rather than taken from the
// package's constants, because a table that sourced its answers from the code under
// test would follow that code anywhere it went — including onto the wrong plane.
//
// It pins filenames, not paths, which is what keeps it from drifting: those three
// names are the whole convention, and a service that gains a schema has to use one
// of them. The per-file inventory is reconciled in both directions somewhere else,
// by docs/scripts/schemas.manifest.mjs.
func TestEverySchemaInTheTreeKeepsTheMountItHad(t *testing.T) {
	mountFor := map[string]struct {
		mount string
		plane string
	}{
		"schema.graphql":          {"/graphql", "tenant"},
		"admin_schema.graphql":    {"/admin/graphql", "identity"},
		"settings_schema.graphql": {"/settings/graphql", "identity"},
	}

	byArea := scanTree(t)
	if len(byArea) < minAreasWithSchemas {
		t.Fatalf("classified schemas in %d area(s), expected at least %d; a scan that found "+
			"almost nothing would satisfy every assertion below", len(byArea), minAreasWithSchemas)
	}

	checked := 0
	for area, schemas := range byArea {
		for _, s := range schemas {
			want, known := mountFor[filepath.Base(s.Path)]
			if !known {
				t.Errorf("%s (%s): no expected mount recorded for this filename; if the "+
					"convention table gained an entry, record what it must answer here too",
					s.Path, area)
				continue
			}
			if s.Mount != want.mount {
				t.Errorf("%s: served at %s, but it was classified to %s before", s.Path, s.Mount, want.mount)
			}
			if string(s.Plane) != want.plane {
				t.Errorf("%s: plane %q, but it carried %q before; a mount that changes the "+
					"principal that reaches it either exposes an identity surface to a tenant "+
					"token or hides it from the only token that can authorize it",
					s.Path, s.Plane, want.plane)
			}
			checked++
		}
	}
	if checked < minSchemas {
		t.Fatalf("checked %d schema artifact(s), expected at least %d", checked, minSchemas)
	}
}

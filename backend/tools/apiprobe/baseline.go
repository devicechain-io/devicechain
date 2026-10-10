// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// baseline is the schema tree a SEED is measured against, and it exists because
// of the one thing an upgrade drill cannot avoid: the coverage table is built
// from the NEW platform, and the seed runs against the OLD one.
//
// # THE PROBLEM IT SOLVES, MEASURED RATHER THAN GUESSED
//
// apiprobe covers every create mutation HEAD serves. Two of them — createGeoFence
// and createCommandBatch — did not exist in v0.11.0. Seeding the whole table into
// a v0.11.0 instance therefore dies at the fifth row with REFUSED, and the drill
// never reaches the upgrade it was built to test. The failure is loud, but it is
// about the tool's own vocabulary rather than about the platform, and every future
// release will add more of them.
//
// So `seed --baseline-schemas <dir>` points at the OLD version's served schemas —
// the rig already extracts that tree to build the matching dcctl — and every
// entity the old schema cannot express is SKIPPED, by name, with the reason. What
// remains is exactly the set of rows that release could hold, which is exactly the
// set an upgrade can be asked to carry forward.
//
// # WHAT IT DELIBERATELY DOES NOT DO
//
// It checks that the MUTATION, the READ QUERY and the INPUT TYPE exist. It does
// NOT check the individual fields a row selects or sends.
//
// That is a decision, not an omission. If a later release adds a field to a type
// the table already covers, field-checking would skip the WHOLE entity — trading
// one new field for the loss of every other field on that row, silently, in a tool
// whose entire job is to notice loss. The honest answer there is that the table's
// selections have to stay expressible by the oldest baseline a drill runs from,
// and that is a maintainer's call. Until it is made, the seed refuses the create
// and says which entity and why — which is the loud failure, in the right place.
//
// # THE FAIL-OPEN THIS COULD BECOME
//
// 🔴 A `supports` that answered "no" too readily would skip the entire table and
// leave a receipt with nothing in it, and verify would then pass instantly having
// checked nothing. Two things stop that: seeding zero rows is an error, and a test
// points a baseline at the CURRENT tree and requires that nothing at all is
// skipped. The second is the one that matters — it is the only check that can tell
// a working filter from one that rejects everything.
type baseline struct {
	dir string
	// schemas is each functional area's served tenant-plane schema, PARSED.
	//
	// 🔴 PARSED, NOT MATCHED AS TEXT. This used to substring-match the SDL
	// (`createX(request:` with whitespace removed, `input X {`, a regex over a
	// signature's parentheses). That is a claim about how the file is FORMATTED, and
	// the schemas' public descriptions broke it: a """description""" above an argument
	// sits between `createDashboard(` and `request:`, and a ")" inside one ends a
	// signature early. Every such row then read as unsupported, and the drill would
	// have skipped it silently — the fail-open described above. The AST answers the
	// question actually being asked, with the server's own parser.
	schemas map[string]*ast.Schema
}

// loadBaseline reads every tenant-plane schema under dir, which is expected to be
// a `backend/services` directory from the release being upgraded FROM.
//
// The identity-token schemas — the admin API and the settings API — are excluded
// for the same reason the coverage test excludes them: they are a separate surface
// under a separate principal, and nothing in the table is served there.
func loadBaseline(dir string) (*baseline, error) {
	b := &baseline{dir: dir, schemas: map[string]*ast.Schema{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, failWith(exitSetup, "read baseline schemas at %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// 🔴 CLASSIFIED, NOT GUESSED. This used to glob both extensions and drop
		// anything whose filename contained "admin", which got the plane wrong in
		// both directions: an area spelling its schemas with an extension the glob
		// missed became one the baseline "serves no schema" for — skipping every
		// entity in it — and an identity-token surface whose name says "settings"
		// rather than "admin" was folded in as though a tenant token reached it.
		// schemaplane answers by mount instead, and an unrecognised artifact is an
		// error rather than a file quietly left out.
		sdl, served, err := schemaplane.SDLAt(filepath.Join(dir, e.Name(), "graphql"), schemaplane.MountTenant)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// An area with no graphql directory in that release's tree.
			continue
		case err != nil:
			return nil, failWith(exitSetup, "scan %s: %w", e.Name(), err)
		case !served:
			continue
		}
		parsed, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
		if err != nil {
			return nil, failWith(exitSetup, "parse the %s baseline schema: %w", e.Name(), err)
		}
		b.schemas[e.Name()] = parsed.AST()
	}
	if len(b.schemas) == 0 {
		// An empty tree would mark every entity unsupported and produce a receipt
		// with nothing on it — a verify that passes having checked nothing. Refuse
		// it here, where the path is still in hand to name.
		return nil, failWith(exitSetup, "%s holds no served schemas; a baseline that declares nothing would skip the whole table", dir)
	}
	return b, nil
}

// supports reports whether the baseline can express this entity, and if not, why
// — the reason is printed beside the skipped row, because "24 of 26" with no
// explanation is indistinguishable from a tool quietly giving up.
func (b *baseline) supports(e entity) (bool, string) {
	s, ok := b.schemas[e.Area]
	if !ok {
		return false, "the baseline serves no " + e.Area + " schema"
	}
	// The field-level check, which is OPT-IN PER ROW rather than applied to every
	// one. See entity.Requires for why the difference matters: checked everywhere it
	// would skip a whole entity over one added field; named on a row written for that
	// field, it skips exactly the row that cannot work without it.
	//
	// It runs FIRST so its reason is the one reported. A row requiring a field the
	// baseline lacks usually also names a type and mutation that release does have, so
	// letting the name checks answer first would report "supported" for a row the
	// server is about to refuse.
	for _, req := range e.Requires {
		typeName, field, found := strings.Cut(req, ".")
		if !found {
			return false, "malformed Requires entry " + req + " (want TYPE.FIELD)"
		}
		if !declaresField(s, typeName, field) {
			return false, "the baseline's " + typeName + " has no " + field + " field"
		}
	}
	// A publish takes scalar arguments and returns a version with no input type, so
	// the two checks below do not apply to it in the shape they are written. It is
	// matched on the pair that DOES decide whether a baseline can express one: the
	// publish mutation keyed by token, and the versions query keyed by token.
	// A criteria-addressed read has no token argument and its input is the CRITERIA
	// type, not the create's, so the two checks below match the wrong things for it.
	if e.ReadInput != "" {
		if !rootFieldTakes(s, "mutation", e.Mutation, e.arg()) {
			return false, "the baseline does not declare " + e.Mutation + "(" + e.arg() + ":…)"
		}
		if !rootFieldTakes(s, "query", e.Read, "criteria") {
			return false, "the baseline does not declare " + e.Read + "(criteria:…)"
		}
		if !declaresInput(s, inputTypeName(e.ReadInput)) {
			return false, "the baseline does not declare input " + inputTypeName(e.ReadInput)
		}
		if input := inputTypeName(e.Input); !declaresInput(s, input) {
			return false, "the baseline does not declare input " + input
		}
		return true, ""
	}
	if e.Publish {
		if !rootFieldTakes(s, "mutation", e.Mutation, "token") {
			return false, "the baseline does not declare " + e.Mutation + "(token:…)"
		}
		if !rootFieldTakes(s, "query", e.Read, "token") {
			return false, "the baseline does not declare " + e.Read + "(token:…)"
		}
		return true, ""
	}
	if !rootFieldTakes(s, "mutation", e.Mutation, e.arg()) {
		return false, "the baseline does not declare " + e.Mutation + "(" + e.arg() + ":…)"
	}
	// The read is matched the way readDoc SPELLS it, so a query that changed from
	// a list lookup to a single one counts as unsupported rather than being read
	// with the wrong document.
	readArg := "tokens"
	if e.Single {
		readArg = e.readArg()
	}
	if !rootFieldTakes(s, "query", e.Read, readArg) {
		return false, "the baseline does not declare " + e.Read + "(" + readArg + ":…)"
	}
	if input := inputTypeName(e.Input); !declaresInput(s, input) {
		return false, "the baseline does not declare input " + input
	}
	return true, ""
}

// adapt returns the entity AS THIS BASELINE CAN EXPRESS IT, which today means one
// thing: dropping the result envelope when the baseline's create returns the
// object directly.
//
// 🔴 WHY THIS IS AN ADAPTATION AND NOT A SKIP. createCommand returns `Command!` in
// v0.11.0 and `CreateCommandResult!` at HEAD, and that is the ONLY difference —
// CommandCreateRequest is byte-identical between the two, every field the table
// selects is on both `Command` types, and commandsByToken is unchanged. Skipping
// the row over a response wrapper would cost command-delivery its ENTIRE
// contribution to the drill (its only other entity, createCommandBatch, does not
// exist in v0.11.0 at all) — and that is the area this release changed most.
//
// 🔑 IT RETURNS AN ENTITY RATHER THAN A FLAG, and that is the whole design. The
// envelope is read in two places — createDoc renders it, createdObjects unwraps
// it — and a flag threaded to both is a flag that can reach one and not the
// other, producing a document whose response is then decoded by the wrong rule.
// Clearing Wrap on a copy makes them read the SAME field, so they cannot disagree.
// Same reason Fields is one string used by both documents.
//
// A nil baseline adapts nothing: seeding the current release writes the table as
// written, which is what a fresh install expects.
//
// The bare document's SELECTION is not checked here, deliberately and for the
// reason the header gives: this file matches document SHAPE, never fields. If the
// older type is missing something the table selects, the platform refuses the
// create and names the entity — the loud failure, in the right place.
func (b *baseline) adapt(e entity) entity {
	if b == nil || e.Wrap == "" {
		return e
	}
	if b.envelopes(e) {
		return e
	}
	e.Wrap = ""
	e.Reject = ""
	return e
}

// envelopes reports whether the baseline's create returns a result envelope
// carrying this entity's Wrap field, rather than the created object itself.
func (b *baseline) envelopes(e entity) bool {
	returns, ok := b.returnTypeOf(e.Area, e.Mutation)
	if !ok {
		return false
	}
	return b.typeDeclaresField(e.Area, returns, e.Wrap)
}

// returnTypeOf reads a mutation's declared result type, stripped of its
// decoration: `createCommand(request: X!): CreateCommandResult!` yields
// "CreateCommandResult".
func (b *baseline) returnTypeOf(area, field string) (string, bool) {
	f := rootField(b.schemas[area], "mutation", field)
	if f == nil {
		return "", false
	}
	return inputTypeName(f.Type.String()), true
}

// typeDeclaresField reports whether the named OBJECT type declares this field.
func (b *baseline) typeDeclaresField(area, typeName, field string) bool {
	s := b.schemas[area]
	if s == nil {
		return false
	}
	t, ok := s.Types[typeName].(*ast.ObjectTypeDefinition)
	return ok && t.Fields.Get(field) != nil
}

// plan decides what a seed should do with one entity: write it, skip it, or
// refuse to run at all.
//
// 🔴 THE THIRD ANSWER IS THE ONE THAT MATTERS. An entity whose token LATER rows
// reference cannot be skipped — the dependents would send an empty string and be
// refused by the platform for a reason that names them rather than the hole,
// several rows after the decision that made it. The drill would report a finding
// about the wrong entity, and the real cause would be a skip printed minutes
// earlier and scrolled past.
//
// A nil baseline means no filtering at all: a seed against the current release
// writes the whole table, which is what `verify` on a fresh install expects.
func plan(e entity, base *baseline) (write bool, why string, err error) {
	if base == nil {
		return true, "", nil
	}
	ok, reason := base.supports(e)
	if ok {
		return true, "", nil
	}
	if e.Record != nil {
		return false, reason, failWith(exitSetup,
			"%s cannot be seeded against this baseline (%s), and later entities reference it; "+
				"this drill cannot skip it, so the table must be pinned to what the baseline can express",
			e.Name, reason)
	}
	return false, reason, nil
}

// rootField returns the field a root operation type (op is "query" or "mutation")
// declares under name, or nil.
func rootField(s *ast.Schema, op, name string) *ast.FieldDefinition {
	if s == nil {
		return nil
	}
	root, ok := s.RootOperationTypes[op].(*ast.ObjectTypeDefinition)
	if !ok {
		return nil
	}
	return root.Fields.Get(name)
}

// rootFieldTakes reports whether a root field exists and takes an argument named arg
// — the argument the generated document addresses it by.
func rootFieldTakes(s *ast.Schema, op, name, arg string) bool {
	f := rootField(s, op, name)
	return f != nil && f.Arguments.Get(arg) != nil
}

// declaresInput reports whether the schema declares an input object of that name.
func declaresInput(s *ast.Schema, name string) bool {
	_, ok := s.Types[name].(*ast.InputObject)
	return ok
}

// declaresField reports whether the schema declares `field` on the input or object
// type `typeName`. A field on ANOTHER type, or a name mentioned only in a comment or a
// description, is not a declaration.
func declaresField(s *ast.Schema, typeName, field string) bool {
	switch t := s.Types[typeName].(type) {
	case *ast.InputObject:
		return t.Values.Get(field) != nil
	case *ast.ObjectTypeDefinition:
		return t.Fields.Get(field) != nil
	}
	return false
}

// inputTypeName reduces a full GraphQL input reference to the bare type name:
// "[EntityRelationshipCreateRequest!]!" is a list of the same type a
// non-list entry names directly, and only the name is declared.
func inputTypeName(input string) string {
	return strings.Trim(input, "[]!")
}

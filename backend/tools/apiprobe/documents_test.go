// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// Every document this tool sends is a STRING. The compiler does not read it, the
// unit tests here did not read it, and the schema it has to satisfy sits in
// another module — so for a long time the only thing that ever read one was a
// cluster, at the far end of a drill that takes twenty minutes to reach the first
// mutation.
//
// 🔴 THAT IS EXACTLY HOW THE PROVISIONING DOCUMENTS SHIPPED BROKEN. `createIdentity`
// omitted two fields the input type declares NOT NULL, and the membership mutation
// named `addTenantAdministrator`, which no schema in this repository has ever
// served. Both were caught by the first live run, after a cluster had been built,
// upgraded and torn down — and neither is subtle. Nothing had ever asked the schema.
//
// So this file asks it, with the SERVER'S OWN VALIDATOR rather than a re-implementation
// of it: the schemas are parsed by the same library the services parse them with, and
// each document is validated the way the server would validate it. The tests here are
// therefore not an approximation that can drift from the real rule — they ARE the real
// rule, run against files instead of over HTTP.
//
// # WHAT THIS CANNOT SEE
//
// Validation is about the DOCUMENT, so a value the schema types as `String` is opaque
// to it. `parameterSchema` is a String carrying an ordered []CommandParameter, and a
// JSON-Schema document in that field validates perfectly and is refused by the
// platform. Only a live run finds that class; this file narrows what a live run has
// to be spent on, it does not replace it.
//
// 🔴 RUN WITH -count=1 AFTER TOUCHING A SCHEMA: the files live outside this module
// and Go's test cache does not track them, so an edited schema is served a stale PASS.

// plane names which of an area's two GraphQL surfaces to load. They are separate
// schemas served at separate paths under separate principals, and folding them
// together would let a document validate against a type the endpoint it is sent to
// does not serve.
type plane bool

const (
	tenantPlane plane = false
	adminPlane  plane = true
)

// servedSchema parses what an area actually serves on one plane, using the same
// library the services use. Both extensions are read: user-management spells its
// schemas `.gql` and every other area spells them `.graphql`.
func servedSchema(t *testing.T, area string, p plane) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "services", area, "graphql")

	var files []string
	for _, ext := range []string{"*.graphql", "*.gql"} {
		found, err := filepath.Glob(filepath.Join(dir, ext))
		if err != nil {
			t.Fatalf("glob %s/%s: %v", dir, ext, err)
		}
		files = append(files, found...)
	}
	sort.Strings(files)

	var sdl strings.Builder
	for _, f := range files {
		if strings.Contains(filepath.Base(f), "admin") != bool(p) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sdl.Write(body)
		sdl.WriteString("\n")
	}
	if sdl.Len() == 0 {
		t.Fatalf("no schema files for area %q on plane %v; a test that parsed nothing would validate everything", area, p)
	}

	// A nil resolver is enough: validation reads the schema, never a resolver.
	schema, err := graphql.ParseSchema(sdl.String(), nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse %s (%v plane): %v", area, p, err)
	}
	return schema
}

// The provisioning triple, validated against the admin schema it is sent to.
// The variables are supplied because ValidateWithVariables checks them too, and a
// document validated with no variables at all reports every one of them as a null
// that violates its own `String!` — noise that would bury the finding this exists for.
func TestEveryProvisioningDocumentValidatesAgainstTheAdminSchema(t *testing.T) {
	schema := servedSchema(t, "user-management", adminPlane)

	for _, c := range []struct {
		name string
		doc  string
		vars map[string]any
	}{
		{"createTenant", createTenantMutation, map[string]any{
			"token": "apiprobe", "name": "API probe", "tier": probeTenantTier,
		}},
		{"createIdentity", createIdentityMutation, map[string]any{
			"email": "apiprobe@apiprobe.invalid", "password": "secret",
		}},
		{"addMembership", addMembershipMutation, map[string]any{
			"email": "apiprobe@apiprobe.invalid", "tenant": "apiprobe",
		}},
	} {
		if errs := schema.ValidateWithVariables(c.doc, c.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against the served admin schema: %v\n  document: %s", c.name, errs, c.doc)
		}
	}
}

// Every create and read-back the coverage table generates, validated against the
// area that serves it — WITH the values the seed actually sends, so a required
// input field the table omits is a failure here rather than a REFUSED exit on a
// cluster.
//
// The state is threaded exactly as the seed threads it, because a create that
// references an earlier entity's token sends an empty string otherwise, and an
// empty string is a perfectly valid String! — the run would pass while testing a
// shape the seed never produces.
func TestEveryEntityDocumentValidatesAgainstItsServedSchema(t *testing.T) {
	st := newState("apiprobe")
	byArea := map[string]*graphql.Schema{}

	for _, e := range allEntities() {
		schema, ok := byArea[e.Area]
		if !ok {
			schema = servedSchema(t, e.Area, tenantPlane)
			byArea[e.Area] = schema
		}

		if errs := schema.ValidateWithVariables(e.createDoc(), e.Vars(st)); len(errs) > 0 {
			t.Errorf("entity %q: the create does not validate against %s: %v\n  document: %s",
				e.Name, e.Area, errs, e.createDoc())
		}
		// The read variables have to be built the way VERIFY builds them, not the way
		// this test finds convenient: a criteria-addressed read takes its whole variable
		// from the table, and validating it with a token would test a document nothing
		// sends.
		readVars := map[string]any{"token": st.tok(e.Name)}
		if e.ReadInput != "" {
			readVars = map[string]any{"c": e.ReadVars}
		}
		if errs := schema.ValidateWithVariables(e.readDoc(), readVars); len(errs) > 0 {
			t.Errorf("entity %q: the read-back does not validate against %s: %v\n  document: %s",
				e.Name, e.Area, errs, e.readDoc())
		}

		if e.Record != nil {
			e.Record(st, map[string]any{"token": st.tok(e.Name)})
		}
	}
}

// An entity that unwraps a result envelope must select the envelope's rejection —
// WHEN THE ENVELOPE HAS ONE.
//
// 🔴 THIS REPLACES A RULE THAT ASSUMED EVERY ENVELOPE CARRIES A REJECTION. Two
// different shapes wear the same Wrap:
//
//   - enqueueCommand returns {command, rejection}: a STRUCTURED refusal, where a
//     declined create arrives as a null object with a reason beside it. Not selecting
//     the rejection there loses the reason, which is the defect the old rule caught.
//   - replaceDevice returns a result with no rejection field at all: a refusal is a
//     GraphQL error, and there is nothing beside the object to ask for. Demanding a
//     Reject there demands a selection the schema cannot serve.
//
// So the question is answered by the schema rather than by a rule about envelopes,
// which is also what stops this drifting: an envelope that GAINS a rejection field
// starts being required to select it, with no list here to remember to update.
func TestAnEnvelopeWithARejectionSelectsIt(t *testing.T) {
	byArea := map[string]*graphql.Schema{}
	checked := 0

	for _, e := range allEntities() {
		if e.Wrap == "" {
			continue
		}
		schema, ok := byArea[e.Area]
		if !ok {
			schema = servedSchema(t, e.Area, tenantPlane)
			byArea[e.Area] = schema
		}
		fields, err := mutationReturnFields(schema, e.Mutation)
		if err != nil {
			t.Errorf("entity %q: %v", e.Name, err)
			continue
		}
		checked++
		if fields["rejection"] && e.Reject == "" {
			t.Errorf("entity %q unwraps %q from a result that DOES declare a rejection, "+
				"and selects none; a refusal would arrive as a bare absent object with no reason",
				e.Name, e.Wrap)
		}
		if !fields["rejection"] && e.Reject != "" {
			t.Errorf("entity %q selects a rejection its result type does not declare; "+
				"the document will not validate", e.Name)
		}
		if !fields[e.Wrap] {
			t.Errorf("entity %q unwraps %q, which its result type does not declare", e.Name, e.Wrap)
		}
	}

	// A vacuous pass reads exactly like a thorough one: if nothing set Wrap, or the
	// schema could not be read, every entity trivially passes.
	if checked == 0 {
		t.Fatal("no entity with a result envelope was examined; the check is inspecting nothing")
	}
}

// mutationReturnFields names the fields of the type a mutation returns, unwrapping
// NON_NULL and LIST to reach it.
//
// It reads the parsed AST rather than running introspection: servedSchema parses with
// a NIL RESOLVER — enough for validation, which is all it was ever for — and Exec on a
// resolverless schema panics. The AST is the same schema the validator reads, and it
// is available without pretending there is a server behind it.
func mutationReturnFields(schema *graphql.Schema, mutation string) (map[string]bool, error) {
	doc := schema.AST()
	root, ok := doc.RootOperationTypes["mutation"].(*ast.ObjectTypeDefinition)
	if !ok {
		return nil, fmt.Errorf("the schema declares no mutation type")
	}
	field := root.Fields.Get(mutation)
	if field == nil {
		return nil, fmt.Errorf("the schema serves no mutation %q", mutation)
	}
	returned := namedTypeOf(field.Type)
	object, ok := doc.Types[returned].(*ast.ObjectTypeDefinition)
	if !ok {
		return nil, fmt.Errorf("%q returns %q, which is not an object type", mutation, returned)
	}
	names := map[string]bool{}
	for _, f := range object.Fields {
		names[f.Name] = true
	}
	return names, nil
}

// namedTypeOf strips the NonNull/List wrappers around a named type.
func namedTypeOf(t ast.Type) string {
	for {
		switch inner := t.(type) {
		case *ast.NonNull:
			t = inner.OfType
		case *ast.List:
			t = inner.OfType
		default:
			return t.String()
		}
	}
}

// The controls are documents too, and a control that cannot be SENT arms nothing —
// which the rig reports as "the control could not be armed", one step removed from
// the reason.
func TestEveryTamperDocumentValidatesAgainstItsServedSchema(t *testing.T) {
	for _, tp := range tampers {
		e, ok := entityNamed(tp.Entity)
		if !ok {
			t.Errorf("tamper %q targets %q, which is not in the coverage table", tp.Mode, tp.Entity)
			continue
		}
		schema := servedSchema(t, e.Area, tenantPlane)
		if errs := schema.ValidateWithVariables(tp.doc(), tp.vars("apiprobe-"+tp.Entity)); len(errs) > 0 {
			t.Errorf("tamper %q does not validate against %s: %v\n  document: %s",
				tp.Mode, e.Area, errs, tp.doc())
		}
	}
}

// 🔴 THE NEGATIVE CONTROLS. Everything above is a check that passes, and a check
// that has never been shown to FAIL is indistinguishable from one that cannot.
// Each case below is a real defect this file has already caught once, replayed
// against the same validator to prove the validator is what caught it.
func TestTheValidatorRejectsTheDefectsItWasBuiltFor(t *testing.T) {
	admin := servedSchema(t, "user-management", adminPlane)
	tenant := servedSchema(t, "device-management", tenantPlane)

	for _, c := range []struct {
		name   string
		schema *graphql.Schema
		doc    string
		vars   map[string]any
		want   string
	}{
		{
			// The shipped defect, verbatim: two NOT NULL fields omitted.
			name:   "a required input field the document omits",
			schema: admin,
			doc: `mutation($email:String!,$password:String!){` +
				`createIdentity(request:{email:$email,password:$password}){email}}`,
			vars: map[string]any{"email": "a@b.invalid", "password": "p"},
			want: "enabled",
		},
		{
			// The other shipped defect: a mutation no schema has ever served.
			name:   "a mutation the schema does not declare",
			schema: admin,
			doc: `mutation($email:String!,$tenant:String!){` +
				`addTenantAdministrator(email:$email,tenantToken:$tenant){email}}`,
			vars: map[string]any{"email": "a@b.invalid", "tenant": "t"},
			want: "addTenantAdministrator",
		},
		{
			// The class the entity table is most exposed to: a selection naming a
			// field that is not on the type. Nothing but the schema can tell.
			name:   "a selected field the type does not have",
			schema: tenant,
			doc:    `query($token:String!){deviceProfilesByToken(tokens:[$token]){token nonesuch}}`,
			vars:   map[string]any{"token": "apiprobe-device-profile"},
			want:   "nonesuch",
		},
		{
			// An input VALUE, not just the document: the seed's variables are
			// validated too, which is the only reason a missing required field
			// inside `$req` is visible at all.
			name:   "a required field missing from the variable's value",
			schema: tenant,
			doc:    `mutation($req:MetricDefinitionCreateRequest!){createMetricDefinition(request:$req){token}}`,
			vars:   map[string]any{"req": map[string]any{"token": "apiprobe-metric-definition"}},
			want:   "deviceProfileToken",
		},
	} {
		errs := c.schema.ValidateWithVariables(c.doc, c.vars)
		if len(errs) == 0 {
			t.Errorf("%s: the validator accepted it, so nothing above is evidence of anything", c.name)
			continue
		}
		var joined []string
		for _, e := range errs {
			joined = append(joined, e.Error())
		}
		if all := strings.Join(joined, "; "); !strings.Contains(all, c.want) {
			t.Errorf("%s: rejected, but for the wrong reason — wanted a message naming %q, got: %s", c.name, c.want, all)
		}
	}
}

// The adapted document has to be VALID, not merely different — and against the
// older schema, which is the one thing the working tree cannot supply. So the
// v0.11.0 shape is written out here and both documents are validated against the
// schema each is meant for.
//
// 🔴 THIS IS THE CHECK THE ADAPTATION EXISTS FOR. Without it, `adapt` is asserted
// only by its own unit test — which compares the entity against itself and would
// pass just as happily if the bare document it produced were unsendable. The
// cluster would say so, twenty minutes in, as a REFUSED naming the platform.
func TestBothEnvelopeShapesOfTheCommandCreateValidate(t *testing.T) {
	// v0.11.0's command-delivery, reduced to what the probe touches: the create
	// returns the object directly, and the read is unchanged.
	const v011 = `
type Mutation {
    createCommand(request: CommandCreateRequest!): Command!
}
type Query {
    commandsByToken(tokens: [String!]!): [Command!]!
}
type Command {
    token: String!
    deviceToken: String!
    name: String!
    payload: String
    metadata: String
}
input CommandCreateRequest {
    token: String!
    deviceToken: String!
    name: String!
    payload: String
    metadata: String
}
`
	old, err := graphql.ParseSchema(v011, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse the v0.11.0 fixture: %v", err)
	}

	command, ok := entityNamed("command")
	if !ok {
		t.Fatal("the coverage table has no \"command\" entity, so this test asserts nothing")
	}
	if command.Wrap == "" {
		t.Fatal("the \"command\" entity no longer wraps, so there is no adaptation to check")
	}
	st := newState("apiprobe")

	// The adapted (bare) document against the old schema.
	bare := command
	bare.Wrap, bare.Reject = "", ""
	if errs := old.ValidateWithVariables(bare.createDoc(), bare.Vars(st)); len(errs) > 0 {
		t.Errorf("the adapted create does not validate against a v0.11.0-shaped schema: %v\n  document: %s",
			errs, bare.createDoc())
	}
	if errs := old.ValidateWithVariables(bare.readDoc(), map[string]any{"token": st.tok("command")}); len(errs) > 0 {
		t.Errorf("the read-back does not validate against a v0.11.0-shaped schema: %v\n  document: %s",
			errs, bare.readDoc())
	}

	// The counterweight: the UNADAPTED document must be refused by that same
	// schema. Without this the test above would pass even if the two documents
	// were identical, and the adaptation would be doing nothing.
	if errs := old.ValidateWithVariables(command.createDoc(), command.Vars(st)); len(errs) == 0 {
		t.Error("the enveloped create validated against a schema with no envelope, so the adaptation is unnecessary and this test proves nothing")
	}

	// And the reverse, against the tree HEAD actually serves: the enveloped
	// document validates, the bare one does not.
	head := servedSchema(t, "command-delivery", tenantPlane)
	if errs := head.ValidateWithVariables(command.createDoc(), command.Vars(st)); len(errs) > 0 {
		t.Errorf("the enveloped create does not validate against the served schema: %v", errs)
	}
	if errs := head.ValidateWithVariables(bare.createDoc(), bare.Vars(st)); len(errs) == 0 {
		t.Error("the bare create validated against HEAD's enveloped schema, so adapting on the wrong baseline would go unnoticed")
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// The READ SWEEP: after the upgrade, does everything the platform SERVES about this
// tenant's data still answer?
//
// # WHY VERIFY IS NOT ENOUGH, AND THE MEASUREMENT THAT SAYS SO
//
// verify reads back the 24 queries in the coverage table, and every one of them is
// "fetch the row I just wrote, by its token". Against the served schemas that is 24 of
// about 124 query fields — device-management alone serves 78. So the drill's guarantee
// has always been the narrower one: EVERY ROW YOU WROTE READS BACK UNCHANGED, not "the
// instance still works". Everything the platform DERIVES from a write sits outside it.
//
// 🔴 THAT GAP IS NOT HYPOTHETICAL; IT IS WHERE #838 LIVED. The content-addressed geometry
// archive shipped with no backfill, so an upgraded instance held fence-set snapshots
// addressed by the empty string and stopped matching geofence rules. The fence ROW was
// untouched, so the round trip was spotless — the drill ran with the defect's exact
// conditions in its database and passed. Seeding another entity would not have helped.
// A round trip of one's own writes cannot see a derived artifact go stale.
//
// # WHAT THIS ASSERTS, AND WHAT IT DELIBERATELY DOES NOT
//
// Only that no query ERRORS. Not that any value is unchanged — that is verify's job, it
// does it against a receipt written before the upgrade, and duplicating it here with no
// receipt to compare against would be a weaker check wearing the same name.
//
// "Does not error" is a low bar that a surprising amount of damage cannot clear:
// hydrating a snapshot through a table that no longer contains its rows, decoding a
// stored document into a shape that has moved, resolving a version that no longer
// exists. Each of those is a 500 on a door a console opens on page load.
//
// # WHY THE LIST IS DERIVED FROM THE SCHEMA
//
// 🔑 Because a hand-written list is the thing that failed. The coverage table is
// hand-written — deliberately, because it is a COVERAGE CLAIM and belongs where someone
// can count it — but it enumerates what to WRITE, and writes need arguments a human has
// to choose. Reads mostly do not. So this half is derived from the served SDL: when a
// future release adds another `currentSomethingSet` door, the sweep calls it without
// anyone remembering to add it. That is the whole point, and it is why a skipped field
// is REPORTED BY NAME rather than dropped — a skip nobody can count is how the coverage
// claim rots back into a hand list.
//
// # THE FAIL-OPEN THIS COULD BECOME
//
// 🔴 A planner that skipped everything would sweep nothing and report a confident pass.
// Two things stop it: an empty plan is an error, and TestReadSweepPlansTheWholeServedSurface
// pins every query field as either PLANNED or EXEMPT-BY-NAME, so a field that starts being
// skipped fails a unit test instead of silently leaving the sweep.

// sweepCall is one planned read: the document to send and the variables to send with it.
type sweepCall struct {
	Area  string
	Field string
	Doc   string
	Vars  map[string]any
}

// sweepSkip is one query field the planner will not call, with the reason. It is a
// FIRST-CLASS RESULT rather than a log line: the sweep prints every one, and the unit
// test requires each to match a declared exemption, so the set of things this does not
// cover stays a list somebody can read.
type sweepSkip struct {
	Area   string
	Field  string
	Reason string
}

// selectionDepth bounds how far into a returned object the selection recurses.
//
// 🔑 TWO IS A MEASURED CHOICE, NOT A ROUND NUMBER. One would be enough for the defect
// this exists to catch: CurrentGeoFenceSetSnapshot hydrates EAGERLY, inside the top-level
// resolver, so `{ __typename }` alone raises #838's error — checked in the source, not
// assumed from the shape of the schema. Two costs almost nothing and widens the sweep to
// resolvers that do their work one level down. Going deeper starts to trade a readability
// check for a load test, on a drill whose failures are read at unsociable hours.
const selectionDepth = 2

// planReadSweep turns one area's served schema into calls and skips.
//
// tokens maps an entity name from the coverage table to the token the seed wrote, which
// is how a `tokens:[String!]!` read reaches a row that exists. A token that does not
// exist would return an empty list rather than an error, which is the failure mode this
// whole file is about: the call would succeed having read nothing.
func planReadSweep(area string, schema *ast.Schema, tokens map[string]string) ([]sweepCall, []sweepSkip) {
	query, ok := schema.Types["Query"].(*ast.ObjectTypeDefinition)
	if !ok {
		return nil, nil
	}

	// Which query field belongs to which seeded entity, from the coverage table, so the
	// mapping cannot drift from the entities the seed actually wrote.
	readOf := map[string]string{}
	for _, e := range allEntities() {
		if tok, ok := tokens[e.Name]; ok && tok != "" {
			readOf[e.Read] = tok
		}
	}

	var calls []sweepCall
	var skips []sweepSkip
	for _, field := range query.Fields {
		name := field.Name
		if strings.HasPrefix(name, "_") {
			skips = append(skips, sweepSkip{area, name, "a schema placeholder, not a door"})
			continue
		}
		if reason, exempt := sweepExemptions[area+"."+name]; exempt {
			skips = append(skips, sweepSkip{area, name, reason})
			continue
		}
		vars, decl, reason := planArguments(field, schema, supply{
			entity: readOf[name],
			named:  tokens,
			field:  area + "." + name,
		})
		if reason != "" {
			skips = append(skips, sweepSkip{area, name, reason})
			continue
		}
		selection := selectionFor(field.Type, schema, selectionDepth)
		doc := "query" + decl + "{" + name + argumentList(field) + selection + "}"
		calls = append(calls, sweepCall{Area: area, Field: name, Doc: doc, Vars: vars})
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Field < calls[j].Field })
	sort.Slice(skips, func(i, j int) bool { return skips[i].Field < skips[j].Field })
	return calls, skips
}

// planArguments supplies every REQUIRED argument of a field, or explains why it cannot.
//
// ⚠️ IT REFUSES RATHER THAN IMPROVISES, and that polarity is the important part. A
// generic value for, say, `source:String!` would be a guess, and a guess the platform
// rejects arrives as a query error — which this sweep reports as a FINDING. A drill that
// invents findings is worse than one with a gap, because the gap is written down and the
// invention is not.
func planArguments(field *ast.FieldDefinition, schema *ast.Schema, from supply) (map[string]any, string, string) {
	vars := map[string]any{}
	var decls []string
	for _, arg := range field.Arguments {
		if !isRequired(arg.Type) {
			continue
		}
		value, ok := argumentValue(arg, schema, from)
		if !ok {
			return nil, "", fmt.Sprintf("no value this tool can defend for %s:%s", arg.Name.Name, arg.Type.String())
		}
		vars[arg.Name.Name] = value
		decls = append(decls, "$"+arg.Name.Name+":"+arg.Type.String())
	}
	if len(decls) == 0 {
		return vars, "", ""
	}
	return vars, "(" + strings.Join(decls, ",") + ")", ""
}

// supply is everything the planner may draw an argument value from: the token of the
// entity this very field reads back, every seeded token by entity name, and the field's
// own qualified name so an ambiguity can be resolved by declaration.
type supply struct {
	entity string
	named  map[string]string
	field  string
}

// argumentValue is the whole supply policy in one place.
func argumentValue(arg *ast.InputValueDefinition, schema *ast.Schema, from supply) (any, bool) {
	name := arg.Name.Name
	if tok, ok := from.tokenFor(name); ok {
		if _, list := underlying(arg.Type).(*ast.List); list {
			return []any{tok}, true
		}
		return tok, true
	}
	return inputValue(arg.Type, schema, 0)
}

// tokenFor answers "which seeded row does this argument mean", in three steps, most
// specific first.
func (s supply) tokenFor(arg string) (string, bool) {
	// 1. A declared ambiguity. See sweepTokenArgs for why these cannot be derived.
	if entity, ok := sweepTokenArgs[s.field+"."+arg]; ok {
		tok := s.named[entity]
		return tok, tok != ""
	}
	// 2. An argument whose NAME says what kind of token it is, platform-wide. `deviceToken`
	//    means a device token wherever it appears; nothing else can name it.
	switch arg {
	case "deviceToken", "deviceTokens":
		tok := s.named["device"]
		return tok, tok != ""
	}
	// 3. A bare `token`/`tokens` on a field that IS an entity's read-back query, which the
	//    coverage table answers exactly.
	switch arg {
	case "token", "tokens":
		return s.entity, s.entity != ""
	}
	return "", false
}

// underlying strips the non-null wrapper so a list can be recognised through it.
func underlying(t ast.Type) ast.Type {
	if nn, ok := t.(*ast.NonNull); ok {
		return nn.OfType
	}
	return t
}

// inputValue builds the smallest legal value of a type: a page-one page-size-one read for
// the pagination scalars every search criteria requires, false for a flag, the first
// member of an enum, and a recursive minimum for a required input object.
//
// It returns false for String and ID rather than inventing one — see planArguments.
func inputValue(t ast.Type, schema *ast.Schema, depth int) (any, bool) {
	if depth > 4 {
		return nil, false
	}
	switch typed := t.(type) {
	case *ast.NonNull:
		return inputValue(typed.OfType, schema, depth)
	case *ast.List:
		// An empty list is legal, cheap and reads nothing. It is offered only where the
		// caller has nothing better; `tokens` is intercepted above precisely so the
		// token-shaped reads do not land here and pass having looked at no row.
		return []any{}, true
	}

	named, ok := t.(ast.NamedType)
	if !ok {
		return nil, false
	}
	switch def := schema.Types[named.String()].(type) {
	case *ast.EnumTypeDefinition:
		if len(def.EnumValuesDefinition) == 0 {
			return nil, false
		}
		return def.EnumValuesDefinition[0].EnumValue, true
	case *ast.InputObject:
		obj := map[string]any{}
		for _, f := range def.Values {
			if !isRequired(f.Type) {
				continue
			}
			v, ok := inputValue(f.Type, schema, depth+1)
			if !ok {
				return nil, false
			}
			obj[f.Name.Name] = v
		}
		return obj, true
	}

	switch named.String() {
	case "Int":
		// 1 serves both pagination scalars: page one, one row. Zero is a plausible
		// alternative and a worse one — a pageSize of 0 is refused by some criteria and
		// the refusal would arrive dressed as a finding.
		return 1, true
	case "Float":
		return 0, true
	case "Boolean":
		return false, true
	}
	return nil, false
}

// argumentList renders the call site for the arguments planArguments declared.
func argumentList(field *ast.FieldDefinition) string {
	var parts []string
	for _, arg := range field.Arguments {
		if !isRequired(arg.Type) {
			continue
		}
		parts = append(parts, arg.Name.Name+":$"+arg.Name.Name)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// selectionFor builds a selection set for a returned type.
//
// `__typename` is always selected and is what makes this total: every object type has it,
// so a type whose every field takes an argument still yields a legal document instead of
// an empty brace pair the server rejects — a rejection that would be reported as a finding
// about the platform when it was a defect in this planner.
//
// ⚠️ THE DEPTH BOUND IS ALSO WHAT TERMINATES THIS, and it is the only thing that needs to.
// The graph is full of cycles (a Device names its DeviceType, which names its DeviceProfile,
// which names its rules...) and an earlier draft carried a `seen` set against them, with a
// comment saying a cycle would otherwise hang the planner. That was wrong: depth decreases
// on every recursion and stops at zero whatever the shape of the graph, so the set could
// never fire and the sentence defending it described a hazard that did not exist. It was
// removed after a mutation that disabled it changed no test and no output.
func selectionFor(t ast.Type, schema *ast.Schema, depth int) string {
	switch typed := t.(type) {
	case *ast.NonNull:
		return selectionFor(typed.OfType, schema, depth)
	case *ast.List:
		return selectionFor(typed.OfType, schema, depth)
	}
	named, ok := t.(ast.NamedType)
	if !ok {
		return ""
	}
	name := named.String()

	var fields ast.FieldsDefinition
	switch def := schema.Types[name].(type) {
	case *ast.ObjectTypeDefinition:
		fields = def.Fields
	case *ast.InterfaceTypeDefinition:
		fields = def.Fields
	default:
		return "" // a scalar or enum: selecting anything on it is an error
	}

	if depth <= 0 {
		return "{__typename}"
	}

	parts := []string{"__typename"}
	for _, f := range fields {
		if hasRequiredArgument(f) {
			continue // its value is a choice, and this planner does not make choices
		}
		sub := selectionFor(f.Type, schema, depth-1)
		parts = append(parts, f.Name+sub)
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func hasRequiredArgument(f *ast.FieldDefinition) bool {
	for _, a := range f.Arguments {
		if isRequired(a.Type) {
			return true
		}
	}
	return false
}

// isRequired is non-null WITHOUT a default. A non-null argument carrying a default is
// optional at the call site, and treating it as required would skip fields this can call.
func isRequired(t ast.Type) bool {
	_, nn := t.(*ast.NonNull)
	return nn
}

// loadServedSchema parses one area's TENANT-plane schema from a services tree.
//
// The admin plane is deliberately excluded: it is served at another path under another
// principal, and the sweep authenticates as the ordinary tenant administrator the seed
// used — for the reason session.provision gives, that this is the principal a real client
// is. Folding the planes together would plan calls this session cannot make and report
// every one of them as a finding.
func loadServedSchema(dir, area string) (*ast.Schema, error) {
	var files []string
	for _, ext := range []string{"*.graphql", "*.gql"} {
		found, err := filepath.Glob(filepath.Join(dir, area, "graphql", ext))
		if err != nil {
			return nil, failWith(exitSetup, "glob %s schemas: %w", area, err)
		}
		files = append(files, found...)
	}
	sort.Strings(files)

	var sdl strings.Builder
	for _, f := range files {
		if strings.Contains(filepath.Base(f), "admin") {
			continue
		}
		body, err := os.ReadFile(f) //nolint:gosec // a path this tool globbed itself
		if err != nil {
			return nil, failWith(exitSetup, "read %s: %w", f, err)
		}
		sdl.Write(body)
		sdl.WriteString("\n")
	}
	if strings.TrimSpace(sdl.String()) == "" {
		return nil, nil
	}
	parsed, err := graphql.ParseSchema(sdl.String(), nil, graphql.UseFieldResolvers())
	if err != nil {
		return nil, failWith(exitSetup, "parse %s schema: %w", area, err)
	}
	return parsed.AST(), nil
}

func runReadSweep(ctx context.Context, argv []string) error {
	fs := flagSetFor("readsweep")
	var c connection
	c.bind(fs)
	var receiptPath, schemaDir string
	fs.StringVar(&receiptPath, "receipt", "", "path to the receipt seed wrote (required)")
	fs.StringVar(&schemaDir, "schemas", "",
		"the DEPLOYED release's `backend/services` tree, whose served schemas name the doors to sweep (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(receiptPath) == "" || strings.TrimSpace(schemaDir) == "" {
		return failWith(exitSetup, "--receipt and --schemas are both required")
	}

	receipt, err := readReceipt(receiptPath)
	if err != nil {
		return err
	}
	c.tenant = receipt.Tenant
	session := c.session(receipt.Identity)

	tokens := map[string]string{}
	for _, r := range receipt.Entities {
		tokens[r.Name] = r.Token
	}

	var calls []sweepCall
	var skips []sweepSkip
	for _, area := range probeAreas() {
		schema, err := loadServedSchema(schemaDir, area)
		if err != nil {
			return err
		}
		if schema == nil {
			continue
		}
		areaCalls, areaSkips := planReadSweep(area, schema, tokens)
		calls = append(calls, areaCalls...)
		skips = append(skips, areaSkips...)
	}

	// 🔴 The fail-open guard. A planner that produced nothing would otherwise sweep
	// nothing and report the most confident pass this tool can print.
	if len(calls) == 0 {
		return failWith(exitSetup, "the read sweep planned no calls at all; that is a defect in this tool, not a verdict on the instance")
	}

	fmt.Printf("read sweep: %d doors planned, %d skipped by name\n", len(calls), len(skips))
	for _, s := range skips {
		fmt.Printf("    SKIP %s.%s — %s\n", s.Area, s.Field, s.Reason)
	}

	var failures []string
	for _, call := range calls {
		var envelope map[string]json.RawMessage
		if err := session.Query(ctx, c.areaURL(call.Area), call.Doc, call.Vars, &envelope); err != nil {
			// Collected rather than returned. One stale stored shape usually breaks several
			// doors at once, and the SET of them is what says which subsystem moved — where
			// the first one alone reads like an isolated resolver bug.
			failures = append(failures, fmt.Sprintf("%s.%s: %v", call.Area, call.Field, err))
		}
	}
	if len(failures) > 0 {
		// ⚠️ THE MESSAGE DOES NOT ASSERT A CAUSE, AND AN EARLIER ONE DID. It said "the
		// rows are still there and a client can no longer read them", which is the
		// conclusion the UPGRADE RIG is entitled to — it has just watched verify pass over
		// the same rows — and which this tool, run on its own against any instance, is not.
		// The first hand run of it named two doors this way that were simply absent from an
		// older deployment than the schema tree it had been given.
		//
		// So it reports the FACT and orders the hypotheses. The rig's own phase draws the
		// sharper conclusion, because there it is earned.
		return failWith(exitUnreadable, "%d of %d served doors ERRORED:\n    %s\n\n"+
			"In order of likelihood:\n"+
			"  1. --schemas does not describe what is DEPLOYED. A door reported as unknown to the\n"+
			"     server is this, every time — point --schemas at the deployed release's tree.\n"+
			"  2. The door needs an authority this principal does not hold. The probe runs as an\n"+
			"     ordinary tenant administrator on purpose; a system-tier door belongs in\n"+
			"     sweepExemptions, with the reason.\n"+
			"  3. A STORED SHAPE the deployed release can no longer make sense of. This is the one\n"+
			"     worth stopping a release for, and it is what the upgrade drill runs this to find —\n"+
			"     but only the drill can tell it apart from 1 and 2, because only the drill has just\n"+
			"     watched the same rows read back unchanged.",
			len(failures), len(calls), strings.Join(failures, "\n    "))
	}
	fmt.Printf("READ SWEEP CLEAN — all %d planned doors answered\n", len(calls))
	return nil
}

// sweepTokenArgs resolves the arguments whose NAME does not say what kind of token they
// want, keyed by area.field.argument and naming an entity from the coverage table.
//
// 🔑 THESE ARE DECLARED, NOT DERIVED, AND THE REASON IS WORTH KEEPING. Every entry is a
// door listing the VERSION HISTORY of something — a device profile's published versions,
// a dashboard's, a connector's, an entity group's. That is exactly the class of stored
// artifact the drill could not see before (#838 was one), so calling them is the point of
// this sweep rather than an extra. Their argument is spelled `token`, which says only
// "a token": inferring the kind from the field's return type would work today and would
// be a guess dressed as a rule, and a wrong guess here does not fail — it returns an
// empty list and reports a clean sweep having read nothing. A short declared list that a
// test holds to real entity names is the honest form.
//
// A new entry is needed only when a door takes an ambiguous token; a door taking
// `deviceToken`, or one that is an entity's own read-back, needs nothing.
var sweepTokenArgs = map[string]string{
	"device-management.deviceProfileVersions.token":          "device-profile",
	"device-management.entityGroupVersions.token":            "entity-group",
	"device-management.resolveDeviceGroupTargets.groupToken": "entity-group",
	"dashboard-management.dashboardVersions.token":           "dashboard",
	"outbound-connectors.connectorVersions.token":            "connector",
}

// sweepExemptions names the served doors this sweep will NOT call, and why.
//
// 🔑 IT IS KEYED BY area.field AND CARRIES A REASON FOR THE SAME REASON tenantpurge's
// exemption registry does: "we do not check this" and "there is nothing here to check"
// are different facts, and collapsing them into silence is how a hole becomes invisible.
// A reader scanning the sweep's output sees each one printed, with its sentence.
var sweepExemptions = map[string]string{
	"device-management.previewSelector": "an authoring preview: it evaluates a selector expression " +
		"the CALLER writes, so there is no stored row for the drill to reach through it",
	"device-management.validateCommandEnqueue": "a pre-flight gate over a command the CALLER is about " +
		"to send; it needs a commandKey from the device's own vocabulary, which is a choice, not a token",
	"device-management.validateCommandEnqueueBatch": "the batch form of validateCommandEnqueue, and " +
		"exempt for the same reason",

	// The two below are exempt because their argument is a GUESS, and a guess that
	// misses does not fail quietly — `geoFenceSetSnapshot(version:)` on an unknown
	// version is an error BY DESIGN, so the sweep would report the platform working
	// exactly as specified as an upgrade defect. Nothing is lost: currentGeoFenceSet
	// and currentGeoFenceSetManifest reach the SAME hydration path (the one the
	// geometry archive broke) with no version to guess at.
	"device-management.geoFenceSetSnapshot": "takes a fence-set VERSION, and an unknown one is an " +
		"error by design; currentGeoFenceSet reaches the same hydration with nothing to guess",
	"device-management.geoFenceSetManifest": "takes a fence-set VERSION, for the reason " +
		"geoFenceSetSnapshot is exempt; currentGeoFenceSetManifest covers the same path",

	// 🔴 A PERMISSION REFUSAL IS NOT A FINDING, and this door produces one every time.
	// Its schema says so in as many words: it requires `command:claim`, which is
	// system-tier, so it is reachable only by a service or identity token and never by
	// the tenant user this probe deliberately runs as. The refusal is the platform
	// working. Left un-exempt it would fail every drill for a reason that has nothing
	// to do with an upgrade — the cried wolf that teaches everyone to read past a red.
	"command-delivery.drainableCommands": "requires the system-tier command:claim authority, which the " +
		"tenant administrator this probe runs as deliberately does not hold; the refusal is correct behaviour",
}

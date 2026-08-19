// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
)

// Every document this package sends is a STRING. The compiler does not read it, the
// unit tests do not read it — they drive the classifiers, which never see one — and
// the schema it must satisfy lives in another module. So until now the only thing
// that ever read one was a cluster, at the far end of a manual gate that needs a
// real deployment to reach.
//
// 🔴 THAT IS EXACTLY HOW A HARNESS SHIPS BROKEN. The v0.12.0 upgrade drill went out
// with two of its three provisioning documents invalid against every schema this
// repository has ever served — one omitting required input fields, one naming a
// mutation that does not exist — after a mutation harness scored it 29/29. A
// mutation harness measures the logic AROUND a string; it never measures the string.
//
// So this file asks the schema, with the SERVER'S OWN VALIDATOR rather than a
// re-implementation of it: the SDL is parsed by the same library the services parse
// it with, and each document is validated the way the server would. It is not an
// approximation that can drift from the real rule — it IS the real rule, run against
// files instead of over HTTP.
//
// WHAT IT CANNOT SEE: validation is about the DOCUMENT. A value the schema types as
// String is opaque to it, so an authored rule definition (a String carrying JSON that
// event-processing decodes with DisallowUnknownFields) validates perfectly here and
// can still be refused by the platform. This narrows what a live run has to be spent
// on; it does not replace one.
//
// 🔴 THE SDL LIVES OUTSIDE THIS MODULE AND GO'S TEST CACHE DOES NOT TRACK IT, so a
// plain `go test` serves a stale PASS over an edited schema — measured, not assumed.
// CI is safe: every module's tests run with `-count=1`. What was not safe is the
// pre-commit sweep in CLAUDE.md, which omitted the flag and so returned green over a
// broken schema; it now carries it. An instruction to remember a flag is the same
// class of thing as a comment asserting an invariant, so the fix is in the recipe.

// servedSchema parses what one functional area serves on its TENANT plane — the only
// plane this package ever sends to; it holds no admin token and by design never will.
// Admin SDL files are excluded rather than merged, because folding the two together
// would let a document validate against a type the endpoint it is sent to does not serve.
func servedSchema(t *testing.T, area string) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "services", area, "graphql")

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
		if strings.Contains(filepath.Base(f), "admin") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sdl.Write(body)
		sdl.WriteString("\n")
	}
	// A test that parsed nothing would validate everything, which is the shape of
	// gate this whole file exists to refuse.
	if sdl.Len() == 0 {
		t.Fatalf("no tenant-plane schema files found for area %q under %s", area, dir)
	}

	// A nil resolver is enough: validation reads the schema, never a resolver.
	//
	// 🔴 THE SAME OPTIONS THE SERVICES PARSE WITH. MaxDepth bites at VALIDATE time, not
	// only at execution, so a validator without it is strictly weaker than the server
	// it stands in for — and a gate weaker than the thing it models will pass a
	// document the deployment refuses.
	schema, err := graphql.ParseSchema(sdl.String(), nil,
		graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatalf("parse %s: %v", area, err)
	}
	return schema
}

// harnessDocument is one document and the variables the harness really sends with it.
//
// The variables are supplied for two reasons, and only one is what it looks like.
// They stop a document validated with no variables at all from reporting every one as
// a null violating its own `String!` — noise that would bury the real finding. And
// they pin the KEY SET, which is the one thing the validator genuinely reads about
// them: this repo runs a patched graphql-go precisely because upstream DISCARDS an
// input-object entry the schema does not define when it arrives through a variable.
//
// 🔴 WHAT IT DOES NOT DO IS TYPE-CHECK VALUES. `pageSize: Int!` given the string
// "two", or an enum given a nonsense member, both validate. So a stand-in differing
// from its real call site in VALUE type is harmless, and one differing in KEY SET
// silently tests a request nobody makes.
type harnessDocument struct {
	name string
	area string
	doc  string
	vars map[string]any
}

// harnessDocuments is EVERY GraphQL document this package sends, each against the
// area that serves it. It is a hand-maintained list on purpose: a list derived from
// the source by scanning for backtick constants would silently shrink to zero the
// day the scan broke, and report a green.
func harnessDocuments() []harnessDocument {
	return []harnessDocument{
		// device-state — the presence harness.
		{"queryDeviceStates", "device-state", queryDeviceStates, map[string]any{
			"tokens": []any{"harness-pres-steady-001"},
		}},
		{"queryAssertedStates", "device-state", queryAssertedStates, map[string]any{
			"source": "mqtt1", "activeOnly": true, "afterId": "42", "pageSize": 2,
		}},
		// assertedDeviceStates types afterId as a NULLABLE ID, and the first page of
		// every walk omits it. A document that only ever validated with the cursor
		// present would not have checked the request that starts the walk.
		{"queryAssertedStates (first page, no cursor)", "device-state", queryAssertedStates, map[string]any{
			"source": "mqtt1", "activeOnly": true, "pageSize": 2,
		}},

		// command-delivery — the command round-trip harness.
		{"queryHarnessCommands", "command-delivery", queryHarnessCommands, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": 500, "deviceToken": "harness-cmd-probe-001"},
		}},

		// command-delivery — the fleet-write harness.
		{"mutationCreateCommandBatch", "command-delivery", mutationCreateCommandBatch, map[string]any{
			"request": map[string]any{
				"token": "harness-batch-1", "name": HarnessBatchCommandKey,
				"deviceTokens": []any{"harness-batch-tgt-001", "harness-batch-tgt-002"},
				"allowPartial": true,
			},
		}},
		{"queryBatchCommands", "command-delivery", queryBatchCommands, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": batchPageSize, "batchToken": "harness-batch-1"},
		}},
		{"queryBatchRecord", "command-delivery", queryBatchRecord, map[string]any{
			"tokens": []any{"harness-batch-1"},
		}},
		{"queryBatchDefinitions", "device-management", queryBatchDefinitions, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": 100, "deviceProfile": HarnessBatchProfileToken},
		}},

		// event-management — the L1 oracle.
		{"countEventsQuery", "event-management", countEventsQuery, map[string]any{
			"c": map[string]any{
				"pageNumber": 1, "pageSize": 1,
				"eventTypes": []any{MeasurementEventType},
				"startTime":  "2026-08-19T00:00:00Z", "endTime": "2026-08-19T00:01:00Z",
			},
		}},

		// device-management — provisioning and the durable alarm oracle.
		{"queryDetectionRulesByToken", "device-management", queryDetectionRulesByToken, map[string]any{
			"tokens": []any{HarnessRuleToken},
		}},
		{"queryCommandRulesByToken", "device-management", queryCommandRulesByToken, map[string]any{
			"tokens": []any{HarnessCommandRuleToken},
		}},
		{"mutationPublishHarnessProfile", "device-management", mutationPublishHarnessProfile, map[string]any{
			"token": HarnessProfileToken,
		}},
		{"mutationPublishCommandProfile", "device-management", mutationPublishCommandProfile, map[string]any{
			"token": HarnessCommandProfileToken,
		}},
		// The L2d-1 layer's ONLY durable read, and it carries a 5-key criteria input —
		// exactly the shape (a criteria object with required and optional members) that
		// shipped broken in the upgrade drill. It was the one document in this package
		// that no test validated, and the coverage cross-check below is what found it.
		{"queryHarnessAlarm", "device-management", queryHarnessAlarm, map[string]any{
			"c": map[string]any{
				"pageNumber": 1, "pageSize": 1,
				"originatorType": "device", "originator": "harness-probe-001",
				"alarmKey": HarnessAlarmKey,
			},
		}},
		{"queryDevicesExist", "device-management", queryDevicesExist, map[string]any{
			"tokens": []any{"harness-cmd-safe-001"},
		}},
	}
}

// TestEveryHarnessDocumentValidatesAgainstItsServedSchema is the gate. A document
// that does not validate here cannot succeed on a cluster, and finding that out from
// a unit test costs seconds instead of a deployment.
func TestEveryHarnessDocumentValidatesAgainstItsServedSchema(t *testing.T) {
	docs := harnessDocuments()
	// A table that lost its entries would validate nothing and report green.
	if len(docs) < 14 {
		t.Fatalf("harnessDocuments() lists only %d document(s); it has previously listed at least 14, so this test is asserting almost nothing", len(docs))
	}
	byArea := map[string]*graphql.Schema{}
	for _, d := range docs {
		schema, ok := byArea[d.area]
		if !ok {
			schema = servedSchema(t, d.area)
			byArea[d.area] = schema
		}
		if errs := schema.ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against the %s schema it is sent to: %v\n  document: %s",
				d.name, d.area, errs, d.doc)
		}
	}
}

// The two documents carrying variables whose shapes are the harness's own decisions
// get their own case, because the list above pins the shape and this pins the
// FIELDS: a rule authored against the wrong input type is the same class of defect
// as a misnamed mutation, and it has to be caught on the same terms.
// authoringDocuments names the documents covered by the test below rather than by
// harnessDocuments(), so the coverage cross-check can see them too.
func authoringDocuments() []string {
	return []string{
		mutationCreateCommandDefinition,
		mutationCreateCommandRule,
		mutationCreateDetectionRule,
	}
}

func TestTheAuthoringMutationsValidateWithTheRequestsTheHarnessBuilds(t *testing.T) {
	schema := servedSchema(t, "device-management")

	defReq := map[string]any{
		"token":              HarnessCommandDefToken,
		"deviceProfileToken": HarnessCommandProfileToken,
		"commandKey":         HarnessCommandKey,
		"name":               "Load-test harness reset",
	}
	if errs := schema.ValidateWithVariables(mutationCreateCommandDefinition, map[string]any{"request": defReq}); len(errs) > 0 {
		t.Errorf("mutationCreateCommandDefinition does not validate: %v", errs)
	}

	cfg := CommandConfig{}.withDefaults()
	def, err := cfg.ruleDefinitionJSON()
	if err != nil {
		t.Fatalf("build the rule definition: %v", err)
	}
	ruleReq := map[string]any{
		"token":              HarnessCommandRuleToken,
		"deviceProfileToken": HarnessCommandProfileToken,
		"name":               "Load-test command harness over-temp",
		"definition":         def,
		"enabled":            true,
	}
	for _, doc := range []struct {
		name string
		doc  string
	}{
		{"mutationCreateCommandRule", mutationCreateCommandRule},
		{"mutationCreateDetectionRule", mutationCreateDetectionRule},
	} {
		if errs := schema.ValidateWithVariables(doc.doc, map[string]any{"request": ruleReq}); len(errs) > 0 {
			t.Errorf("%s does not validate: %v\n  document: %s", doc.name, errs, doc.doc)
		}
	}
}

// 🔴 THE NEGATIVE CONTROL. Everything above is a check that passes, and a check that
// has never been shown to FAIL is indistinguishable from one that cannot — which is
// precisely what the harness's own conventions say about every other gate here.
//
// Each case is a real defect shape, replayed against the same validator, so a
// refactor that quietly stopped validating anything fails HERE rather than shipping
// a green.
func TestTheValidatorRejectsTheDefectsItExistsFor(t *testing.T) {
	deviceState := servedSchema(t, "device-state")

	for _, c := range []struct {
		name string
		doc  string
		vars map[string]any
		want string
	}{
		{
			// The shipped shape: a query naming a field no schema has ever served.
			name: "a query field that does not exist",
			doc:  `query($tokens:[String!]!){deviceStatesByToken(deviceTokens:$tokens){active}}`,
			vars: map[string]any{"tokens": []any{"a"}},
			want: "deviceStatesByToken",
		},
		{
			name: "a selected field the type does not declare",
			doc:  `query($tokens:[String!]!){deviceStatesByDeviceToken(deviceTokens:$tokens){presenceState}}`,
			vars: map[string]any{"tokens": []any{"a"}},
			want: "presenceState",
		},
		{
			// pageSize is Int! and required. Omitting it is how a walk silently takes the
			// server's idea of a page instead of the tiny one the invariant depends on.
			name: "a required argument the document omits",
			doc:  `query($source:String!){assertedDeviceStates(source:$source,activeOnly:true){deviceToken}}`,
			vars: map[string]any{"source": "mqtt1"},
			want: "pageSize",
		},
		{
			// sessionId is a String because it is a UnixNano that overflows a 32-bit Int.
			// A variable typed Int here is the exact mistake that motivates the parse.
			name: "a variable typed against the wrong scalar",
			doc:  `query($source:String!,$pageSize:String!){assertedDeviceStates(source:$source,activeOnly:true,pageSize:$pageSize){deviceToken}}`,
			vars: map[string]any{"source": "mqtt1", "pageSize": "2"},
			want: "pageSize",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			errs := deviceState.ValidateWithVariables(c.doc, c.vars)
			if len(errs) == 0 {
				t.Fatalf("the validator accepted %q — it cannot be what catches this class", c.name)
			}
			var joined strings.Builder
			for _, e := range errs {
				joined.WriteString(e.Error())
				joined.WriteString("; ")
			}
			if !strings.Contains(joined.String(), c.want) {
				t.Errorf("the rejection does not mention %q, so it may be failing for another reason: %s", c.want, joined.String())
			}
		})
	}
}

// documentConstPattern matches a package-level const whose value is a backtick-quoted
// GraphQL operation. Deliberately narrow: a looser pattern matching other strings
// would inflate the count and mask a real omission.
var documentConstPattern = regexp.MustCompile("(?s)const ([A-Za-z][A-Za-z0-9_]*) = `\\s*(query|mutation|subscription)\\b")

// 🔴 THE LIST CAN SHRINK TO NOTHING AND EVERY TEST ABOVE STILL REPORTS GREEN, unless
// something asserts otherwise. That is exactly the failure this file's own comment
// accuses a DERIVED list of — and the hand-maintained one has it too, minus the
// excuse: empty the table and the suite passes, negative control included.
//
// So the source is scanned as a CROSS-CHECK rather than as the source of truth. The
// scan cannot quietly succeed at finding nothing (it fails below its own floor), and
// the table cannot quietly shrink (every scanned document must appear in it). Each
// guards the other's failure mode, which is what neither could do alone.
func TestEveryGraphQLDocumentInThePackageIsCovered(t *testing.T) {
	covered := map[string]bool{}
	for _, d := range harnessDocuments() {
		covered[d.doc] = true
	}
	for _, d := range authoringDocuments() {
		covered[d] = true
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	found := map[string]string{} // const name -> document
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		src := string(body)
		for _, loc := range documentConstPattern.FindAllStringSubmatchIndex(src, -1) {
			name := src[loc[2]:loc[3]]
			// The document runs from the opening backtick to the next one.
			open := strings.Index(src[loc[0]:], "`") + loc[0]
			closeIdx := strings.Index(src[open+1:], "`")
			if closeIdx < 0 {
				t.Fatalf("unterminated document constant %s in %s", name, f)
			}
			found[name] = src[open+1 : open+1+closeIdx]
		}
	}

	// A scan that found nothing would certify everything. The floor is a literal, so a
	// regex that stops matching fails here rather than reporting full coverage.
	const minimumDocuments = 17
	if len(found) < minimumDocuments {
		t.Fatalf("the source scan found only %d GraphQL document constant(s); it has previously found %d, so the SCAN is broken and this test would otherwise certify everything",
			len(found), minimumDocuments)
	}

	var uncovered []string
	for name, doc := range found {
		if !covered[doc] {
			uncovered = append(uncovered, name)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		t.Errorf("%d GraphQL document(s) in this package are sent to a cluster and validated by nothing: %s",
			len(uncovered), strings.Join(uncovered, ", "))
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 THE COUNTERWEIGHT, and the only test here that can tell a working filter
// from one that rejects everything. Pointed at the CURRENT tree, the baseline
// must support every single row: this is the same schema the table was written
// against, so anything skipped is the filter misreading it, not the platform
// missing something.
//
// Without this, a `supports` that always answered no would look like a healthy
// upgrade drill — a receipt with nothing on it, and a verify that passes
// instantly having checked nothing.
//
// 🔴 RUN WITH -count=1 AFTER TOUCHING A SCHEMA: the files live outside this
// module and Go's test cache does not track them.
func TestTheCurrentTreeSupportsEveryEntity(t *testing.T) {
	b, err := loadBaseline(filepath.Join("..", "..", "services"))
	if err != nil {
		t.Fatalf("load the current tree as a baseline: %v", err)
	}
	for _, e := range entities {
		if ok, why := b.supports(e); !ok {
			t.Errorf("the current tree does not support %q: %s", e.Name, why)
		}
	}
}

// The other half of the same claim: a baseline that is genuinely missing
// something must say so. Together with the test above, this pins the filter to
// the schema rather than to a constant answer.
func TestAMissingMutationIsUnsupported(t *testing.T) {
	dir := writeBaseline(t, map[string]string{
		"device-management": `
type Mutation {
    createAsset(request: AssetCreateRequest): Asset!
}
type Query {
    assetsByToken(tokens: [String!]!): [Asset!]!
}
input AssetCreateRequest {
    token: String!
}
`,
	})
	b, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	asset, _ := entityNamed("asset")
	if ok, why := b.supports(asset); !ok {
		t.Errorf("asset should be supported by a schema that declares it: %s", why)
	}

	// Everything else in device-management is absent from this stub.
	deviceType, _ := entityNamed("device-type")
	ok, why := b.supports(deviceType)
	if ok {
		t.Error("device-type was supported by a schema that never declares createDeviceType")
	}
	if !strings.Contains(why, "createDeviceType") {
		t.Errorf("the reason does not name what is missing: %q", why)
	}
}

// Each of the three checks has to be able to fail on its own. A `supports` that
// only ever consulted the mutation would pass every test above while letting an
// entity through whose read query or input type is gone — and the seed would
// then be refused mid-run, which is the outcome the filter exists to prevent.
func TestEachHalfOfTheCheckCanFailAlone(t *testing.T) {
	asset, _ := entityNamed("asset")
	const full = `
type Mutation {
    createAsset(request: AssetCreateRequest): Asset!
}
type Query {
    assetsByToken(tokens: [String!]!): [Asset!]!
}
input AssetCreateRequest {
    token: String!
}
`
	cases := []struct{ name, drop, wantReason string }{
		{"the mutation is gone", "createAsset(request: AssetCreateRequest): Asset!", "createAsset"},
		{"the read query is gone", "assetsByToken(tokens: [String!]!): [Asset!]!", "assetsByToken"},
		{"the input type is gone", "input AssetCreateRequest {", "input AssetCreateRequest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := loadBaseline(writeBaseline(t, map[string]string{
				"device-management": strings.Replace(full, c.drop, "", 1),
			}))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			ok, why := b.supports(asset)
			if ok {
				t.Fatalf("asset was supported with %q removed", c.drop)
			}
			if !strings.Contains(why, c.wantReason) {
				t.Errorf("reason %q does not mention %q", why, c.wantReason)
			}
		})
	}
}

// An area the baseline does not serve at all is unsupported, and says which —
// the shape of a release that predates a whole service.
func TestAnAreaTheBaselineDoesNotServeIsUnsupported(t *testing.T) {
	b, err := loadBaseline(writeBaseline(t, map[string]string{
		"device-management": "type Query { placeholder: String }\n",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	dashboard, _ := entityNamed("dashboard")
	ok, why := b.supports(dashboard)
	if ok {
		t.Error("dashboard was supported by a baseline with no dashboard-management schema")
	}
	if !strings.Contains(why, "dashboard-management") {
		t.Errorf("the reason does not name the missing area: %q", why)
	}
}

// A baseline directory holding no schemas at all would mark every entity
// unsupported — the receipt-with-nothing-on-it failure. Refused where the path
// is still in hand to name, rather than several minutes later as an empty run.
func TestAnEmptyBaselineIsRefused(t *testing.T) {
	_, err := loadBaseline(t.TempDir())
	assertCode(t, err, exitSetup)

	_, err = loadBaseline(filepath.Join(t.TempDir(), "not-there"))
	assertCode(t, err, exitSetup)
}

// The read is matched the way readDoc SPELLS it. A single lookup and a list
// lookup take differently-named arguments, so a baseline serving the other shape
// is unsupported rather than being read with a document that cannot match it.
func TestTheReadArgumentSpellingIsPartOfTheCheck(t *testing.T) {
	b, err := loadBaseline(writeBaseline(t, map[string]string{
		"dashboard-management": `
type Mutation {
    createDashboard(request: DashboardCreateRequest): Dashboard!
}
type Query {
    dashboard(tokens: [String!]!): [Dashboard!]!
}
input DashboardCreateRequest {
    token: String!
}
`,
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	dashboard, _ := entityNamed("dashboard")
	if ok, _ := b.supports(dashboard); ok {
		t.Error("a list-shaped dashboard query satisfied an entity that reads a single object")
	}
}

// 🔴 THE ENVELOPE ADAPTATION, both ways. `createCommand` returned `Command!` in
// v0.11.0 and returns `CreateCommandResult!` at HEAD, so an entity selecting
// THROUGH `command{…}` cannot be sent to the older release — while the mutation
// name, the argument name and the input type all still match, which is everything
// the rest of `supports` looks at.
//
// The row is adapted rather than skipped, because the envelope is the ONLY thing
// that differs: same input type, same selectable fields, same read query. Skipping
// would cost command-delivery every row it contributes, since its other entity
// does not exist in v0.11.0 at all.
//
// Both directions are asserted because only the pair distinguishes a working
// adaptation from one that answers the same way regardless. Stripping always would
// send HEAD a bare document its own schema refuses; stripping never would put the
// row back where it started. The third and fourth assertions are the ones that
// matter most: adaptation must change the envelope and NOTHING else — not the
// selection, not the input, and never the read document, which no baseline
// difference justifies touching.
func TestAnUnenvelopedBaselineDropsTheEnvelopeRatherThanTheRow(t *testing.T) {
	const enveloped = `
type Mutation {
    createCommand(request: CommandCreateRequest!): CreateCommandResult!
}
type Query {
    commandsByToken(tokens: [String!]!): [Command!]!
}
type CreateCommandResult {
    command: Command
    rejection: CommandRejection
}
type Command {
    token: String!
}
type CommandRejection {
    code: String!
    reason: String!
}
input CommandCreateRequest {
    token: String!
}
`
	// The v0.11.0 shape: same mutation, same argument, same input — the object
	// itself comes back, with no envelope to select through.
	bare := strings.Replace(enveloped,
		"createCommand(request: CommandCreateRequest!): CreateCommandResult!",
		"createCommand(request: CommandCreateRequest!): Command!", 1)
	if bare == enveloped {
		t.Fatal("the fixture edit did not apply, so both cases below are the same schema")
	}

	command, ok := entityNamed("command")
	if !ok {
		t.Fatal("the coverage table has no \"command\" entity, so this test asserts nothing")
	}
	if command.Wrap == "" {
		t.Fatal("the \"command\" entity no longer declares a Wrap, so this test asserts nothing")
	}

	b, err := loadBaseline(writeBaseline(t, map[string]string{"command-delivery": bare}))
	if err != nil {
		t.Fatalf("load the bare baseline: %v", err)
	}
	if ok, why := b.supports(command); !ok {
		t.Fatalf("the bare baseline was refused outright, so nothing is adapted: %s", why)
	}
	adapted := b.adapt(command)
	if adapted.Wrap != "" || adapted.Reject != "" {
		t.Errorf("the envelope survived adaptation (Wrap=%q Reject=%q), so the create still selects a field the baseline's type does not have",
			adapted.Wrap, adapted.Reject)
	}
	if strings.Contains(adapted.createDoc(), command.Wrap+"{") {
		t.Errorf("the adapted create still wraps its selection: %s", adapted.createDoc())
	}
	// The adaptation must not cost the row anything ELSE. Same selection, same
	// input, same read — only the envelope goes.
	if adapted.Fields != command.Fields || adapted.Input != command.Input || adapted.Read != command.Read {
		t.Error("adaptation changed more than the envelope, so the seeded row is not the row the table describes")
	}
	if adapted.readDoc() != command.readDoc() {
		t.Errorf("adaptation changed the READ document, which no baseline difference justifies:\n  %s\n  %s",
			command.readDoc(), adapted.readDoc())
	}

	b, err = loadBaseline(writeBaseline(t, map[string]string{"command-delivery": enveloped}))
	if err != nil {
		t.Fatalf("load the enveloped baseline: %v", err)
	}
	if ok, why := b.supports(command); !ok {
		t.Errorf("an enveloped createCommand was refused: %s", why)
	}
	if got := b.adapt(command); got.Wrap != command.Wrap || got.Reject != command.Reject {
		t.Errorf("an enveloped baseline had its envelope stripped anyway (Wrap=%q Reject=%q), so the adaptation fires on everything and the HEAD path is dead",
			got.Wrap, got.Reject)
	}
}

// A nil baseline is a seed against the current release, which must write the table
// exactly as written — an adaptation there would silently change what a fresh
// install is verified against.
func TestNoBaselineAdaptsNothing(t *testing.T) {
	var b *baseline
	for _, e := range entities {
		if got := b.adapt(e); got.Wrap != e.Wrap || got.Reject != e.Reject {
			t.Errorf("entity %q was adapted with no baseline in hand", e.Name)
		}
	}
}

// 🔴 THE COUNTERWEIGHT for adapt, matching the one supports already has: pointed
// at the CURRENT tree, nothing may be adapted. The table is written against these
// schemas, so an entity that loses its envelope here is the detector misreading
// the tree — and it would seed a document HEAD refuses, reported as the platform
// declining a row it has always accepted.
func TestTheCurrentTreeAdaptsNothing(t *testing.T) {
	b, err := loadBaseline(filepath.Join("..", "..", "services"))
	if err != nil {
		t.Fatalf("load the current tree as a baseline: %v", err)
	}
	wrapped := 0
	for _, e := range entities {
		if e.Wrap != "" {
			wrapped++
		}
		if got := b.adapt(e); got.Wrap != e.Wrap || got.Reject != e.Reject {
			t.Errorf("entity %q lost its envelope against the tree its own selection was written from", e.Name)
		}
	}
	// Without an enveloped entity in the table the loop above passes vacuously,
	// and would keep passing if adapt were changed to strip everything.
	if wrapped == 0 {
		t.Fatal("no entity in the table declares a Wrap, so this test asserts nothing")
	}
}

// A bulk entry names its type inside brackets, and only the bare name is ever
// declared. Getting this wrong would make every bulk row unsupported against
// every baseline — a silent halving of the drill on the rows that carry more
// than one object each.
func TestABulkInputResolvesToItsBareTypeName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"AssetCreateRequest!", "AssetCreateRequest"},
		{"[EntityRelationshipCreateRequest!]!", "EntityRelationshipCreateRequest"},
		{"DeviceBulkCreateRequest!", "DeviceBulkCreateRequest"},
	} {
		if got := inputTypeName(c.in); got != c.want {
			t.Errorf("inputTypeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Admin schemas are excluded, so an entity declared ONLY on an admin surface is
// not treated as available on the tenant plane the probe writes through.
func TestAdminSchemasDoNotCount(t *testing.T) {
	dir := t.TempDir()
	area := filepath.Join(dir, "device-management", "graphql")
	if err := os.MkdirAll(area, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
type Mutation {
    createAsset(request: AssetCreateRequest): Asset!
}
type Query {
    assetsByToken(tokens: [String!]!): [Asset!]!
}
input AssetCreateRequest {
    token: String!
}
`
	if err := os.WriteFile(filepath.Join(area, "admin.graphql"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(area, "schema.graphql"), []byte("type Query { placeholder: String }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	asset, _ := entityNamed("asset")
	if ok, _ := b.supports(asset); ok {
		t.Error("an entity declared only in an admin schema was treated as available on the tenant plane")
	}
}

// 🔑 THE SKIP THAT MUST NOT BE A SKIP. An entity whose token later rows
// reference cannot be dropped: the dependents would send an empty token and be
// refused for a reason that names them rather than the hole. Both entities the
// v0.11.0 baseline actually drops — geo-fence and command-batch — are leaves,
// and this pins that property rather than leaving it to have been true once.
func TestTheEntitiesAV011BaselineDropsAreLeaves(t *testing.T) {
	for _, name := range []string{"geo-fence", "command-batch"} {
		e, ok := entityNamed(name)
		if !ok {
			t.Errorf("no entity %q", name)
			continue
		}
		if e.Record != nil {
			t.Errorf("%q feeds the shared state, so a baseline that cannot express it cannot skip it either — "+
				"the seed must refuse instead, and this drill would stop working against v0.11.0", name)
		}
	}
}

// ---------------------------------------------------------------------------
// plan — what a seed does with an entity the baseline cannot express
// ---------------------------------------------------------------------------

// No baseline means no filtering: a seed against the current release writes the
// whole table. A `plan` that filtered anyway would silently shrink every fresh
// install's coverage, and the receipt would still look complete.
func TestWithNoBaselineEverythingIsWritten(t *testing.T) {
	for _, e := range entities {
		write, why, err := plan(e, nil)
		if !write || err != nil || why != "" {
			t.Errorf("plan(%q, nil) = %v, %q, %v; want everything written", e.Name, write, why, err)
		}
	}
}

// A leaf the baseline cannot express is SKIPPED — not an error, because there is
// nothing downstream to break and an old release genuinely could not hold it.
func TestAnUnsupportedLeafIsSkipped(t *testing.T) {
	b, err := loadBaseline(writeBaseline(t, map[string]string{
		"device-management": "type Query { placeholder: String }\n",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	geo, ok := entityNamed("geo-fence")
	if !ok {
		t.Fatal("no geo-fence entity")
	}
	write, why, err := plan(geo, b)
	if err != nil {
		t.Errorf("skipping a leaf was an error: %v", err)
	}
	if write {
		t.Error("an entity the baseline cannot express was written anyway")
	}
	if why == "" {
		t.Error("the skip carries no reason, so the seed would print a bare name")
	}
}

// 🔴 THE ONE THAT IS NOT A SKIP. An entity other rows depend on cannot be
// dropped: the dependents would send an empty token and be refused for a reason
// that names THEM, several rows after the decision that caused it. The run has
// to stop where the cause is still in hand.
func TestAnUnsupportedDEPENDENCYIsRefusedRatherThanSkipped(t *testing.T) {
	b, err := loadBaseline(writeBaseline(t, map[string]string{
		"device-management": "type Query { placeholder: String }\n",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// device-type publishes its token for device to adopt.
	deviceType, ok := entityNamed("device-type")
	if !ok {
		t.Fatal("no device-type entity")
	}
	if deviceType.Record == nil {
		t.Fatal("device-type no longer feeds the state; this test is aimed at the wrong entity")
	}
	write, _, err := plan(deviceType, b)
	if write {
		t.Error("an unsupported dependency was written anyway")
	}
	assertCode(t, err, exitSetup)
	if err == nil || !strings.Contains(err.Error(), "later entities reference it") {
		t.Errorf("the message does not say why this one cannot simply be skipped: %v", err)
	}
}

// 🔴 THE TEST THAT WOULD HAVE CAUGHT IT, AND DID NOT EXIST. Everything else here
// either points the baseline at the CURRENT tree (where nothing may be skipped) or at
// a two-line stub (where everything is). Neither shape is what a drill actually runs:
// a PREVIOUS RELEASE's schema, which declares the mutation, the read and the input
// type this table names — and not the FIELD a new row sends through them.
//
// That gap is why the asset-property rows reached CI green and died in the upgrade
// drill with `Field "propertySchema" is not defined by type "AssetTypeCreateRequest"`.
// supports() answered yes, because it matches names and the names were all there.
//
// The stand-in is built from the real current schema with exactly the lines a release
// predating this feature would not have: the two field declarations and the four
// version doors. That makes it a faithful older release rather than a hand-written
// fiction that could agree with the code by construction.
//
// It asserts BOTH directions, which is what makes it a control rather than a hope:
// the three field-gated rows are skipped, and every other row in the whole table is
// still supported. A Requires that matched nothing would fail the second half.
func TestAReleaseWithoutTheNewFieldsSkipsExactlyTheRowsThatNeedThem(t *testing.T) {
	current, err := os.ReadFile(filepath.Join("..", "..", "services", "device-management",
		"graphql", "schema.graphql"))
	if err != nil {
		t.Fatalf("read the current device-management schema: %v", err)
	}

	var kept []string
	var droppedFields, droppedDoors int
	for _, line := range strings.Split(string(current), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "propertySchema:") || strings.HasPrefix(trimmed, "properties:") {
			droppedFields++
			continue
		}
		if strings.HasPrefix(trimmed, "publishAssetType(") ||
			strings.HasPrefix(trimmed, "rollbackAssetType(") ||
			strings.HasPrefix(trimmed, "assetTypeVersions(") ||
			strings.HasPrefix(trimmed, "activeAssetTypeVersion(") {
			droppedDoors++
			continue
		}
		kept = append(kept, line)
	}
	// If the SDL is reshaped so these no longer match, this test would silently become
	// "the current tree supports everything", which the test above already says.
	if droppedFields == 0 || droppedDoors == 0 {
		t.Fatalf("the stand-in removed %d field line(s) and %d door(s); it is no longer "+
			"standing in for a release without them", droppedFields, droppedDoors)
	}

	// Every other area is copied verbatim: this is a release missing ONE feature, not a
	// tree missing every area, and a row skipped for the wrong reason must not be able
	// to hide behind a missing schema.
	schemas := map[string]string{"device-management": strings.Join(kept, "\n")}
	areas, err := os.ReadDir(filepath.Join("..", "..", "services"))
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	for _, a := range areas {
		if !a.IsDir() || a.Name() == "device-management" {
			continue
		}
		for _, ext := range []string{"*.graphql", "*.gql"} {
			found, _ := filepath.Glob(filepath.Join("..", "..", "services", a.Name(), "graphql", ext))
			for _, f := range found {
				body, err := os.ReadFile(f)
				if err != nil {
					t.Fatalf("read %s: %v", f, err)
				}
				schemas[a.Name()] += string(body) + "\n"
			}
		}
	}

	b, err := loadBaseline(writeBaseline(t, schemas))
	if err != nil {
		t.Fatalf("load the stand-in baseline: %v", err)
	}

	wantSkipped := map[string]bool{
		"asset-type-schema":     true,
		"asset-type-version":    true,
		"asset-with-properties": true,
	}
	for _, e := range allEntities() {
		ok, why := b.supports(e)
		if wantSkipped[e.Name] {
			if ok {
				t.Errorf("%q is supported by a baseline that lacks the field it sends; "+
					"the seed would be REFUSED by the server instead of skipping the row", e.Name)
			}
			// A skipped row must also be SKIPPABLE. plan() refuses rather than skips a row
			// other rows read a token from, so a Requires on a recorded row turns the
			// escape hatch into a hard failure — the exact shape this is here to avoid.
			write, reason, err := plan(e, b)
			if err != nil {
				t.Errorf("%q could not be skipped: %v", e.Name, err)
			}
			if write {
				t.Errorf("%q was planned for writing despite being unsupported", e.Name)
			}
			if reason == "" {
				t.Errorf("%q skipped with no reason", e.Name)
			}
			continue
		}
		if !ok {
			t.Errorf("%q is skipped by a baseline that only lacks the asset-property "+
				"fields: %s", e.Name, why)
		}
	}
}

// The narrow unit under the test above: the field scan itself, including the two ways
// a substring match would say yes when it must say no.
func TestDeclaresField(t *testing.T) {
	raw := `input AssetTypeCreateRequest {
    token: String!
    # propertySchema: String  -- mentioned in a comment, not declared
    propertySchemaVersion: String
}

type Asset {
    token: String!
    properties: String
}
`
	if declaresField(raw, "AssetTypeCreateRequest", "propertySchema") {
		t.Error("a commented-out declaration and a longer field name were read as the field")
	}
	if !declaresField(raw, "Asset", "properties") {
		t.Error("a real declaration on a `type` was not found")
	}
	if !declaresField(raw, "AssetTypeCreateRequest", "propertySchemaVersion") {
		t.Error("a real declaration on an `input` was not found")
	}
	if declaresField(raw, "Asset", "propertySchema") {
		t.Error("a field declared on ANOTHER type was attributed to this one")
	}
	if declaresField(raw, "NoSuchType", "properties") {
		t.Error("a type the schema does not declare answered yes")
	}
}

// writeBaseline lays out a throwaway `backend/services`-shaped tree.
func writeBaseline(t *testing.T, schemas map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for area, body := range schemas {
		path := filepath.Join(dir, area, "graphql")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "schema.graphql"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

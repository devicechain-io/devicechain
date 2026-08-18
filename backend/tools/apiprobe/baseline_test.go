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

// 🔴 THE ENVELOPE CHECK, both ways. `createCommand` returned `Command!` in
// v0.11.0 and returns `CreateCommandResult!` at HEAD, so an entity that selects
// THROUGH `command{…}` cannot be sent to the older release at all — while the
// mutation name, the argument name and the input type all still match, which is
// everything the rest of `supports` looks at.
//
// Both directions are asserted because only the pair distinguishes a working
// check from one that answers the same way regardless: the bare-object baseline
// must be refused, and the enveloped one must still be accepted. A check that
// only refuses would skip the entity against EVERY baseline, including HEAD's own
// tree — which is the fail-open this file's counterweight test exists to catch,
// arriving one entity at a time instead of all at once.
func TestAnUnenvelopedCreateCannotCarryAWrappedSelection(t *testing.T) {
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
	got, why := b.supports(command)
	if got {
		t.Error("a createCommand returning the bare object accepted a document that selects through an envelope")
	}
	if !strings.Contains(why, command.Wrap) || !strings.Contains(why, "Command") {
		t.Errorf("the reason names neither the missing field nor the type it is missing from: %q", why)
	}

	b, err = loadBaseline(writeBaseline(t, map[string]string{"command-delivery": enveloped}))
	if err != nil {
		t.Fatalf("load the enveloped baseline: %v", err)
	}
	if got, why := b.supports(command); !got {
		t.Errorf("an enveloped createCommand was refused, so the check rejects every baseline: %s", why)
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

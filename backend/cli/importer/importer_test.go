// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	"github.com/devicechain-io/dc-microservice/userclient"
	graphql "github.com/graph-gophers/graphql-go"
)

const (
	fz1    = "d1000000-0000-4000-8000-000000000001"
	storeN = "a1000000-0000-4000-8000-000000000001"
	storeS = "a1000000-0000-4000-8000-000000000002"
	room   = "a2000000-0000-4000-8000-000000000001"
)

func sample(t *testing.T, o Options) *Plan {
	t.Helper()
	ex, err := Load(filepath.Join("testdata", "sample-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	return Build(ex, o)
}

func lines(p *Plan, kind string) []Line {
	var out []Line
	for _, l := range p.Report {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}

func TestBuildCountsAndTokens(t *testing.T) {
	p := sample(t, Options{})
	if len(p.Profiles) != 2 || len(p.AssetTypes) != 2 || len(p.Assets) != 3 || len(p.Devices) != 3 {
		t.Fatalf("counts: %d profiles, %d asset types, %d assets, %d devices",
			len(p.Profiles), len(p.AssetTypes), len(p.Assets), len(p.Devices))
	}
	d := p.Devices[0]
	if d.Token != fz1 || d.ExternalID != "FZ-001" || d.DeviceTypeToken != "6a1f0000-0000-4000-8000-000000000001" || d.Description != "Freezer 1" {
		t.Errorf("device row: %+v", d)
	}
	if !strings.Contains(d.Metadata, `"source"`) || !strings.Contains(d.Metadata, "customerId") {
		t.Errorf("source facts must survive in metadata: %s", d.Metadata)
	}
}

func TestSecondParentIsDemotedNotMerged(t *testing.T) {
	p := sample(t, Options{})
	if len(p.Parents) != 1 || p.Parents[0].Child != room || p.Parents[0].Parent != storeN {
		t.Fatalf("parents: %+v (the lowest parent id must win, deterministically)", p.Parents)
	}
	var extra *RelPlan
	for i, r := range p.Relations {
		if r.Type == "contains-extra" {
			extra = &p.Relations[i]
		}
	}
	if extra == nil || extra.Source != storeS || extra.Target != room {
		t.Fatalf("second parent must become a contains-extra edge, got %+v", p.Relations)
	}
	found := false
	for _, l := range p.Report {
		if l.Class == Partial && l.Kind == "RELATION" && strings.Contains(l.Message, "one parent") {
			found = true
		}
	}
	if !found {
		t.Error("the demotion must be reported")
	}
}

func TestAssetContainsDeviceReversesDirection(t *testing.T) {
	p := sample(t, Options{})
	for _, r := range p.Relations {
		if r.Type == "assigned" {
			if r.SourceType != "device" || r.Source != fz1 || r.TargetType != "asset" || r.Target != room {
				t.Fatalf("assigned edge: %+v", r)
			}
			return
		}
	}
	t.Fatal("no assigned edge")
}

func TestCustomRelationAndUnsupportedEnds(t *testing.T) {
	p := sample(t, Options{})
	if len(p.RelTypes) != 2 { // Monitors, contains-extra
		t.Errorf("relationship types: %+v", p.RelTypes)
	}
	unsupported := 0
	for _, l := range lines(p, "RELATION") {
		if l.Class == Unsupported && strings.Contains(l.Ref, "Manages") {
			unsupported++
		}
	}
	if unsupported != 1 {
		t.Errorf("the customer relation must be reported unsupported once, got %d", unsupported)
	}
}

func TestRelationsAreDeduplicated(t *testing.T) {
	// Each end of an edge may list it; it must be planned once.
	recs, err := Parse([]byte(`[
	 {"entityType":"ASSET_PROFILE","entity":{"id":{"entityType":"ASSET_PROFILE","id":"p"}}},
	 {"entityType":"ASSET","entity":{"id":{"entityType":"ASSET","id":"a"},"assetProfileId":{"entityType":"ASSET_PROFILE","id":"p"}},
	  "relations":[{"from":{"entityType":"ASSET","id":"a"},"to":{"entityType":"ASSET","id":"b"},"type":"Feeds","typeGroup":"COMMON"}]},
	 {"entityType":"ASSET","entity":{"id":{"entityType":"ASSET","id":"b"},"assetProfileId":{"entityType":"ASSET_PROFILE","id":"p"}},
	  "relations":[{"from":{"entityType":"ASSET","id":"a"},"to":{"entityType":"ASSET","id":"b"},"type":"Feeds","typeGroup":"COMMON"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	p := Build(&Export{Records: recs}, Options{})
	if len(p.Relations) != 1 || p.Relations[0].Type != "Feeds" {
		t.Fatalf("relations: %+v", p.Relations)
	}
}

func TestAttributesSkipPlatformKeysAndKeepScope(t *testing.T) {
	p := sample(t, Options{})
	got := map[string]AttrPlan{}
	for _, a := range p.Attributes {
		got[a.Entity+"/"+a.Key] = a
	}
	for _, k := range []string{fz1 + "/active", fz1 + "/fw_version"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is platform-maintained and must not be copied", k)
		}
	}
	if a := got[fz1+"/tempHigh"]; a.Scope != "SERVER" || a.ValueType != "DOUBLE" || a.Value != "-15.5" {
		t.Errorf("tempHigh: %+v", a)
	}
	if a := got[fz1+"/reportingInterval"]; a.Scope != "SHARED" || a.ValueType != "LONG" {
		t.Errorf("reportingInterval: %+v", a)
	}
	if a := got[storeN+"/city"]; a.EntityType != "asset" || a.ValueType != "STRING" {
		t.Errorf("asset attribute: %+v", a)
	}
}

func TestCredentialsAreOptInAndNeverSilentlyDropped(t *testing.T) {
	off := sample(t, Options{})
	for _, d := range off.Devices {
		if d.CredentialID != "" {
			t.Fatalf("access tokens must not travel unless asked: %+v", d)
		}
	}
	on := sample(t, Options{CarryAccessTokens: true})
	if on.Devices[0].CredentialID != "fz001-access-token" || on.Devices[0].CredentialType != "ACCESS_TOKEN" {
		t.Fatalf("token not carried: %+v", on.Devices[0])
	}
	if on.Devices[1].CredentialID != "" || on.Devices[2].CredentialID != "" {
		t.Error("basic and certificate credentials are never carried")
	}
	// Every device gets exactly one CREDENTIAL line, whatever happened to its credential.
	if n := len(lines(on, "CREDENTIAL")); n != 3 {
		t.Errorf("credential lines: %d, want 3", n)
	}
}

func TestAlarmClassification(t *testing.T) {
	p := sample(t, Options{})
	al := lines(p, "ALARM_RULE")
	if len(al) != 3 {
		t.Fatalf("alarm lines: %d", len(al))
	}
	want := []struct {
		class Class
		text  string
	}{
		{Partial, "hysteresis"},
		{Partial, "boolean"},
		{Unsupported, "schedule"},
	}
	for i, w := range want {
		if al[i].Class != w.class || !strings.Contains(al[i].Message, w.text) {
			t.Errorf("alarm %d: %+v, want %s containing %q", i, al[i], w.class, w.text)
		}
	}
	if !strings.Contains(al[0].Message, "attribute") {
		t.Errorf("the dynamic threshold must be noted: %s", al[0].Message)
	}
}

func TestUncoveredKindsAreCatalogued(t *testing.T) {
	p := sample(t, Options{})
	for _, k := range []string{"CUSTOMER", "DASHBOARD", "RULE_CHAIN"} {
		if len(lines(p, k)) == 0 {
			t.Errorf("%s must appear in the report", k)
		}
	}
	if l := lines(p, "DEVICE_PROFILE"); len(l) < 3 { // two transforms + the MQTT transport line
		t.Errorf("profile lines: %+v", l)
	}
}

func TestMissingProfileAndBadTokenAreReportedNotImported(t *testing.T) {
	recs, err := Parse([]byte(`[
	 {"entityType":"DEVICE","entity":{"id":{"entityType":"DEVICE","id":"ok-1"},"name":"orphan","deviceProfileId":{"entityType":"DEVICE_PROFILE","id":"gone"}}},
	 {"entityType":"DEVICE_PROFILE","entity":{"id":{"entityType":"DEVICE_PROFILE","id":"has space"},"name":"bad"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	p := Build(&Export{Records: recs}, Options{})
	if len(p.Devices) != 0 || len(p.Profiles) != 0 {
		t.Fatalf("nothing should be planned: %+v", p)
	}
	if Summary(p.Report)[Unsupported] != 2 {
		t.Errorf("both must be reported: %+v", p.Report)
	}
}

func TestLongNameIsCutAndReported(t *testing.T) {
	long := strings.Repeat("n", 200)
	recs, _ := Parse([]byte(`{"entityType":"ASSET_PROFILE","entity":{"id":{"entityType":"ASSET_PROFILE","id":"p"},"name":"` + long + `"}}`))
	p := Build(&Export{Records: recs}, Options{})
	if len(p.AssetTypes[0].Name) != 128 {
		t.Errorf("name length %d", len(p.AssetTypes[0].Name))
	}
	if Summary(p.Report)[Partial] != 1 {
		t.Errorf("the cut must be reported: %+v", p.Report)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{``, `42`, `{"entityType":"DEVICE"}`, `[{"entity":{}}]`, `[`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%q must be refused", in)
		}
	}
}

func TestLoadDirectory(t *testing.T) {
	ex, err := Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Records) != 13 {
		t.Errorf("records: %d", len(ex.Records))
	}
}

func TestReportRenders(t *testing.T) {
	var b bytes.Buffer
	PrintReport(&b, sample(t, Options{}))
	for _, want := range []string{"UNSUPPORTED", "PARTIAL", "TRANSFORMED", "[DASHBOARD 1]"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// ---- Apply ---------------------------------------------------------------------------------

type call struct {
	query string
	vars  map[string]any
}

// fake records every call. fail answers an error per query; outcome is what importDevices
// reports for every row.
type fake struct {
	calls   []call
	fail    func(query string) error
	outcome string
}

func (f *fake) Query(_ context.Context, _ string, q string, v map[string]any, out any) error {
	f.calls = append(f.calls, call{q, v})
	if f.fail != nil {
		if err := f.fail(q); err != nil {
			return err
		}
	}
	if res, ok := out.(*importResult); ok {
		outcome := f.outcome
		if outcome == "" {
			outcome = "CREATED"
		}
		for i, r := range v["rows"].([]any) {
			res.ImportDevices.Rows = append(res.ImportDevices.Rows, struct {
				Row     int     `json:"row"`
				Token   string  `json:"token"`
				Outcome string  `json:"outcome"`
				Code    *string `json:"code"`
				Message *string `json:"message"`
			}{Row: i + 1, Token: r.(map[string]any)["token"].(string), Outcome: outcome})
		}
	}
	return nil
}

func tally(ts []Tally, kind string) Tally {
	for _, t := range ts {
		if t.Kind == kind {
			return t
		}
	}
	return Tally{}
}

func TestApplyOrderAndModes(t *testing.T) {
	p := sample(t, Options{CarryAccessTokens: true})
	f := &fake{}
	tallies, err := Apply(context.Background(), f, "http://x", p)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, c := range f.calls {
		if _, ok := pos[c.query]; !ok {
			pos[c.query] = i
		}
	}
	order := []string{createProfileMutation, publishProfileMutation, createTypeMutation, createAssetTypeMutation,
		createAssetMutation, importDevicesMutation, createRelTypeMutation, setParentMutation, createRelMutation, setAttrMutation}
	for i := 1; i < len(order); i++ {
		if pos[order[i-1]] >= pos[order[i]] {
			t.Errorf("dependency order broken between %q and %q", order[i-1], order[i])
		}
	}
	modes := map[string]int{}
	for _, c := range f.calls {
		if c.query == importDevicesMutation {
			modes[c.vars["mode"].(string)]++
			if c.vars["dryRun"] != false {
				t.Error("apply must write")
			}
		}
	}
	if modes["NONE"] != 1 || modes["FACTORY_CREDENTIALS"] != 1 {
		t.Errorf("credential rows must go in their own mode: %v", modes)
	}
	if d := tally(tallies, "devices"); d.Created != 3 {
		t.Errorf("devices tally: %+v", d)
	}
}

func TestApplyRerunIsIdempotentAndPublishesOnce(t *testing.T) {
	p := sample(t, Options{})
	f := &fake{
		outcome: "EXISTS",
		fail: func(q string) error {
			switch q {
			case createProfileMutation, createTypeMutation, createAssetTypeMutation, createAssetMutation,
				createRelTypeMutation, createRelMutation:
				return &userclient.GraphQLError{Messages: []string{"token in use"}, Codes: []string{"CONFLICT"}}
			}
			return nil
		},
	}
	tallies, err := Apply(context.Background(), f, "http://x", p)
	if err != nil {
		t.Fatalf("a re-run over existing data must succeed: %v", err)
	}
	for _, c := range f.calls {
		if c.query == publishProfileMutation {
			t.Fatal("a profile that already existed must not be published again")
		}
	}
	for _, ta := range tallies {
		if ta.Failed != 0 {
			t.Errorf("%s failed: %v", ta.Kind, ta.Errors)
		}
	}
	if d := tally(tallies, "devices"); d.Exists != 3 || d.Created != 0 {
		t.Errorf("devices should all be EXISTS: %+v", d)
	}
}

func TestApplyReportsFailuresAndKeepsGoing(t *testing.T) {
	p := sample(t, Options{})
	f := &fake{fail: func(q string) error {
		if q == createAssetMutation {
			return errors.New("boom")
		}
		return nil
	}}
	tallies, err := Apply(context.Background(), f, "http://x", p)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("failure must surface: %v", err)
	}
	if a := tally(tallies, "assets"); a.Failed != 3 {
		t.Errorf("assets: %+v", a)
	}
	if d := tally(tallies, "devices"); d.Created != 3 {
		t.Errorf("later kinds must still run: %+v", d)
	}
}

func TestApplyRefusedDeviceRowIsAFailureNotASuccess(t *testing.T) {
	p := sample(t, Options{})
	f := &fake{outcome: "CONFLICT"}
	tallies, err := Apply(context.Background(), f, "http://x", p)
	if err == nil {
		t.Fatal("a CONFLICT row must fail the run")
	}
	if d := tally(tallies, "devices"); d.Failed != 3 || d.Created != 0 {
		t.Errorf("devices: %+v", d)
	}
}

// ---- documents against the served schema ---------------------------------------------------

func servedSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("no tenant schema under %s: served=%v err=%v", dir, served, err)
	}
	schema, err := graphql.ParseSchema(sdl, nil,
		graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// Every document and variable map Apply sends must validate against the schema the server
// serves. The variables are the ones recorded from the real code path, because graphql-go
// rejects an input-object entry the schema does not define when it arrives through a variable.
func TestEveryDocumentValidatesAgainstTheServedSchema(t *testing.T) {
	schema := servedSchema(t)
	f := &fake{}
	if _, err := Apply(context.Background(), f, "http://x", sample(t, Options{CarryAccessTokens: true})); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range f.calls {
		seen[c.query] = true
		if errs := schema.ValidateWithVariables(c.query, c.vars); len(errs) > 0 {
			t.Errorf("%s: %v", c.query, errs)
		}
	}
	for _, d := range []string{createProfileMutation, publishProfileMutation, createTypeMutation, createAssetTypeMutation,
		createAssetMutation, importDevicesMutation, createRelTypeMutation, setParentMutation, createRelMutation, setAttrMutation} {
		if !seen[d] {
			t.Errorf("document never sent: %s", d)
		}
	}
}

// NEGATIVE CONTROL: the validator must reject a misnamed input key by that name.
func TestValidatorRejectsAMisnamedInputKey(t *testing.T) {
	schema := servedSchema(t)
	vars := map[string]any{"request": map[string]any{"token": "t", "assetTypeTok": "x"}}
	errs := schema.ValidateWithVariables(createAssetMutation, vars)
	if len(errs) == 0 || !strings.Contains(errs[0].Message, "assetTypeTok") {
		t.Errorf("want a rejection naming assetTypeTok, got %v", errs)
	}
}

// realExport is a regenerated, anonymised export of a small fleet taken from a live instance of
// another platform through its REST API: ids are rewritten, tokens regenerated, and nothing in it
// is a secret. It is the first input this importer has seen that no one wrote by hand.
const realExport = "testdata/real-export"

func realPlan(t *testing.T, o Options) *Plan {
	t.Helper()
	ex, err := Load(realExport)
	if err != nil {
		t.Fatal(err)
	}
	return Build(ex, o)
}

func TestRealExportBuilds(t *testing.T) {
	p := realPlan(t, Options{CarryAccessTokens: true})
	if len(p.Profiles) != 3 || len(p.AssetTypes) != 3 || len(p.Assets) != 4 || len(p.Devices) != 20 {
		t.Fatalf("counts: %d profiles, %d asset types, %d assets, %d devices",
			len(p.Profiles), len(p.AssetTypes), len(p.Assets), len(p.Devices))
	}
	if carried(p.Devices) != 20 {
		t.Errorf("every device has an access token, %d carried", carried(p.Devices))
	}
	if len(p.Parents) != 3 {
		t.Errorf("three floors sit under the building: %+v", p.Parents)
	}
	// 20 devices x (serial, installed, calibration, location, targetTemp); the connectivity
	// keys the source maintains itself are not copied.
	if len(p.Attributes) != 100 {
		t.Errorf("attributes: %d", len(p.Attributes))
	}
}

func TestRealExportNullCustomerIsNoCustomer(t *testing.T) {
	p := realPlan(t, Options{})
	named := 0
	for _, d := range p.Devices {
		if strings.Contains(d.Metadata, "customerId") {
			named++
		}
	}
	// The source writes an all-sentinel customer id on every entity that has none; only the one
	// device actually assigned to a customer may name one.
	if named != 1 {
		t.Errorf("%d devices carry a customer id, want 1", named)
	}
	for _, l := range p.Report {
		if l.Kind == "CUSTOMER" && l.Class == Partial && l.Ref != "1" {
			t.Errorf("customer reference count: %+v", l)
		}
	}
}

func TestRealExportAttributesAreTypedFromTheirJSONShape(t *testing.T) {
	p := realPlan(t, Options{})
	got := map[string]string{}
	for _, a := range p.Attributes {
		got[a.Key] = a.ValueType
		if a.Key == "active" || a.Key == "inactivityAlarmTime" {
			t.Errorf("platform-maintained key %q was copied", a.Key)
		}
	}
	want := map[string]string{"serial": "STRING", "installed": "BOOLEAN", "calibration": "DOUBLE", "location": "JSON", "targetTemp": "LONG"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attribute %s typed %q, want %q", k, got[k], v)
		}
	}
}

func TestUntypedValue(t *testing.T) {
	for _, c := range []struct{ in, vt, v string }{
		{`"x"`, "STRING", "x"}, {`true`, "BOOLEAN", "true"}, {`false`, "BOOLEAN", "false"},
		{`7`, "LONG", "7"}, {`-7`, "LONG", "-7"}, {`1.5`, "DOUBLE", "1.5"}, {`1e3`, "DOUBLE", "1e3"},
		{`{"a":1}`, "JSON", `{"a":1}`}, {`[1]`, "JSON", `[1]`},
	} {
		vt, v, ok := untypedValue([]byte(c.in))
		if !ok || vt != c.vt || v != c.v {
			t.Errorf("%s -> %q %q %v", c.in, vt, v, ok)
		}
	}
	for _, in := range []string{``, `null`, `nul`, `tru`} {
		if _, _, ok := untypedValue([]byte(in)); ok {
			t.Errorf("%q must carry no value", in)
		}
	}
}

func TestReportGroupsRepeatedMessages(t *testing.T) {
	p := realPlan(t, Options{})
	var out bytes.Buffer
	PrintReport(&out, p)
	text := out.String()
	if n := strings.Count(text, "platform-maintained attribute(s)"); n != 1 {
		t.Errorf("the per-device attribute note must print once, printed %d times:\n%s", n, text)
	}
	if !strings.Contains(text, "(and 19 more like it)") {
		t.Errorf("a grouped line must say how many it stands for:\n%s", text)
	}
	if len(p.Report) < 40 {
		t.Errorf("grouping is for the text only; the plan keeps every line, got %d", len(p.Report))
	}
}

func TestRealExportValidatesAgainstTheServedSchema(t *testing.T) {
	schema := servedSchema(t)
	f := &fake{}
	if _, err := Apply(context.Background(), f, "http://x", realPlan(t, Options{CarryAccessTokens: true})); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 {
		t.Fatal("nothing was sent")
	}
	for _, c := range f.calls {
		if errs := schema.ValidateWithVariables(c.query, c.vars); len(errs) > 0 {
			t.Errorf("%s: %v", c.query, errs)
		}
	}
}

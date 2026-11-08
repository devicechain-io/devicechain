// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

const secretValue = "s3cr3t-hunter2-VALUE"

// fixture is a product that exercises every resource family and every opaque document.
func fixture(t testing.TB) *Bundle {
	t.Helper()
	b := &Bundle{
		Connectors: []Connector{{
			Token: "audit-sink", Name: "Audit sink", Type: "mqtt",
			Config:     Doc(`{"urls":["tcp://broker.example.com:1883"],"topic":"alerts","qos":1}`),
			Credential: &Credential{Handle: "env:DC_CONNECTOR_AUDIT_SINK"},
		}},
		Profiles: []Profile{
			{
				Token: "thermostat", Name: "Thermostat", Category: "hvac", Metadata: Doc(`{"owner":"hvac","tier":2}`),
				Location:                 &Location{ExpectedAccuracyMeters: f64(5.5), ExpectedUpdateIntervalSeconds: ip(60)},
				ConfigurationDeclaration: []ConfigKey{{Key: "setpoint", ValueType: "DOUBLE", Description: "target"}, {Key: "mode", ValueType: "STRING"}},
				Decoder: Doc(`{"kind":"CEL","envVersion":1,"input":"FRAME","vectors":[{"name":"v1","frameHex":"0102","expect":{"temp-c":21.5}}],` +
					`"outputs":[{"key":"temp-c","expr":"double(frame[0]) / 2.0"}]}`),
				Metrics: []Metric{
					{Token: "m-temp-c", Key: "temp-c", Name: "Temperature", DataType: "DOUBLE", Unit: "Cel", Min: f64(-40), Max: f64(85)},
					{Token: "m-mode", Key: "mode-code", DataType: "INT", Enum: Doc(`["0","1","2"]`)},
				},
				CalculatedMetrics: []CalculatedMetric{{Token: "m-temp-f", Key: "temp-f", DataType: "DOUBLE", Unit: "[degF]", Expression: "temp-c * 9 / 5 + 32"}},
				Commands: []Command{{
					Token: "c-reboot", Key: "reboot", Name: "Reboot", Impact: "DISRUPTIVE",
					ParameterSchema: Doc(`[{"name":"delay","dataType":"INT","required":false}]`),
				}},
				Rules: []Rule{{
					Token: "r-overheat", Name: "Overheat", Enabled: true,
					Definition:     Doc(`{"when":"temp-c > 30","raise":{"severity":"MAJOR"}}`),
					AuthoringGraph: Doc(`{"nodes":[{"id":"n1","type":"threshold"}],"edges":[]}`),
				}},
			},
			{Token: "meter", Name: "Meter", Metrics: []Metric{{Token: "m-kwh", Key: "kwh", DataType: "DOUBLE", Unit: "kW.h"}}},
		},
		DeviceTypes: []DeviceType{
			{Token: "thermo-v1", Name: "Thermo v1", Profile: "thermostat", Manufacturer: "Acme", Model: "T1", Icon: "thermometer", Metadata: Doc(`{"sku":"T-1"}`)},
			{Token: "meter-v1", Name: "Meter v1", Profile: "meter"},
		},
		Integrations: []Integration{
			{Token: "ttn", Name: "The things", Type: "TTS_V3", Enabled: true, DeviceTypes: []string{"thermo-v1"}, LinkMetrics: true},
			{Token: "hook", Type: "GENERIC_JSON", Enabled: false, Identity: "ALIAS:custom.name",
				Generic: Doc(`{"idFrom":"pointer:/id"}`), DeviceTypes: []string{"meter-v1", "thermo-v1"}},
		},
		Dashboards: []Dashboard{{Token: "overview", Name: "Overview", Definition: Doc(`{"widgets":[{"id":"w1","type":"gauge","bind":"temp-c"}],"layout":{"cols":12}}`)}},
	}
	if err := b.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	return b
}

func env(m map[string]string) Options {
	return Options{Getenv: func(k string) string { return m[k] }}
}

var defaultEnv = map[string]string{"DC_CONNECTOR_AUDIT_SINK": secretValue}

// apply reads dir and applies it to f, returning the plan it applied.
func apply(t *testing.T, f *fakeTenant, dir string, o Options) *Plan {
	t.Helper()
	b, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanBundle(context.Background(), f, ep(), b, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return p
}

func plan(t *testing.T, f *fakeTenant, dir string, o Options) *Plan {
	t.Helper()
	b, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanBundle(context.Background(), f, ep(), b, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func export(t *testing.T, f *fakeTenant, dir string) {
	t.Helper()
	if _, err := Export(context.Background(), f, ep(), mustKinds(t, ""), false, dir, ""); err != nil {
		t.Fatal(err)
	}
}

func mustKinds(t *testing.T, s string) KindSet {
	k, err := ParseKinds(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// tree reads every file under dir into a map keyed by slash path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, a, b map[string]string) {
	t.Helper()
	var names []string
	for k := range a {
		names = append(names, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if a[n] != b[n] {
			t.Errorf("%s differs:\n--- first\n%s\n--- second\n%s", n, a[n], b[n])
		}
	}
}

// seeded builds a tenant holding the fixture, by applying it, and returns the tenant and a
// bundle directory written from the fixture.
func seeded(t *testing.T) (*fakeTenant, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := fixture(t).Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	apply(t, f, dir, env(defaultEnv))
	return f, dir
}

func TestExportApplyExportIsByteStable(t *testing.T) {
	a, fixtureDir := seeded(t)

	dirA := filepath.Join(t.TempDir(), "a")
	export(t, a, dirA)
	// The export of a tenant seeded from the fixture IS the fixture, byte for byte.
	sameTree(t, tree(t, fixtureDir), tree(t, dirA))

	// Applying it to an empty tenant and exporting again changes nothing.
	b := newFake(t)
	apply(t, b, dirA, env(defaultEnv))
	dirB := filepath.Join(t.TempDir(), "b")
	export(t, b, dirB)
	sameTree(t, tree(t, dirA), tree(t, dirB))

	// A second export over the same directory is a no-op on bytes.
	before := tree(t, dirB)
	export(t, b, dirB)
	sameTree(t, before, tree(t, dirB))
}

func TestApplyToAnEmptyTenantReproducesEveryResource(t *testing.T) {
	f, _ := seeded(t)
	if len(f.deviceTypes) != 2 || len(f.profiles) != 2 || len(f.integrations) != 2 || len(f.connectors) != 1 || len(f.dashboards) != 1 {
		t.Fatalf("unexpected tenant: %d types, %d profiles, %d integrations, %d connectors, %d dashboards",
			len(f.deviceTypes), len(f.profiles), len(f.integrations), len(f.connectors), len(f.dashboards))
	}
	if len(f.metrics) != 4 || len(f.commands) != 1 || len(f.rules) != 1 {
		t.Fatalf("definitions: %d metrics, %d commands, %d rules", len(f.metrics), len(f.commands), len(f.rules))
	}
	// Both profiles, the connector and the dashboard were published by apply.
	for tok, p := range f.profiles {
		if p["_active"] == nil {
			t.Errorf("profile %s was not published", tok)
		}
	}
	if f.connectors["audit-sink"]["_versions"] != 1 {
		t.Errorf("connector not published: %v", f.connectors["audit-sink"]["_versions"])
	}
	if f.dashboards["overview"]["_published"] == nil {
		t.Error("dashboard not published")
	}
	if f.secrets["audit-sink"] != secretValue {
		t.Error("the connector credential was not sent from the handle's environment variable")
	}
}

func TestPlanAgainstAnUnchangedTenantReportsNoChanges(t *testing.T) {
	f, dir := seeded(t)
	before := f.writes
	p := plan(t, f, dir, env(defaultEnv))
	if !p.Empty() || len(p.Blockers) != 0 {
		var sb strings.Builder
		p.Fprint(&sb)
		t.Fatalf("expected no changes:\n%s", sb.String())
	}
	if f.writes != before {
		t.Fatal("plan wrote to the tenant")
	}
	var sb strings.Builder
	p.Fprint(&sb)
	if !strings.Contains(sb.String(), "No changes") {
		t.Errorf("plan output: %q", sb.String())
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f, dir := seeded(t)
	before := f.writes
	apply(t, f, dir, env(defaultEnv))
	apply(t, f, dir, env(defaultEnv))
	if f.writes != before {
		t.Fatalf("a re-apply of an unchanged bundle made %d write(s): %v", f.writes-before, f.log[len(f.log)-(f.writes-before):])
	}
}

// editBundle rewrites the bundle in dir after mutate has changed it.
func editBundle(t *testing.T, dir string, mutate func(b *Bundle)) {
	t.Helper()
	b, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	mutate(b)
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
}

func planText(p *Plan) string {
	var sb strings.Builder
	p.Fprint(&sb)
	return sb.String()
}

func TestAChangeIsPlannedAppliedAndPublishedOnce(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		b.Profiles[0].Metrics[0].Unit = "MW.h"             // meter / kwh
		b.DeviceTypes[0].Name = "Thermo v1 renamed"        // meter-v1? sorted: meter-v1 first
		b.Dashboards[0].Definition = Doc(`{"widgets":[]}`) // draft changes, then a publish
		b.Profiles[1].Rules[0].Enabled = false             // thermostat rule: draft-only change
	})
	p := plan(t, f, dir, env(defaultEnv))
	text := planText(p)
	for _, want := range []string{
		"~ Metric           meter/m-kwh  update: unit",
		"~ DetectionRule    thermostat/r-overheat  update: enabled",
		"^ DeviceProfile    meter  publish (the draft changed)",
		"^ DeviceProfile    thermostat  publish (the draft changed)",
		"~ Dashboard        overview  update: definition",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan lacks %q:\n%s", want, text)
		}
	}
	apply(t, f, dir, env(defaultEnv))
	if p2 := plan(t, f, dir, env(defaultEnv)); !p2.Empty() {
		t.Fatalf("after apply the plan is not empty:\n%s", planText(p2))
	}
	if v := f.profiles["meter"]["_active"].(map[string]any)["version"]; v != float64(2) {
		t.Errorf("meter should be on version 2, got %v", v)
	}
	if v := f.dashboards["overview"]["_published"].(map[string]any)["version"]; v != float64(2) {
		t.Errorf("dashboard should be on version 2, got %v", v)
	}
}

func TestNoPublishConvergesDraftsOnly(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Profiles[0].Metrics[0].Unit = "MW.h" })
	o := env(defaultEnv)
	o.NoPublish = true
	p := plan(t, f, dir, o)
	if strings.Contains(planText(p), "publish") {
		t.Fatalf("--no-publish planned a publish:\n%s", planText(p))
	}
	apply(t, f, dir, o)
	if v := f.profiles["meter"]["_active"].(map[string]any)["version"]; v != float64(1) {
		t.Errorf("--no-publish published: version %v", v)
	}
	// A later publishing apply publishes what the draft holds.
	p2 := plan(t, f, dir, env(defaultEnv))
	if !strings.Contains(planText(p2), "^ DeviceProfile    meter  publish (the published version differs from the bundle)") {
		t.Errorf("expected a publish of meter:\n%s", planText(p2))
	}
}

func TestDefinitionsUnderAProfileConvergeIncludingRemoval(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		p := &b.Profiles[1] // thermostat
		p.Commands = nil
		p.CalculatedMetrics = nil
	})
	p := plan(t, f, dir, env(defaultEnv))
	text := planText(p)
	for _, want := range []string{"- Command", "- CalculatedMetric"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan lacks %q:\n%s", want, text)
		}
	}
	apply(t, f, dir, env(defaultEnv))
	if len(f.commands) != 0 || f.metrics["m-temp-f"] != nil {
		t.Fatalf("removals were not applied: %d commands", len(f.commands))
	}
}

func TestOnlyTheBundlesResourcesAreTouched(t *testing.T) {
	f, dir := seeded(t)
	// A device type and a dashboard the bundle does not carry.
	f.deviceTypes["extra"] = map[string]any{"token": "extra"}
	f.dashboards["extra-dash"] = map[string]any{"token": "extra-dash", "definition": `{}`, "updatedAt": "x"}
	before := f.writes
	p := plan(t, f, dir, env(defaultEnv))
	if !p.Empty() {
		t.Fatalf("an unmanaged resource changed the plan:\n%s", planText(p))
	}
	if !strings.Contains(planText(p), "1 device type(s) in the tenant are not in the bundle and are left alone: extra") {
		t.Errorf("no note about the unmanaged device type:\n%s", planText(p))
	}
	apply(t, f, dir, env(defaultEnv))
	if f.writes != before || f.deviceTypes["extra"] == nil || f.dashboards["extra-dash"] == nil {
		t.Fatal("apply touched a resource outside the bundle")
	}
}

func TestMovingADefinitionBetweenProfilesIsAnUpdateNotARecreate(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		// kwh moves from meter to thermostat.
		m := b.Profiles[0].Metrics[0]
		b.Profiles[0].Metrics = nil
		b.Profiles[1].Metrics = append(b.Profiles[1].Metrics, m)
	})
	p := plan(t, f, dir, env(defaultEnv))
	text := planText(p)
	if !strings.Contains(text, "~ Metric") || !strings.Contains(text, "profile") || strings.Contains(text, "- Metric") {
		t.Fatalf("a move should be one update:\n%s", text)
	}
	apply(t, f, dir, env(defaultEnv))
	if f.metrics["m-kwh"]["_profile"] != "thermostat" {
		t.Fatalf("m-kwh is under %v", f.metrics["m-kwh"]["_profile"])
	}
	if p2 := plan(t, f, dir, env(defaultEnv)); !p2.Empty() {
		t.Fatalf("plan not empty after the move:\n%s", planText(p2))
	}
}

func TestMeasuredAndCalculatedCannotChangePlaces(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		p := &b.Profiles[1]
		p.Metrics = append(p.Metrics, Metric{Token: "m-temp-f", Key: "temp-f", DataType: "DOUBLE"})
		p.CalculatedMetrics = nil
	})
	p := plan(t, f, dir, env(defaultEnv))
	if len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], "cannot be changed") {
		t.Fatalf("expected a blocker:\n%s", planText(p))
	}
	before := f.writes
	err := p.Apply(context.Background(), &bytes.Buffer{})
	if err == nil || f.writes != before {
		t.Fatalf("apply must refuse a blocked plan before writing: %v", err)
	}
}

func TestIntegrationTypeIsImmutable(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Integrations[0].Type = "TTS_V3" })
	p := plan(t, f, dir, env(defaultEnv))
	if len(p.Blockers) == 0 {
		t.Fatalf("expected a blocker:\n%s", planText(p))
	}
}

func TestANewIntegrationsBearerTokenIsNeverRequestedOrKept(t *testing.T) {
	f, _ := seeded(t)
	if f.tokensIssued != 2 {
		t.Fatalf("issued %d", f.tokensIssued)
	}
	for _, c := range f.calls {
		if strings.Contains(c.query, "issuedToken") {
			t.Errorf("a document selects the bearer token: %s", c.query)
		}
	}
}

// ---- concurrency ----------------------------------------------------------------------

func TestAConnectorEditedBetweenPlanAndApplyIsNotOverwritten(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Connectors[0].Name = "Renamed by the bundle" })
	b, _ := Read(dir)
	p, err := PlanBundle(context.Background(), f, ep(), b, env(defaultEnv))
	if err != nil {
		t.Fatal(err)
	}
	// Someone else writes the connector after the plan read it.
	f.connectors["audit-sink"]["updatedAt"] = "someone-elses-write"
	f.connectors["audit-sink"]["description"] = "their edit"
	err = p.Apply(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "modified by another writer") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if f.connectors["audit-sink"]["name"] == "Renamed by the bundle" || f.connectors["audit-sink"]["description"] != "their edit" {
		t.Fatal("the stale write landed")
	}
}

func TestADashboardEditedBetweenPlanAndApplyIsNotOverwritten(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Dashboards[0].Name = "Mine" })
	b, _ := Read(dir)
	p, err := PlanBundle(context.Background(), f, ep(), b, env(defaultEnv))
	if err != nil {
		t.Fatal(err)
	}
	f.dashboards["overview"]["updatedAt"] = "other"
	if err := p.Apply(context.Background(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "modified by another writer") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if f.dashboards["overview"]["name"] == "Mine" {
		t.Fatal("the stale write landed")
	}
}

func TestAnApplyThatFailsPartwayIsFinishedByRunningItAgain(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(t.TempDir(), "b")
	if err := fixture(t).Write(dir); err != nil {
		t.Fatal(err)
	}
	// A definition the plan cannot see (under a profile the tenant does not list) already holds
	// the rule's token, so creating the rule fails once the earlier steps have succeeded.
	f.metrics["r-overheat"] = map[string]any{"token": "r-overheat", "_profile": "ghost", "metricKey": "x", "dataType": "INT"}
	b, _ := Read(dir)
	p, err := PlanBundle(context.Background(), f, ep(), b, env(defaultEnv))
	if err != nil {
		t.Fatal(err)
	}
	err = p.Apply(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "run it again") || !strings.Contains(err.Error(), "r-overheat") {
		t.Fatalf("expected a partial failure naming the step and how to continue, got %v", err)
	}
	if len(f.profiles) == 0 {
		t.Fatal("nothing was applied before the failure")
	}
	delete(f.metrics, "r-overheat")
	apply(t, f, dir, env(defaultEnv))
	if !plan(t, f, dir, env(defaultEnv)).Empty() {
		t.Fatal("re-running did not converge")
	}
}

// ---- secrets --------------------------------------------------------------------------

func TestNoBundleFileHoldsASecretValue(t *testing.T) {
	f, _ := seeded(t)
	// The server also holds an integration bearer token digest; make it findable.
	f.integrations["ttn"]["_secretDigest"] = "digest-of-bearer-token-XYZ"
	dir := filepath.Join(t.TempDir(), "out")
	export(t, f, dir)
	for name, content := range tree(t, dir) {
		for _, s := range []string{secretValue, "digest-of-bearer-token-XYZ"} {
			if strings.Contains(content, s) {
				t.Errorf("%s contains a secret value", name)
			}
		}
		if strings.Contains(strings.ToLower(content), "password") {
			t.Errorf("%s mentions a password", name)
		}
	}
	conn := tree(t, dir)["connectors/audit-sink.yaml"]
	if !strings.Contains(conn, "handle: env:DC_CONNECTOR_AUDIT_SINK") {
		t.Errorf("the connector should carry a credential handle:\n%s", conn)
	}
	if res := (&Bundle{}); len(res.Scan()) != 0 {
		t.Error("an empty bundle has findings")
	}
	b, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fs := b.Scan(); len(fs) != 0 {
		t.Fatalf("the exported bundle has findings: %v", fs)
	}
}

func TestExportRefusesAndWritesNothingWhenAValueLooksLikeASecret(t *testing.T) {
	for name, cfg := range map[string]string{
		"a secret-named key":   `{"urls":["tcp://b:1883"],"topic":"t","password":"` + secretValue + `"}`,
		"credentials in a URL": `{"urls":["tcp://user:` + secretValue + `@b:1883"],"topic":"t"}`,
		"an AWS key":           `{"urls":["tcp://b:1883"],"topic":"t","accessKeyId":"AKIAABCDEFGHIJKLMNOP"}`,
		"a PEM block":          `{"topic":"-----BEGIN RSA PRIVATE KEY-----\nMIIB"}`,
		"a JWT":                `{"topic":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc"}`,
		"a bearer credential":  `{"topic":"Bearer abcdefghijklmnopqrstuvwxyz0123456789"}`,
		"a secret in metadata": ``,
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := seeded(t)
			if cfg == "" {
				f.integrations["ttn"]["metadata"] = `{"apiKey":"` + secretValue + `"}`
			} else {
				f.connectors["audit-sink"]["config"] = cfg
			}
			dir := filepath.Join(t.TempDir(), "out")
			_, err := Export(context.Background(), f, ep(), mustKinds(t, ""), false, dir, "")
			if err == nil {
				t.Fatal("export did not refuse")
			}
			if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("the refusal echoed the value: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ManifestFile)); statErr == nil {
				t.Fatal("export wrote files despite refusing")
			}
		})
	}
}

// NEGATIVE CONTROL: the detector must stay quiet about the legitimate shapes it sits next to,
// otherwise the refusals above prove only that it refuses everything.
func TestScannerLeavesHandlesAndIdentifiersAlone(t *testing.T) {
	b := fixture(t)
	b.Dashboards[0].Definition = Doc(`{"title":"password policy","token":"overview","widgets":[{"secretary":"x"}]}`)
	if err := b.Normalize(); err != nil {
		t.Fatal(err)
	}
	if fs := b.Scan(); len(fs) != 0 {
		t.Fatalf("false positives: %v", fs)
	}
	// And the same detector flags a value the moment it sits under a secret-named key.
	b.Dashboards[0].Definition = Doc(`{"password":"abc"}`)
	if fs := b.Scan(); len(fs) != 1 || strings.Contains(fs[0].String(), "abc") {
		t.Fatalf("expected one value-free finding, got %v", fs)
	}
}

func TestScanFindsAPasswordPlantedInAHandEditedBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	b := fixture(t)
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "connectors", "audit-sink.yaml")
	data, _ := os.ReadFile(p)
	data = append(data, []byte("\n")...)
	if err := os.WriteFile(p, []byte(strings.Replace(string(data), "topic: alerts", "topic: alerts\n  password: "+secretValue, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scan()) == 0 {
		t.Fatal("the planted password was not found")
	}
}

func TestACredentialIsSentOnlyToAConnectorThatHasNone(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Connectors[0].Name = "x" })
	other := map[string]string{"DC_CONNECTOR_AUDIT_SINK": "a-different-value"}
	apply(t, f, dir, env(other))
	if f.secrets["audit-sink"] != secretValue {
		t.Fatal("an existing credential was replaced without --rotate-secrets")
	}
	o := env(other)
	o.RotateSecrets = true
	// With nothing else changed, rotation is the change.
	p := plan(t, f, dir, o)
	if !strings.Contains(planText(p), "credential") {
		t.Fatalf("rotation should be planned:\n%s", planText(p))
	}
	apply(t, f, dir, o)
	if f.secrets["audit-sink"] != "a-different-value" {
		t.Fatal("--rotate-secrets did not send the credential")
	}
	if f.connectors["audit-sink"]["_versions"] != 3 {
		// the name change published (2); the rotation moved the draft's stamp, so it published
		// again (3) rather than leave a draft newer than every version.
		t.Errorf("versions: %v", f.connectors["audit-sink"]["_versions"])
	}
}

func TestAMissingCredentialVariableBlocksTheCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	if err := fixture(t).Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	p := plan(t, f, dir, env(nil))
	if len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], "DC_CONNECTOR_AUDIT_SINK") {
		t.Fatalf("expected a blocker naming the variable:\n%s", planText(p))
	}
	if err := p.Apply(context.Background(), &bytes.Buffer{}); err == nil || f.writes != 0 {
		t.Fatalf("apply wrote despite a blocker: %v", err)
	}
}

func TestACredentialRemovedFromTheBundleIsDeleted(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Connectors[0].Credential = nil })
	apply(t, f, dir, env(defaultEnv))
	if _, ok := f.secrets["audit-sink"]; ok {
		t.Fatal("the credential survived")
	}
}

func TestOnlyEnvHandlesAreAccepted(t *testing.T) {
	for _, h := range []string{"vault:secret/x", "literal", "env:", "env:1BAD", "env:HAS SPACE"} {
		if _, err := parseHandle(h); err == nil {
			t.Errorf("%q was accepted", h)
		}
	}
	if v, err := parseHandle("env:GOOD_NAME1"); err != nil || v != "GOOD_NAME1" {
		t.Fatalf("env handle: %v %v", v, err)
	}
}

// ---- the bundle on disk -----------------------------------------------------------------

func TestReadIsStrict(t *testing.T) {
	write := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "b")
		if err := fixture(t).Write(dir); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	edit := func(t *testing.T, dir, rel string, fn func(string) string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(fn(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]struct {
		mutate func(t *testing.T, dir string)
		want   string
	}{
		"an unknown field": {func(t *testing.T, d string) {
			edit(t, d, "device-types/meter-v1.yaml", func(s string) string { return s + "bogus: 1\n" })
		}, "bogus"},
		"a future apiVersion": {func(t *testing.T, d string) {
			edit(t, d, "product.yaml", func(s string) string { return strings.Replace(s, "v1alpha1", "v9", 1) })
		}, "apiVersion"},
		"a file not named for its token": {func(t *testing.T, d string) {
			if err := os.Rename(filepath.Join(d, "device-types", "meter-v1.yaml"), filepath.Join(d, "device-types", "other.yaml")); err != nil {
				t.Fatal(err)
			}
		}, "file name must be the token"},
		"the wrong kind": {func(t *testing.T, d string) {
			edit(t, d, "device-types/meter-v1.yaml", func(s string) string { return strings.Replace(s, "kind: DeviceType", "kind: Dashboard", 1) })
		}, "kind is"},
		"an unknown directory": {func(t *testing.T, d string) {
			if err := os.MkdirAll(filepath.Join(d, "widgets"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "unknown directory"},
		"a stray file in a kind directory": {func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "profiles", "notes.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "only .yaml"},
		"a credential handle with another scheme": {func(t *testing.T, d string) {
			edit(t, d, "connectors/audit-sink.yaml", func(s string) string { return strings.Replace(s, "env:", "vault:", 1) })
		}, "scheme"},
		"two metrics with one key": {func(t *testing.T, d string) {
			edit(t, d, "profiles/meter.yaml", func(s string) string {
				return strings.Replace(s, "metrics:", "metrics:\n- dataType: INT\n  key: kwh\n  token: dup\n", 1)
			})
		}, "declared by both"},
		"no manifest": {func(t *testing.T, d string) {
			if err := os.Remove(filepath.Join(d, "product.yaml")); err != nil {
				t.Fatal(err)
			}
		}, "not a product bundle"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := write(t)
			c.mutate(t, dir)
			_, err := Read(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestWriteRefusesAnUnrelatedNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fixture(t).Write(dir); err == nil {
		t.Fatal("wrote a bundle into someone's source tree")
	}
}

func TestReExportRemovesFilesOfResourcesThatNoLongerExist(t *testing.T) {
	f, _ := seeded(t)
	dir := filepath.Join(t.TempDir(), "out")
	export(t, f, dir)
	delete(f.dashboards, "overview")
	export(t, f, dir)
	if _, err := os.Stat(filepath.Join(dir, "dashboards", "overview.json")); err == nil {
		t.Fatal("a stale file survived the re-export")
	}
}

func TestOpaqueDocumentsSurviveAServerThatReformatsThem(t *testing.T) {
	f, dir := seeded(t)
	// The fake returns every document indented. If any comparison used raw text the plan would
	// not be empty; it is, so every comparison goes through canonical form.
	if !plan(t, f, dir, env(defaultEnv)).Empty() {
		t.Fatal("a reformatted document read as a change")
	}
	if got := tree(t, dir)["dashboards/overview.json"]; !strings.Contains(got, `"layout"`) {
		t.Fatalf("dashboard file: %s", got)
	}
}

func TestKindsRestrictAndReject(t *testing.T) {
	if _, err := ParseKinds("profiles,bogus"); err == nil {
		t.Fatal("accepted an unknown kind")
	}
	f, _ := seeded(t)
	dir := filepath.Join(t.TempDir(), "out")
	if _, err := Export(context.Background(), f, ep(), mustKinds(t, "dashboards"), false, dir, "demo"); err != nil {
		t.Fatal(err)
	}
	got := tree(t, dir)
	if len(got) != 2 || got["product.yaml"] == "" || !strings.Contains(got["product.yaml"], "name: demo") {
		t.Fatalf("a --kinds export wrote %v", got)
	}
}

func TestEnvNameIsAValidVariable(t *testing.T) {
	if got := envName("audit-sink.v2"); got != "DC_CONNECTOR_AUDIT_SINK_V2" {
		t.Fatal(got)
	}
	if _, err := parseHandle("env:" + envName("a b")); err != nil {
		t.Fatal(err)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the failure modes an independent review of the first cut found. Each one
// fails against the code as it was before the fix.

func TestAnEmptyEnumIsNoEnumOnBothSides(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	b := fixture(t)
	b.Profiles[0].Metrics[0].Enum = Doc(`[]`)
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	apply(t, f, dir, env(defaultEnv))
	// The empty list was published as no list; if it were kept, every plan would want a publish.
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatalf("an empty enum does not converge:\n%s", planText(p))
	}
}

func TestAConnectorUpdatedButNotPublishedIsPublishedByTheNextRun(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Connectors[0].Name = "renamed" })
	o := env(defaultEnv)
	o.NoPublish = true
	apply(t, f, dir, o)
	if f.connectors["audit-sink"]["_versions"] != 1 {
		t.Fatalf("--no-publish published: %v", f.connectors["audit-sink"]["_versions"])
	}
	// Having SOME version is not enough: the draft is newer than the latest one.
	p := plan(t, f, dir, env(defaultEnv))
	if !strings.Contains(planText(p), "^ Connector        audit-sink  publish (the draft is newer than the published version)") {
		t.Fatalf("the unpublished draft was not noticed:\n%s", planText(p))
	}
	apply(t, f, dir, env(defaultEnv))
	if f.connectors["audit-sink"]["_versions"] != 2 {
		t.Fatalf("not published: %v", f.connectors["audit-sink"]["_versions"])
	}
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatalf("does not converge:\n%s", planText(p))
	}
}

func TestRepublishFinishesWhatAFailedPublishLeftBehind(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Profiles[1].Rules[0].Enabled = false })
	o := env(defaultEnv)
	o.NoPublish = true
	apply(t, f, dir, o)
	before := f.profiles["thermostat"]["_active"].(map[string]any)["version"]
	// A rule is not readable from a published version, so a plain run cannot see the gap...
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatalf("a plain plan should not see an unreadable difference:\n%s", planText(p))
	}
	// ...and --republish is the way to close it.
	r := env(defaultEnv)
	r.Republish = true
	p := plan(t, f, dir, r)
	text := planText(p)
	for _, want := range []string{"^ DeviceProfile    thermostat  publish (--republish)", "^ Connector        audit-sink  publish (--republish)", "^ Dashboard        overview  publish (--republish)"} {
		if !strings.Contains(text, want) {
			t.Errorf("--republish plan lacks %q:\n%s", want, text)
		}
	}
	apply(t, f, dir, r)
	if after := f.profiles["thermostat"]["_active"].(map[string]any)["version"]; after == before {
		t.Fatal("--republish did not publish")
	}
}

func TestSwappingTwoDefinitionsKeysIsBlockedNotAnEndlessFailure(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		m := b.Profiles[1].Metrics // m-mode (mode-code), m-temp-c (temp-c)
		m[0].Key, m[1].Key = m[1].Key, m[0].Key
	})
	p := plan(t, f, dir, env(defaultEnv))
	if len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], "separate apply") {
		t.Fatalf("expected a blocker:\n%s", planText(p))
	}
	before := f.writes
	if err := p.Apply(context.Background(), &bytes.Buffer{}); err == nil || f.writes != before {
		t.Fatalf("apply wrote before refusing a key swap: %v", err)
	}
}

func TestRemovingOneLocationFieldClearsIt(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) { b.Profiles[1].Location = &Location{ExpectedUpdateIntervalSeconds: ip(60)} })
	p := plan(t, f, dir, env(defaultEnv))
	if !strings.Contains(planText(p), "update: location") {
		t.Fatalf("the change was not planned:\n%s", planText(p))
	}
	apply(t, f, dir, env(defaultEnv))
	if loc := f.profiles["thermostat"]["location"].(map[string]any); len(loc) != 1 {
		t.Fatalf("the omitted field survived: %v", loc)
	}
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatalf("does not converge:\n%s", planText(p))
	}
}

func TestADefinitionMovingOutOfAProfileRepublishesTheProfileItLeft(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		// The rule moves from thermostat to meter.
		r := b.Profiles[1].Rules[0]
		b.Profiles[1].Rules = nil
		b.Profiles[0].Rules = append(b.Profiles[0].Rules, r)
	})
	text := planText(plan(t, f, dir, env(defaultEnv)))
	for _, want := range []string{"^ DeviceProfile    meter  publish (the draft changed)", "^ DeviceProfile    thermostat  publish (a definition moved out of it)"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan lacks %q:\n%s", want, text)
		}
	}
	apply(t, f, dir, env(defaultEnv))
	if f.rules["r-overheat"]["_profile"] != "meter" {
		t.Fatal("the rule did not move")
	}
}

func TestADashboardThatIsNotInTheBundleIsReported(t *testing.T) {
	f, dir := seeded(t)
	f.dashboards["other"] = map[string]any{"token": "other", "definition": `{}`, "updatedAt": "x"}
	text := planText(plan(t, f, dir, env(defaultEnv)))
	if !strings.Contains(text, "1 dashboard(s) in the tenant are not in the bundle and are left alone: other") {
		t.Fatalf("no note:\n%s", text)
	}
}

// ---- secrets: what the review found leaking or slipping through -------------------------

func TestARejectedCredentialHandleIsNeverEchoed(t *testing.T) {
	const pasted = "AKIAABCDEFGHIJKLMNOP"
	for _, handle := range []string{pasted, "vault:" + pasted, "env:" + pasted + " x", "env:"} {
		dir := filepath.Join(t.TempDir(), "b")
		if err := fixture(t).Write(dir); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "connectors", "audit-sink.yaml")
		data, _ := os.ReadFile(p)
		if err := os.WriteFile(p, []byte(strings.Replace(string(data), "env:DC_CONNECTOR_AUDIT_SINK", handle, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Read(dir)
		if err == nil {
			t.Fatalf("%q was accepted", handle)
		}
		if strings.Contains(err.Error(), pasted) {
			t.Errorf("the error repeats what was pasted: %v", err)
		}
	}
}

func TestTheManifestIsScannedToo(t *testing.T) {
	b := fixture(t)
	b.Product.Name = "Bearer abcdefghijklmnopqrstuvwxyz0123456789"
	fs := b.Scan()
	if len(fs) != 1 || !strings.HasPrefix(fs[0].Where, ManifestFile) || strings.Contains(fs[0].String(), "abcdefgh") {
		t.Fatalf("findings: %v", fs)
	}
}

func TestASecretNestedUnderASecretNamedKeyIsFound(t *testing.T) {
	for name, doc := range map[string]string{
		"an object":         `{"password":{"value":"plaintext"}}`,
		"an array":          `{"apiKey":["plaintext"]}`,
		"an object in list": `{"secret":[{"v":"plaintext"}]}`,
	} {
		b := fixture(t)
		b.Dashboards[0].Definition = Doc(doc)
		if fs := b.Scan(); len(fs) != 1 || strings.Contains(fs[0].String(), "plaintext") {
			t.Errorf("%s: findings %v", name, fs)
		}
	}
	// A handle under a secret-named key is the sanctioned form.
	b := fixture(t)
	b.Dashboards[0].Definition = Doc(`{"password":{"value":"env:SOME_VAR"}}`)
	if fs := b.Scan(); len(fs) != 0 {
		t.Errorf("a handle was flagged: %v", fs)
	}
}

func TestAKeyThatIsASecretIsNotRepeatedInAFinding(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdef"
	b := fixture(t)
	b.Dashboards[0].Definition = Doc(`{"` + jwt + `":{"password":"x"}}`)
	fs := b.Scan()
	if len(fs) == 0 {
		t.Fatal("nothing found")
	}
	for _, f := range fs {
		if strings.Contains(f.String(), "eyJhbGci") {
			t.Errorf("the key was repeated: %s", f)
		}
	}
}

func TestADashboardFileWithContentAfterTheDocumentIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	if err := fixture(t).Write(dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "dashboards", "overview.json")
	data, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(data, []byte(`{"password":"x"}`)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "after the dashboard document") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestARefusedReExportLeavesTheExistingBundleIntact(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	if err := fixture(t).Write(dir); err != nil {
		t.Fatal(err)
	}
	// An unexpected directory in a LATER managed directory: before the fix the earlier ones
	// had already been emptied by the time it was noticed.
	if err := os.MkdirAll(filepath.Join(dir, "dashboards", "stray"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	if err := fixture(t).Write(dir); err == nil {
		t.Fatal("expected a refusal")
	}
	after := tree(t, dir)
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("%s was removed by a refused write", name)
		}
	}
}

func TestADashboardWhoseDraftWasNeverPublishedOrDiffersFromItsPublishedVersionIsPublished(t *testing.T) {
	f, dir := seeded(t)
	// Same draft as the bundle, but the published version is older.
	d := f.dashboards["overview"]
	d["_published"] = map[string]any{"version": float64(1), "definition": `{"widgets":["old"]}`}
	text := planText(plan(t, f, dir, env(defaultEnv)))
	if !strings.Contains(text, "^ Dashboard        overview  publish (the draft differs from the published version)") {
		t.Fatalf("a stale published version was not noticed:\n%s", text)
	}
	apply(t, f, dir, env(defaultEnv))
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatalf("does not converge:\n%s", planText(p))
	}
	// Never published at all.
	delete(f.dashboards["overview"], "_published")
	text = planText(plan(t, f, dir, env(defaultEnv)))
	if !strings.Contains(text, "^ Dashboard        overview  publish (never published)") {
		t.Fatalf("an unpublished dashboard was not noticed:\n%s", text)
	}
}

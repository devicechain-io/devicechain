// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// ---- A fake device-management GraphQL, enough to exercise ensureProfile -------
//
// ensureProfile owns the decision this file exists for: WHEN to publish, and what
// to reconcile first. Both are functions of what previous runs left behind, so they
// are invisible to any pure test — they are exercised against a server that
// remembers, rather than reasoned about.
//
// Scoped to ensureProfile rather than all of Provision on purpose: it is the unit
// that owns the rules, and faking the whole chain (devices, credentials,
// relationships, dashboards) would be a large surface whose failures would mostly
// be the fake's own.

type fakeMetric struct{ Name, DataType, Unit string }
type fakeCommand struct{ CommandKey, Name, ParameterSchema string }
type fakeRule struct {
	Name       string
	Definition string
	Enabled    bool
}

type fakeProfileServer struct {
	mu       sync.Mutex
	exists   bool
	metrics  map[string]fakeMetric
	commands map[string]fakeCommand
	rules    map[string]fakeRule
	// Published version labels, in order. The active version is the last one.
	versions []string
	// Counters, so a test can distinguish "did nothing" from "did the same thing".
	updates int
	// failNextPublish makes exactly one publish fail, modelling the ADR-044
	// validation gate failing closed on a transiently unavailable event-processing.
	failNextPublish bool
}

func newFakeProfileServer(t *testing.T) (*fakeProfileServer, *Runtime) {
	t.Helper()
	f := &fakeProfileServer{
		metrics:  map[string]fakeMetric{},
		commands: map[string]fakeCommand{},
		rules:    map[string]fakeRule{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)

	return f, &Runtime{
		Endpoints:  Endpoints{DeviceMgmtGraphQL: srv.URL, UserGraphQL: srv.URL},
		InstanceId: "dc",
		Tenant:     "acme",
		HTTPClient: srv.Client(),
		Session:    userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", "acme"),
	}
}

func (f *fakeProfileServer) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	data, status := f.dispatch(req.Query, req.Variables)
	if status != 0 {
		http.Error(w, "fake: "+http.StatusText(status), status)
		return
	}
	if data == nil {
		// An operation the fake does not model would otherwise decode as an empty
		// result and read as "absent" — exactly the answer that makes a
		// reconciling provisioner create. Fail loudly instead. This caught the
		// version query the moment ensureProfile started making it.
		http.Error(w, "fake server has no handler for: "+req.Query, http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func requestArg(vars map[string]any) map[string]any {
	req, _ := vars["request"].(map[string]any)
	return req
}

func (f *fakeProfileServer) dispatch(query string, vars map[string]any) (map[string]any, int) {
	switch {
	case strings.Contains(query, "login("):
		return map[string]any{"login": map[string]any{"identityToken": "identity", "superuser": true}}, 0
	case strings.Contains(query, "selectTenant("):
		return map[string]any{"selectTenant": map[string]any{"accessToken": "access", "refreshToken": "refresh"}}, 0

	case strings.Contains(query, "deviceProfilesByToken"):
		if !f.exists {
			return map[string]any{"deviceProfilesByToken": []any{}}, 0
		}
		return map[string]any{"deviceProfilesByToken": []any{f.profileJSON()}}, 0

	case strings.Contains(query, "createDeviceProfile"):
		f.exists = true
		return map[string]any{"createDeviceProfile": map[string]any{"token": str(requestArg(vars), "token")}}, 0

	case strings.Contains(query, "deviceProfileVersions"):
		out := make([]any, 0, len(f.versions))
		for i, label := range f.versions {
			out = append(out, map[string]any{"version": i + 1, "label": label})
		}
		return map[string]any{"deviceProfileVersions": out}, 0

	case strings.Contains(query, "publishDeviceProfile"):
		if f.failNextPublish {
			f.failNextPublish = false
			// The gate fails CLOSED, so the mutation errors and no version is cut.
			return nil, http.StatusInternalServerError
		}
		label, _ := vars["label"].(string)
		f.versions = append(f.versions, label)
		return map[string]any{"publishDeviceProfile": map[string]any{"version": len(f.versions)}}, 0

	case strings.Contains(query, "createMetricDefinition"):
		r := requestArg(vars)
		f.metrics[str(r, "token")] = fakeMetric{str(r, "name"), str(r, "dataType"), str(r, "unit")}
		return map[string]any{"createMetricDefinition": map[string]any{"token": str(r, "token")}}, 0
	case strings.Contains(query, "updateMetricDefinition"):
		r := requestArg(vars)
		f.updates++
		f.metrics[str(r, "token")] = fakeMetric{str(r, "name"), str(r, "dataType"), str(r, "unit")}
		return map[string]any{"updateMetricDefinition": map[string]any{"token": str(r, "token")}}, 0

	case strings.Contains(query, "createCommandDefinition"):
		r := requestArg(vars)
		f.commands[str(r, "token")] = fakeCommand{str(r, "commandKey"), str(r, "name"), str(r, "parameterSchema")}
		return map[string]any{"createCommandDefinition": map[string]any{"token": str(r, "token")}}, 0
	case strings.Contains(query, "updateCommandDefinition"):
		r := requestArg(vars)
		f.updates++
		f.commands[str(r, "token")] = fakeCommand{str(r, "commandKey"), str(r, "name"), str(r, "parameterSchema")}
		return map[string]any{"updateCommandDefinition": map[string]any{"token": str(r, "token")}}, 0

	case strings.Contains(query, "createDetectionRule"):
		r := requestArg(vars)
		enabled, _ := r["enabled"].(bool)
		f.rules[str(r, "token")] = fakeRule{str(r, "name"), str(r, "definition"), enabled}
		return map[string]any{"createDetectionRule": map[string]any{"token": str(r, "token")}}, 0
	case strings.Contains(query, "updateDetectionRule"):
		r := requestArg(vars)
		enabled, _ := r["enabled"].(bool)
		f.updates++
		f.rules[str(r, "token")] = fakeRule{str(r, "name"), str(r, "definition"), enabled}
		return map[string]any{"updateDetectionRule": map[string]any{"token": str(r, "token")}}, 0
	}
	return nil, 0
}

func (f *fakeProfileServer) profileJSON() map[string]any {
	var active any
	if len(f.versions) > 0 {
		active = len(f.versions)
	}
	metrics := make([]any, 0, len(f.metrics))
	for token, m := range f.metrics {
		metrics = append(metrics, map[string]any{
			"token": token, "name": m.Name, "dataType": m.DataType, "unit": m.Unit})
	}
	commands := make([]any, 0, len(f.commands))
	for token, c := range f.commands {
		commands = append(commands, map[string]any{
			"token": token, "commandKey": c.CommandKey, "name": c.Name, "parameterSchema": c.ParameterSchema})
	}
	rules := make([]any, 0, len(f.rules))
	for token, r := range f.rules {
		rules = append(rules, map[string]any{
			"token": token, "name": r.Name, "definition": r.Definition, "enabled": r.Enabled})
	}
	return map[string]any{
		"token": "profile", "activeVersion": active,
		"metricDefinitions": metrics, "commandDefinitions": commands, "detectionRules": rules,
	}
}

func (f *fakeProfileServer) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.versions)
}

func (f *fakeProfileServer) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updates
}

func (f *fakeProfileServer) rule(token string) fakeRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rules[token]
}

// ---- The publish decision ----------------------------------------------------

func widgetlabProfileSpec(t *testing.T) ProfileSpec {
	t.Helper()
	for _, p := range NewWidgetlab(1, Load{}).Manifest().Profiles {
		if p.Token == WidgetlabProfileToken {
			return p
		}
	}
	t.Fatal("widgetlab declares no profile")
	return ProfileSpec{}
}

func run(t *testing.T, rt *Runtime, spec ProfileSpec) error {
	t.Helper()
	return ensureProfile(context.Background(), rt, spec)
}

func TestEnsureProfilePublishesOnceOnAFreshTenant(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	if err := run(t, rt, widgetlabProfileSpec(t)); err != nil {
		t.Fatalf("ensureProfile: %v", err)
	}
	if got := f.publishCount(); got != 1 {
		t.Errorf("published %d times on a fresh tenant, want exactly 1", got)
	}
}

// The counterweight to every republish assertion below: an unchanged re-run must
// publish NOTHING. publishDeviceProfile always mints a new version, so a
// provisioner that cannot tell "nothing to do" from "something new" would grow a
// junk version on every reset — and every republish test would prove nothing.
func TestEnsureProfileDoesNotRepublishAnUnchangedProfile(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)
	for i := 0; i < 3; i++ {
		if err := run(t, rt, spec); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if got := f.publishCount(); got != 1 {
		t.Errorf("published %d times across three identical runs, want 1", got)
	}
	if got := f.updateCount(); got != 0 {
		t.Errorf("issued %d updates across three identical runs, want 0 — the reconciler is "+
			"rewriting content that already matches", got)
	}
}

// "The scenario grows with the catalog." ADR-045's draft is inert until published,
// so anything a later run adds and does not publish exists only as a draft: the
// tenant looks fully provisioned and a widget bound to the new vocabulary renders
// empty. Each kind is a separate call site, so each is checked separately.
func TestEnsureProfileRepublishesWhenARerunAddsSomething(t *testing.T) {
	base := widgetlabProfileSpec(t)

	grow := map[string]func(ProfileSpec) ProfileSpec{
		"a new metric": func(p ProfileSpec) ProfileSpec {
			p.Metrics = append(append([]MetricSpec(nil), p.Metrics...),
				MetricSpec{Key: "later", Name: "Later", DataType: "DOUBLE", Unit: "x"})
			return p
		},
		// Prepended, not appended. An earlier design derived the publish decision
		// from a per-item "did I create this" flag, and a plausible slip there
		// (assigning rather than or-ing) went undetected because every growth case
		// added its item last.
		"a metric added at the front": func(p ProfileSpec) ProfileSpec {
			p.Metrics = append([]MetricSpec{{Key: "first", Name: "First", DataType: "DOUBLE", Unit: "x"}},
				p.Metrics...)
			return p
		},
		"a new command": func(p ProfileSpec) ProfileSpec {
			p.Commands = append(append([]CommandSpec(nil), p.Commands...),
				CommandSpec{Token: "wl-later-cmd", CommandKey: "later", Name: "Later"})
			return p
		},
		"a new detection rule": func(p ProfileSpec) ProfileSpec {
			p.DetectionRules = append(append([]DetectionRuleSpec(nil), p.DetectionRules...),
				DetectionRuleSpec{Token: "wl-later-rule", Name: "Later",
					Definition: `{"name":"later"}`, Metric: WidgetlabTemperatureKey, Enabled: true})
			return p
		},
	}

	for name, addition := range grow {
		t.Run(name, func(t *testing.T) {
			f, rt := newFakeProfileServer(t)
			if err := run(t, rt, base); err != nil {
				t.Fatalf("first run: %v", err)
			}
			if err := run(t, rt, addition(base)); err != nil {
				t.Fatalf("second run: %v", err)
			}
			if got := f.publishCount(); got != 2 {
				t.Errorf("published %d times after a re-run introduced %s, want 2 — without the "+
					"second publish it stays a draft the active version never contains", got, name)
			}
		})
	}
}

// A CHANGED definition must be rewritten and republished, not skipped because its
// token already exists. This is what makes a fix deliverable: this scenario shipped
// a rule the compiler rejects, and a provisioner matching on token alone could not
// have healed any tenant that had already run the broken build.
func TestEnsureProfileConvergesAChangedRuleDefinition(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	base := widgetlabProfileSpec(t)
	if err := run(t, rt, base); err != nil {
		t.Fatalf("first run: %v", err)
	}

	fixed := base
	fixed.DetectionRules = append([]DetectionRuleSpec(nil), base.DetectionRules...)
	fixed.DetectionRules[0].Definition = strings.Replace(
		fixed.DetectionRules[0].Definition, `"threshold":30`, `"threshold":28`, 1)
	if fixed.DetectionRules[0].Definition == base.DetectionRules[0].Definition {
		t.Fatal("the test's own edit changed nothing; it would assert convergence of an " +
			"unchanged definition")
	}

	if err := run(t, rt, fixed); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := f.rule(WidgetlabRuleToken).Definition; got != fixed.DetectionRules[0].Definition {
		t.Errorf("the stored rule was not updated to the declared definition:\n got %s\nwant %s",
			got, fixed.DetectionRules[0].Definition)
	}
	if got := f.publishCount(); got != 2 {
		t.Errorf("published %d times after the rule changed, want 2 — the corrected rule would "+
			"otherwise sit in the draft while the broken one stays active", got)
	}
}

// A definition the platform round-tripped through its own marshaller comes back
// with different key order and spacing. Comparing bytes would report every
// unchanged rule as changed, rewriting and republishing on every single bootstrap.
func TestEnsureProfileDoesNotRewriteAReformattedDefinition(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Re-marshal the stored definition through a map, which reorders keys.
	var value map[string]any
	if err := json.Unmarshal([]byte(f.rule(WidgetlabRuleToken).Definition), &value); err != nil {
		t.Fatalf("stored definition is not JSON: %v", err)
	}
	reordered, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	f.mu.Lock()
	stored := f.rules[WidgetlabRuleToken]
	stored.Definition = string(reordered)
	f.rules[WidgetlabRuleToken] = stored
	f.mu.Unlock()

	if err := run(t, rt, spec); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := f.updateCount(); got != 0 {
		t.Errorf("issued %d updates against a merely reformatted definition, want 0", got)
	}
	if got := f.publishCount(); got != 1 {
		t.Errorf("published %d times against a merely reformatted definition, want 1", got)
	}
}

// 🔴 The stranding path. A run adds something and the publish then FAILS — the
// ADR-044 gate fails closed, so a transiently unavailable event-processing is
// enough. The next run must still publish.
//
// A publish decision derived from "did THIS run create something" gets this wrong
// permanently: the next run finds the addition already present, concludes it added
// nothing, sees a non-nil activeVersion, and skips the publish forever. The
// addition is stranded as a draft, and the tenant looks fully provisioned.
func TestEnsureProfileRecoversFromAFailedPublish(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)

	f.mu.Lock()
	f.failNextPublish = true
	f.mu.Unlock()

	if err := run(t, rt, spec); err == nil {
		t.Fatal("the first run reported success though its publish failed")
	}
	if got := f.publishCount(); got != 0 {
		t.Fatalf("a failed publish still cut %d version(s); the fake does not model the gate", got)
	}

	// Everything was already created, so a "did I create something" decision would
	// see nothing new here.
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if got := f.publishCount(); got != 1 {
		t.Errorf("published %d times after recovering from a failed publish, want 1 — the "+
			"scenario's whole vocabulary would otherwise stay a draft forever", got)
	}
}

// A profile created by an interrupted run — everything present, never published.
func TestEnsureProfilePublishesAnExistingUnpublishedProfile(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)

	f.mu.Lock()
	f.exists = true
	for _, m := range spec.Metrics {
		f.metrics[spec.Token+"-"+m.Key] = fakeMetric{m.Name, m.DataType, m.Unit}
	}
	for _, c := range spec.Commands {
		f.commands[c.Token] = fakeCommand{c.CommandKey, c.Name, c.ParameterSchema}
	}
	for _, r := range spec.DetectionRules {
		f.rules[r.Token] = fakeRule{r.Name, r.Definition, r.Enabled}
	}
	f.mu.Unlock()

	if err := run(t, rt, spec); err != nil {
		t.Fatalf("ensureProfile: %v", err)
	}
	if got := f.publishCount(); got != 1 {
		t.Errorf("published %d times over a created-but-unpublished profile, want 1", got)
	}
}

// The marker is what makes the publish decision durable, so it must actually
// distinguish content — a constant marker would make every profile look published.
func TestProfileContentMarkerChangesWithTheContent(t *testing.T) {
	base := widgetlabProfileSpec(t)
	marker := profileContentMarker(base)

	if profileContentMarker(base) != marker {
		t.Fatal("the marker is not stable for identical content")
	}
	changed := base
	changed.DetectionRules = append([]DetectionRuleSpec(nil), base.DetectionRules...)
	changed.DetectionRules[0].Definition = `{"name":"different"}`
	if profileContentMarker(changed) == marker {
		t.Error("a changed rule definition produced the same marker, so the corrected rule " +
			"would never be published")
	}

	reordered := base
	reordered.Metrics = append([]MetricSpec{base.Metrics[1]}, base.Metrics[0])
	if profileContentMarker(reordered) == marker {
		t.Error("reordered metrics produced the same marker; order is part of the declared " +
			"content and a reader would expect the change to be published")
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// fakeProfileToken is the profile every test here provisions. Named once so the
// fake's own responses (its profile token, and the version-bearing rule ids it mints)
// agree with what the sim actually queries for — a fake answering about a different
// profile than the one asked about is a fake that cannot catch a mis-wired argument.
const fakeProfileToken = WidgetlabProfileToken

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

	// liveRules is what event-processing's ENGINE holds — the ruleHealth read —
	// which is a different thing from the rules device-management stores. Publishing
	// loads the active version's enabled rules into it, and the two knobs below model
	// the ways they diverge on an instance whose publish-time validator was skipped:
	// a rule the engine dropped at load, and one it surfaced as COMPILE_ERROR.
	//
	// Modelled separately rather than derived, because "the publish succeeded" and
	// "the rule is running" being the same thing is precisely the assumption under
	// test. A fake that reported health straight from its stored rules could only
	// ever agree with itself.
	liveRules       map[string]string // rule token → RuleStatus
	liveVersion     int               // the profile version those rules belong to
	dropRuleAtLoad  string            // a rule token the engine never loads
	breakRuleAtLoad string            // a rule token the engine reports COMPILE_ERROR

	// 🔴 settleAfterPolls models the ASYNCHRONOUS seam, and it is the reason this
	// fake is not merely a mirror of the code under test.
	//
	// ruleHealth answers from event-processing's OWN projection, written by a NATS
	// consumer — so for a window after a publish it still serves the PREVIOUS
	// version, whose rows carry the SAME rule tokens, all ACTIVE. While this fake
	// loaded the new rules synchronously inside publishDeviceProfile, that window
	// could not be expressed, and a check matching on rule token alone passed on its
	// first read for a version the engine had not loaded. The fake agreed with the
	// code by construction on exactly the axis that mattered.
	//
	// With this, ruleHealth keeps answering for the previous version until it has
	// been polled this many times.
	settleAfterPolls int
	ruleHealthPolls  int
	// pendingRules/pendingVersion are what the engine will serve once it settles.
	pendingRules   map[string]string
	pendingVersion int
	// unhealthyForNPolls makes the first N ruleHealth requests fail at the transport
	// layer, modelling event-processing restarting mid-bootstrap.
	unhealthyForNPolls int
}

func newFakeProfileServer(t *testing.T) (*fakeProfileServer, *Runtime) {
	t.Helper()
	f := &fakeProfileServer{
		metrics:   map[string]fakeMetric{},
		commands:  map[string]fakeCommand{},
		rules:     map[string]fakeRule{},
		liveRules: map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)

	// Shorten the rule-health settle window for the whole package: a negative case
	// must outlast it, and 30s per case is how a suite gets skipped.
	t.Cleanup(func(to time.Duration, pi time.Duration) func() {
		return func() { ruleHealthSettleTimeout, ruleHealthPollInterval = to, pi }
	}(ruleHealthSettleTimeout, ruleHealthPollInterval))
	ruleHealthSettleTimeout, ruleHealthPollInterval = 150*time.Millisecond, 10*time.Millisecond

	return f, &Runtime{
		Endpoints: Endpoints{
			DeviceMgmtGraphQL:      srv.URL,
			UserGraphQL:            srv.URL,
			EventProcessingGraphQL: srv.URL,
		},
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
		// Publishing hands a new active version to the engine — EVENTUALLY. Only
		// ENABLED rules are loaded, matching the runtime (a disabled rule is inert and
		// the publish gate does not even submit it).
		loaded := map[string]string{}
		for token, r := range f.rules {
			if !r.Enabled || token == f.dropRuleAtLoad {
				continue
			}
			status := "ACTIVE"
			if token == f.breakRuleAtLoad {
				status = "COMPILE_ERROR"
			}
			loaded[token] = status
		}
		if f.settleAfterPolls > 0 {
			// Hold the new version back: ruleHealth keeps answering for the previous
			// one until it has been polled settleAfterPolls times.
			f.pendingRules, f.pendingVersion = loaded, len(f.versions)
			f.ruleHealthPolls = 0
		} else {
			f.liveRules, f.liveVersion = loaded, len(f.versions)
		}
		return map[string]any{"publishDeviceProfile": map[string]any{"version": len(f.versions)}}, 0

	case strings.Contains(query, "ruleHealth"):
		// The fake must READ the argument, or a query that names the wrong profile
		// would pass every test while failing every live bootstrap.
		if got := str(vars, "profileToken"); got != fakeProfileToken {
			return nil, http.StatusBadRequest
		}
		f.ruleHealthPolls++
		if f.unhealthyForNPolls > 0 {
			f.unhealthyForNPolls--
			return nil, http.StatusServiceUnavailable
		}
		if f.pendingRules != nil && f.ruleHealthPolls > f.settleAfterPolls {
			f.liveRules, f.liveVersion = f.pendingRules, f.pendingVersion
			f.pendingRules = nil
		}
		out := make([]any, 0, len(f.liveRules))
		for token, status := range f.liveRules {
			entry := map[string]any{
				// The real id shape: the version here is what tells a caller which
				// version the engine is answering for.
				"ruleId":    fmt.Sprintf("acme/%s@%d/%s", fakeProfileToken, f.liveVersion, token),
				"ruleToken": token, "status": status, "message": nil,
			}
			if status == "COMPILE_ERROR" {
				entry["message"] = "predicate exceeds the cost ceiling"
			}
			out = append(out, entry)
		}
		return map[string]any{"ruleHealth": out}, 0

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
		"token": fakeProfileToken, "activeVersion": active,
		"metricDefinitions": metrics, "commandDefinitions": commands, "detectionRules": rules,
	}
}

// versionCount is the number of versions that EXIST, which is not the same as the
// number of times this fake was asked to publish — a test may append a version
// directly to model somebody else republishing. Named for what it counts.
func (f *fakeProfileServer) versionCount() int {
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
	if got := f.versionCount(); got != 1 {
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
	if got := f.versionCount(); got != 1 {
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
			if got := f.versionCount(); got != 2 {
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
	if got := f.versionCount(); got != 2 {
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
	if got := f.versionCount(); got != 1 {
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
	if got := f.versionCount(); got != 0 {
		t.Fatalf("a failed publish still cut %d version(s); the fake does not model the gate", got)
	}

	// Everything was already created, so a "did I create something" decision would
	// see nothing new here.
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if got := f.versionCount(); got != 1 {
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
	if got := f.versionCount(); got != 1 {
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

// ---- Proving the rules are LIVE, not merely published -------------------------

// 🔑 THE SKIPPED-VALIDATOR GATE. device-management compiles a profile's enabled
// rules at publish and fails closed — but only when a validator is WIRED. With the
// service secret unset the check returns nil having validated nothing, so a rule the
// compiler would reject publishes cleanly, event-processing drops it at load with a
// log line nobody reads, and the scenario's alarm widgets are permanently empty with
// every step reporting success.
//
// "The publish did not error" and "the rule is running" are different claims, and
// the first was standing in for the second. This asserts the second.
func TestEnsureProfileFailsWhenARuleNeverReachesTheEngine(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)
	if len(spec.DetectionRules) == 0 {
		t.Fatal("the spec declares no detection rules, so this test asserts nothing")
	}
	dropped := spec.DetectionRules[0].Token

	f.mu.Lock()
	f.dropRuleAtLoad = dropped
	f.mu.Unlock()

	err := run(t, rt, spec)
	if err == nil {
		t.Fatal("bootstrap reported success though its rule never reached the engine")
	}
	if !strings.Contains(err.Error(), dropped) {
		t.Errorf("error %q does not name the rule that is missing", err)
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("error %q does not say the rule is absent, so a reader cannot tell it from "+
			"a rule that failed to compile", err)
	}
	// The publish itself must still have happened: the failure is the POST-ASSERTION,
	// and a test that passed because nothing was published would prove nothing.
	if got := f.versionCount(); got != 1 {
		t.Errorf("published %d times, want 1 — the failure must come from the health check, "+
			"not from never publishing", got)
	}
}

// The other half of the same read: a rule the engine surfaced rather than dropped.
// Reported differently because it points at a different cause, and a gate that said
// only "not healthy" would send the reader to the wrong place.
func TestEnsureProfileFailsWhenARuleDoesNotCompileInTheEngine(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)
	broken := spec.DetectionRules[0].Token

	f.mu.Lock()
	f.breakRuleAtLoad = broken
	f.mu.Unlock()

	err := run(t, rt, spec)
	if err == nil {
		t.Fatal("bootstrap reported success over a rule the engine cannot compile")
	}
	if !strings.Contains(err.Error(), "COMPILE_ERROR") {
		t.Errorf("error %q does not report the engine's status", err)
	}
	// The engine's own diagnostic has to survive into the message, or the operator
	// has to go find a log to learn anything actionable.
	if !strings.Contains(err.Error(), "cost ceiling") {
		t.Errorf("error %q drops the engine's diagnostic", err)
	}
}

// The counterweight: a healthy profile must pass, and pass without waiting out the
// settle window. Rejecting a broken rule is worthless if a correct one is refused too.
func TestEnsureProfileAcceptsRulesThatAreLive(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)

	if err := run(t, rt, spec); err != nil {
		t.Fatalf("a profile whose rules are live was rejected: %v", err)
	}
	f.mu.Lock()
	live, polls := len(f.liveRules), f.ruleHealthPolls
	f.mu.Unlock()
	// Asserted as a poll COUNT rather than as elapsed time: a healthy profile must be
	// confirmed by reading once, and a wall-clock bound for that is a flake on a
	// loaded runner rather than a statement about the code.
	if polls != 1 {
		t.Errorf("ruleHealth was polled %d times for an already-live profile, want 1 — the "+
			"check is waiting rather than reading", polls)
	}
	if live == 0 {
		t.Fatal("the fake loaded no rules into its engine, so the acceptance above is vacuous")
	}
}

// A scenario with enabled rules and no endpoint to check them against must FAIL, not
// quietly skip the check. A verification that silently does nothing when its endpoint
// is missing is worse than none: it reports the same green as a real pass.
func TestEnsureProfileRefusesRulesItCannotVerify(t *testing.T) {
	_, rt := newFakeProfileServer(t)
	rt.Endpoints.EventProcessingGraphQL = ""

	err := run(t, rt, widgetlabProfileSpec(t))
	if err == nil {
		t.Fatal("bootstrap succeeded with no way to confirm its rules are running")
	}
	if !strings.Contains(err.Error(), "eventProcessingGraphQL") {
		t.Errorf("error %q does not name the missing handshake field", err)
	}
}

// A profile with no ENABLED rules needs no endpoint: a disabled rule is inert by
// design, and requiring it to be live would refuse a scenario that parks one
// deliberately. Without this the check would make a legitimate manifest unrunnable.
func TestEnsureProfileWithNoEnabledRulesNeedsNoRuleHealth(t *testing.T) {
	_, rt := newFakeProfileServer(t)
	rt.Endpoints.EventProcessingGraphQL = ""

	spec := widgetlabProfileSpec(t)
	spec.DetectionRules = append([]DetectionRuleSpec(nil), spec.DetectionRules...)
	for i := range spec.DetectionRules {
		spec.DetectionRules[i].Enabled = false
	}
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("a profile whose rules are all disabled was refused: %v", err)
	}
}

// 🔴 THE STALE-MARKER GATE. The publish decision reads the ACTIVE version's label,
// not the set of every label ever published. Matching the whole history means a
// marker sitting in a SUPERSEDED version answers "already published" forever: a
// console user edits the draft and republishes, the new active version does not carry
// the scenario's rule, and every later bootstrap skips the publish and reports
// success over content the platform is not serving.
func TestEnsureProfileRepublishesWhenSomethingElseSupersededIt(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)

	if err := run(t, rt, spec); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := f.versionCount(); got != 1 {
		t.Fatalf("published %d times on a fresh profile, want 1", got)
	}

	// Somebody else publishes over it — the sim's label is now history.
	f.mu.Lock()
	f.versions = append(f.versions, "edited-in-the-console")
	f.mu.Unlock()

	if err := run(t, rt, spec); err != nil {
		t.Fatalf("run after being superseded: %v", err)
	}
	if got := f.versionCount(); got != 3 {
		t.Errorf("published %d times total, want 3 — the scenario's content is no longer the "+
			"active version, so it must be republished rather than assumed present", got)
	}
	// And the active version must now be the scenario's own again.
	f.mu.Lock()
	active := f.versions[len(f.versions)-1]
	f.mu.Unlock()
	if !strings.HasPrefix(active, "sim-") {
		t.Errorf("the active version's label is %q, not the scenario's content marker", active)
	}
}

// 🔴🔴 THE STALE-PROJECTION GATE — the one a token-only check could not see.
//
// ruleHealth answers from event-processing's OWN projection, written asynchronously.
// For a window after a publish it still serves the PREVIOUS version, whose rows carry
// the SAME rule tokens (tokens are stable authoring ids that survive every definition
// change), all ACTIVE. A check matching on token alone therefore passes on its FIRST
// read and confirms a version the engine has not loaded.
//
// That is not a corner case. It is "ship a corrected rule and re-bootstrap" — exactly
// the flow the reconciler exists for, and the one where a false green costs most:
// the corrected rule is the one most likely to be broken.
func TestEnsureProfileDoesNotAcceptThePreviousVersionsRules(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)

	// v1: healthy, and live.
	if err := run(t, rt, spec); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// A corrected definition — same rule TOKEN, new content, so a republish. The
	// engine lags, then loads it broken.
	changed := spec
	changed.DetectionRules = append([]DetectionRuleSpec(nil), spec.DetectionRules...)
	changed.DetectionRules[0].Definition = `{"name":"corrected","when":{"metric":"temperature"}}`
	f.mu.Lock()
	f.settleAfterPolls = 3
	f.breakRuleAtLoad = changed.DetectionRules[0].Token
	f.mu.Unlock()

	err := run(t, rt, changed)
	if err == nil {
		t.Fatal("bootstrap reported success while event-processing was still serving the " +
			"PREVIOUS version's rules — the corrected rule is broken and nothing noticed")
	}
	if !strings.Contains(err.Error(), "COMPILE_ERROR") {
		t.Errorf("error %q is not the engine's verdict on the new version; the check may have "+
			"timed out on the stale rows instead of ever reading the new ones", err)
	}
}

// The counterweight, and the reason the version check must not simply fail on lag:
// once the engine catches up, the same run must succeed. Without this, tolerating lag
// and refusing it are indistinguishable.
func TestEnsureProfileWaitsForTheEngineToCatchUp(t *testing.T) {
	f, rt := newFakeProfileServer(t)
	spec := widgetlabProfileSpec(t)

	f.mu.Lock()
	f.settleAfterPolls = 3
	f.mu.Unlock()

	if err := run(t, rt, spec); err != nil {
		t.Fatalf("bootstrap failed though the engine caught up within the settle window: %v", err)
	}
	f.mu.Lock()
	polls := f.ruleHealthPolls
	live := f.liveVersion
	f.mu.Unlock()
	if polls <= 1 {
		t.Errorf("ruleHealth was polled %d time(s); the settle loop never retried, so the "+
			"asynchronous seam this test exists for was not exercised", polls)
	}
	if live == 0 {
		t.Error("the fake never settled, so the success above is not the case under test")
	}
}

// A single 5xx must not fail the bootstrap. event-processing gates its data plane on
// an auth-readiness check and is restarted by ordinary rollouts, so a transient error
// is the same asynchronous seam the loop already absorbs for a lagging projection —
// failing on it would make bootstrap flaky for a reason that resolves itself.
func TestEnsureProfileRetriesATransientRuleHealthFailure(t *testing.T) {
	f, rt := newFakeProfileServer(t)

	f.mu.Lock()
	f.unhealthyForNPolls = 3
	f.mu.Unlock()

	if err := run(t, rt, widgetlabProfileSpec(t)); err != nil {
		t.Fatalf("a transient ruleHealth failure failed the bootstrap: %v", err)
	}
}

// And the counterweight to THAT: an endpoint that never recovers must still fail,
// with a message naming the likely causes rather than convicting one of them.
func TestEnsureProfileFailsWhenRuleHealthNeverRecovers(t *testing.T) {
	f, rt := newFakeProfileServer(t)

	f.mu.Lock()
	f.unhealthyForNPolls = 1 << 30
	f.mu.Unlock()

	err := run(t, rt, widgetlabProfileSpec(t))
	if err == nil {
		t.Fatal("bootstrap succeeded though ruleHealth never answered")
	}
	if !strings.Contains(err.Error(), "could not be reached") {
		t.Errorf("error %q does not report that event-processing was unreachable", err)
	}
	// The most likely cause on a chart profile without event-processing has to be in
	// the message, or the failure reads as a bug in the sim.
	if !strings.Contains(err.Error(), "not deployed") {
		t.Errorf("error %q does not mention that the instance may not deploy event-processing, "+
			"so an operator on a telemetry/ingest-only profile has nothing to act on", err)
	}
}

// ruleIdVersion is what makes the version check possible, so its failure modes are
// pinned directly: an id it cannot parse must never read as a match.
func TestRuleIdVersionParsesOnlyTheMintedShape(t *testing.T) {
	if v, ok := ruleIdVersion("acme/wl-sensor-profile@7/wl-over-temp", "wl-sensor-profile"); !ok || v != 7 {
		t.Errorf("ruleIdVersion of a minted id = (%d, %t), want (7, true)", v, ok)
	}
	for _, bad := range []string{
		"",
		"wl-over-temp",                          // no tenant prefix
		"acme/wl-over-temp",                     // no profile-version segment
		"acme/wl-sensor-profile/wl-over-temp",   // no @version
		"acme/wl-sensor-profile@x/wl-over-temp", // non-numeric version
		"acme/other-profile@7/wl-over-temp",     // another profile's rule
		"acme/wl-sensor-profile@/wl-over-temp",  // empty version
	} {
		if v, ok := ruleIdVersion(bad, "wl-sensor-profile"); ok {
			t.Errorf("ruleIdVersion(%q) = (%d, true); an id it cannot confirm must not read "+
				"as a match", bad, v)
		}
	}
}

// firstUnhealthyRule is the pure heart of the liveness check, and each of its verdicts
// points somewhere different — a lagging engine, a dropped rule, a rule that does not
// compile, an id the check cannot interpret. Tested directly because the fake mints
// only well-formed ids for the version under test, so the bootstrap-level tests reach
// some of these branches by accident and others not at all.
func TestFirstUnhealthyRuleVerdicts(t *testing.T) {
	const profile = "wl-sensor-profile"
	id := func(version int, rule string) string {
		return fmt.Sprintf("acme/%s@%d/%s", profile, version, rule)
	}
	want := []string{"r1", "r2"}

	cases := []struct {
		name     string
		health   []ruleHealthEntry
		contains string // "" == must be healthy
	}{
		{
			name: "every declared rule active for this version",
			health: []ruleHealthEntry{
				{RuleId: id(4, "r1"), RuleToken: "r1", Status: "ACTIVE"},
				{RuleId: id(4, "r2"), RuleToken: "r2", Status: "ACTIVE"},
			},
		},
		{
			// The stale-projection case, stated as lag rather than as absence: the
			// tokens ARE there, which is exactly why reading them as an answer is wrong.
			name: "only the previous version is live",
			health: []ruleHealthEntry{
				{RuleId: id(3, "r1"), RuleToken: "r1", Status: "ACTIVE"},
				{RuleId: id(3, "r2"), RuleToken: "r2", Status: "ACTIVE"},
			},
			contains: "still serving an older version",
		},
		{
			name:     "the engine knows nothing about this profile yet",
			health:   nil,
			contains: "absent",
		},
		{
			name: "a declared rule is missing from this version",
			health: []ruleHealthEntry{
				{RuleId: id(4, "r1"), RuleToken: "r1", Status: "ACTIVE"},
			},
			contains: "absent",
		},
		{
			name: "a declared rule does not compile",
			health: []ruleHealthEntry{
				{RuleId: id(4, "r1"), RuleToken: "r1", Status: "ACTIVE"},
				{RuleId: id(4, "r2"), RuleToken: "r2", Status: "COMPILE_ERROR", Message: "cost ceiling"},
			},
			contains: "COMPILE_ERROR",
		},
		{
			// 🔴 An id the check cannot interpret must never read as agreement. Without
			// the explicit rejection this degrades into "still serving an older
			// version", which sends the reader to wait for a projection that is
			// already current.
			name: "an id that does not carry a version",
			health: []ruleHealthEntry{
				{RuleId: "not-a-rule-id", RuleToken: "r1", Status: "ACTIVE"},
			},
			contains: "does not carry a",
		},
		{
			name: "an id belonging to a different profile",
			health: []ruleHealthEntry{
				{RuleId: "acme/other-profile@4/r1", RuleToken: "r1", Status: "ACTIVE"},
			},
			contains: "does not carry a",
		},
		{
			// A rule this scenario did not declare is not this scenario's problem: a
			// tenant may author its own on the same profile, and refusing to bootstrap
			// over one would make the sim hostile to the instance it runs on.
			name: "an extra rule nobody declared is ignored",
			health: []ruleHealthEntry{
				{RuleId: id(4, "r1"), RuleToken: "r1", Status: "ACTIVE"},
				{RuleId: id(4, "r2"), RuleToken: "r2", Status: "ACTIVE"},
				{RuleId: id(4, "console-authored"), RuleToken: "console-authored", Status: "ACTIVE"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstUnhealthyRule(want, tc.health, profile, 4)
			if tc.contains == "" {
				if got != "" {
					t.Errorf("reported %q, want healthy", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("reported healthy, want a verdict containing %q", tc.contains)
			}
			if !strings.Contains(got, tc.contains) {
				t.Errorf("verdict %q does not contain %q — the message points at the wrong cause",
					got, tc.contains)
			}
		})
	}
}

// The enabled-rules fail-fast: a handshake with no event-processing endpoint cannot
// confirm a rule whatever the cluster is running, so Provision decides it BEFORE
// touching the network. verifyDetectionRulesAreLive makes the same check, but only
// after a customer, its areas, its assets and a profile version already exist — the
// error is the same, the mess left behind is not.
//
// The server fails the test on any request, which is what makes this about the ORDER
// rather than merely about the error text.
func TestProvisionRefusesEnabledRulesWithNoEventProcessingEndpointBeforeAnyCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("Provision made a network call before deciding it could not verify its rules")
	}))
	t.Cleanup(srv.Close)

	manifest := NewBuildingpulse(1, Load{}).Manifest()
	if manifest.EnabledDetectionRuleCount() == 0 {
		t.Fatal("buildingpulse declares no enabled rule, so this test proves nothing")
	}
	rt := &Runtime{
		Endpoints: Endpoints{
			DeviceMgmtGraphQL:    srv.URL,
			DashboardMgmtGraphQL: srv.URL,
			// EventProcessingGraphQL deliberately absent — the condition under test.
		},
		InstanceId: "dc",
		Tenant:     "acme",
		HTTPClient: srv.Client(),
	}

	err := Provision(context.Background(), rt, manifest)
	if err == nil {
		t.Fatal("Provision succeeded with no way to confirm the rules it published are running")
	}
	if !strings.Contains(err.Error(), "eventProcessingGraphQL") {
		t.Errorf("error does not name the missing endpoint, so a reader cannot act on it: %v", err)
	}
	if len(rt.Devices) != 0 {
		t.Errorf("Provision populated %d devices before refusing", len(rt.Devices))
	}
}

// The counterweight: the check must not fire for a scenario with no enabled rules, or
// devicepulse would stop working on any instance without event-processing.
func TestProvisionDoesNotRequireEventProcessingWithoutEnabledRules(t *testing.T) {
	manifest := NewDevicepulse(1, Load{}).Manifest()
	if got := manifest.EnabledDetectionRuleCount(); got != 0 {
		t.Fatalf("devicepulse now declares %d enabled rule(s); this test needs a rule-less "+
			"scenario to say anything", got)
	}
	_, rt := newFakeProfileServer(t)
	rt.Endpoints.EventProcessingGraphQL = ""

	if err := Provision(context.Background(), rt, manifest); err != nil &&
		strings.Contains(err.Error(), "eventProcessingGraphQL") {
		t.Fatalf("a scenario with no enabled rules was refused for a missing rule endpoint: %v", err)
	}
}

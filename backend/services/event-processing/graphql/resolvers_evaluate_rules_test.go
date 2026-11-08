// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// evalQuery selects every field of the result, so a field the schema or a resolver drops fails here.
const evalQuery = `query($input: EvaluateRulesInput!) {
  evaluateRules(input: $input) {
    ok
    refusal
    evaluatedRules
    degraded
    degradations { reason count }
    skippedRules { rule reason group message }
    diagnostics { severity message nodeId code }
    stats { eventsFed eventsDropped evalErrors lateReadings rulesEvaluated edgeCount intentCount wallMs }
    edges { occurredAt series rule kind severity signal value }
    intents { occurredAt series rule signal kind token alarmKey severity value command payload expiresAt connector url method body timeoutMs }
  }
}`

type evalWire struct {
	Ok             bool     `json:"ok"`
	Refusal        *string  `json:"refusal"`
	EvaluatedRules []string `json:"evaluatedRules"`
	Degraded       *string  `json:"degraded"`
	Degradations   []struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	} `json:"degradations"`
	SkippedRules []struct {
		Rule    string  `json:"rule"`
		Reason  string  `json:"reason"`
		Group   *string `json:"group"`
		Message string  `json:"message"`
	} `json:"skippedRules"`
	Diagnostics []struct {
		Severity string  `json:"severity"`
		Message  string  `json:"message"`
		NodeID   *string `json:"nodeId"`
		Code     *string `json:"code"`
	} `json:"diagnostics"`
	Stats struct {
		EventsFed, EventsDropped, EvalErrors, LateReadings, RulesEvaluated, EdgeCount, IntentCount, WallMs int
	} `json:"stats"`
	Edges []struct {
		OccurredAt, Series, Rule, Kind, Signal string
		Severity                               *string
		Value                                  *float64
	} `json:"edges"`
	Intents []struct {
		OccurredAt, Series, Rule, Signal, Kind, Token string
		AlarmKey, Severity, Command, Payload          *string
		ExpiresAt, Connector, URL, Method, Body       *string
		Value                                         *float64
		TimeoutMs                                     *int
	} `json:"intents"`
}

func evalSchema(t *testing.T, r *SchemaResolver) *gql.Schema {
	t.Helper()
	s, err := gql.ParseSchema(SchemaContent, r)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return s
}

// exec runs evaluateRules through the served schema, exactly as a client does, so variable
// coercion and every resolver are in the path.
func evExec(t *testing.T, s *gql.Schema, ctx context.Context, input map[string]any) (evalWire, []string) {
	t.Helper()
	resp := s.Exec(ctx, evalQuery, "", map[string]any{"input": input})
	if len(resp.Errors) > 0 {
		var msgs []string
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return evalWire{}, msgs
	}
	var out struct {
		EvaluateRules evalWire `json:"evaluateRules"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode %s: %v", resp.Data, err)
	}
	return out.EvaluateRules, nil
}

func evEvent(dev string, at time.Time, metric string, v float64) map[string]any {
	return map[string]any{"deviceToken": dev, "occurredAt": at.UTC().Format(time.RFC3339Nano),
		"measurements": []any{map[string]any{"name": metric, "value": v}}}
}

var evT0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

const hotDefinition = `{"name":"hot","type":"threshold","severity":"critical",
 "when":{"metric":"temperature","op":"gt","threshold":80},
 "actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"overheat"}},
            {"type":"sendCommand","sendCommand":{"command":"set-fan","payload":"{\"speed\":3}","expiresAfter":"15m"}}]}`

func evDraft(def string, events ...map[string]any) map[string]any {
	evs := make([]any, len(events))
	for i, e := range events {
		evs[i] = e
	}
	return map[string]any{"profileToken": "boiler", "ruleSet": "DRAFT", "ruleDefinition": def, "events": evs}
}

func TestEvaluateDraftThroughTheServedSchema(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition,
		evEvent("d1", evT0, "temperature", 70),
		evEvent("d1", evT0.Add(time.Minute), "temperature", 90),
		evEvent("d1", evT0.Add(2*time.Minute), "temperature", 60),
	))
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if !got.Ok || got.Refusal != nil || got.Degraded != nil || len(got.Degradations) != 0 || len(got.SkippedRules) != 0 ||
		len(got.EvaluatedRules) != 1 || got.EvaluatedRules[0] != "draft" {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Edges) != 2 || got.Edges[0].Signal != "RAISED" || got.Edges[0].Rule != "draft" || got.Edges[0].Series != "d1" ||
		got.Edges[0].Kind != "threshold" || *got.Edges[0].Severity != "critical" || *got.Edges[0].Value != 90 ||
		got.Edges[0].OccurredAt != "2026-03-01T00:01:00Z" || got.Edges[1].Signal != "RESOLVED" || got.Edges[1].Value != nil {
		t.Fatalf("edges = %+v", got.Edges)
	}
	if len(got.Intents) != 3 {
		t.Fatalf("intents = %+v, want raise, command, clear", got.Intents)
	}
	ra, cmd, clr := got.Intents[0], got.Intents[1], got.Intents[2]
	if ra.Kind != "RAISE_ALARM" || *ra.AlarmKey != "overheat" || *ra.Severity != "critical" || *ra.Value != 90 || ra.Signal != "RAISED" {
		t.Fatalf("raise = %+v", ra)
	}
	if cmd.Kind != "SEND_COMMAND" || *cmd.Command != "set-fan" || *cmd.Payload != `{"speed":3}` ||
		*cmd.ExpiresAt != "2026-03-01T00:16:00Z" || cmd.Body != nil || cmd.TimeoutMs != nil {
		t.Fatalf("command = %+v", cmd)
	}
	if clr.Kind != "CLEAR_ALARM" || clr.Signal != "RESOLVED" || *clr.AlarmKey != "overheat" || clr.Token == ra.Token {
		t.Fatalf("clear = %+v", clr)
	}
	if got.Stats.EventsFed != 3 || got.Stats.EdgeCount != 2 || got.Stats.IntentCount != 3 || got.Stats.RulesEvaluated != 1 {
		t.Fatalf("stats = %+v", got.Stats)
	}
}

func TestEvaluateDraftGraph(t *testing.T) {
	// A canvas with one source, one condition and one raise-alarm action.
	canvas := `{"schemaVersion":1,
		"nodes":[
			{"id":"s","type":"source","config":{"scope":{"kind":"profile","profileToken":"boiler"}}},
			{"id":"hotcond","type":"threshold","config":{"name":"hot","severity":"critical","when":{"metric":"temperature","op":"gt","threshold":{"kind":"literal","value":80}}}},
			{"id":"a","type":"action","config":{"action":"raiseAlarm","alarmKey":"overheat"}}
		],
		"edges":[{"from":"s:out","to":"hotcond:in"},{"from":"hotcond:signal","to":"a:in"}]}`
	s := evalSchema(t, &SchemaResolver{})
	in := evDraft("", evEvent("d1", evT0, "temperature", 95))
	delete(in, "ruleDefinition")
	in["graph"] = canvas
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), in)
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if !got.Ok {
		t.Fatalf("the canvas was rejected: %+v", got.Diagnostics)
	}
	if len(got.Edges) != 1 || got.Edges[0].Rule != "hotcond" || len(got.Intents) != 1 || got.Intents[0].Kind != "RAISE_ALARM" {
		t.Fatalf("edges=%+v intents=%+v, want the condition node named as the rule", got.Edges, got.Intents)
	}
}

func TestEvaluateRefusals(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	ev := evEvent("d1", evT0, "temperature", 90)
	cases := map[string]struct {
		mut    func(map[string]any)
		want   string
		reason string
	}{
		"no profile":        {func(m map[string]any) { m["profileToken"] = "" }, "profileToken is required", "INVALID_INPUT"},
		"no draft":          {func(m map[string]any) { delete(m, "ruleDefinition") }, "exactly one of graph and ruleDefinition", "RULE_REJECTED"},
		"both drafts":       {func(m map[string]any) { m["graph"] = "{}" }, "exactly one of graph and ruleDefinition", "RULE_REJECTED"},
		"bad definition":    {func(m map[string]any) { m["ruleDefinition"] = "{" }, "unexpected", "RULE_REJECTED"},
		"uncompilable rule": {func(m map[string]any) { m["ruleDefinition"] = `{"name":"x","type":"threshold","when":{"metric":"t"}}` }, `rule "draft"`, "RULE_REJECTED"},
		"bad time": {func(m map[string]any) {
			m["events"] = []any{evEvent("d", evT0, "t", 1), map[string]any{"deviceToken": "d", "occurredAt": "yesterday", "measurements": []any{map[string]any{"name": "t", "value": 1}}}}
		}, "events[1]: invalid occurredAt", "INVALID_INPUT"},
		"bad advance":    {func(m map[string]any) { m["advanceTo"] = "soon" }, "invalid advanceTo", "INVALID_INPUT"},
		"advance before": {func(m map[string]any) { m["advanceTo"] = evT0.Add(-time.Hour).Format(time.RFC3339) }, "before the last event", "INVALID_INPUT"},
		"no readings": {func(m map[string]any) {
			m["events"] = []any{map[string]any{"deviceToken": "d", "occurredAt": evT0.Format(time.RFC3339), "measurements": []any{}}}
		}, "no readings", "INVALID_INPUT"},
		"no events":       {func(m map[string]any) { m["events"] = []any{} }, "no events were sent", "INVALID_INPUT"},
		"event limit":     {func(m map[string]any) { m["limits"] = map[string]any{"maxEvents": 1}; m["events"] = []any{ev, ev} }, "above the limit of 1", "LIMIT_EXCEEDED"},
		"limit ceiling":   {func(m map[string]any) { m["limits"] = map[string]any{"maxEvents": 999999999} }, "above the maximum", "LIMIT_EXCEEDED"},
		"edge ceiling":    {func(m map[string]any) { m["limits"] = map[string]any{"maxEdges": 999999999} }, "edge limit", "LIMIT_EXCEEDED"},
		"negative limit":  {func(m map[string]any) { m["limits"] = map[string]any{"maxIntents": -1} }, "must not be negative", "INVALID_INPUT"},
		"published+draft": {func(m map[string]any) { m["ruleSet"] = "PUBLISHED" }, "are for ruleSet DRAFT", "INVALID_INPUT"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := evDraft(hotDefinition, ev)
			c.mut(in)
			got, errs := evExec(t, s, previewCtx(auth.DeviceRead), in)
			if errs != nil {
				t.Fatalf("errors: %v", errs)
			}
			var msgs []string
			for _, d := range got.Diagnostics {
				msgs = append(msgs, d.Message)
			}
			if got.Ok || !strings.Contains(strings.Join(msgs, "|"), c.want) {
				t.Fatalf("ok=%v diagnostics=%v, want a rejection containing %q", got.Ok, msgs, c.want)
			}
			if got.Refusal == nil || *got.Refusal != c.reason {
				t.Fatalf("refusal = %v, want %s", got.Refusal, c.reason)
			}
			if d := got.Diagnostics[0]; d.Code == nil || *d.Code != c.reason {
				t.Fatalf("diagnostic code = %v, want %s", d.Code, c.reason)
			}
			if len(got.Edges) != 0 || len(got.Intents) != 0 {
				t.Fatalf("a rejected request must derive nothing: %+v", got)
			}
		})
	}
}

func TestEvaluateRuleSetEnumIsEnforcedBySchema(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	in := evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90))
	in["ruleSet"] = "LIVE"
	if _, errs := evExec(t, s, previewCtx(auth.DeviceRead), in); len(errs) == 0 {
		t.Fatal("an unknown ruleSet must be a GraphQL error")
	}
}

func TestEvaluateAuthorizationAndTenant(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	in := evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90))
	if _, errs := evExec(t, s, previewCtx(), in); len(errs) == 0 {
		t.Fatal("evaluateRules without device:read must be refused")
	}
	noTenant := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.DeviceRead)}})
	if _, errs := evExec(t, s, noTenant, in); len(errs) == 0 {
		t.Fatal("evaluateRules with no tenant must be refused")
	}
}

func evStores(t *testing.T) (*model.DetectRuleStore, *model.ProfileActiveStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.DetectRule{}, &model.ProfileActive{}); err != nil {
		t.Fatal(err)
	}
	m := &rdb.RdbManager{Database: db}
	return model.NewDetectRuleStore(m), model.NewProfileActiveStore(m)
}

func evPublish(t *testing.T, rs *model.DetectRuleStore, ps *model.ProfileActiveStore, tenant, profile, version string, rows ...model.DetectRule) {
	t.Helper()
	ctx := dccore.WithTenant(context.Background(), tenant)
	if err := ps.Upsert(ctx, &model.ProfileActive{Tenant: tenant, ProfileToken: profile,
		ActiveVersionToken: profile + "@" + version, PublishedAt: evT0}); err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		rows[i].Tenant, rows[i].ProfileVersionToken = tenant, profile+"@"+version
		rows[i].RuleId = fmt.Sprintf("%s/%s@%s/%s", tenant, profile, version, rows[i].RuleToken)
	}
	if err := rs.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluatePublishedRules(t *testing.T) {
	rs, ps := evStores(t)
	evPublish(t, rs, ps, "acme", "boiler", "3",
		model.DetectRule{RuleToken: "hot", Definition: hotDefinition},
		model.DetectRule{RuleToken: "grouped", Definition: hotDefinition, EntityGroupToken: "north", EntityGroupVersion: 2},
	)
	// Another tenant's profile of the same name, with a rule that would fire on the same events.
	evPublish(t, rs, ps, "other", "boiler", "9", model.DetectRule{RuleToken: "theirs", Definition: hotDefinition})
	s := evalSchema(t, &SchemaResolver{DetectRules: rs, Profiles: ps})

	in := map[string]any{"profileToken": "boiler", "ruleSet": "PUBLISHED",
		"events": []any{evEvent("d1", evT0, "temperature", 90)}}
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), in)
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if !got.Ok || len(got.Edges) != 1 || got.Edges[0].Rule != "hot" {
		t.Fatalf("edges = %+v, want the tenant's own rule only", got.Edges)
	}
	if len(got.SkippedRules) != 1 || got.SkippedRules[0].Rule != "grouped" || got.SkippedRules[0].Reason != "GROUP_SCOPED" ||
		got.SkippedRules[0].Group == nil || *got.SkippedRules[0].Group != "north@2" ||
		!strings.HasPrefix(got.SkippedRules[0].Message, "grouped: scoped to the group north@2") {
		t.Fatalf("skippedRules = %+v", got.SkippedRules)
	}
	if len(got.EvaluatedRules) != 1 || got.EvaluatedRules[0] != "hot" {
		t.Fatalf("evaluatedRules = %v, want the one rule that ran", got.EvaluatedRules)
	}
	if got.Stats.RulesEvaluated != 1 {
		t.Fatalf("stats = %+v", got.Stats)
	}

	// An unpublished profile is a rejection that says how to proceed.
	in["profileToken"] = "ghost"
	got, _ = evExec(t, s, previewCtx(auth.DeviceRead), in)
	if got.Ok || len(got.Diagnostics) != 1 || !strings.Contains(got.Diagnostics[0].Message, "has no published version") {
		t.Fatalf("unpublished: %+v", got)
	}

	// A published profile whose every rule is group-scoped runs nothing: a refusal that names the
	// skipped rules, never an empty success a neverRaised expectation would pass on.
	evPublish(t, rs, ps, "acme", "scoped", "1", model.DetectRule{RuleToken: "g", Definition: hotDefinition, EntityGroupToken: "n", EntityGroupVersion: 1})
	in["profileToken"] = "scoped"
	got, _ = evExec(t, s, previewCtx(auth.DeviceRead), in)
	if got.Ok || *got.Refusal != "ALL_RULES_SKIPPED" || len(got.Diagnostics) != 1 || !strings.Contains(got.Diagnostics[0].Message, "every published rule") || len(got.SkippedRules) != 1 {
		t.Fatalf("all skipped: %+v", got)
	}

	// A profile with no rules at all.
	evPublish(t, rs, ps, "acme", "bare", "1")
	in["profileToken"] = "bare"
	got, _ = evExec(t, s, previewCtx(auth.DeviceRead), in)
	if got.Ok || *got.Refusal != "NO_RULES" || len(got.Diagnostics) != 1 || !strings.Contains(got.Diagnostics[0].Message, "no published rules") {
		t.Fatalf("no rules: %+v", got)
	}
}

func TestEvaluatePublishedWithoutTheProjectionIsAnError(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	in := map[string]any{"profileToken": "boiler", "ruleSet": "PUBLISHED", "events": []any{evEvent("d1", evT0, "temperature", 90)}}
	if _, errs := evExec(t, s, previewCtx(auth.DeviceRead), in); len(errs) == 0 || !strings.Contains(strings.Join(errs, "|"), "unavailable") {
		t.Fatalf("errs = %v, want it to say the projection is not configured", errs)
	}
}

// An evaluation takes one of the tenant's preview slots, and is REFUSED (ok false) when they are
// all in use: an empty success would let a neverRaised expectation pass on a run that never ran.
func TestEvaluateSharesThePreviewConcurrencyGate(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	for i := 0; i < maxConcurrentPreviewsPerTenant; i++ {
		if !globalPreviewGate.acquire("acme") {
			t.Fatal("could not take a slot")
		}
	}
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90)))
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if got.Ok || got.Refusal == nil || *got.Refusal != "BUSY" || len(got.Diagnostics) != 1 || !strings.Contains(got.Diagnostics[0].Message, "already running") || len(got.Edges) != 0 {
		t.Fatalf("got %+v, want a refusal (ok false)", got)
	}
	for i := 0; i < maxConcurrentPreviewsPerTenant; i++ {
		globalPreviewGate.release("acme")
	}
	// With the slots free it runs, and the slot it takes is given back.
	got, errs = evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90)))
	if errs != nil || len(got.Edges) != 1 {
		t.Fatalf("errs=%v got=%+v", errs, got)
	}
	globalPreviewGate.mu.Lock()
	inUse := globalPreviewGate.inUse["acme"]
	globalPreviewGate.mu.Unlock()
	if inUse != 0 {
		t.Fatalf("slots in use after the evaluation: %d", inUse)
	}
}

// A no-argument command reports the empty object it means; a connector action reports its body.
func TestEvaluateReportsEmptyPayloadAndConnectorFields(t *testing.T) {
	def := `{"name":"hot","type":"threshold","severity":"minor","when":{"metric":"temperature","op":"gt","threshold":80},
	 "actions":[{"type":"sendCommand","sendCommand":{"command":"reboot"}},
	   {"type":"httpCall","httpCall":{"url":"https://hooks.example.com/a","bodyTemplate":"series","timeoutMs":2000}},
	   {"type":"publish","publish":{"connectorRef":"plant","payloadTemplate":"\"hot\""}}]}`
	s := evalSchema(t, &SchemaResolver{})
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(def, evEvent("d7", evT0, "temperature", 90)))
	if errs != nil || !got.Ok {
		t.Fatalf("errs=%v got=%+v", errs, got)
	}
	if len(got.Intents) != 3 {
		t.Fatalf("intents = %+v", got.Intents)
	}
	if *got.Intents[0].Payload != "{}" || got.Intents[0].ExpiresAt != nil {
		t.Fatalf("command = %+v", got.Intents[0])
	}
	h := got.Intents[1]
	if *h.URL != "https://hooks.example.com/a" || *h.Method != "POST" || *h.Body != "d7" || *h.TimeoutMs != 2000 {
		t.Fatalf("httpCall = %+v", h)
	}
	p := got.Intents[2]
	if *p.Connector != "plant" || *p.Body != "hot" || *p.TimeoutMs != 0 {
		t.Fatalf("publish = %+v", p)
	}
}

// Evaluations across ALL tenants share a small process-wide bound: with it full, a tenant whose own
// preview slots are free is still refused as BUSY.
func TestEvaluateHasAProcessWideBound(t *testing.T) {
	s := evalSchema(t, &SchemaResolver{})
	for i := 0; i < maxConcurrentEvaluations; i++ {
		if !acquireEvaluationSlot() {
			t.Fatal("could not take a slot")
		}
	}
	got, errs := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90)))
	for i := 0; i < maxConcurrentEvaluations; i++ {
		releaseEvaluationSlot()
	}
	if errs != nil || got.Ok || got.Refusal == nil || *got.Refusal != "BUSY" {
		t.Fatalf("errs=%v got=%+v, want BUSY while the process-wide bound is full", errs, got)
	}
	globalPreviewGate.mu.Lock()
	inUse := globalPreviewGate.inUse["acme"]
	globalPreviewGate.mu.Unlock()
	if inUse != 0 {
		t.Fatalf("the tenant's preview slot was not given back on a process-wide refusal: %d", inUse)
	}
	if got, _ := evExec(t, s, previewCtx(auth.DeviceRead), evDraft(hotDefinition, evEvent("d1", evT0, "temperature", 90))); !got.Ok {
		t.Fatalf("got %+v once the bound is free", got)
	}
}

// The resolver hands the evaluator live's lateness: the reviewer's out-of-order reproduction
// detects through the served schema when the service is configured with live's default.
func TestEvaluateUsesTheServicesLateness(t *testing.T) {
	def := `{"name":"avg","type":"aggregate","windowMode":"tumbling","severity":"minor","metric":"temperature",
	  "agg":"avg","op":"gt","threshold":50,"window":"1m0s"}`
	in := evDraft(def,
		evEvent("d1", evT0, "temperature", 10),
		evEvent("d1", evT0.Add(62*time.Second), "temperature", 10),
		evEvent("d1", evT0.Add(59*time.Second), "temperature", 500))
	in["advanceTo"] = evT0.Add(3 * time.Minute).Format(time.RFC3339)
	got, errs := evExec(t, evalSchema(t, &SchemaResolver{EvaluationLateness: 5 * time.Second}), previewCtx(auth.DeviceRead), in)
	if errs != nil || !got.Ok || len(got.Edges) == 0 || got.Edges[0].Signal != "RAISED" {
		t.Fatalf("errs=%v got=%+v, want the late 500 used and the pane raised", errs, got)
	}
	got, _ = evExec(t, evalSchema(t, &SchemaResolver{}), previewCtx(auth.DeviceRead), in)
	if len(got.Edges) != 0 || len(got.Degradations) != 1 || got.Degradations[0].Reason != "LATE_READINGS" {
		t.Fatalf("got %+v with no lateness, want no edge and a LATE_READINGS degradation", got)
	}
}

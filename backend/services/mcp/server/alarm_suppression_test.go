// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Derive the pre-suppression schema from the actual served SDL. Removing every
// suppression type, root field and field on the existing alarm/event/search
// types keeps the compatibility fake in step with all unrelated schema changes.
func suppressionSchema(t *testing.T, old bool) *graphql.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../device-management/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	sdl := string(raw)
	if old {
		// Descriptions can precede a removed definition/field; discard descriptions
		// before removing definitions so none become attached to the next field.
		sdl = regexp.MustCompile(`(?s)""".*?"""`).ReplaceAllString(sdl, "")
		sdl = regexp.MustCompile(`(?ms)^(?:type|input|enum) AlarmSuppression\w*[^\{]*\{.*?^\}`).ReplaceAllString(sdl, "")
		sdl = regexp.MustCompile(`(?m)^\s*(?:\w*[Ss]uppression\w*|suppressedUntil)\s*(?:\([^)]*\))?\s*:\s*[\w!\[\]]+[^\S\r\n]*`).ReplaceAllString(sdl, "")
		for _, addition := range []string{"AlarmSuppression", "suppression:", "raisedDuringSuppression:", "suppressedUntil:", "suppressionWindowToken:", "alarmSuppressionsForDevice"} {
			if strings.Contains(sdl, addition) {
				t.Fatalf("old SDL still contains %s", addition)
			}
		}
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse old=%v SDL: %v", old, err)
	}
	if old {
		// This fake must actually reject the dangerous base-document addition.
		bad := `query { alarmsByToken(tokens: ["a1"]) { suppression { windowToken until } raisedDuringSuppression } }`
		if len(schema.Validate(bad)) != 2 {
			t.Fatal("old schema did not reject both new Alarm fields")
		}
	}
	return schema
}

type suppressionRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// Every incoming document is validated with the very same validator and error
// JSON encoding graph-gophers uses, including supplied variable values. No canned
// unknown-field message can accidentally accept an incompatible base document.
func suppressionUpstream(t *testing.T, old bool, bearer string, serve func(http.ResponseWriter, suppressionRequest)) *Tools {
	t.Helper()
	schema := suppressionSchema(t, old)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+bearer {
			t.Errorf("caller bearer = %q, want %q", got, "Bearer "+bearer)
		}
		var req suppressionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if errs := schema.ValidateWithVariables(req.Query, req.Variables); len(errs) != 0 {
			_ = json.NewEncoder(w).Encode(&graphql.Response{Errors: errs})
			return
		}
		serve(w, req)
	}))
	t.Cleanup(ts.Close)
	return NewTools(testClient(ts.URL))
}

func suppressionData(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func baseSuppressionData(w http.ResponseWriter, req suppressionRequest, alarms []alarmRaw) bool {
	switch operationName(document{text: req.Query}) {
	case "ListAlarms":
		suppressionData(w, map[string]any{"alarms": map[string]any{"results": alarms, "pagination": map[string]any{"totalRecords": len(alarms)}}})
	case "GetAlarm":
		suppressionData(w, map[string]any{"alarmsByToken": alarms})
	default:
		return false
	}
	return true
}

func assertAlarmOverlay(t *testing.T, alarms []AlarmSummary, unavailable bool) {
	t.Helper()
	if unavailable || len(alarms) != 3 {
		t.Fatalf("unavailable=%v alarms=%+v", unavailable, alarms)
	}
	for i, want := range []string{"w1", "restricted", ""} {
		got := alarms[i].Suppression
		if want == "" {
			if got != nil {
				t.Fatalf("unsuppressed alarm has overlay: %+v", got)
			}
		} else if got == nil || got.WindowToken != want || got.Until != "2026-11-19T20:00:00Z" {
			t.Fatalf("alarm %s suppression=%+v, want %s and deadline", alarms[i].Token, got, want)
		}
	}
}

func TestSuppressionOldServerCompatibility(t *testing.T) {
	var bases atomic.Int32
	tools := suppressionUpstream(t, true, "caller-old", func(w http.ResponseWriter, req suppressionRequest) {
		if !baseSuppressionData(w, req, []alarmRaw{{Token: "a1", State: "ACTIVE"}}) {
			t.Errorf("unexpected valid old-server query: %s", req.Query)
		}
		bases.Add(1)
	})
	_, list, err := tools.ListAlarms(context.Background(), authedReq("caller-old"), ListAlarmsInput{})
	if err != nil || len(list.Alarms) != 1 || list.TotalRecords != 1 || !list.SuppressionUnavailable {
		t.Fatalf("old list: %+v %v", list, err)
	}
	_, get, err := tools.GetAlarm(context.Background(), authedReq("caller-old"), GetAlarmInput{Tokens: []string{"a1"}})
	if err != nil || len(get.Alarms) != 1 || !get.SuppressionUnavailable {
		t.Fatalf("old get: %+v %v", get, err)
	}
	for _, out := range []any{list, get} {
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), `"suppression":`) {
			t.Fatalf("old result has suppression: %s", raw)
		}
	}
	if bases.Load() != 2 {
		t.Fatalf("base reads=%d want 2", bases.Load())
	}
	_, _, err = tools.ListSuppressionWindows(context.Background(), authedReq("caller-old"), ListSuppressionWindowsInput{})
	if err == nil || err.Error() != "suppression windows are not available on this server" {
		t.Fatalf("old windows must clearly error: %v", err)
	}

}

func TestSuppressionNewServerMergeAndCallerBearer(t *testing.T) {
	var statusCalls atomic.Int32
	tools := suppressionUpstream(t, false, "caller-new", func(w http.ResponseWriter, req suppressionRequest) {
		if baseSuppressionData(w, req, []alarmRaw{{Token: "a1"}, {Token: "a2"}, {Token: "a3"}}) {
			return
		}
		if operationName(document{text: req.Query}) != "AlarmSuppressionStatus" {
			t.Errorf("unexpected query %s", req.Query)
		}
		if strings.Contains(req.Query, "window {") {
			t.Error("expensive nested window selected")
		}
		statusCalls.Add(1)
		tokens := req.Variables["tokens"].([]any)
		if fmt.Sprint(tokens) != "[a1 a2 a3]" {
			t.Errorf("status tokens: %v", tokens)
		}
		suppressionData(w, map[string]any{"alarmSuppressionStatus": []any{
			map[string]any{"alarmToken": "a2", "suppression": AlarmSuppression{WindowToken: "restricted", Until: "2026-11-19T20:00:00Z"}},
			map[string]any{"alarmToken": "a3", "suppression": nil},
			map[string]any{"alarmToken": "a1", "suppression": AlarmSuppression{WindowToken: "w1", Until: "2026-11-19T20:00:00Z"}},
			map[string]any{"alarmToken": "unrequested", "suppression": AlarmSuppression{WindowToken: "wrong"}},
		}})
	})
	_, list, err := tools.ListAlarms(context.Background(), authedReq("caller-new"), ListAlarmsInput{})
	if err != nil {
		t.Fatal(err)
	}
	assertAlarmOverlay(t, list.Alarms, list.SuppressionUnavailable)
	_, get, err := tools.GetAlarm(context.Background(), authedReq("caller-new"), GetAlarmInput{Tokens: []string{"a1", "a2", "a3"}})
	if err != nil {
		t.Fatal(err)
	}
	assertAlarmOverlay(t, get.Alarms, get.SuppressionUnavailable)
	if statusCalls.Load() != 2 {
		t.Fatalf("status calls=%d want 2", statusCalls.Load())
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "suppressionUnavailable") {
		t.Fatalf("successful lookup marked unavailable: %s", raw)
	}
}

func TestSuppressionStatusChunksAtSDLTokenCap(t *testing.T) {
	alarms := make([]alarmRaw, 401)
	for i := range alarms {
		alarms[i].Token = fmt.Sprintf("a%d", i)
	}
	var calls atomic.Int32
	tools := suppressionUpstream(t, false, "caller-chunks", func(w http.ResponseWriter, req suppressionRequest) {
		if baseSuppressionData(w, req, alarms) {
			return
		}
		tokens := req.Variables["tokens"].([]any)
		call := int(calls.Add(1)) - 1
		want := 200
		if call == 2 {
			want = 1
		}
		if len(tokens) != want {
			t.Errorf("chunk %d size %d want %d", call, len(tokens), want)
		}
		statuses := []any{}
		for i, token := range tokens {
			if token != fmt.Sprintf("a%d", call*200+i) {
				t.Errorf("wrong chunk token: %v", token)
			}
			statuses = append(statuses, map[string]any{"alarmToken": token, "suppression": AlarmSuppression{WindowToken: "w"}})
		}
		suppressionData(w, map[string]any{"alarmSuppressionStatus": statuses})
	})
	_, out, err := tools.ListAlarms(context.Background(), authedReq("caller-chunks"), ListAlarmsInput{PageSize: 1000})
	if err != nil || out.SuppressionUnavailable || len(out.Alarms) != 401 || calls.Load() != 3 {
		t.Fatalf("chunking: alarms=%d calls=%d unavailable=%v err=%v", len(out.Alarms), calls.Load(), out.SuppressionUnavailable, err)
	}
	for _, alarm := range out.Alarms {
		if alarm.Suppression == nil {
			t.Fatalf("missing merge on %s", alarm.Token)
		}
	}
}

func TestSuppressionLookupFailuresKeepBaseAndMarkUnknown(t *testing.T) {
	for _, failure := range []string{"transport", "http500", "http401", "graphqlAuth", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			tools := suppressionUpstream(t, false, "caller-failure", func(w http.ResponseWriter, req suppressionRequest) {
				if baseSuppressionData(w, req, []alarmRaw{{Token: "a1"}}) {
					return
				}
				switch failure {
				case "transport":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case "http500":
					w.WriteHeader(500)
				case "http401":
					w.WriteHeader(401)
				case "graphqlAuth":
					_, _ = w.Write([]byte(`{"errors":[{"message":"forbidden","path":["alarmSuppressionStatus"],"extensions":{"code":"FORBIDDEN"}}],"data":null}`))
				case "malformed":
					_, _ = w.Write([]byte(`oops`))
				}
			})
			_, list, err := tools.ListAlarms(context.Background(), authedReq("caller-failure"), ListAlarmsInput{})
			if err != nil || len(list.Alarms) != 1 || !list.SuppressionUnavailable || list.Alarms[0].Suppression != nil {
				t.Fatalf("list base fallback: %+v %v", list, err)
			}
			_, get, err := tools.GetAlarm(context.Background(), authedReq("caller-failure"), GetAlarmInput{Tokens: []string{"a1"}})
			if err != nil || len(get.Alarms) != 1 || !get.SuppressionUnavailable || get.Alarms[0].Suppression != nil {
				t.Fatalf("get base fallback: %+v %v", get, err)
			}
		})
	}
}

func TestListSuppressionWindowsFiltersPagingAndCallerBearer(t *testing.T) {
	var calls atomic.Int32
	tools := suppressionUpstream(t, false, "caller-windows", func(w http.ResponseWriter, req suppressionRequest) {
		calls.Add(1)
		criteria := req.Variables["criteria"].(map[string]any)
		if criteria["pageNumber"] != float64(1) || criteria["pageSize"] != float64(100) || criteria["status"] != "ACTIVE" || criteria["targetToken"] != "d1" {
			t.Errorf("criteria=%v", criteria)
		}
		suppressionData(w, map[string]any{"alarmSuppressionWindows": map[string]any{
			"results":    []any{map[string]any{"token": "w1", "name": injection, "description": injection, "reason": injection, "kind": "RECURRING", "status": "ACTIVE", "targets": []any{map[string]any{"type": "TENANT", "token": nil}}, "alarmKeys": []string{injection}, "createdBy": injection, "bypassSeverity": "CRITICAL", "timezone": "America/New_York", "validFrom": "from", "validUntil": "until", "endedAt": "ended"}},
			"pagination": map[string]any{"totalRecords": 7},
		}})
	})
	_, out, err := tools.ListSuppressionWindows(context.Background(), authedReq("caller-windows"), ListSuppressionWindowsInput{Status: "ACTIVE", TargetToken: "d1", PageSize: 500})
	if err != nil || out.TotalRecords != 7 || len(out.Windows) != 1 {
		t.Fatalf("windows=%+v %v", out, err)
	}
	w := out.Windows[0]
	if w.Token != "w1" || w.Kind != "RECURRING" || w.Status != "ACTIVE" || len(w.Targets) != 1 || w.Targets[0].Type != "TENANT" || w.Targets[0].Token != nil || w.BypassSeverity != "CRITICAL" || w.Timezone != "America/New_York" || w.ValidFrom != "from" || w.ValidUntil != "until" || w.EndedAt != "ended" {
		t.Fatalf("window fields=%+v", w)
	}
	for _, marked := range []*Marked{w.Untrusted.Name, w.Untrusted.Description, w.Untrusted.Reason, w.Untrusted.CreatedBy, w.Untrusted.AlarmKeys[0]} {
		if marked.Value != injection || marked.Source != SourceOperator {
			t.Fatalf("operator text escaped marking: %+v", marked)
		}
	}
	if _, _, err := tools.ListSuppressionWindows(context.Background(), authedReq("caller-windows"), ListSuppressionWindowsInput{Status: "WRONG"}); err == nil {
		t.Fatal("invalid status accepted")
	}
	if _, _, err := tools.ListSuppressionWindows(context.Background(), &mcp.CallToolRequest{}, ListSuppressionWindowsInput{}); err == nil {
		t.Fatal("unauthenticated windows accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected calls=%d", calls.Load())
	}
}

func TestGraphQLValidationSignals(t *testing.T) {
	for _, tc := range []struct {
		body       string
		validation bool
	}{
		{`{"errors":[{"message":"opaque","locations":[{"line":1,"column":2}]}]}`, true},
		{`{"errors":[{"message":"opaque","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`, true},
		{`{"data":null,"errors":[{"message":"opaque","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`, true},
		{`{"data":{},"errors":[{"message":"opaque","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`, false},
		{`{"errors":[{"message":"Cannot query field"}]}`, false},
		{`{"errors":[{"message":"opaque","locations":[{"line":1,"column":2}],"extensions":{"code":"FORBIDDEN"}}]}`, false},
		{`{"errors":[{"message":"opaque","locations":[{"line":1,"column":2}],"path":["alarms"]}]}`, false},
		{`{"data":null,"errors":[{"message":"opaque","locations":[{"line":1,"column":2}]}]}`, false},
	} {
		tools, done := toolsAgainst(t, tc.body)
		err := tools.gql.Query(context.Background(), listSuppressionWindowsQuery, "caller", nil, nil)
		done()
		if err == nil || isGraphQLValidationError(err) != tc.validation {
			t.Errorf("validation=%v error=%v for %s", tc.validation, err, tc.body)
		}
	}
}

func TestSuppressionOldServerReturnsWireToolError(t *testing.T) {
	schema := suppressionSchema(t, true)
	var bearer string
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			t.Error("window query lost caller bearer")
		}
		var req suppressionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		errs := schema.ValidateWithVariables(req.Query, req.Variables)
		if len(errs) == 0 {
			t.Error("window query unexpectedly validated against old schema")
		}
		_ = json.NewEncoder(w).Encode(&graphql.Response{Errors: errs})
	}))
	defer ts.Close()
	cs, token := servedToolSession(t, ts.URL)
	bearer = token
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_suppression_windows"})
	if err != nil || result == nil || !result.IsError || !strings.Contains(contentText(result), "suppression windows are not available on this server") {
		t.Fatalf("old-server tool error: %+v %v", result, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}

func TestSuppressionWindowsDiscoveryAndAudit(t *testing.T) {
	up, url := newRecordingUpstream(t, `{"data":{"alarmSuppressionWindows":{"results":[],"pagination":{"totalRecords":0}}}}`)
	cs, bearer := servedToolSession(t, url)
	listing, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *mcp.Tool
	for _, tool := range listing.Tools {
		if tool.Name == "list_suppression_windows" {
			found = tool
		}
	}
	if found == nil || found.Annotations == nil || !found.Annotations.ReadOnlyHint || found.Meta[riskMetaKey] == nil {
		t.Fatalf("missing read-only/risk metadata: %+v", found)
	}
	buf := captureLog(t)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_suppression_windows"})
	mustSucceed(t, result, err)
	if up.hits != 1 || up.auth != "Bearer "+bearer {
		t.Fatalf("caller bearer was not forwarded: %+v", up)
	}
	if line := onlyAuditLine(t, buf); line["tool"] != "list_suppression_windows" || line["outcome"] != "ok" {
		t.Fatalf("audit=%v", line)
	}
}

func TestSuppressionWindowsOmittedFiltersAndOneShot(t *testing.T) {
	tools, captured, done := toolsCapturing(t, `{"data":{"alarmSuppressionWindows":{"results":[{"token":"w1","kind":"ONE_SHOT","startTime":"start","endTime":"end"}],"pagination":{"totalRecords":1}}}}`)
	defer done()
	_, out, err := tools.ListSuppressionWindows(context.Background(), authedReq("caller"), ListSuppressionWindowsInput{})
	if err != nil || len(out.Windows) != 1 || out.Windows[0].StartTime != "start" || out.Windows[0].EndTime != "end" {
		t.Fatalf("one shot=%+v %v", out, err)
	}
	criteria := (*captured)["variables"].(map[string]any)["criteria"].(map[string]any)
	if len(criteria) != 2 || criteria["pageNumber"] != float64(1) || criteria["pageSize"] != float64(25) {
		t.Fatalf("omitted criteria=%v", criteria)
	}
}

func TestSuppressionFailedLaterChunkDoesNotReturnPartialOverlay(t *testing.T) {
	alarms := make([]AlarmSummary, 201)
	for i := range alarms {
		alarms[i].Token = fmt.Sprintf("a%d", i)
	}
	var calls atomic.Int32
	tools := suppressionUpstream(t, false, "caller", func(w http.ResponseWriter, req suppressionRequest) {
		if calls.Add(1) == 2 {
			w.WriteHeader(500)
			return
		}
		suppressionData(w, map[string]any{"alarmSuppressionStatus": []any{map[string]any{"alarmToken": "a0", "suppression": AlarmSuppression{WindowToken: "w"}}}})
	})
	if !tools.enrichAlarmSuppression(context.Background(), "caller", alarms) {
		t.Fatal("failed chunk was not marked unavailable")
	}
	for _, a := range alarms {
		if a.Suppression != nil {
			t.Fatal("partially successful enrichment leaked through")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

// The status query omits an alarm it cannot see or no longer knows. That alarm's suppression is
// unknown, so the result must say so instead of reporting it as not suppressed.
func TestSuppressionMissingStatusRowMarksUnknown(t *testing.T) {
	tools := suppressionUpstream(t, false, "caller-missing", func(w http.ResponseWriter, req suppressionRequest) {
		if baseSuppressionData(w, req, []alarmRaw{{Token: "a1"}, {Token: "a2"}}) {
			return
		}
		suppressionData(w, map[string]any{"alarmSuppressionStatus": []any{
			map[string]any{"alarmToken": "a1", "suppression": AlarmSuppression{WindowToken: "w1", Until: "2026-11-19T20:00:00Z"}},
		}})
	})
	_, list, err := tools.ListAlarms(context.Background(), authedReq("caller-missing"), ListAlarmsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !list.SuppressionUnavailable {
		t.Fatal("a missing status row must mark suppression unknown")
	}
	for _, a := range list.Alarms {
		if a.Suppression != nil {
			t.Fatalf("partial overlay on %s", a.Token)
		}
	}
}

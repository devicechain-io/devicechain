// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func requireDiagnostic(t *testing.T, err error, parts ...string) string {
	t.Helper()
	if err == nil {
		t.Fatal("diagnostics changed a failing verdict to passing")
	}
	text := err.Error()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Errorf("diagnostic lacks %q: %s", part, text)
		}
	}
	return text
}

func TestAlarmDiagnosticUsesFinalGraphQLBracket(t *testing.T) {
	reads := 0
	row := S7Alarm{Token: "alarm-1", OriginatorID: "id-1", OriginatorToken: "gauge", AlarmKey: "flood", State: "ACTIVE", Severity: "CRITICAL"}
	r := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
		reads++
		rows := []S7Alarm{row}
		if reads == 1 {
			rows = nil // First bracket moves; only the second may decide.
		}
		return map[string]any{"alarms": map[string]any{"results": rows, "pagination": map[string]any{"totalRecords": len(rows)}}}, 0
	})
	r.endpoints.DeviceMgmtGraphQL = r.endpoints.UpdateMgmtGraphQL
	m := s7TestMCP(t, func(string) map[string]any {
		wrong := row
		wrong.Severity = "MAJOR"
		extra := row
		extra.Token = "alarm-2"
		return map[string]any{"alarms": []S7Alarm{wrong, extra}, "totalRecords": 2}
	})
	pair, _, _, err := s7AlarmComparison(context.Background(), r, m)
	if err != nil || reads != 4 {
		t.Fatalf("bracketing changed: reads=%d err=%v", reads, err)
	}
	for _, check := range []func(Observations) error{tenantIsolation, mcpFleetQA} {
		s := cleanS7()
		s.IsolationReads["list_alarms"], s.QAReads["list_alarms"] = pair, pair
		requireDiagnostic(t, check(Observations{S7: s}), "list_alarms answer differs from tenant GraphQL truth", "MCP=2 GraphQL=1", "GraphQL_before=1 GraphQL_after=1", `token="alarm-1"`, `severity GraphQL="CRITICAL" MCP="MAJOR"`)
	}
}

func TestAlarmDiagnosticBoundsAndStructuralDifferences(t *testing.T) {
	var expected, actual []S7Alarm
	for i := 0; i < 8; i++ {
		expected = append(expected, S7Alarm{Token: fmt.Sprintf("alarm-%d", i), AlarmKey: "flood", State: "ACTIVE", Severity: "CRITICAL", OriginatorID: "own-id"})
		wrong := expected[i]
		wrong.AlarmKey, wrong.State, wrong.Severity, wrong.OriginatorID = strings.Repeat("x", 10000), "CLEARED", "MAJOR", "other-id"
		actual = append(actual, wrong)
	}
	pair := S7Comparison{Expected: s7JSON(expected), Actual: s7JSON(actual)}
	text := requireDiagnostic(t, s7Compare(map[string]S7Comparison{"list_alarms": pair}, []string{"list_alarms"}), "MCP=8 GraphQL=8", "differing_alarms=8 shown=5", "alarmKey", "state", "severity", "originatorId", "...")
	if strings.Count(text, "token=") != 5 || strings.Contains(text, "alarm-5") || strings.Contains(text, strings.Repeat("x", 49)) || len(text) > 6000 {
		t.Fatalf("alarm diff is not bounded: length=%d", len(text))
	}
	for _, pair := range []S7Comparison{
		{Expected: s7JSON(expected[:1]), Actual: s7JSON([]S7Alarm{})},
		{Expected: s7JSON([]S7Alarm{}), Actual: s7JSON(expected[:1])},
	} {
		requireDiagnostic(t, s7Compare(map[string]S7Comparison{"list_alarms": pair}, []string{"list_alarms"}), `token="alarm-0"`, "<missing>")
	}
	// Preserve the existing equality test even for order, null and extra fields.
	for _, pair := range []S7Comparison{
		{Expected: s7JSON(expected[:2]), Actual: s7JSON([]S7Alarm{expected[1], expected[0]})},
		{Expected: json.RawMessage(`null`), Actual: json.RawMessage(`null`)},
		{Expected: json.RawMessage(`[{"token":"a"}]`), Actual: json.RawMessage(`[{"token":"a","extra":"different"}]`)},
	} {
		if s7Compare(map[string]S7Comparison{"list_alarms": pair}, []string{"list_alarms"}) == nil {
			t.Fatal("diagnostic normalization changed comparison semantics")
		}
	}
}

func TestSamplingDiagnosticReadsCreatedAndUpdatedWithoutChangingVerdict(t *testing.T) {
	r := s5ReaderServer(t, func(doc string, vars map[string]json.RawMessage) (any, int) {
		if !strings.Contains(doc, "createdAt updatedAt") {
			t.Error("command read omits diagnostic timestamps")
		}
		return map[string]any{"commands": map[string]any{"results": []any{map[string]any{
			"deviceToken": sim.HarborpulseSamplingGauge, "name": "other-command", "status": "QUEUED",
			"queuedTime": at(16.6).Format(time.RFC3339Nano), "createdAt": at(16.6).Format(time.RFC3339Nano), "updatedAt": at(16.7).Format(time.RFC3339Nano),
		}}, "pagination": map[string]any{"totalRecords": 1}}}, 0
	})
	r.endpoints.CommandMgmtGraphQL = r.endpoints.UpdateMgmtGraphQL
	rows, err := r.Commands(context.Background(), sim.HarborpulseSamplingGauge)
	if err != nil {
		t.Fatal(err)
	}
	o := cleanDay().observe()
	o.Commands = rows
	requireDiagnostic(t, downlinkAcknowledged(o), "sampling command: want exactly one SUCCESSFUL acknowledged command", "device_rows_read=1 window_rows=1 matched_commands=0", "status=QUEUED", "createdAt="+at(16.6).Format(time.RFC3339Nano), "updatedAt="+at(16.7).Format(time.RFC3339Nano), "window=["+at(16.5).Format(time.RFC3339Nano)+","+at(17).Format(time.RFC3339Nano)+"]")
}

func TestSamplingDiagnosticBoundsAndSelection(t *testing.T) {
	o := cleanDay().observe()
	o.Commands = nil
	requireDiagnostic(t, downlinkAcknowledged(o), "device_rows_read=0 window_rows=0 matched_commands=0", "rows=[]")
	for i := 0; i < 20; i++ {
		o.Commands = append(o.Commands, Command{Device: sim.HarborpulseSamplingGauge, Name: sim.HarborpulseSamplingCommand, Status: "FAILED", Queued: at(16.5), Responded: at(16.6)})
	}
	// Other devices and times cannot change the matching predicate.
	o.Commands = append(o.Commands, Command{Device: "other", Status: "QUEUED", Queued: at(16.5)}, Command{Device: sim.HarborpulseSamplingGauge, Status: "TIMEOUT", Queued: at(17).Add(time.Nanosecond)})
	text := requireDiagnostic(t, downlinkAcknowledged(o), "device_rows_read=21 window_rows=20 matched_commands=20", "omitted=16")
	if strings.Count(text, "status=") != 5 || len(text) > 3000 {
		t.Fatalf("command diagnostic is not bounded: length=%d", len(text))
	}
}

func TestDiagnosticsExcludeCredentials(t *testing.T) {
	secrets := map[string]string{
		"accessToken": "bearer-canary-DO-NOT-PRINT", "authorizationCode": "code-canary-DO-NOT-PRINT",
		"code_verifier": "pkce-canary-DO-NOT-PRINT", "clientSecret": "client-canary-DO-NOT-PRINT",
		"operatorCredential": "operator-canary-DO-NOT-PRINT",
	}
	row := map[string]any{"token": "alarm-safe", "alarmKey": "flood", "severity": "MAJOR", "state": "ACTIVE", "originatorId": "id-safe"}
	for k, v := range secrets {
		row[k] = v
	}
	row["metadata"], row["description"], row["payload"] = secrets, secrets, secrets
	row["Authorization"] = "Bearer " + secrets["accessToken"]
	for _, raw := range []json.RawMessage{s7JSON([]any{row}), s7JSON(secrets), s7JSON([]any{secrets}), s7JSON([]any{strings.Repeat(secrets["accessToken"], 500)})} {
		for _, pair := range []S7Comparison{{Expected: s7JSON([]any{}), Actual: raw}, {Expected: raw, Actual: s7JSON([]any{})}} {
			text := requireDiagnostic(t, s7Compare(map[string]S7Comparison{"list_alarms": pair}, []string{"list_alarms"}))
			for _, secret := range secrets {
				if strings.Contains(text, secret) {
					t.Fatal("alarm diagnostics disclosed a credential")
				}
			}
		}
	}
	for _, secret := range secrets {
		o := cleanDay().observe()
		o.Commands = nil
		raw := s7JSON([]any{map[string]any{"device": sim.HarborpulseSamplingGauge, "name": secret, "status": secret, "queued": at(16.6), "createdAt": secret, "updatedAt": secret, "payload": secrets}})
		if err := json.Unmarshal(raw, &o.Commands); err != nil {
			t.Fatal(err)
		}
		text := requireDiagnostic(t, downlinkAcknowledged(o), "status=<unknown>", "createdAt=<invalid>", "updatedAt=<invalid>")
		if strings.Contains(text, secret) {
			t.Fatal("sampling diagnostics disclosed a credential")
		}
	}
}

func TestDiagnosticTimestampsCannotAffectPassingInvariant(t *testing.T) {
	o := cleanDay().observe()
	for i := range o.Commands {
		o.Commands[i].CreatedAt, o.Commands[i].UpdatedAt = "invalid-diagnostic-only", "invalid-diagnostic-only"
	}
	if err := downlinkAcknowledged(o); err != nil {
		t.Fatalf("diagnostic metadata changed the verdict: %v", err)
	}
}

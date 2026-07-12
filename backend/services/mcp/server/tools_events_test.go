// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureClient records the request body of the single downstream call so a test
// can assert what criteria were sent, and returns a canned body.
func toolsCapturing(t *testing.T, respBody string) (*Tools, *map[string]any, func()) {
	t.Helper()
	captured := map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		_, _ = w.Write([]byte(respBody))
	}))
	return NewTools(testClient(ts.URL)), &captured, ts.Close
}

func TestQueryMeasurements(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"measurementEvents":{"results":[{"deviceToken":"d1","name":"temp","value":20.0,"occurredTime":"2026-07-12T00:00:00Z"}],"pagination":{"totalRecords":1}}}}`)
	defer done()
	_, out, err := tools.QueryMeasurements(context.Background(), authedReq("tok"), QueryMeasurementsInput{DeviceToken: "d1"})
	if err != nil {
		t.Fatalf("QueryMeasurements: %v", err)
	}
	if len(out.Measurements) != 1 || out.TotalRecords != 1 || out.Measurements[0].Name != "temp" {
		t.Errorf("unexpected: %+v", out)
	}

	if _, _, err := tools.QueryMeasurements(context.Background(), authedReq("tok"), QueryMeasurementsInput{}); err == nil {
		t.Errorf("missing deviceToken should error")
	}
}

func TestAggregateMeasurements(t *testing.T) {
	tools, captured, done := toolsCapturing(t, `{"data":{"bucketedMeasurements":[{"bucketStart":"2026-07-12T00:00:00Z","name":"temp","avg":20.5,"count":6}]}}`)
	defer done()
	_, out, err := tools.AggregateMeasurements(context.Background(), authedReq("tok"),
		AggregateMeasurementsInput{DeviceToken: "d1", Name: "temp", IntervalSeconds: 3600})
	if err != nil {
		t.Fatalf("AggregateMeasurements: %v", err)
	}
	if len(out.Buckets) != 1 || out.Buckets[0].Avg == nil || *out.Buckets[0].Avg != 20.5 || out.Buckets[0].Count != 6 {
		t.Errorf("unexpected buckets: %+v", out.Buckets)
	}
	// The interval + name criteria are forwarded.
	crit := (*captured)["variables"].(map[string]any)["criteria"].(map[string]any)
	if crit["intervalSeconds"].(float64) != 3600 || crit["name"] != "temp" {
		t.Errorf("criteria not forwarded: %v", crit)
	}

	// A non-positive interval is rejected before any call.
	if _, _, err := tools.AggregateMeasurements(context.Background(), authedReq("tok"),
		AggregateMeasurementsInput{DeviceToken: "d1"}); err == nil {
		t.Errorf("zero intervalSeconds should error")
	}
}

func TestListAlarms_FilterMapping(t *testing.T) {
	// ACTIVE is the real alarm state (not RAISED — that's an event type).
	tools, captured, done := toolsCapturing(t, `{"data":{"alarms":{"results":[{"token":"a1","alarmKey":"overheat","state":"ACTIVE","severity":"CRITICAL","acknowledged":false}],"pagination":{"totalRecords":1}}}}`)
	defer done()
	ack := true
	_, out, err := tools.ListAlarms(context.Background(), authedReq("tok"),
		ListAlarmsInput{Originator: "d1", State: "ACTIVE", Acknowledged: &ack})
	if err != nil {
		t.Fatalf("ListAlarms: %v", err)
	}
	if len(out.Alarms) != 1 || out.Alarms[0].AlarmKey != "overheat" || out.Alarms[0].Severity != "CRITICAL" {
		t.Errorf("unexpected alarms: %+v", out.Alarms)
	}
	crit := (*captured)["variables"].(map[string]any)["criteria"].(map[string]any)
	// An originator device token expands to originatorType=device + originator.
	if crit["originatorType"] != "device" || crit["originator"] != "d1" || crit["state"] != "ACTIVE" || crit["acknowledged"] != true {
		t.Errorf("alarm criteria not mapped: %v", crit)
	}
}

// An unset optional filter must be ABSENT from the criteria, not sent as "" — a
// blank filter would match nothing and silently empty every result. This pins the
// putIfSet behavior so a regression to always-send fails a test.
func TestListAlarms_NoFiltersOmitsOptionalCriteria(t *testing.T) {
	tools, captured, done := toolsCapturing(t, `{"data":{"alarms":{"results":[],"pagination":{"totalRecords":0}}}}`)
	defer done()
	if _, _, err := tools.ListAlarms(context.Background(), authedReq("tok"), ListAlarmsInput{}); err != nil {
		t.Fatalf("ListAlarms: %v", err)
	}
	crit := (*captured)["variables"].(map[string]any)["criteria"].(map[string]any)
	for _, k := range []string{"state", "severity", "alarmKey", "originator", "originatorType", "acknowledged"} {
		if _, present := crit[k]; present {
			t.Errorf("unset filter %q must be absent from criteria, got %v", k, crit[k])
		}
	}
	// Only the required pagination fields are sent.
	if _, ok := crit["pageNumber"]; !ok {
		t.Errorf("pageNumber must always be sent")
	}
}

func TestGetAlarm(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"alarmsByToken":[{"token":"a1","alarmKey":"overheat","state":"CLEARED","severity":"CRITICAL","acknowledged":true}]}}`)
	defer done()
	_, out, err := tools.GetAlarm(context.Background(), authedReq("tok"), GetAlarmInput{Tokens: []string{"a1"}})
	if err != nil {
		t.Fatalf("GetAlarm: %v", err)
	}
	if len(out.Alarms) != 1 || out.Alarms[0].Token != "a1" {
		t.Errorf("unexpected: %+v", out.Alarms)
	}
	if _, _, err := tools.GetAlarm(context.Background(), authedReq("tok"), GetAlarmInput{}); err == nil {
		t.Errorf("empty tokens should error")
	}
}

func TestListCommands(t *testing.T) {
	tools, captured, done := toolsCapturing(t, `{"data":{"commands":{"results":[{"token":"c1","deviceToken":"d1","name":"reboot","status":"SENT"}],"pagination":{"totalRecords":1}}}}`)
	defer done()
	_, out, err := tools.ListCommands(context.Background(), authedReq("tok"), ListCommandsInput{DeviceToken: "d1", Status: "SENT"})
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if len(out.Commands) != 1 || out.Commands[0].Name != "reboot" || out.Commands[0].Status != "SENT" {
		t.Errorf("unexpected commands: %+v", out.Commands)
	}
	crit := (*captured)["variables"].(map[string]any)["criteria"].(map[string]any)
	if crit["deviceToken"] != "d1" || crit["status"] != "SENT" {
		t.Errorf("command criteria not forwarded: %v", crit)
	}
}

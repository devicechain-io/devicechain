// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

func namedEvent() *dmmodel.AlarmStateChangeEvent {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	return &dmmodel.AlarmStateChangeEvent{
		EventType: dmmodel.AlarmEventRaised, AlarmToken: "alarm-1", AlarmKey: "over-temp",
		OriginatorType: "device", OriginatorId: 42, State: "ACTIVE", Severity: "CRITICAL",
		RaisedTime: at, OccurredTime: at,
		DeviceToken: "boiler-7", DeviceName: "Boiler 7", DeviceTypeToken: "boiler",
	}
}

// A person reads a name, not a numeric id. The structured payload carries all three device
// fields beside the id, and the human summary leads with the name.
func TestRenderNamesTheDevice(t *testing.T) {
	r := renderNotification(namedEvent())
	if !strings.Contains(r.Subject, "on Boiler 7") || strings.Contains(r.Subject, "device 42") {
		t.Fatalf("subject should name the device, not its id: %q", r.Subject)
	}
	for k, want := range map[string]string{"deviceToken": "boiler-7", "deviceName": "Boiler 7", "deviceTypeToken": "boiler"} {
		if r.Payload[k] != want {
			t.Fatalf("payload %s = %v, want %q", k, r.Payload[k], want)
		}
	}
	if r.Payload["originatorId"] != uint(42) {
		t.Fatalf("the id stays in the payload for anything keyed on it: %v", r.Payload["originatorId"])
	}
	if r.Alarm.Originator != "Boiler 7" || r.Alarm.DeviceToken != "boiler-7" || r.Alarm.DeviceTypeToken != "boiler" {
		t.Fatalf("facts do not carry the device: %+v", r.Alarm)
	}
}

// An event from a producer that predates the fields (a rolling upgrade has both running) names
// nothing: the keys are ABSENT, not empty strings, and the summary falls back to the id.
func TestRenderOmitsWhatTheEventDidNotName(t *testing.T) {
	e := namedEvent()
	e.DeviceToken, e.DeviceName, e.DeviceTypeToken = "", "", ""
	r := renderNotification(e)
	for _, k := range []string{"deviceToken", "deviceName", "deviceTypeToken"} {
		if _, ok := r.Payload[k]; ok {
			t.Fatalf("payload carries %s though the event did not name it", k)
		}
	}
	if !strings.Contains(r.Subject, "device 42") {
		t.Fatalf("the summary should fall back to the id: %q", r.Subject)
	}
	// A device with a token but no name shows the token.
	e.DeviceToken = "boiler-7"
	if r := renderNotification(e); !strings.Contains(r.Subject, "on boiler-7") || r.Payload["deviceName"] != nil {
		t.Fatalf("unnamed device: subject %q, payload %v", r.Subject, r.Payload)
	}
}

// An event serialised by an older pod (a held quiet-hours deferral is stored as JSON) decodes
// with empty names and renders unnamed.
func TestAnEventWithoutNamesDecodesAndRenders(t *testing.T) {
	var e dmmodel.AlarmStateChangeEvent
	if err := json.Unmarshal([]byte(`{"EventType":"RAISED","AlarmToken":"a","OriginatorType":"device","OriginatorId":9,"Severity":"MAJOR"}`), &e); err != nil {
		t.Fatal(err)
	}
	if r := renderNotification(&e); !strings.Contains(r.Subject, "device 9") {
		t.Fatalf("subject = %q", r.Subject)
	}
}

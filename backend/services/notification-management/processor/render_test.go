// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

func floatPtr(v float64) *float64 { return &v }
func strPtr(s string) *string     { return &s }

func TestRenderNotificationRaised(t *testing.T) {
	raised := time.Date(2026, 7, 6, 11, 58, 0, 0, time.UTC)
	occurred := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	event := &dmmodel.AlarmStateChangeEvent{
		EventType:      dmmodel.AlarmEventRaised,
		AlarmToken:     "alarm-1",
		OriginatorType: "device",
		OriginatorId:   42,
		AlarmKey:       "temperature.high",
		MetricKey:      "temperature",
		State:          "ACTIVE",
		Severity:       "CRITICAL",
		LastValue:      floatPtr(42.5),
		Message:        strPtr("Temperature above threshold"),
		RaisedTime:     raised,
		OccurredTime:   occurred,
	}

	r := renderNotification(event)

	if !strings.Contains(r.Subject, "[CRITICAL]") || !strings.Contains(r.Subject, "temperature.high") ||
		!strings.Contains(r.Subject, "device 42") {
		t.Fatalf("subject missing pieces: %q", r.Subject)
	}
	for _, want := range []string{"CRITICAL", "ACTIVE", "temperature", "42.5", "Temperature above threshold"} {
		if !strings.Contains(r.TextBody, want) {
			t.Fatalf("body missing %q:\n%s", want, r.TextBody)
		}
	}
	// Payload is Slack-friendly (text) and structured.
	if r.Payload["text"] != r.Subject {
		t.Fatalf("payload text != subject")
	}
	if r.Payload["eventType"] != "RAISED" || r.Payload["severity"] != "CRITICAL" {
		t.Fatalf("payload wrong: %+v", r.Payload)
	}
	if r.Payload["value"] != 42.5 {
		t.Fatalf("payload value = %v", r.Payload["value"])
	}
	if r.Payload["occurredTime"] != "2026-07-06T12:00:00Z" {
		t.Fatalf("payload occurredTime = %v", r.Payload["occurredTime"])
	}
}

// Optional fields absent → no stray keys / lines.
func TestRenderNotificationMinimal(t *testing.T) {
	event := &dmmodel.AlarmStateChangeEvent{
		EventType:        dmmodel.AlarmEventEscalated,
		AlarmToken:       "alarm-2",
		OriginatorType:   "device",
		OriginatorId:     7,
		AlarmKey:         "battery.low",
		Severity:         "MAJOR",
		State:            "ACTIVE",
		PreviousSeverity: "MINOR",
	}
	r := renderNotification(event)
	if _, ok := r.Payload["value"]; ok {
		t.Fatalf("value should be absent")
	}
	if _, ok := r.Payload["message"]; ok {
		t.Fatalf("message should be absent")
	}
	if r.Payload["previousSeverity"] != "MINOR" {
		t.Fatalf("previousSeverity missing")
	}
	if !strings.Contains(r.Subject, "escalated") {
		t.Fatalf("subject verb wrong: %q", r.Subject)
	}
}

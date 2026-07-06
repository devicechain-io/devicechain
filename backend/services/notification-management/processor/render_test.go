// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
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

// The escalation rendering is built from the persisted state (no original event) and
// carries the identity + progress a human needs to recognize the still-open alarm.
func TestRenderEscalation(t *testing.T) {
	first := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	last := time.Date(2026, 7, 6, 12, 5, 0, 0, time.UTC)
	state := &model.NotificationState{
		AlarmToken:      "alarm-9",
		AlarmKey:        "temperature.high",
		Severity:        "CRITICAL",
		NotifyCount:     2,
		FirstNotifiedAt: sql.NullTime{Time: first, Valid: true},
		LastNotifiedAt:  sql.NullTime{Time: last, Valid: true},
	}

	r := renderEscalation(state, 3)

	if !strings.Contains(r.Subject, "[CRITICAL]") || !strings.Contains(r.Subject, "temperature.high") ||
		!strings.Contains(r.Subject, "escalation 3") {
		t.Fatalf("subject missing pieces: %q", r.Subject)
	}
	for _, want := range []string{"CRITICAL", "temperature.high", "2 notification"} {
		if !strings.Contains(r.TextBody, want) {
			t.Fatalf("body missing %q:\n%s", want, r.TextBody)
		}
	}
	if r.Payload["text"] != r.Subject {
		t.Fatalf("payload text != subject")
	}
	if r.Payload["eventType"] != "RENOTIFIED" || r.Payload["escalation"] != 3 {
		t.Fatalf("payload wrong: %+v", r.Payload)
	}
	if r.Payload["lastNotifiedTime"] != "2026-07-06T12:05:00Z" {
		t.Fatalf("payload lastNotifiedTime = %v", r.Payload["lastNotifiedTime"])
	}
}

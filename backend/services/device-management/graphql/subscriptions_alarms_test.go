// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
)

func strp(s string) *string { return &s }
func uintp(u uint) *uint    { return &u }

// alarmEventFilter.matches ANDs every set predicate and ignores unset ones; the
// originator is matched by the pre-resolved id, not the token.
func TestAlarmEventFilterMatches(t *testing.T) {
	ev := &model.AlarmStateChangeEvent{
		OriginatorType: "device",
		OriginatorId:   7,
		AlarmKey:       "over-temp",
		State:          string(model.AlarmStateActive),
		Severity:       string(model.AlarmSeverityCritical),
	}
	cases := []struct {
		name   string
		filter alarmEventFilter
		want   bool
	}{
		{"empty filter matches all", alarmEventFilter{}, true},
		{"matching originator id", alarmEventFilter{originatorId: uintp(7)}, true},
		{"other originator id", alarmEventFilter{originatorId: uintp(8)}, false},
		{"matching type", alarmEventFilter{originatorType: strp("device")}, true},
		{"other type", alarmEventFilter{originatorType: strp("area")}, false},
		{"matching state", alarmEventFilter{state: strp("ACTIVE")}, true},
		{"other state", alarmEventFilter{state: strp("CLEARED")}, false},
		{"matching severity", alarmEventFilter{severity: strp("CRITICAL")}, true},
		{"other severity", alarmEventFilter{severity: strp("MAJOR")}, false},
		{"matching key", alarmEventFilter{alarmKey: strp("over-temp")}, true},
		{"other key", alarmEventFilter{alarmKey: strp("low-batt")}, false},
		{"all matching ANDed", alarmEventFilter{
			originatorType: strp("device"), originatorId: uintp(7),
			state: strp("ACTIVE"), severity: strp("CRITICAL"), alarmKey: strp("over-temp"),
		}, true},
		{"one mismatch fails the AND", alarmEventFilter{
			originatorId: uintp(7), severity: strp("MAJOR"),
		}, false},
	}
	for _, c := range cases {
		if got := c.filter.matches(ev); got != c.want {
			t.Errorf("%s: matches = %v, want %v", c.name, got, c.want)
		}
	}
}

// The AlarmEvent resolver maps the envelope's scalars straight through, formats times
// with the API's RFC3339 convention, and renders an absent previous severity /
// nullable field as null.
func TestAlarmEventResolverFields(t *testing.T) {
	raised := time.Date(2026, 7, 4, 10, 30, 0, 0, time.UTC)
	occurred := time.Date(2026, 7, 4, 10, 42, 17, 0, time.UTC)
	by := "op@example.com"
	last := 123.5
	msg := "too hot"
	r := &AlarmEventResolver{E: &model.AlarmStateChangeEvent{
		EventType:        model.AlarmEventEscalated,
		AlarmToken:       "alarm-tok",
		OriginatorType:   "device",
		OriginatorId:     7,
		AlarmKey:         "over-temp",
		MetricKey:        "temp",
		State:            string(model.AlarmStateActive),
		Severity:         string(model.AlarmSeverityCritical),
		PreviousSeverity: string(model.AlarmSeverityMajor),
		Acknowledged:     true,
		AcknowledgedBy:   &by,
		LastValue:        &last,
		Message:          &msg,
		RaisedTime:       raised,
		OccurredTime:     occurred,
	}}

	if r.EventType() != "ESCALATED" || r.AlarmToken() != "alarm-tok" ||
		r.OriginatorType() != "device" || string(r.OriginatorId()) != "7" ||
		r.AlarmKey() != "over-temp" || r.MetricKey() != "temp" ||
		r.State() != "ACTIVE" || r.Severity() != "CRITICAL" || !r.Acknowledged() {
		t.Fatalf("unexpected scalar mapping: %+v", r)
	}
	if r.PreviousSeverity() == nil || *r.PreviousSeverity() != "MAJOR" {
		t.Errorf("previousSeverity = %v, want MAJOR", r.PreviousSeverity())
	}
	if r.OccurredTime() != "2026-07-04T10:42:17Z" {
		t.Errorf("occurredTime = %q", r.OccurredTime())
	}
	if r.RaisedTime() == nil || *r.RaisedTime() != "2026-07-04T10:30:00Z" {
		t.Errorf("raisedTime = %v", r.RaisedTime())
	}
	if r.AcknowledgedBy() == nil || *r.AcknowledgedBy() != by ||
		r.LastValue() == nil || *r.LastValue() != last ||
		r.Message() == nil || *r.Message() != msg {
		t.Errorf("nullable passthrough mismatch: %+v", r)
	}

	// A first-raise / auto-clear event: no previous severity, no ack identity.
	bare := &AlarmEventResolver{E: &model.AlarmStateChangeEvent{
		EventType:    model.AlarmEventRaised,
		OccurredTime: occurred,
	}}
	if bare.PreviousSeverity() != nil || bare.AcknowledgedBy() != nil ||
		bare.LastValue() != nil || bare.Message() != nil || bare.RaisedTime() != nil {
		t.Errorf("expected nils on a bare event, got %+v", bare)
	}
}

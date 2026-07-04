// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// AlarmEventType.Valid accepts exactly the five known transition kinds.
func TestAlarmEventTypeValid(t *testing.T) {
	valid := []AlarmEventType{
		AlarmEventRaised, AlarmEventEscalated, AlarmEventDeescalated,
		AlarmEventCleared, AlarmEventAcknowledged,
	}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("expected %q to be valid", v)
		}
	}
	for _, bad := range []AlarmEventType{"", "RESOLVED", "raised", "UNKNOWN"} {
		if bad.Valid() {
			t.Errorf("expected %q to be invalid", bad)
		}
	}
}

// severityTransition classifies an in-place severity move as an escalation (toward a
// more-severe / lower-rank level) or de-escalation, and reports no change when the
// severity is unchanged or unknown (so a value-only update emits nothing).
func TestSeverityTransition(t *testing.T) {
	cases := []struct {
		prev, next  string
		wantType    AlarmEventType
		wantChanged bool
	}{
		// MINOR (rank 2) -> CRITICAL (rank 0): more severe -> escalated.
		{string(AlarmSeverityMinor), string(AlarmSeverityCritical), AlarmEventEscalated, true},
		{string(AlarmSeverityWarning), string(AlarmSeverityMajor), AlarmEventEscalated, true},
		// CRITICAL (rank 0) -> MAJOR (rank 1): less severe -> de-escalated.
		{string(AlarmSeverityCritical), string(AlarmSeverityMajor), AlarmEventDeescalated, true},
		{string(AlarmSeverityMajor), string(AlarmSeverityWarning), AlarmEventDeescalated, true},
		// Unchanged -> no event.
		{string(AlarmSeverityMajor), string(AlarmSeverityMajor), "", false},
		// Unknown severity on either side -> no classifiable change.
		{"BOGUS", string(AlarmSeverityMajor), "", false},
		{string(AlarmSeverityMajor), "BOGUS", "", false},
	}
	for _, c := range cases {
		gotType, gotChanged := severityTransition(c.prev, c.next)
		if gotType != c.wantType || gotChanged != c.wantChanged {
			t.Errorf("severityTransition(%q,%q) = (%q,%v), want (%q,%v)",
				c.prev, c.next, gotType, gotChanged, c.wantType, c.wantChanged)
		}
	}
}

// newAlarmStateChangeEvent copies the alarm's post-transition snapshot, mapping the
// nullable columns to pointers so a subscriber can tell "absent" from a zero value,
// and carries the transition kind and prior severity through unchanged.
func TestNewAlarmStateChangeEvent(t *testing.T) {
	a := &Alarm{
		TokenReference: rdb.TokenReference{Token: "alarm-tok"},
		OriginatorType: "device",
		OriginatorId:   7,
		AlarmKey:       "over-temp",
		MetricKey:      "temp",
		State:          string(AlarmStateActive),
		Severity:       string(AlarmSeverityCritical),
		Acknowledged:   true,
		AcknowledgedBy: sql.NullString{String: "op@example.com", Valid: true},
		LastValue:      sql.NullFloat64{Float64: 123.5, Valid: true},
		Message:        sql.NullString{String: "too hot", Valid: true},
	}
	ev := newAlarmStateChangeEvent(a, AlarmEventEscalated, string(AlarmSeverityMajor), a.RaisedTime)
	if ev.EventType != AlarmEventEscalated || ev.AlarmToken != "alarm-tok" ||
		ev.OriginatorType != "device" || ev.OriginatorId != 7 ||
		ev.AlarmKey != "over-temp" || ev.MetricKey != "temp" ||
		ev.State != string(AlarmStateActive) || ev.Severity != string(AlarmSeverityCritical) ||
		ev.PreviousSeverity != string(AlarmSeverityMajor) || !ev.Acknowledged {
		t.Fatalf("unexpected scalar mapping: %+v", ev)
	}
	if ev.AcknowledgedBy == nil || *ev.AcknowledgedBy != "op@example.com" {
		t.Errorf("acknowledgedBy = %v, want op@example.com", ev.AcknowledgedBy)
	}
	if ev.LastValue == nil || *ev.LastValue != 123.5 {
		t.Errorf("lastValue = %v, want 123.5", ev.LastValue)
	}
	if ev.Message == nil || *ev.Message != "too hot" {
		t.Errorf("message = %v, want 'too hot'", ev.Message)
	}

	// A cleared, never-acknowledged alarm leaves the nullable pointers nil.
	b := &Alarm{
		TokenReference: rdb.TokenReference{Token: "b"},
		State:          string(AlarmStateCleared),
		Severity:       string(AlarmSeverityWarning),
	}
	evb := newAlarmStateChangeEvent(b, AlarmEventCleared, "", b.ClearedTime.Time)
	if evb.AcknowledgedBy != nil || evb.LastValue != nil || evb.Message != nil {
		t.Errorf("expected nil nullable pointers, got %+v", evb)
	}
	if evb.PreviousSeverity != "" {
		t.Errorf("expected empty previousSeverity, got %q", evb.PreviousSeverity)
	}
}

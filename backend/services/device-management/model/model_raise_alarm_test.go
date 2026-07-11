// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"
)

// TestRaiseAlarmRequestRoundTrip proves a raise-alarm request marshals and unmarshals faithfully.
func TestRaiseAlarmRequestRoundTrip(t *testing.T) {
	req := &RaiseAlarmRequest{
		DeviceToken: "device-1", AlarmKey: "acme/p@1/r1", MetricKey: "temperature",
		Severity: "critical", Value: 91.5, OccurredTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
	b, err := MarshalRaiseAlarmRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalRaiseAlarmRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *req {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", *got, *req)
	}
}

// TestRaiseAlarmRejectsInvalidInput proves the exported wrapper fails closed on an empty alarm key
// or an out-of-set severity BEFORE touching the store (so these paths need no DB).
func TestRaiseAlarmRejectsInvalidInput(t *testing.T) {
	api := &Api{} // no RDB: the validation returns before any DB access
	at := time.Now()
	if err := api.RaiseAlarm(context.Background(), 1, "", "m", "CRITICAL", 0, at); err == nil {
		t.Fatal("empty alarm key must be rejected")
	}
	if err := api.RaiseAlarm(context.Background(), 1, "key", "m", "catastrophic", 0, at); err == nil {
		t.Fatal("unknown severity must be rejected")
	}
	if err := api.RaiseAlarm(context.Background(), 1, "key", "m", "critical", 0, at); err == nil {
		t.Fatal("a lowercase severity is not a valid AlarmSeverity and must be rejected (the consumer uppercases)")
	}
}

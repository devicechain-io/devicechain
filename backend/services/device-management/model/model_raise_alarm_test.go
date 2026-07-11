// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"
)

func fptr(v float64) *float64 { return &v }

// TestRaiseAlarmRequestRoundTrip proves a raise-alarm request marshals and unmarshals faithfully,
// including a present value and a NULL (nil) value (a silence-driven fire) — the pointer must
// survive the round-trip as nil, not decode back to a 0.
func TestRaiseAlarmRequestRoundTrip(t *testing.T) {
	occurred := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	// A value-bearing request.
	req := &RaiseAlarmRequest{
		DeviceToken: "device-1", AlarmKey: "acme/p@1/r1", MetricKey: "temperature",
		Severity: "critical", Value: fptr(91.5), OccurredTime: occurred,
	}
	b, err := MarshalRaiseAlarmRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalRaiseAlarmRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceToken != req.DeviceToken || got.AlarmKey != req.AlarmKey || got.MetricKey != req.MetricKey ||
		got.Severity != req.Severity || !got.OccurredTime.Equal(req.OccurredTime) || got.Value == nil || *got.Value != 91.5 {
		t.Fatalf("round-trip mismatch:\n got=%+v (value=%v)\nwant=%+v", *got, got.Value, *req)
	}

	// A value-less request: nil must survive as nil (not a decoded 0), so the alarm's last value
	// stays NULL downstream.
	nilReq := &RaiseAlarmRequest{DeviceToken: "d", AlarmKey: "k", Severity: "critical", OccurredTime: occurred}
	nb, err := MarshalRaiseAlarmRequest(nilReq)
	if err != nil {
		t.Fatal(err)
	}
	ngot, err := UnmarshalRaiseAlarmRequest(nb)
	if err != nil {
		t.Fatal(err)
	}
	if ngot.Value != nil {
		t.Fatalf("a value-less request must round-trip with a nil value; got %v", *ngot.Value)
	}
}

// TestRaiseAlarmRejectsInvalidInput proves the exported wrapper fails closed on an empty alarm key
// or an out-of-set severity BEFORE touching the store (so these paths need no DB).
func TestRaiseAlarmRejectsInvalidInput(t *testing.T) {
	api := &Api{} // no RDB: the validation returns before any DB access
	at := time.Now()
	if err := api.RaiseAlarm(context.Background(), 1, "", "m", "CRITICAL", nil, at); err == nil {
		t.Fatal("empty alarm key must be rejected")
	}
	if err := api.RaiseAlarm(context.Background(), 1, "key", "m", "catastrophic", nil, at); err == nil {
		t.Fatal("unknown severity must be rejected")
	}
	if err := api.RaiseAlarm(context.Background(), 1, "key", "m", "critical", nil, at); err == nil {
		t.Fatal("a lowercase severity is not a valid AlarmSeverity and must be rejected (the consumer uppercases)")
	}
}

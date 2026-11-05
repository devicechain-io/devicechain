// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventtype

import "testing"

// The numbers are wire values: a type is only ever appended. IntegrationUplink is 7.
func TestWireValuesAndNamesAreStable(t *testing.T) {
	want := map[EventType]string{
		NewRelationship: "NewRelationship", Location: "Location", Measurement: "Measurement", Alert: "Alert",
		StateChange: "StateChange", CommandInvocation: "CommandInvocation", CommandResponse: "CommandResponse",
		IntegrationUplink: "IntegrationUplink",
	}
	for i, name := range []string{"NewRelationship", "Location", "Measurement", "Alert", "StateChange",
		"CommandInvocation", "CommandResponse", "IntegrationUplink"} {
		if got := EventType(i).String(); got != name {
			t.Errorf("EventType(%d) is %q, want %q", i, got, name)
		}
	}
	for typ, name := range want {
		if typ.String() != name {
			t.Errorf("%d is %q, want %q", int64(typ), typ.String(), name)
		}
	}
	if IntegrationUplink != 7 {
		t.Errorf("IntegrationUplink is %d, want 7", int64(IntegrationUplink))
	}
	if got := EventType(8).String(); got != "EventType(8)" {
		t.Errorf("the first undeclared value must stringify out of range, got %q", got)
	}
}

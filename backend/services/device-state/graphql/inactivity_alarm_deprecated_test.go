// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
)

// The deprecated inactivityAlarmTime serves the stored column exactly as v1.0 did: the instant the
// sweep marked an INACTIVE device, null otherwise. A disconnect report alone (last_disconnect_time
// with no stored alarm time) answers null; v1.1 answered the disconnect time.
func TestTheDeprecatedInactivityAlarmTimeKeepsItsV10Behaviour(t *testing.T) {
	swept := sql.NullTime{Time: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Valid: true}
	other := sql.NullTime{Time: time.Date(2026, 9, 1, 12, 5, 0, 0, time.UTC), Valid: true}
	for _, tc := range []struct {
		name string
		row  model.DeviceState
		want *string
	}{
		{"swept and inactive", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept, InactivityAlarmTime: swept}, ptr("2026-09-01T12:00:00Z")},
		{"swept, then a later disconnect report", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: other, InactivityAlarmTime: swept}, ptr("2026-09-01T12:00:00Z")},
		{"disconnect report, never swept", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept}, nil},
		{"active again", model.DeviceState{Active: true, PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept}, nil},
		{"active with a stale stored value", model.DeviceState{Active: true, InactivityAlarmTime: swept}, nil},
		{"never disconnected", model.DeviceState{PresenceSource: model.PresenceSourceInferred}, nil},
	} {
		got := (&DeviceStateResolver{M: tc.row}).InactivityAlarmTime()
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, deref(got), deref(tc.want))
		}
	}
}

func ptr(s string) *string { return &s }
func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

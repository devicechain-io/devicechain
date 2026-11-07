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
		// A disconnect REPORT stamps presence_time with last_disconnect_time (or later): v1.0 answered null.
		{"disconnect report, never swept", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept, PresenceTime: swept}, nil},
		{"disconnect report, then demoted to INFERRED", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept, PresenceTime: other}, nil},
		// The row a v1.1 pod's sweep leaves during a rolling upgrade: last_disconnect_time stamped, the
		// column not. v1.0 answered the sweep instant, which is that last_disconnect_time.
		{"swept by a v1.1 pod", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept}, ptr("2026-09-01T12:00:00Z")},
		{"swept by a v1.1 pod after a demotion", model.DeviceState{PresenceSource: model.PresenceSourceInferred, PresenceTime: swept, LastDisconnectTime: other}, ptr("2026-09-01T12:05:00Z")},
		{"swept by a v1.1 pod, since back online", model.DeviceState{Active: true, PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept}, nil},
		{"asserted offline is no sweep", model.DeviceState{PresenceSource: model.PresenceSourceAsserted, LastDisconnectTime: swept}, nil},
		{"swept by a v1.1 pod, disconnect before the last connect", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastConnectTime: other, LastDisconnectTime: swept}, nil},
		{"active again", model.DeviceState{Active: true, PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: swept}, nil},
		{"active with a stale stored value", model.DeviceState{Active: true, InactivityAlarmTime: swept}, nil},
		{"stale value older than the last connect", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastConnectTime: other, LastDisconnectTime: other, InactivityAlarmTime: swept}, nil},
		{"value after the last connect", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastConnectTime: swept, LastDisconnectTime: other, InactivityAlarmTime: other}, ptr("2026-09-01T12:05:00Z")},
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

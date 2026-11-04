// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
)

// inactivityAlarmTime is deprecated and derived: lastDisconnectTime for an INFERRED device
// that is inactive, null for every other row.
func TestTheDeprecatedInactivityAlarmTimeIsDerived(t *testing.T) {
	at := sql.NullTime{Time: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Valid: true}
	for _, tc := range []struct {
		name string
		row  model.DeviceState
		want bool
	}{
		{"inferred and inactive", model.DeviceState{PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: at}, true},
		{"inferred and active again", model.DeviceState{Active: true, PresenceSource: model.PresenceSourceInferred, LastDisconnectTime: at}, false},
		{"asserted and disconnected", model.DeviceState{PresenceSource: model.PresenceSourceAsserted, LastDisconnectTime: at}, false},
		{"inferred, inactive, never disconnected", model.DeviceState{PresenceSource: model.PresenceSourceInferred}, false},
	} {
		got := (&DeviceStateResolver{M: tc.row}).InactivityAlarmTime()
		if (got != nil) != tc.want {
			t.Errorf("%s: got %v, want value=%v", tc.name, got, tc.want)
		}
	}
}

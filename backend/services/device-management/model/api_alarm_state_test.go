// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
)

// AlarmState.Valid accepts only the known alarm-state vocabulary (ADR-041).
func TestAlarmStateValid(t *testing.T) {
	valid := []AlarmState{AlarmStateActive, AlarmStateCleared}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("expected state %q to be valid", s)
		}
	}

	invalid := []AlarmState{"", "active", "OPEN", "ACTIVE ", "RESOLVED"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("expected state %q to be invalid", s)
		}
	}
}

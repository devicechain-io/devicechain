// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/stretchr/testify/require"
)

func TestEscalationBase(t *testing.T) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	stamp := func(offset time.Duration) sql.NullTime { return sql.NullTime{Time: at.Add(offset), Valid: true} }
	for _, tc := range []struct {
		name                        string
		base, claimed, legacy, want sql.NullTime
	}{
		{"all null", sql.NullTime{}, sql.NullTime{}, sql.NullTime{}, sql.NullTime{}},
		{"legacy fallback", sql.NullTime{}, sql.NullTime{}, stamp(0), stamp(0)},
		{"base ignores future event time", stamp(0), sql.NullTime{}, stamp(time.Hour), stamp(0)},
		{"old pod claimed later", stamp(0), stamp(time.Minute), stamp(time.Hour), stamp(time.Minute)},
		{"page after claim", stamp(time.Minute), stamp(0), stamp(time.Hour), stamp(time.Minute)},
		{"claim without base", sql.NullTime{}, stamp(0), stamp(-time.Hour), stamp(0)},
		{"equal stamps", stamp(0), stamp(0), stamp(time.Hour), stamp(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &model.NotificationState{EscalationBaseAt: tc.base, LastEscalatedAt: tc.claimed, LastNotifiedAt: tc.legacy}
			require.Equal(t, tc.want, escalationBase(state))
			p := escalatingPolicy("p", 1800, 5)
			require.Equal(t, tc.want.Valid, escalationDue(state, p, tc.want.Time.Add(30*time.Minute), 5))
			require.False(t, escalationDue(state, p, tc.want.Time.Add(29*time.Minute), 5))
		})
	}
}

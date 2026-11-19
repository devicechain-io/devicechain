// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Behavioural half of the DETECT isolation contract: identical contributor edges
// fold to identical rows and ordered events, regardless of declared maintenance.
func TestSuppressionReplayPreservesAlarmFold(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Microsecond)
	type replay struct {
		rows   []Alarm
		events []AlarmStateChangeEvent
	}
	run := func(window bool) replay {
		api, ctx, id := newSuppressionTestApi(t)
		if window {
			w := suppressionTestWindow()
			start, end := at.Add(-time.Hour), at.Add(time.Hour)
			w.StartTime, w.EndTime = &start, &end
			_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
			require.NoError(t, err)
		}
		pub := &captureAlarmPublisher{}
		api.AlarmPublisher = pub
		result := replay{}
		for i, e := range []struct{ rule, edge, severity string }{
			{"low", AlarmEdgeRaised, "MINOR"}, {"high", AlarmEdgeRaised, "CRITICAL"},
			{"high", AlarmEdgeResolved, ""}, {"low", AlarmEdgeResolved, ""}, {"low", AlarmEdgeRaised, "MAJOR"},
		} {
			require.NoError(t, api.ApplyAlarmContributorEdge(ctx, id, "hot", "temp", e.rule, e.edge, e.severity, nil, at.Add(time.Duration(i)*time.Second), new(bool)))
			row, err := api.alarmByOriginatorKey(ctx, "device", id, "hot")
			require.NoError(t, err)
			require.NotNil(t, row)
			// Store identity and wall-clock row bookkeeping differ between fresh stores.
			// All alarm semantics, including contributors/version and cycle times, remain.
			row.Model = gorm.Model{}
			row.Token = "alarm"
			result.rows = append(result.rows, *row)
		}
		require.Len(t, pub.events, 5)
		for i, emitted := range pub.events {
			event := *emitted
			if window && (i == 0 || i == 4) {
				require.NotEmpty(t, event.RaisedDuringSuppressionToken)
			}
			if window && (i == 0 || i == 1 || i == 4) {
				require.NotNil(t, event.SuppressedUntil)
			}
			event.AlarmToken = "alarm"
			event.SuppressedUntil = nil
			event.SuppressionWindowToken = ""
			event.RaisedDuringSuppressionToken = ""
			result.events = append(result.events, event)
		}
		var stamps []AlarmCycleSuppression
		require.NoError(t, api.RDB.DB(ctx).Find(&stamps).Error)
		if window {
			require.Len(t, stamps, 2)
		} else {
			require.Empty(t, stamps)
		}
		return result
	}
	plain, suppressed := run(false), run(true)
	require.Equal(t, plain.rows, suppressed.rows)
	require.Equal(t, plain.events, suppressed.events)
	require.Equal(t, []AlarmEventType{AlarmEventRaised, AlarmEventEscalated, AlarmEventDeescalated, AlarmEventCleared, AlarmEventRaised}, []AlarmEventType{plain.events[0].EventType, plain.events[1].EventType, plain.events[2].EventType, plain.events[3].EventType, plain.events[4].EventType})
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// droppedMessage is the LA-04 event as device-management publishes it for
// {"status_text":"ok","battery":50} against a profile that declares neither: battery is stored,
// status_text rides on the sample as a drop record. It is marshalled onto the wire, so the
// processor reads real bytes.
func droppedMessage(t *testing.T, device string, at time.Time, value string) messaging.Message {
	t.Helper()
	event := &resolvedevent.ResolvedEvent{
		Source: mqttTestSource, SourceDeviceToken: device, EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "battery", Value: "50"}},
			Dropped: []resolvedevent.DroppedMeasurementEntry{
				{Name: "status_text", Reason: resolvedevent.DropUndeclaredNonNumeric, Value: value}}}}},
	}
	encoded, err := resolvedevent.MarshalResolvedEvent(event)
	require.NoError(t, err)
	return messaging.Message{Subject: locationTestSubject, Value: encoded}
}

func droppedFor(t *testing.T, sp *StateProcessor, device string) []*model.DroppedMeasurement {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "tenant1")
	rows, err := sp.Api.DroppedMeasurementsByDeviceToken(ctx, device)
	require.NoError(t, err)
	return rows
}

// The per-message path: the event yields one battery reading AND one drop row
// (status_text, undeclared_non_numeric, "ok", 1).
func TestADroppedEntryReachesTheRecordOnThePerMessagePath(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	sp.mergeOne(context.Background(), droppedMessage(t, "pump-1", at, "ok"))

	battery := loadProjectedMeasurement(t, sp, ctx, "pump-1", "battery")
	assert.Equal(t, 50.0, battery.Value.Float64)
	rows := droppedFor(t, sp, "pump-1")
	require.Len(t, rows, 1)
	assert.Equal(t, "status_text", rows[0].Name)
	assert.Equal(t, resolvedevent.DropUndeclaredNonNumeric, rows[0].Reason)
	assert.Equal(t, "ok", rows[0].LastValue)
	assert.EqualValues(t, 1, rows[0].Count)
	assert.True(t, at.Equal(rows[0].LastTime))
}

// The batch path writes the same record.
func TestADroppedEntryReachesTheRecordOnTheBatchPath(t *testing.T) {
	sp := newLocationProcessor(t)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var batch []pendingMerge
	for i, v := range []string{"ok", "fault"} {
		p, ok := sp.admit(context.Background(), droppedMessage(t, "pump-2", at.Add(time.Duration(i)*time.Second), v))
		require.True(t, ok)
		batch = append(batch, p)
	}
	sp.mergeBatch(context.Background(), batch)

	rows := droppedFor(t, sp, "pump-2")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0].Count)
	assert.Equal(t, "fault", rows[0].LastValue)
}

// Negative control: an event that dropped nothing writes no record, on either path.
func TestAnEventThatDroppedNothingWritesNoRecord(t *testing.T) {
	sp := newLocationProcessor(t)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	msg := measurementMessage(t, "pump-3", at, []struct {
		at    time.Time
		name  string
		value string
	}{{at, "battery", "50"}})
	sp.mergeOne(context.Background(), msg)
	assert.Empty(t, droppedFor(t, sp, "pump-3"))

	p, ok := sp.admit(context.Background(), msg)
	require.True(t, ok)
	assert.Empty(t, p.update.Dropped)
}

// A resolved event written before the record existed decodes with no drops, and a drop
// record never reaches the latest-value projection as a reading.
func TestDroppedEntriesAreNotReadings(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p, ok := newLocationProcessor(t).admit(context.Background(), droppedMessage(t, "pump-4", at, "ok"))
	require.True(t, ok)
	require.Len(t, p.update.Measurements, 1)
	assert.Equal(t, "battery", p.update.Measurements[0].Name)
	require.Len(t, p.update.Dropped, 1)
	assert.Equal(t, "status_text", p.update.Dropped[0].Name)
}

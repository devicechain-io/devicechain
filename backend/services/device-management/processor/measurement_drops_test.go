// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testDropMetrics() *MeasurementDropMetrics {
	m := &MeasurementDropMetrics{dropped: map[string]prometheus.Counter{}, warned: newLruSet[struct{}](16)}
	for _, r := range measurementDropReasons {
		m.dropped[r] = prometheus.NewCounter(prometheus.CounterOpts{Name: "drop_" + r})
	}
	return m
}

func (m *MeasurementDropMetrics) count(reason string) float64 {
	return testutil.ToFloat64(m.dropped[reason])
}

// resolveUndeclared resolves one entry against a profile that declares nothing.
func resolveUndeclared(t *testing.T, rez *EventResolver, m map[string]string) []resolvedevent.ResolvedMeasurementEntry {
	t.Helper()
	device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "d-1"}}
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "p@1"}, nil)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	event := &esmodel.UnresolvedEvent{Source: "test", Device: "d-1", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at.Add(time.Hour),
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{Measurements: m, OccurredTime: &at}}}}
	out, err := rez.ResolveMeasurementsEventPayload(core.WithTenant(context.Background(), "acme"), device, profile, event)
	require.NoError(t, err)
	return out.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries
}

// A text value for an undeclared key is dropped on its own, and that is COUNTED: the device was
// acknowledged normally and nothing is dead-lettered, so the counter is the operator's signal.
// An undeclared "true" is text too: the 1/0 coercion applies to a DECLARED boolean only.
func TestDroppedMeasurementIsCountedAndSiblingsStored(t *testing.T) {
	drops := testDropMetrics()
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.drops = drops

	got := resolveUndeclared(t, rez, map[string]string{"status_text": "ok", "battery": "50", "flag": "true"})
	assert.Equal(t, []string{"battery"}, names(got), "only the numeric reading is stored")
	assert.EqualValues(t, 2, drops.count(dropUndeclaredNonNumeric))

	// Counted every time, even though the warning is once per device and key.
	resolveUndeclared(t, rez, map[string]string{"status_text": "ok", "battery": "49"})
	assert.EqualValues(t, 3, drops.count(dropUndeclaredNonNumeric))
	assert.EqualValues(t, 0, drops.count(dropUndeclaredUnstorable))
}

func TestUnstorableNumericIsCountedUnderItsOwnReason(t *testing.T) {
	drops := testDropMetrics()
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.drops = drops

	got := resolveUndeclared(t, rez, map[string]string{"huge": "1e30", "nan": "NaN", "ok": "1"})
	assert.Equal(t, []string{"ok"}, names(got))
	assert.EqualValues(t, 2, drops.count(dropUndeclaredUnstorable))
	assert.EqualValues(t, 0, drops.count(dropUndeclaredNonNumeric))
}

// The warning is once per (tenant, device, key, reason) so a chatty device cannot flood the log;
// every drop is still counted, and a nil receiver (a resolver built without metrics) is inert.
func TestDropRecordCountsEveryDropAndToleratesNil(t *testing.T) {
	m := testDropMetrics()
	m.record("acme", "d-1", "k", "v", dropUndeclaredNonNumeric, "x")
	m.record("acme", "d-1", "k", "v", dropUndeclaredNonNumeric, "x")
	m.record("acme", "d-2", "k", "v", dropUndeclaredNonNumeric, "x")
	assert.EqualValues(t, 3, m.count(dropUndeclaredNonNumeric))
	assert.Equal(t, 2, m.warned.len(), "one claim per device and key")
	var nilMetrics *MeasurementDropMetrics
	nilMetrics.record("a", "b", "c", "d", dropUndeclaredNonNumeric, "x")
}

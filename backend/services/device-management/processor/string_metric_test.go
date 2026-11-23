// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// resolveDeclared resolves one entry against a profile that declares a STRING metric "mode", a
// DOUBLE metric "temp" and a BOOLEAN metric "on".
func resolveDeclared(t *testing.T, rez *EventResolver, m map[string]string) []resolvedevent.ResolvedMeasurementEntry {
	t.Helper()
	device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "d-1"}}
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "p@1"}, []*model.MetricDefinition{
		{MetricKey: "mode", DataType: string(model.MetricString)},
		{MetricKey: "temp", DataType: string(model.MetricDouble)},
		{MetricKey: "on", DataType: string(model.MetricBoolean)},
	})
	at := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	event := &esmodel.UnresolvedEvent{Source: "test", Device: "d-1", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at.Add(time.Hour),
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{Measurements: m, OccurredTime: &at}}}}
	out, err := rez.ResolveMeasurementsEventPayload(core.WithTenant(context.Background(), "acme"), device, profile, event)
	require.NoError(t, err)
	return out.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries
}

// A declared STRING metric resolves to a reading that carries its text, its data type and its
// classifier, beside the numeric readings of the same message, and nothing is dropped.
func TestADeclaredStringMetricResolvesAsAReading(t *testing.T) {
	drops := testDropMetrics()
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.drops = drops

	got := resolveDeclared(t, rez, map[string]string{"mode": "heating", "temp": "21.5", "on": "true"})
	byName := map[string]resolvedevent.ResolvedMeasurementEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	require.Len(t, byName, 3)
	mode := byName["mode"]
	assert.Equal(t, "heating", mode.Value)
	require.NotNil(t, mode.DataType)
	assert.Equal(t, "STRING", *mode.DataType)
	assert.NotNil(t, mode.Classifier)
	assert.Equal(t, "1", byName["on"].Value, "a BOOLEAN still normalizes to 1/0")
	assert.EqualValues(t, 0, drops.count(dropDeclaredNotStorable))
}

// A STRING value that event-management and device-state would both refuse (over the bound, not
// UTF-8, holding a NUL) is dropped on its own and counted, and its siblings are kept: left in the
// message it would take every other reading down with it.
func TestAStringValueThatCannotBeStoredIsDroppedAlone(t *testing.T) {
	for name, bad := range map[string]string{
		"over the bound": strings.Repeat("x", resolvedevent.MaxStringValueBytes+1),
		"a NUL byte":     "a\x00b",
		"invalid UTF-8":  "a\xffb",
	} {
		t.Run(name, func(t *testing.T) {
			drops := testDropMetrics()
			rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
			rez.drops = drops

			got := resolveDeclared(t, rez, map[string]string{"mode": bad, "temp": "21.5"})
			assert.Equal(t, []string{"temp"}, names(got))
			assert.EqualValues(t, 1, drops.count(dropDeclaredNotStorable))
		})
	}

	// A value at the bound is kept.
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	got := resolveDeclared(t, rez, map[string]string{"mode": strings.Repeat("x", resolvedevent.MaxStringValueBytes)})
	assert.Equal(t, []string{"mode"}, names(got))
}

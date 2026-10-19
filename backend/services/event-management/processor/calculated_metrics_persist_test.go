// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"strconv"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Calculated metrics (ADR-082) as the historian receives them: each reading below is
// resolved by device-management's REAL resolver under a profile version carrying formulas,
// written onto the real resolved-event wire and decoded, exactly the bytes the persistence
// worker consumes.

var calcAt = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// calcProfile is a meter profile version: voltage and current measured, power_kw computed
// by formula.
func calcProfile(version, formula string) dmmodel.ProfileResolution {
	return dmmodel.NewProfileResolution(dmmodel.ProfileScope{DeviceTypeToken: "meter", ProfileVersionToken: version},
		[]*dmmodel.MetricDefinition{
			{Model: gorm.Model{ID: 1}, MetricKey: "voltage", DataType: "DOUBLE", Unit: sql.NullString{String: "V", Valid: true}},
			{Model: gorm.Model{ID: 2}, MetricKey: "current", DataType: "DOUBLE", Unit: sql.NullString{String: "A", Valid: true}},
			{Model: gorm.Model{ID: 5}, MetricKey: "power_kw", DataType: "DOUBLE", Unit: sql.NullString{String: "kW", Valid: true},
				Expression: sql.NullString{String: formula, Valid: true}},
		})
}

// resolveOverTheWire resolves one reading (no alternate id, the shape LwM2M and Sparkplug
// produce) under profile and returns it as the historian decodes it from format.
func resolveOverTheWire(t testing.TB, profile dmmodel.ProfileResolution, readings map[string]string,
	format eventtime.WireFormat) resolvedevent.ResolvedEvent {
	t.Helper()
	rez := dmprocessor.NewEventResolver(1, nil, "", dmprocessor.EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	device := &dmmodel.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-1"}}
	unresolved := &esmodel.UnresolvedEvent{Source: "lwm2m", Device: "meter-1", EventType: esmodel.Measurement,
		OccurredTime: calcAt, ProcessedTime: calcAt,
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: readings}}}}
	payload, err := rez.ResolveMeasurementsEventPayload(context.Background(), device, profile, unresolved)
	require.NoError(t, err)
	scope := profile.Scope()
	resolved := &resolvedevent.ResolvedEvent{Source: "lwm2m", SourceDeviceToken: "meter-1", EventType: esmodel.Measurement,
		OccurredTime: calcAt, ProcessedTime: calcAt, DeviceTypeToken: scope.DeviceTypeToken,
		ProfileVersionToken: scope.ProfileVersionToken, Payload: payload}
	encoded, err := resolvedevent.MarshalResolvedEventAs(resolved, format)
	require.NoError(t, err)
	decoded, err := resolvedevent.UnmarshalResolvedEvent(encoded)
	require.NoError(t, err)
	return *decoded
}

var meterReading = map[string]string{"voltage": "230.5", "current": "12.25"}

// The same device reading delivered twice — a device-level duplicate, a new inbound message
// with no alternate id — with a profile publish that changed the formula landing between
// the two copies, stores exactly ONE row for each raw value and ONE for the calculated
// value, and the calculated row keeps the value of the copy written first.
func TestACalculatedValueIsStoredOnceAcrossAFormulaChange(t *testing.T) {
	for _, format := range []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact} {
		t.Run(format.String(), func(t *testing.T) {
			ep, db, _ := newFencedPersistenceWorker(t)
			ctx := core.WithTenant(context.Background(), "acme")

			first := resolveOverTheWire(t, calcProfile("meter@1", "voltage * current / 1000"), meterReading, format)
			second := resolveOverTheWire(t, calcProfile("meter@2", "voltage * current"), meterReading, format)
			require.Equal(t, "2.823625", derivedValue(t, first, "power_kw"))
			require.Equal(t, "2823.625", derivedValue(t, second, "power_kw"),
				"precondition: the two copies carry different calculated values")

			for _, ev := range []resolvedevent.ResolvedEvent{first, second, first} {
				_, err := ep.PersistEvent(ctx, ev)
				require.NoError(t, err)
			}

			var events int64
			require.NoError(t, sysDB(db).Model(&model.Event{}).Count(&events).Error)
			assert.EqualValues(t, 1, events, "one reading is one event, whichever formula computed its copy")
			var rows []model.MeasurementEvent
			require.NoError(t, sysDB(db).Order("name").Find(&rows).Error)
			require.Len(t, rows, 3, "one row per raw value and one for the calculated value")
			assert.Equal(t, []string{"current", "power_kw", "voltage"}, []string{rows[0].Name, rows[1].Name, rows[2].Name})
			assert.InDelta(t, 2.823625, rows[1].Value.Float64, 1e-12, "the first value written is the one kept")
			require.NotNil(t, rows[1].Classifier)
			assert.EqualValues(t, 5, *rows[1].Classifier, "the calculated row carries its definition's classifier")
			assert.Equal(t, "kW", *rows[1].Unit)
		})
	}

	// The negative control: the same two copies with the flag stripped — calculated values
	// treated as raw, which is what identity did before this rule — store the reading
	// twice. Without it the counts above could hold for a reason that has nothing to do
	// with the rule.
	t.Run("without the Derived flag the reading stores twice", func(t *testing.T) {
		ep, db, _ := newFencedPersistenceWorker(t)
		ctx := core.WithTenant(context.Background(), "acme")
		for _, formula := range []string{"voltage * current / 1000", "voltage * current"} {
			ev := resolveOverTheWire(t, calcProfile("v", formula), meterReading, eventtime.WireCompact)
			for i := range ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries {
				ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[i].Derived = false
			}
			_, err := ep.PersistEvent(ctx, ev)
			require.NoError(t, err)
		}
		var events, rows int64
		require.NoError(t, sysDB(db).Model(&model.Event{}).Count(&events).Error)
		require.NoError(t, sysDB(db).Model(&model.MeasurementEvent{}).Count(&rows).Error)
		assert.EqualValues(t, 2, events)
		assert.EqualValues(t, 6, rows)
	})
}

// derivedValue is the calculated value of key on a resolved reading, failing when it is
// absent or not flagged Derived.
func derivedValue(t testing.TB, ev resolvedevent.ResolvedEvent, key string) string {
	t.Helper()
	for _, mx := range ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries {
		if mx.Name == key {
			require.True(t, mx.Derived, "%s is not flagged Derived after the wire", key)
			return mx.Value
		}
	}
	t.Fatalf("no %s on the resolved reading", key)
	return ""
}

// A reading of a profile WITHOUT formulas resolves, crosses the wire and derives the event
// id it always had: the identity is pinned against the literal document the historian hashed
// before calculated metrics existed.
func TestAFormulaFreeReadingKeepsItsEventIdThroughTheResolver(t *testing.T) {
	profile := dmmodel.NewProfileResolution(dmmodel.ProfileScope{ProfileVersionToken: "meter@1"},
		[]*dmmodel.MetricDefinition{
			{Model: gorm.Model{ID: 1}, MetricKey: "voltage", DataType: "DOUBLE", Unit: sql.NullString{String: "V", Valid: true}},
			{Model: gorm.Model{ID: 2}, MetricKey: "current", DataType: "DOUBLE", Unit: sql.NullString{String: "A", Valid: true}},
		})
	const literal = `{"Entries":[{"Entries":[` +
		`{"Name":"current","Value":"12.25","Classifier":2,"Unit":"A","DataType":"DOUBLE"},` +
		`{"Name":"voltage","Value":"230.5","Classifier":1,"Unit":"V","DataType":"DOUBLE"}],` +
		`"OccurredTime":"2026-10-16T12:00:00Z"}]}`
	parent := &model.Event{DeviceToken: "meter-1", EventType: esmodel.Measurement, OccurredTime: calcAt, Source: "lwm2m"}
	want := model.DeriveEventId("acme", parent, []byte(literal))
	for _, format := range []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact} {
		ev := resolveOverTheWire(t, profile, meterReading, format)
		got, err := model.DeriveEventIdForPayload("acme", parent, ev.Payload)
		require.NoError(t, err)
		assert.Equalf(t, want, got, "%s: a formula-free reading's event id moved", format)
	}
	// And the same reading under a version that DOES compute power_kw is the same event.
	ev := resolveOverTheWire(t, calcProfile("meter@2", "voltage * current / 1000"), meterReading, eventtime.WireCompact)
	got, err := model.DeriveEventIdForPayload("acme", parent, ev.Payload)
	require.NoError(t, err)
	assert.Equal(t, want, got, "adding a formula to the profile must not move the reading's event id")
}

// One sample resolved 1000 times — the resolver ranges a map, so its iteration order
// differs between runs, and the two wire formats alternate — yields ONE event id and
// byte-identical calculated entries. The id no longer depends on the calculated value, but
// the value is still the value of record: it is what DETECT, preview and replay read, so it
// must not wobble either.
func TestOneSampleResolvesToOneIdAndOneCalculatedValue(t *testing.T) {
	profile := calcProfileFour()
	reading := map[string]string{"voltage": "230.123456789", "current": "12.987654321", "temp": "81.3", "fan": "0"}
	parent := &model.Event{DeviceToken: "meter-1", EventType: esmodel.Measurement, OccurredTime: calcAt, Source: "lwm2m"}
	formats := []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact}

	run := func(i int, format func(resolvedevent.ResolvedMeasurementEntry) string) (string, string) {
		ev := resolveOverTheWire(t, profile, reading, formats[i%2])
		id, err := model.DeriveEventIdForPayload("acme", parent, ev.Payload)
		require.NoError(t, err)
		var derived []string
		for _, mx := range ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries {
			if mx.Derived {
				derived = append(derived, mx.Name+"="+format(mx))
			}
		}
		require.Len(t, derived, 4, "every formula produced a value")
		b, err := json.Marshal(derived)
		require.NoError(t, err)
		return fmt.Sprintf("%x", id), string(b)
	}
	asStored := func(mx resolvedevent.ResolvedMeasurementEntry) string { return mx.Value }

	ids, values := map[string]int{}, map[string]int{}
	for i := 0; i < 1000; i++ {
		id, v := run(i, asStored)
		ids[id]++
		values[v]++
	}
	assert.Len(t, ids, 1, "1000 resolutions of one sample derived %d event ids", len(ids))
	assert.Len(t, values, 1, "1000 resolutions of one sample produced %d different calculated entries: %v", len(values), values)

	// The negative control: the same check over a formatter whose output depends on the run
	// (the value re-rendered at a precision that cycles) must FAIL the value check, while the
	// id stays one — so the check above can see a wobble, and the id is not what sees it.
	ids, values = map[string]int{}, map[string]int{}
	for i := 0; i < 30; i++ {
		unstable := func(mx resolvedevent.ResolvedMeasurementEntry) string {
			f, err := strconv.ParseFloat(mx.Value, 64)
			require.NoError(t, err)
			return strconv.FormatFloat(f, 'f', 6+i%3, 64)
		}
		id, v := run(i, unstable)
		ids[id]++
		values[v]++
	}
	assert.Len(t, ids, 1, "the event id must not depend on a calculated value's formatting")
	assert.Greater(t, len(values), 1, "the determinism check must catch an unstable formatter")
}

// calcProfileFour is the resolver benchmark's meter profile with all four formulas.
func calcProfileFour() dmmodel.ProfileResolution {
	calc := func(id uint, key, dataType, expr string) *dmmodel.MetricDefinition {
		return &dmmodel.MetricDefinition{Model: gorm.Model{ID: id}, MetricKey: key, DataType: dataType,
			Expression: sql.NullString{String: expr, Valid: true}}
	}
	return dmmodel.NewProfileResolution(dmmodel.ProfileScope{ProfileVersionToken: "meter@4"},
		[]*dmmodel.MetricDefinition{
			{Model: gorm.Model{ID: 1}, MetricKey: "voltage", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 2}, MetricKey: "current", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 3}, MetricKey: "temp", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 4}, MetricKey: "fan", DataType: "INT"},
			calc(5, "power_kw", "DOUBLE", "voltage * current / 1000"),
			calc(6, "temp_f", "DOUBLE", "temp * 1.8 + 32"),
			calc(7, "apparent", "DOUBLE", "math.sqrt(voltage * voltage + current * current)"),
			calc(8, "over_temp", "BOOLEAN", "temp > 80.0 && (has(m.fan) ? fan == 0 : true)"),
		})
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
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

// resolveUndeclaredSamples is resolveUndeclared returning the whole resolved payload.
func resolveUndeclaredSamples(t *testing.T, rez *EventResolver, m map[string]string) *resolvedevent.ResolvedMeasurementsPayload {
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
	return out.(*resolvedevent.ResolvedMeasurementsPayload)
}

// The LA-04 payload: battery is stored, and status_text is not only discarded and counted but
// RECORDED on the sample, with its reason and value, for the device-state projection.
func TestDroppedEntryIsRecordedOnTheSample(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.drops = testDropMetrics()

	got := resolveUndeclaredSamples(t, rez, map[string]string{"status_text": "ok", "battery": "50", "huge": "1e30"})
	require.Len(t, got.Entries, 1)
	assert.Equal(t, []string{"battery"}, names(got.Entries[0].Entries))
	assert.Equal(t, []resolvedevent.DroppedMeasurementEntry{
		{Name: "huge", Reason: dropUndeclaredUnstorable, Value: "1e30"},
		{Name: "status_text", Reason: dropUndeclaredNonNumeric, Value: "ok"},
	}, got.Entries[0].Dropped, "recorded in name order, so the resolved bytes do not depend on map order")

	// The record is built even by a resolver with no metrics (nil metrics record no count,
	// never the fact).
	bare := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	got = resolveUndeclaredSamples(t, bare, map[string]string{"status_text": "ok", "battery": "50"})
	assert.Len(t, got.Entries[0].Dropped, 1)

	// Negative control: a payload that loses nothing carries no record at all.
	clean := resolveUndeclaredSamples(t, rez, map[string]string{"battery": "50"})
	assert.Nil(t, clean.Entries[0].Dropped)
}

// The value on the record is cut to resolvedevent.MaxDroppedValueBytes.
func TestDroppedValueIsTruncated(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	long := strings.Repeat("x", 500)
	got := resolveUndeclaredSamples(t, rez, map[string]string{"blob": long, "battery": "1"})
	require.Len(t, got.Entries[0].Dropped, 1)
	assert.Len(t, got.Entries[0].Dropped[0].Value, resolvedevent.MaxDroppedValueBytes)
	assert.True(t, strings.HasPrefix(long, got.Entries[0].Dropped[0].Value))
}

// A sample whose EVERY entry is dropped behaves as it did before the record existed: it is
// still part of the payload, empty, at its own instant. The record is all that is new.
func TestAllDroppedSampleIsStillAnEmptySample(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	got := resolveUndeclaredSamples(t, rez, map[string]string{"status_text": "ok"})
	require.Len(t, got.Entries, 1, "the empty sample is kept, not removed")
	assert.Empty(t, got.Entries[0].Entries)
	assert.NotNil(t, got.Entries[0].Entries, "an empty slice, as before, not nil")
	assert.Equal(t, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), got.Entries[0].OccurredTime)
	require.Len(t, got.Entries[0].Dropped, 1)

	// Its identity document is the document of the same empty sample resolved before the
	// record existed.
	b, err := json.Marshal(got.IdentityView())
	require.NoError(t, err)
	assert.JSONEq(t, `{"Entries":[{"Entries":[],"OccurredTime":"2026-10-16T12:00:00Z"}]}`, string(b))
}

// A drop never moves an event's identity document: the payload with its drop and the payload
// of a device that never sent the key hash from the same bytes once the identity view is taken.
func TestDropDoesNotMoveIdentity(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	with := resolveUndeclaredSamples(t, rez, map[string]string{"status_text": "ok", "battery": "50"})
	without := resolveUndeclaredSamples(t, rez, map[string]string{"battery": "50"})
	enc := func(p *resolvedevent.ResolvedMeasurementsPayload) string {
		b, err := json.Marshal(p)
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, enc(without), enc(with.IdentityView()))
	assert.NotEqual(t, enc(without), enc(with), "negative control: unfiltered, the record is visible")
}

// The metric label vocabulary is the shared one.
func TestDropReasonsAreTheSharedVocabulary(t *testing.T) {
	assert.Equal(t, resolvedevent.DroppedReasons, measurementDropReasons)
}

// A discarded value that is not UTF-8 is repaired on the record rather than carried onto the
// wire, where it would refuse the whole event (negative control: the same event with the raw
// bytes is refused).
func TestAnInvalidUTF8DropCannotRefuseTheEvent(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	got := resolveUndeclaredSamples(t, rez, map[string]string{"bad\xff": "v\xfe\xfd", "battery": "50"})
	at := got.Entries[0].OccurredTime
	event := func(p *resolvedevent.ResolvedMeasurementsPayload) *resolvedevent.ResolvedEvent {
		return &resolvedevent.ResolvedEvent{Source: "s", SourceDeviceToken: "d-1", EventType: eventtype.Measurement,
			OccurredTime: at, ProcessedTime: at, Payload: p}
	}
	_, err := resolvedevent.MarshalResolvedEvent(event(got))
	assert.NoError(t, err)

	raw := &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
		OccurredTime: at, Dropped: []resolvedevent.DroppedMeasurementEntry{{Name: "k", Reason: dropUndeclaredNonNumeric, Value: "\xff"}}}}}
	_, err = resolvedevent.MarshalResolvedEvent(event(raw))
	assert.Error(t, err)
}

// A NUL (JSON carries "\u0000") is removed from a dropped name and value: PostgreSQL refuses
// 0x00 in a varchar (22021), which would fail the consumer's write of the whole batch on every
// delivery. Negative control: the all-NUL name is empty after the strip, so it is still a drop.
func TestANULInADropIsRemovedAtTheProducer(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	got := resolveUndeclaredSamples(t, rez, map[string]string{"la\x00bel": "o\x00k\x00", "battery": "50"})
	require.Len(t, got.Entries[0].Dropped, 1)
	d := got.Entries[0].Dropped[0]
	assert.Equal(t, "label", d.Name)
	assert.Equal(t, "ok", d.Value)
	assert.NotContains(t, d.Name+d.Value, "\x00")
}

// The dropped key is cut to the width of the column that stores it, on a UTF-8 boundary.
func TestADroppedNameIsCutOnARuneBoundary(t *testing.T) {
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	// 'é' is two bytes: 127 ASCII bytes then é straddles the 128-byte limit.
	long := strings.Repeat("a", 127) + "é" + strings.Repeat("b", 100)
	got := resolveUndeclaredSamples(t, rez, map[string]string{long: "ok", "battery": "50"})
	require.Len(t, got.Entries[0].Dropped, 1)
	name := got.Entries[0].Dropped[0].Name
	assert.Equal(t, strings.Repeat("a", 127), name, "the straddling rune is dropped whole, never split")
	assert.LessOrEqual(t, len(name), resolvedevent.MaxDroppedNameBytes)

	short := strings.Repeat("c", resolvedevent.MaxDroppedNameBytes)
	got = resolveUndeclaredSamples(t, rez, map[string]string{short: "ok"})
	assert.Equal(t, short, got.Entries[0].Dropped[0].Name, "a name at the limit is kept whole")
}

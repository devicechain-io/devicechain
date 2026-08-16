// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// The live projections stamp each reading at the SAMPLE's instant, not the message's.
//
// 🔴 This is the surface a person actually looks at — the "latest" card on a dashboard —
// and it is the one that used to disagree with its own history chart about the same
// reading. The projection honoured the sample's time (leniently, silently ignoring an
// unparseable one) while the history table flattened every reading in a batch onto the
// message's. Both now read the same already-decided value off the resolved entry, so the
// two cannot drift apart again: there is no rule left here to reimplement differently.
//
// The fixtures give the samples instants DISTINCT from the envelope's on purpose. With
// everything set to one time, a projection that read the envelope would pass every
// assertion below while being exactly wrong.

// measurementMessage marshals a resolved measurement batch onto the wire as
// device-management publishes it, one sample per entry with its own instant.
func measurementMessage(t *testing.T, deviceToken string, envelope time.Time,
	samples []struct {
		at    time.Time
		name  string
		value string
	}) messaging.Message {
	t.Helper()
	entries := make([]dmmodel.ResolvedMeasurementsEntry, 0, len(samples))
	for _, s := range samples {
		entries = append(entries, dmmodel.ResolvedMeasurementsEntry{
			OccurredTime: s.at,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: s.name, Value: s.value}},
		})
	}
	event := &dmmodel.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: deviceToken,
		EventType:         esmodel.Measurement,
		OccurredTime:      envelope,
		ProcessedTime:     envelope,
		Payload:           &dmmodel.ResolvedMeasurementsPayload{Entries: entries},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	return messaging.Message{Subject: locationTestSubject, Value: encoded}
}

func loadProjectedMeasurement(t *testing.T, sp *StateProcessor, ctx context.Context,
	token, name string) model.LatestMeasurement {
	t.Helper()
	api := sp.Api.(*model.Api)
	var m model.LatestMeasurement
	if err := api.RDB.DB(ctx).Where("device_token = ? AND name = ?", token, name).First(&m).Error; err != nil {
		t.Fatalf("load latest measurement %s/%s: %v", token, name, err)
	}
	return m
}

// Each key in a batch is projected at its own sample's instant.
func TestLatestMeasurementKeepsTheSamplesInstant(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	envelope := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	buffered := envelope.Add(-90 * time.Second)

	sp.mergeOne(context.Background(), measurementMessage(t, "probe-01", envelope, []struct {
		at    time.Time
		name  string
		value string
	}{
		{buffered, "temperature", "21.5"},
		{envelope, "humidity", "48"},
	}))

	temp := loadProjectedMeasurement(t, sp, ctx, "probe-01", "temperature")
	if !temp.OccurredTime.Equal(buffered) {
		t.Fatalf("the buffered reading is projected at %v, want its own instant %v",
			temp.OccurredTime, buffered)
	}
	humidity := loadProjectedMeasurement(t, sp, ctx, "probe-01", "humidity")
	if !humidity.OccurredTime.Equal(envelope) {
		t.Fatalf("the live reading is projected at %v, want %v", humidity.OccurredTime, envelope)
	}
}

// The sample's instant is also the ORDERING key the newer-wins guard compares on, so the
// NEWEST reading in a batch wins wherever it sits in the batch.
//
// 🔴 THE ORDER OF THE TWO SAMPLES BELOW IS THE TEST. On the message's time they are
// simultaneous, and the guard is strictly-newer, so an equal time never overwrites: the
// FIRST entry wins and every later one in the same upload is silently discarded. Putting
// the older reading first is therefore what makes this discriminating — with the newer one
// first, a message-time projection reaches the right answer by accident and the test proves
// nothing. For a store-and-forward device uploading oldest-first, which is the normal
// shape, that accident is the everyday case: the stalest reading in the buffer became the
// device's "latest" value.
func TestTheNewestSampleInABatchWins(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	envelope := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	sp.mergeOne(context.Background(), measurementMessage(t, "probe-02", envelope, []struct {
		at    time.Time
		name  string
		value string
	}{
		{envelope.Add(-5 * time.Minute), "temperature", "18.0"},
		{envelope, "temperature", "22.5"},
	}))

	got := loadProjectedMeasurement(t, sp, ctx, "probe-02", "temperature")
	if !got.OccurredTime.Equal(envelope) {
		t.Fatalf("the projection is stamped %v, want the newest sample's instant %v",
			got.OccurredTime, envelope)
	}
	if !got.Value.Valid || got.Value.Float64 != 22.5 {
		t.Fatalf("the newest reading must survive a batch that uploads oldest-first; got %+v", got.Value)
	}
}

// The last-known-position projection reads the fix's own instant for the same reason.
func TestLatestLocationKeepsTheFixesInstant(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	envelope := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	fixTaken := envelope.Add(-2 * time.Minute)

	sp.mergeOne(context.Background(), locationMessage(t, "rover-02", envelope,
		[]dmmodel.ResolvedLocationEntry{{
			Latitude: str("33.749"), Longitude: str("-84.388"), OccurredTime: fixTaken,
		}}))

	got := loadProjectedLocation(t, sp, ctx, "rover-02")
	if !got.OccurredTime.Equal(fixTaken) {
		t.Fatalf("the fix is projected at %v, want the instant it was taken %v",
			got.OccurredTime, fixTaken)
	}
	// Connectivity is a property of the MESSAGE, not of the fix: a device that uploads a
	// two-minute-old position was heard from just now, so its liveness advances to the
	// envelope. Reading the fix's time here would age a live device by the depth of its
	// upload buffer, which for a store-and-forward tracker is hours.
	state := loadState(t, sp, ctx, "rover-02")
	if !state.LastActivityTime.Valid || !state.LastActivityTime.Time.Equal(envelope) {
		t.Fatalf("last activity is %v, want the message's time %v", state.LastActivityTime, envelope)
	}
}

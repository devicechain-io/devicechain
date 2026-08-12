// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The historian's WRITE SEAM: the worker turns a resolved payload into create requests,
// and each request must carry the SAMPLE's instant while the parent event keeps the
// MESSAGE's.
//
// 🔴 THIS IS THE SEAM THE WHOLE CHANGE IS ABOUT, AND IT HAD NO TEST. The model-level
// tests pin what CreateXEvents does with a correct request; nothing pinned the worker
// BUILDING one. Reverting these three lines to the envelope — restoring the exact defect
// this work exists to remove, a batch of readings all persisted at one instant — left
// every test in this module green. Measured, not assumed.
//
// Every fixture below gives the samples instants DISTINCT from the envelope's, because a
// fixture where they match cannot tell the two implementations apart.

// captured holds the create requests of each payload type, read back off a fake Api. The
// package MockApi records nothing about its arguments, so a test built on it could only
// assert that a call happened — which is the assertion that already existed and already
// missed this.
type capturingEntryTimeApi struct {
	*emtest.MockApi
	measurements []*model.MeasurementEventCreateRequest
	alerts       []*model.AlertEventCreateRequest
	locations    []*model.LocationEventCreateRequest
}

func (api *capturingEntryTimeApi) CreateMeasurementEvents(ctx context.Context, db *gorm.DB,
	requests []*model.MeasurementEventCreateRequest) ([]*model.MeasurementEvent, error) {
	api.measurements = requests
	return []*model.MeasurementEvent{}, nil
}

func (api *capturingEntryTimeApi) CreateAlertEvents(ctx context.Context, db *gorm.DB,
	requests []*model.AlertEventCreateRequest) ([]*model.AlertEvent, error) {
	api.alerts = requests
	return []*model.AlertEvent{}, nil
}

func (api *capturingEntryTimeApi) CreateLocationEvents(ctx context.Context, db *gorm.DB,
	requests []*model.LocationEventCreateRequest) ([]*model.LocationEvent, error) {
	api.locations = requests
	return []*model.LocationEvent{}, nil
}

// entryTimeWorker returns a worker over the capturing Api, plus the envelope every test
// below persists under and a pair of earlier sample instants.
func entryTimeWorker() (*EventPersistenceWorker, *capturingEntryTimeApi, model.Event, time.Time, time.Time) {
	api := &capturingEntryTimeApi{MockApi: new(emtest.MockApi)}
	envelope := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	parent := model.Event{
		DeviceToken:   "device-4",
		EventType:     esmodel.Measurement,
		OccurredTime:  envelope,
		ProcessedTime: envelope,
	}
	return &EventPersistenceWorker{WorkerId: 1, Api: api}, api, parent,
		envelope.Add(-90 * time.Second), envelope.Add(-30 * time.Second)
}

func TestPersistMeasurementEventsCarriesEachSamplesInstant(t *testing.T) {
	worker, api, parent, first, second := entryTimeWorker()

	_, err := worker.PersistMeasurementEvents(context.Background(), nil, parent,
		dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
			{OccurredTime: first, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5"}}},
			{OccurredTime: second, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: "22.5"}}},
		}})
	require.NoError(t, err)
	require.Len(t, api.measurements, 2)

	assert.True(t, api.measurements[0].EntryOccurredTime.Equal(first),
		"reading 0 is written at %v, want its own instant %v", api.measurements[0].EntryOccurredTime, first)
	assert.True(t, api.measurements[1].EntryOccurredTime.Equal(second),
		"reading 1 is written at %v, want its own instant %v", api.measurements[1].EntryOccurredTime, second)
	// The parent keeps the envelope. Moving a per-sample time onto the embedded Event
	// would make N samples describe N parents sharing one derived event_id, and
	// upsertParentEvents would silently keep whichever it saw first.
	for i, req := range api.measurements {
		assert.True(t, req.Event.OccurredTime.Equal(parent.OccurredTime),
			"request %d moved the parent event's time to %v", i, req.Event.OccurredTime)
	}
}

func TestPersistAlertEventsCarriesEachEntrysInstant(t *testing.T) {
	worker, api, parent, first, second := entryTimeWorker()

	_, err := worker.PersistAlertEvents(context.Background(), nil, parent,
		dmmodel.ResolvedAlertsPayload{Entries: []dmmodel.ResolvedAlertEntry{
			{OccurredTime: first, Type: "SENSOR_FAULT", Level: 3, Message: "probe A open circuit"},
			{OccurredTime: second, Type: "SENSOR_FAULT", Level: 3, Message: "probe B open circuit"},
		}})
	require.NoError(t, err)
	require.Len(t, api.alerts, 2)

	assert.True(t, api.alerts[0].EntryOccurredTime.Equal(first),
		"alert 0 is written at %v, want %v", api.alerts[0].EntryOccurredTime, first)
	assert.True(t, api.alerts[1].EntryOccurredTime.Equal(second),
		"alert 1 is written at %v, want %v", api.alerts[1].EntryOccurredTime, second)
}

func TestPersistLocationEventsCarriesEachFixesInstant(t *testing.T) {
	worker, api, parent, first, second := entryTimeWorker()
	s := func(v string) *string { return &v }

	_, err := worker.PersistLocationEvents(context.Background(), nil, parent,
		dmmodel.ResolvedLocationsPayload{Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: s("33.749"), Longitude: s("-84.388"), OccurredTime: first},
			{Latitude: s("47.6062"), Longitude: s("-122.3321"), OccurredTime: second},
		}})
	require.NoError(t, err)
	require.Len(t, api.locations, 2)

	assert.True(t, api.locations[0].EntryOccurredTime.Equal(first),
		"fix 0 is written at %v, want %v", api.locations[0].EntryOccurredTime, first)
	assert.True(t, api.locations[1].EntryOccurredTime.Equal(second),
		"fix 1 is written at %v, want %v", api.locations[1].EntryOccurredTime, second)
}

// A zero entry time is classified DETERMINISTIC, so the message dead-letters on its first
// failure rather than burning its whole redelivery budget.
//
// 🔴 The zero instant is reachable from the wire: "0001-01-01T00:00:00Z" is valid RFC 3339
// and is exactly Go's zero time. The device-facing decoder refuses it, which is where a
// device gets told — but a value that can never succeed must not spin if one arrives by
// another route. Misfiled as transient it retries to MaxDeliver and then dead-letters
// under "API call failed", which is the wrong story about a message whose content is the
// problem.
func TestAZeroEntryTimeIsADeterministicFailure(t *testing.T) {
	err := model.ErrZeroEntryTime
	classified := classifyPersistFailure(err)
	require.Error(t, classified)
	assert.ErrorIs(t, classified, ErrDeterministic,
		"a zero entry time can never succeed on retry and must not be classified transient")
	assert.ErrorIs(t, classified, model.ErrZeroEntryTime, "the original cause must survive classification")

	// The counterweight: classification must still leave a genuinely transient failure on
	// the retry path. A classifier that called everything deterministic would satisfy the
	// assertion above and discard recoverable data.
	transient := errors.New("connection refused")
	assert.NotErrorIs(t, classifyPersistFailure(transient), ErrDeterministic,
		"an unrecognised failure must stay retryable")
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
)

// Worker used to persist event entities.
type EventPersistenceWorker struct {
	WorkerId    int
	Api         model.EventManagementApi
	Unpersisted <-chan messaging.Message
	Invalid     func(error, messaging.Message)
	Failed      func(string, uint, dmmodel.ResolvedEvent, error)
}

// Results of event persistence process.
type EventPersistenceResults struct {
	Events []interface{}
}

// Create a new event resolver.
func NewEventPersistenceWorker(workerId int, api model.EventManagementApi,
	unpersisted <-chan messaging.Message,
	invalid func(error, messaging.Message),
	failed func(string, uint, dmmodel.ResolvedEvent, error)) *EventPersistenceWorker {
	return &EventPersistenceWorker{
		WorkerId:    workerId,
		Api:         api,
		Unpersisted: unpersisted,
		Invalid:     invalid,
		Failed:      failed,
	}
}

// Parse a (possibly null) string into a float64.
func parseNullableFloat64(val *string) (*float64, error) {
	if val == nil {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(*val, 64)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// Persists a location event to the datastore.
func (ep *EventPersistenceWorker) PersistLocationEvents(ctx context.Context, event model.Event,
	payload dmmodel.ResolvedLocationsPayload) (*EventPersistenceResults, error) {
	events := make([]interface{}, 0)
	for _, location := range payload.Entries {
		lat, err := parseNullableFloat64(location.Latitude)
		if err != nil {
			return nil, err
		}
		lon, err := parseNullableFloat64(location.Longitude)
		if err != nil {
			return nil, err
		}
		ele, err := parseNullableFloat64(location.Elevation)
		if err != nil {
			return nil, err
		}
		lreq := &model.LocationEventCreateRequest{
			Event:     event,
			Latitude:  lat,
			Longitude: lon,
			Elevation: ele,
		}
		locevt, err := ep.Api.CreateLocationEvent(ctx, lreq)
		if err != nil {
			return nil, err
		}
		events = append(events, locevt)
	}
	results := &EventPersistenceResults{
		Events: events,
	}
	return results, nil
}

// Persists measurement events to the datastore.
func (ep *EventPersistenceWorker) PersistMeasurementEvents(ctx context.Context, event model.Event,
	payload dmmodel.ResolvedMeasurementsPayload) (*EventPersistenceResults, error) {
	events := make([]interface{}, 0)
	for _, mxentry := range payload.Entries {
		for _, mx := range mxentry.Entries {
			val := mx.Value
			fval, err := parseNullableFloat64(&val)
			if err != nil {
				return nil, err
			}
			var classifier *uint
			if mx.Classifier != nil {
				c := uint(*mx.Classifier)
				classifier = &c
			}
			mreq := &model.MeasurementEventCreateRequest{
				Event:      event,
				Name:       mx.Name,
				Value:      fval,
				Classifier: classifier,
			}
			mevt, err := ep.Api.CreateMeasurementEvent(ctx, mreq)
			if err != nil {
				return nil, err
			}
			events = append(events, mevt)
		}
	}
	results := &EventPersistenceResults{
		Events: events,
	}
	return results, nil
}

// Persists alert events to the datastore.
func (ep *EventPersistenceWorker) PersistAlertEvents(ctx context.Context, event model.Event,
	payload dmmodel.ResolvedAlertsPayload) (*EventPersistenceResults, error) {
	events := make([]interface{}, 0)
	for _, alert := range payload.Entries {
		areq := &model.AlertEventCreateRequest{
			Event:   event,
			Type:    alert.Type,
			Level:   alert.Level,
			Message: alert.Message,
			Source:  alert.Source,
		}
		aevt, err := ep.Api.CreateAlertEvent(ctx, areq)
		if err != nil {
			return nil, err
		}
		events = append(events, aevt)
	}
	results := &EventPersistenceResults{
		Events: events,
	}
	return results, nil
}

// Persists a resolved event to the datastore. The resolved event already carries
// its relationship target as a uniform (type, id) reference (ADR-013), which maps
// directly onto the event's (anchor_type, anchor_id).
func (ep *EventPersistenceWorker) PersistEvent(ctx context.Context, event dmmodel.ResolvedEvent) (*EventPersistenceResults, error) {
	pevent := model.Event{
		DeviceId:      event.SourceDeviceId,
		OccurredTime:  event.OccurredTime,
		Source:        event.Source,
		AltId:         rdb.NullStrOf(event.AltId),
		AnchorType:    event.TargetType,
		AnchorId:      event.TargetId,
		ProcessedTime: event.ProcessedTime,
		EventType:     event.EventType,
	}
	switch event.EventType {
	case esmodel.Location:
		if payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload); ok {
			return ep.PersistLocationEvents(ctx, pevent, *payload)
		}
		return nil, fmt.Errorf("non-location payload in location event")
	case esmodel.Measurement:
		if payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload); ok {
			return ep.PersistMeasurementEvents(ctx, pevent, *payload)
		}
		return nil, fmt.Errorf("non-measurement payload in measurement event")
	case esmodel.Alert:
		if payload, ok := event.Payload.(*dmmodel.ResolvedAlertsPayload); ok {
			return ep.PersistAlertEvents(ctx, pevent, *payload)
		}
		return nil, fmt.Errorf("non-alert payload in alert event")
	}
	return nil, fmt.Errorf("unhandled event type in persistence: %s", event.EventType.String())
}

// Converts unresolved events into resolved events.
func (ep *EventPersistenceWorker) Process(ctx context.Context) {
	for {
		unpersisted, more := <-ep.Unpersisted
		if more {
			log.Debug().Msg(fmt.Sprintf("Event persistence handled by worker id %d", ep.WorkerId))

			// Derive the per-message tenant from the message subject and build a
			// tenant-scoped context. Without a parseable tenant the message can
			// not be persisted safely (fail-closed) so it is skipped rather than
			// written without a tenant. The tenant string is carried onto the
			// persisted/failed channels so the downstream producer scopes its
			// publish to the same tenant.
			msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, unpersisted.Subject)
			if !ok {
				log.Warn().Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", unpersisted.Subject))
				continue
			}

			// Attempt to unmarshal event.
			event, err := dmproto.UnmarshalResolvedEvent(unpersisted.Value)
			if err != nil {
				ep.Invalid(err, unpersisted)
				continue
			}

			if log.Debug().Enabled() {
				jevent, err := json.MarshalIndent(event, "", "  ")
				if err == nil {
					log.Debug().Msg(fmt.Sprintf("Received %s event:\n%s", event.EventType.String(), jevent))
				}
			}

			// Persist the event using the per-message tenant context.
			if _, err := ep.PersistEvent(msgctx, *event); err != nil {
				ep.Failed(tenant, 0, *event, err)
			}
		} else {
			log.Debug().Msg("Event persister received shutdown signal.")
			return
		}
	}
}

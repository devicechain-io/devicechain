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
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Worker used to persist event entities.
type EventPersistenceWorker struct {
	WorkerId    int
	Api         model.EventManagementApi
	Unpersisted <-chan messaging.Message
	Invalid     func(error, messaging.Message)
	Failed      func(string, uint, dmmodel.ResolvedEvent, error, string)
	// metrics records RED-style instrumentation for each message handled by this
	// worker (ADR-022 E13). Shared by reference with sibling workers; may be nil
	// in tests, so it is only ever touched via the nil-safe Start().
	metrics *core.ProcessorMetrics
}

// Results of event persistence process.
type EventPersistenceResults struct {
	Events []interface{}
}

// Create a new event resolver.
func NewEventPersistenceWorker(workerId int, api model.EventManagementApi,
	unpersisted <-chan messaging.Message,
	invalid func(error, messaging.Message),
	failed func(string, uint, dmmodel.ResolvedEvent, error, string),
	metrics *core.ProcessorMetrics) *EventPersistenceWorker {
	return &EventPersistenceWorker{
		WorkerId:    workerId,
		Api:         api,
		Unpersisted: unpersisted,
		Invalid:     invalid,
		Failed:      failed,
		metrics:     metrics,
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

// Persists a location event to the datastore. All of the message's location
// rows are inserted as a single batch on the supplied (transaction-bound) db
// handle so they commit all-or-nothing (ADR-022 E5).
func (ep *EventPersistenceWorker) PersistLocationEvents(ctx context.Context, db *gorm.DB, event model.Event,
	payload dmmodel.ResolvedLocationsPayload) (*EventPersistenceResults, error) {
	requests := make([]*model.LocationEventCreateRequest, 0, len(payload.Entries))
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
		requests = append(requests, &model.LocationEventCreateRequest{
			Event:     event,
			Latitude:  lat,
			Longitude: lon,
			Elevation: ele,
		})
	}
	created, err := ep.Api.CreateLocationEvents(ctx, db, requests)
	if err != nil {
		return nil, err
	}
	events := make([]interface{}, 0, len(created))
	for _, locevt := range created {
		events = append(events, locevt)
	}
	results := &EventPersistenceResults{
		Events: events,
	}
	return results, nil
}

// Persists measurement events to the datastore. All of the message's
// measurement rows are inserted as a single batch on the supplied
// (transaction-bound) db handle so they commit all-or-nothing (ADR-022 E5).
func (ep *EventPersistenceWorker) PersistMeasurementEvents(ctx context.Context, db *gorm.DB, event model.Event,
	payload dmmodel.ResolvedMeasurementsPayload) (*EventPersistenceResults, error) {
	requests := make([]*model.MeasurementEventCreateRequest, 0)
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
			requests = append(requests, &model.MeasurementEventCreateRequest{
				Event:      event,
				Name:       mx.Name,
				Value:      fval,
				Classifier: classifier,
			})
		}
	}
	created, err := ep.Api.CreateMeasurementEvents(ctx, db, requests)
	if err != nil {
		return nil, err
	}
	events := make([]interface{}, 0, len(created))
	for _, mevt := range created {
		events = append(events, mevt)
	}
	results := &EventPersistenceResults{
		Events: events,
	}
	return results, nil
}

// Persists alert events to the datastore. All of the message's alert rows are
// inserted as a single batch on the supplied (transaction-bound) db handle so
// they commit all-or-nothing (ADR-022 E5).
func (ep *EventPersistenceWorker) PersistAlertEvents(ctx context.Context, db *gorm.DB, event model.Event,
	payload dmmodel.ResolvedAlertsPayload) (*EventPersistenceResults, error) {
	requests := make([]*model.AlertEventCreateRequest, 0, len(payload.Entries))
	for _, alert := range payload.Entries {
		requests = append(requests, &model.AlertEventCreateRequest{
			Event:   event,
			Type:    alert.Type,
			Level:   alert.Level,
			Message: alert.Message,
			Source:  alert.Source,
		})
	}
	created, err := ep.Api.CreateAlertEvents(ctx, db, requests)
	if err != nil {
		return nil, err
	}
	events := make([]interface{}, 0, len(created))
	for _, aevt := range created {
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
	// All of a single message's inserts run inside one transaction so the
	// message's events are persisted all-or-nothing (ADR-022 E5): a mid-message
	// failure rolls the whole message back rather than leaving some rows
	// committed while the message routes to the failed/dead-letter path. The
	// transaction handle (tx) carries the tenant-scoped ctx, so the global
	// tenant-scope create callback still fires on every batched insert.
	var results *EventPersistenceResults
	err := ep.Api.PersistInTx(ctx, func(tx *gorm.DB) error {
		var perr error
		switch event.EventType {
		case esmodel.Location:
			if payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload); ok {
				results, perr = ep.PersistLocationEvents(ctx, tx, pevent, *payload)
				return perr
			}
			return fmt.Errorf("non-location payload in location event")
		case esmodel.Measurement:
			if payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload); ok {
				results, perr = ep.PersistMeasurementEvents(ctx, tx, pevent, *payload)
				return perr
			}
			return fmt.Errorf("non-measurement payload in measurement event")
		case esmodel.Alert:
			if payload, ok := event.Payload.(*dmmodel.ResolvedAlertsPayload); ok {
				results, perr = ep.PersistAlertEvents(ctx, tx, pevent, *payload)
				return perr
			}
			return fmt.Errorf("non-alert payload in alert event")
		}
		return fmt.Errorf("unhandled event type in persistence: %s", event.EventType.String())
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// Converts unresolved events into resolved events.
func (ep *EventPersistenceWorker) Process(ctx context.Context) {
	for {
		unpersisted, more := <-ep.Unpersisted
		if more {
			// Mark the message in-flight and record its result+duration on every
			// disposition path below (ADR-022 E13). Start() is nil-safe.
			done := ep.metrics.Start()

			log.Debug().Int("worker", ep.WorkerId).Str("correlation", unpersisted.CorrelationID()).
				Msg("Event persistence handled by worker")

			// Derive the per-message tenant from the message subject and build a
			// tenant-scoped context. Without a parseable tenant the message can
			// not be persisted safely (fail-closed) so it is skipped rather than
			// written without a tenant. The tenant string is carried onto the
			// persisted/failed channels so the downstream producer scopes its
			// publish to the same tenant.
			msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, unpersisted.Subject)
			if !ok {
				log.Warn().Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", unpersisted.Subject))
				// Poison message: a message with no parseable tenant can not be
				// persisted and redelivery can not help, so ack it to drop it.
				unpersisted.Ack()
				done(core.ResultInvalid)
				continue
			}

			// Attempt to unmarshal event.
			event, err := dmproto.UnmarshalResolvedEvent(unpersisted.Value)
			if err != nil {
				ep.Invalid(err, unpersisted)
				// Terminal: routed to the failed-events DLQ, so ack to drop it.
				unpersisted.Ack()
				done(core.ResultInvalid)
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
				// Persist errors are treated as transient and retryable. Retry
				// via redelivery up to the redelivery cap; on the final attempt
				// route to the dead-letter path and ack to stop retrying.
				if unpersisted.NumDelivered >= messaging.MaxDeliver {
					ep.Failed(tenant, 0, *event, err, unpersisted.CorrelationID())
					unpersisted.Ack()
					done(core.ResultFailed)
				} else {
					unpersisted.Nak()
					done(core.ResultRetry)
				}
			} else {
				// Durably persisted: ack so the message is not redelivered.
				unpersisted.Ack()
				done(core.ResultOK)
			}
		} else {
			log.Debug().Msg("Event persister received shutdown signal.")
			return
		}
	}
}

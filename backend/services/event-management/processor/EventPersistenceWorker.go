// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
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

// ErrDeterministic marks a persistence failure that no amount of redelivery can
// fix — bad data, such as a non-numeric measurement or location value — so the
// event is dead-lettered on the first failure rather than retried (left unacked) to
// the delivery cap (ADR-024). A transient failure (e.g. a DB blip) is not wrapped and
// keeps the retry path.
var ErrDeterministic = errors.New("deterministic persistence failure")

// Parse a (possibly null) string into a float64. A non-numeric value is a
// deterministic failure (the value can never be stored in the numeric column), so
// the error is wrapped as such rather than left to retry (unacked) pointlessly.
func parseNullableFloat64(val *string) (*float64, error) {
	if val == nil {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(*val, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not numeric: %v", ErrDeterministic, *val, err)
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
				Unit:       mx.Unit,
				DataType:   mx.DataType,
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

// Persists a resolved event to the datastore. The event's relationship anchors
// (ADR-013) are stored as a set of event_anchors rows alongside the base event,
// so the same reading is queryable by each of the device's assignment dimensions.
func (ep *EventPersistenceWorker) PersistEvent(ctx context.Context, event dmmodel.ResolvedEvent) (*EventPersistenceResults, error) {
	pevent := model.Event{
		DeviceToken:   event.SourceDeviceToken,
		OccurredTime:  event.OccurredTime,
		Source:        event.Source,
		AltId:         rdb.NullStrOf(event.AltId),
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
		// Idempotent ingestion: a redelivered resolved event carrying an
		// alternateId that was already persisted is a no-op, so the at-least-once
		// consume path (ADR-022 Wave-2 redelivery/DLQ) does not double-write. The
		// (tenant_id, alt_id, occurred_time) partial unique index is the backstop
		// for a concurrent-redelivery race; this check skips the common sequential
		// case without erroring. Events without an alternateId are not deduped.
		if event.AltId != nil {
			exists, derr := ep.Api.EventExistsByAltId(ctx, tx, *event.AltId, event.OccurredTime)
			if derr != nil {
				return derr
			}
			if exists {
				log.Info().Str("altId", *event.AltId).
					Msg("Skipping already-persisted event (idempotent redelivery)")
				results = &EventPersistenceResults{}
				return nil
			}
		}

		var perr error
		switch event.EventType {
		case esmodel.Location:
			payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload)
			if !ok {
				return fmt.Errorf("non-location payload in location event")
			}
			results, perr = ep.PersistLocationEvents(ctx, tx, pevent, *payload)
		case esmodel.Measurement:
			payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload)
			if !ok {
				return fmt.Errorf("non-measurement payload in measurement event")
			}
			results, perr = ep.PersistMeasurementEvents(ctx, tx, pevent, *payload)
		case esmodel.Alert:
			payload, ok := event.Payload.(*dmmodel.ResolvedAlertsPayload)
			if !ok {
				return fmt.Errorf("non-alert payload in alert event")
			}
			results, perr = ep.PersistAlertEvents(ctx, tx, pevent, *payload)
		default:
			return fmt.Errorf("unhandled event type in persistence: %s", event.EventType.String())
		}
		if perr != nil {
			return perr
		}
		// Persist the event's anchor set in the same transaction, so the event and
		// its queryable dimensions commit atomically (ADR-013 addendum 2026-07-01).
		return ep.persistEventAnchors(ctx, tx, event)
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// persistEventAnchors writes one event_anchors row per resolved anchor, so the
// event is queryable by each of the device's tracked-relationship dimensions. An
// unassigned event carries no anchors and writes nothing.
func (ep *EventPersistenceWorker) persistEventAnchors(ctx context.Context, db *gorm.DB, event dmmodel.ResolvedEvent) error {
	if len(event.Anchors) == 0 {
		return nil
	}
	anchors := make([]*model.EventAnchor, 0, len(event.Anchors))
	for _, a := range event.Anchors {
		anchors = append(anchors, &model.EventAnchor{
			DeviceToken:  event.SourceDeviceToken,
			EventType:    event.EventType,
			OccurredTime: event.OccurredTime,
			AnchorType:   a.AnchorType,
			AnchorToken:  a.AnchorToken,
		})
	}
	return ep.Api.CreateEventAnchors(ctx, db, anchors)
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
				// A deterministic failure (bad data) can never succeed on redelivery,
				// so dead-letter it on the first failure (ADR-024). A transient
				// failure is retried via redelivery up to the cap, then dead-lettered.
				switch {
				case errors.Is(err, ErrDeterministic):
					ep.Failed(tenant, uint(dmproto.FailureReason_Invalid), *event, err, unpersisted.CorrelationID())
					unpersisted.Ack()
					done(core.ResultFailed)
				case unpersisted.NumDelivered >= messaging.MaxDeliver:
					ep.Failed(tenant, uint(dmproto.FailureReason_ApiCallFailed), *event, err, unpersisted.CorrelationID())
					unpersisted.Ack()
					done(core.ResultFailed)
				default:
					// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
					// an immediate nak would burn MaxDeliver in ~1.4ms inside a Postgres
					// outage. Reference disposition: event-sources' settler (ADR-030).
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

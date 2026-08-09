// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
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
	// Deduped reports that the persist was a no-op because the row already existed —
	// only the StateChange path sets it (its idempotency unique index absorbs a
	// JetStream redelivery). When true the caller MUST skip anchor persistence: a
	// StateChange carries no AltId, so without this a redelivery would re-insert its
	// anchor set (event_anchors has no unique index), duplicating it.
	Deduped bool
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

// classifyPersistFailure re-classifies a database error that no redelivery can fix.
//
// 🔴 This is the BACKSTOP the decode-time range check cannot be. Validation lives in
// the JSON decoder, so it covers every device-facing JSON event and NOTHING that
// reaches this service another way — lwm2m-ingest and sparkplug-ingest build their
// payload structs directly and marshal protobuf, bypassing the decoder entirely, and
// measurement values are not range-checked anywhere at all (the column is
// numeric(20,8), so "1e13" parses cleanly and overflows at the INSERT).
//
// Without this, such a value comes back from the driver unwrapped, falls to the
// dispatch default as "transient", and burns its whole MaxDeliver budget before
// being filed as a downstream API failure rather than as invalid data. That is the
// exact poison loop the location range check was written to prevent, still reachable
// on every path the decoder does not sit on.
//
// The test is SQLSTATE class 22 — "data exception" — and it is deliberately no wider.
// A class-22 error is a statement about the VALUE (out of range, bad syntax for the
// type, string too long), so the same bytes reproduce it on every delivery forever.
// Integrity-constraint classes are not included: some of those genuinely do resolve
// on retry once a concurrent writer commits, and misfiling a transient failure as
// deterministic discards data rather than merely delaying it. When in doubt this
// stays on the retry path, because that error is recoverable and the other is not.
func classifyPersistFailure(err error) error {
	if err == nil || errors.Is(err, ErrDeterministic) {
		return err
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && strings.HasPrefix(pgerr.Code, "22") {
		return fmt.Errorf("%w: database rejected the value (SQLSTATE %s): %w",
			ErrDeterministic, pgerr.Code, err)
	}
	return err
}

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
		acc, err := parseNullableFloat64(location.Accuracy)
		if err != nil {
			return nil, err
		}
		spd, err := parseNullableFloat64(location.Speed)
		if err != nil {
			return nil, err
		}
		hdg, err := parseNullableFloat64(location.Heading)
		if err != nil {
			return nil, err
		}
		requests = append(requests, &model.LocationEventCreateRequest{
			Event:     event,
			Latitude:  lat,
			Longitude: lon,
			Elevation: ele,
			Accuracy:  acc,
			Speed:     spd,
			Heading:   hdg,
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

// Persists an authoritative presence transition to the append-only history hypertable
// (ADR-067 decision 5, S3). A StateChange is a single connect/disconnect edge (one
// row), not a batch. Idempotent redelivery is handled inside CreateStateChangeEvents
// (ON CONFLICT on the idempotency index — a StateChange carries no AltId so the
// base-event dedup does not engage). The live authoritative presence is written
// separately by device-state's projection; this is the queryable timeline.
func (ep *EventPersistenceWorker) PersistStateChangeEvents(ctx context.Context, db *gorm.DB, event model.Event,
	payload dmmodel.ResolvedStateChangePayload) (*EventPersistenceResults, error) {
	created, affected, err := ep.Api.CreateStateChangeEvents(ctx, db, []*model.StateChangeEventCreateRequest{{
		Event:     event,
		State:     payload.State,
		Reason:    payload.Reason,
		SessionId: payload.SessionId,
	}})
	if err != nil {
		return nil, err
	}
	events := make([]interface{}, 0, len(created))
	for _, scevt := range created {
		events = append(events, scevt)
	}
	// A redelivery inserts nothing (the idempotency index conflicts); tell the caller to
	// skip anchors so it does not re-insert the anchor set.
	return &EventPersistenceResults{Events: events, Deduped: affected == 0}, nil
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
	// Give the event an identity of its own, derived from its content, BEFORE anything is
	// written — the base event, its payload rows and its anchors all key off it, so it has
	// to exist before the first insert. Deriving here rather than at ingest is what makes
	// it hold on every transport: lwm2m-ingest and sparkplug-ingest have no capture stream
	// to carry a minted id through, and a redelivery replays the raw publish, so only a
	// value computed from the content itself converges on the same row every time.
	//
	// ProcessedTime is deliberately NOT part of the identity: it is when WE handled the
	// message, so including it would make every redelivery a new event and defeat the
	// dedup this exists to protect.
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	payloadBytes, perr := json.Marshal(event.Payload)
	if perr != nil {
		return nil, fmt.Errorf("canonicalizing payload for the event identity: %w", perr)
	}
	pevent.EventId = model.DeriveEventId(tenant, &pevent, payloadBytes)
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

		// A payload whose Go type does not match its event type is as deterministic as
		// a failure gets — the same bytes produce the same mismatch on every delivery —
		// but these four returned a BARE error, which the dispatch below classifies by
		// its default branch as transient. So the event was redelivered until it burned
		// its whole MaxDeliver budget and was then filed as a downstream API failure
		// rather than as invalid data. Wrapping ErrDeterministic dead-letters it on the
		// first delivery, with the right reason attached.
		var perr error
		switch event.EventType {
		case esmodel.Location:
			payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload)
			if !ok {
				return fmt.Errorf("%w: non-location payload in location event", ErrDeterministic)
			}
			results, perr = ep.PersistLocationEvents(ctx, tx, pevent, *payload)
		case esmodel.Measurement:
			payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload)
			if !ok {
				return fmt.Errorf("%w: non-measurement payload in measurement event", ErrDeterministic)
			}
			results, perr = ep.PersistMeasurementEvents(ctx, tx, pevent, *payload)
		case esmodel.Alert:
			payload, ok := event.Payload.(*dmmodel.ResolvedAlertsPayload)
			if !ok {
				return fmt.Errorf("%w: non-alert payload in alert event", ErrDeterministic)
			}
			results, perr = ep.PersistAlertEvents(ctx, tx, pevent, *payload)
		case esmodel.StateChange:
			// ADR-067 presence history (S3): persist the connect/disconnect edge to the
			// append-only state_change_events hypertable, then fall through to
			// persistEventAnchors like every other event type. The live authoritative
			// presence is device-state's projection; this is the queryable timeline.
			payload, ok := event.Payload.(*dmmodel.ResolvedStateChangePayload)
			if !ok {
				return fmt.Errorf("%w: non-state-change payload in state change event", ErrDeterministic)
			}
			results, perr = ep.PersistStateChangeEvents(ctx, tx, pevent, *payload)
		default:
			return fmt.Errorf("unhandled event type in persistence: %s", event.EventType.String())
		}
		if perr != nil {
			return perr
		}
		// A deduped persist (a StateChange redelivery absorbed by its idempotency index)
		// must NOT re-run anchor persistence — event_anchors has no unique index, so a
		// plain re-insert would duplicate the anchor set.
		if results != nil && results.Deduped {
			return nil
		}
		// Persist the event's anchor set in the same transaction, so the event and
		// its queryable dimensions commit atomically (ADR-013 addendum 2026-07-01).
		return ep.persistEventAnchors(ctx, tx, pevent.EventId, event)
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// persistEventAnchors writes one event_anchors row per resolved anchor, so the
// event is queryable by each of the device's tracked-relationship dimensions. An
// unassigned event carries no anchors and writes nothing.
func (ep *EventPersistenceWorker) persistEventAnchors(ctx context.Context, db *gorm.DB,
	eventId []byte, event dmmodel.ResolvedEvent) error {
	if len(event.Anchors) == 0 {
		return nil
	}
	anchors := make([]*model.EventAnchor, 0, len(event.Anchors))
	for _, a := range event.Anchors {
		anchors = append(anchors, &model.EventAnchor{
			EventId:      eventId,
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
				err = classifyPersistFailure(err)
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

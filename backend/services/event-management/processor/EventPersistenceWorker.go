// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
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

	// MaxBatch is the most messages this worker commits in one transaction. 1 or less —
	// the zero value included, so a worker built by literal keeps the per-message path —
	// gives every message a transaction of its own.
	MaxBatch int
	// Linger is how long a batch that is not full waits for more messages before it is
	// committed. Zero takes only what is already waiting.
	Linger time.Duration

	// metrics records RED-style instrumentation for each message handled by this
	// worker (ADR-022 E13) and the batch signals. Shared by reference with sibling
	// workers; may be nil in tests, so it is only ever touched via its nil-safe methods.
	metrics *PersistMetrics
}

// Results of event persistence process.
type EventPersistenceResults struct {
	Events []interface{}
	// Deduped reports that the persist was a no-op because the row already existed —
	// only the StateChange path sets it (its idempotency unique index absorbs a
	// JetStream redelivery). When true the caller skips anchor persistence, which is
	// now an optimization rather than the correctness guard it originally was:
	// uq_event_anchors_idem (tenant_id, event_id, occurred_time, anchor_type,
	// anchor_token) has since made the anchor set idempotent on its own, and
	// CreateEventAnchors upserts against exactly those columns. The older comment here
	// claimed event_anchors carried no unique index; that stopped being true when the
	// index was added, and the claim survived the change.
	Deduped bool
}

// Create a new event resolver.
func NewEventPersistenceWorker(workerId int, api model.EventManagementApi,
	unpersisted <-chan messaging.Message,
	invalid func(error, messaging.Message),
	failed func(string, uint, dmmodel.ResolvedEvent, error, string),
	metrics *PersistMetrics) *EventPersistenceWorker {
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
// event is abandoned on the first failure rather than retried (left unacked) to
// the delivery cap (ADR-024). A transient failure (e.g. a DB blip) is not wrapped and
// keeps the retry path.
//
// 🔴 "ABANDONED", NOT "DEAD-LETTERED", AND THE DIFFERENCE IS THAT NOTHING DRAINS
// failed-events. The platform HAS a dead-letter sink — streams.DeadLetters, whose
// letters user-management stores and command-delivery reads back — and this is not
// it. streams.FailedEvents has exactly two references in the backend, and both are
// NewWriter (this service and device-management): no consumer, no store, no console
// surface. So an event that lands there is retained for the stream's cold-tier
// window and then expires unread, and the operator-visible record of the failure is
// the log line and the RED metrics counter, not a queue anyone can work off.
//
// That is a real gap rather than a naming quibble, and it is recorded here instead
// of being papered over: the vocabulary is the only thing that told a reader a queue
// existed to be drained.
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
	// A payload row with no instant of its own can never acquire one on redelivery: the
	// same bytes resolve to the same zero. Retrying it burns the whole MaxDeliver budget
	// and then files it as an API failure, which is the wrong story about a message whose
	// content is the problem.
	if errors.Is(err, model.ErrZeroEntryTime) {
		return fmt.Errorf("%w: %w", ErrDeterministic, err)
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
			Event:             event,
			EntryOccurredTime: location.OccurredTime,
			Latitude:          lat,
			Longitude:         lon,
			Elevation:         ele,
			Accuracy:          acc,
			Speed:             spd,
			Heading:           hdg,
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
				Event:             event,
				EntryOccurredTime: mxentry.OccurredTime,
				Name:              mx.Name,
				Value:             fval,
				Classifier:        classifier,
				Unit:              mx.Unit,
				DataType:          mx.DataType,
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
			Event:             event,
			EntryOccurredTime: alert.OccurredTime,
			Type:              alert.Type,
			Level:             alert.Level,
			Message:           alert.Message,
			Source:            alert.Source,
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

// Persists a resolved event to the datastore, in a transaction of its own. The event's
// relationship anchors (ADR-013) are stored as a set of event_anchors rows alongside the
// base event, so the same reading is queryable by each of the device's assignment
// dimensions.
func (ep *EventPersistenceWorker) PersistEvent(ctx context.Context, event dmmodel.ResolvedEvent) (*EventPersistenceResults, error) {
	pevent, err := ep.baseEvent(ctx, event)
	if err != nil {
		return nil, err
	}
	// All of a single message's inserts run inside one transaction so the
	// message's events are persisted all-or-nothing (ADR-022 E5): a mid-message
	// failure rolls the whole message back rather than leaving some rows
	// committed while the message routes to the failed-events path. Every statement
	// writeEvent makes binds ctx itself, so the global tenant-scope create callback
	// fires on every batched insert.
	var results *EventPersistenceResults
	err = ep.Api.PersistInTx(ctx, func(tx *gorm.DB) error {
		var werr error
		results, werr = ep.writeEvent(ctx, tx, pevent, event)
		return werr
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// baseEvent derives a message's base event row, including its content-derived identity,
// under the tenant in ctx.
func (ep *EventPersistenceWorker) baseEvent(ctx context.Context, event dmmodel.ResolvedEvent) (model.Event, error) {
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
	// ProcessedTime is deliberately NOT part of the identity: it is when the platform
	// received the message, which a transport with no capture stream stamps afresh on
	// every redelivery of the raw publish, so including it would make each such
	// redelivery a new event and defeat the dedup this exists to protect.
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return model.Event{}, core.ErrNoTenant
	}
	eventId, ierr := model.DeriveEventIdForPayload(tenant, &pevent, event.Payload)
	if ierr != nil {
		return model.Event{}, ierr
	}
	pevent.EventId = eventId
	return pevent, nil
}

// writeEvent runs one message's statements on tx, each bound to ctx — the MESSAGE's
// tenant-scoped context, never the transaction's. That is what lets one transaction carry
// several messages, from several tenants: tenant scope and the erasure fence both read the
// tenant from the statement, and the statement's is this message's.
//
// Every write it makes is idempotent: the event id is derived from the content and every
// insert carries an ON CONFLICT DO NOTHING arbiter, so replaying a message — a
// redelivery, or a batch that rolled back and is written again — adds nothing twice.
func (ep *EventPersistenceWorker) writeEvent(ctx context.Context, tx *gorm.DB, pevent model.Event,
	event dmmodel.ResolvedEvent) (*EventPersistenceResults, error) {
	var results *EventPersistenceResults
	err := func() error {
		// Idempotent ingestion: a redelivered resolved event carrying an
		// alternateId that was already persisted is a no-op, so the at-least-once
		// consume path (ADR-022 Wave-2 redelivery) does not double-write. It is a
		// shortcut, not the guard: without an alternateId (or racing a concurrent
		// redelivery) the content-derived event id and the ON CONFLICT arbiters are
		// what keep a replay from writing twice.
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
		// rather than as invalid data. Wrapping ErrDeterministic gives up on it on the
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
		// skips anchor persistence. This is an optimization, NOT the correctness guard the
		// comment here used to claim: event_anchors does carry a unique index
		// (uq_event_anchors_idem) and CreateEventAnchors upserts on exactly its columns, so
		// re-running this would be a no-op rather than a duplicated anchor set. Removing
		// the skip would cost a round trip, not correctness.
		if results != nil && results.Deduped {
			return nil
		}
		// Persist the event's anchor set in the same transaction, so the event and
		// its queryable dimensions commit atomically (ADR-013 addendum 2026-07-01).
		return ep.persistEventAnchors(ctx, tx, pevent.EventId, event)
	}()
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

// Process persists what arrives on Unpersisted until the channel is closed: it collects a
// batch, commits it, and repeats. The last batch is persisted before it returns, so a
// shutdown drains the buffer rather than dropping it.
func (ep *EventPersistenceWorker) Process(ctx context.Context) {
	for {
		batch, open := ep.collect(ctx)
		if len(batch) > 0 {
			ep.persistBatch(ctx, batch)
		}
		if !open {
			log.Debug().Msg("Event persister received shutdown signal.")
			return
		}
	}
}

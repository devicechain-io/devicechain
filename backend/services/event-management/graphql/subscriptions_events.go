// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"strconv"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
)

// MeasurementStream streams measurement events to the subscriber as they resolve
// (ADR-037). It taps the live resolved-events feed for the caller's tenant, maps
// each resolved measurement entry to the same MeasurementEvent shape the query
// returns, and applies the optional device / name filters — so a live chart and
// a historical query share one type. The feed is torn down when the client
// unsubscribes or disconnects (ctx cancelled). Named distinctly from the
// measurementEvents query because both resolve off the one root resolver.
func (r *SchemaResolver) MeasurementStream(ctx context.Context, args struct {
	DeviceToken *string
	Name        *string
}) (<-chan *MeasurementEventResolver, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}

	live, err := r.GetNats(ctx).SubscribeLive(ctx, tenant, streams.ResolvedEvents)
	if err != nil {
		return nil, err
	}

	out := make(chan *MeasurementEventResolver)
	go func() {
		defer close(out)
		for msg := range live {
			resolved, err := dmproto.UnmarshalResolvedEvent(msg.Value)
			if err != nil {
				log.Debug().Err(err).Msg("subscription: skipping undecodable resolved event")
				continue
			}
			if resolved.EventType != esmodel.Measurement {
				continue
			}
			if args.DeviceToken != nil && resolved.SourceDeviceToken != *args.DeviceToken {
				continue
			}
			payload, ok := resolved.Payload.(*dmmodel.ResolvedMeasurementsPayload)
			if !ok {
				continue
			}
			for _, entry := range payload.Entries {
				for _, mx := range entry.Entries {
					if args.Name != nil && mx.Name != *args.Name {
						continue
					}
					me, merr := measurementFromResolved(tenant, resolved, entry, mx)
					if merr != nil {
						log.Debug().Err(merr).Msg("subscription: skipping a reading with no derivable identity")
						continue
					}
					select {
					case out <- &MeasurementEventResolver{M: me, S: r, C: ctx}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out, nil
}

// measurementFromResolved maps a single resolved measurement reading onto the
// MeasurementEvent read model (mirrors the persistence worker's mapping, minus the DB
// round trip), so a streamed event resolves identically to a queried one.
//
// 🔴 IT TAKES THE SAMPLE (entry) AS WELL AS THE READING (mx), and that is what makes
// the sentence above true rather than aspirational. The reading itself carries no time —
// a batch's instants live on the enclosing sample — so a version of this function that
// saw only mx could not do anything but stamp the envelope's time, which is precisely
// what it used to do while its comment claimed identical resolution. It was the one
// consumer that could not be fixed without a signature change, which is how it stayed
// wrong while the others were being argued about.
//
// 🔴 IT ALSO DERIVES THE ROW'S IDENTITY, FOR THE SAME REASON AND WITH THE SAME HAZARD.
// A streamed reading is never read back from storage, so nothing fills event_id or
// payload_id for it — and once `id` resolves from payload_id, a version of this function
// that left them empty would push every streamed reading out under an EMPTY `ID!`, which
// is the collision the id change exists to remove, arriving on the live path instead of
// the historical one. The identity is derived through the SAME model-side functions the
// persist path uses (model/payload_identity.go), so the id a subscriber sees now is the id
// the row will carry once the persister catches up. That is why the tenant is a parameter:
// an event's identity is scoped to it.
//
// It returns an error rather than a half-identified row, and the caller drops that reading
// with a log line instead of failing the whole subscription: one unencodable reading must
// not tear down a live chart that is otherwise working.
func measurementFromResolved(tenant string, e *dmmodel.ResolvedEvent,
	entry dmmodel.ResolvedMeasurementsEntry,
	mx dmmodel.ResolvedMeasurementEntry) (model.MeasurementEvent, error) {
	me := model.MeasurementEvent{
		DeviceToken:  e.SourceDeviceToken,
		EventType:    e.EventType,
		OccurredTime: entry.OccurredTime,
		Name:         mx.Name,
	}
	if f, err := strconv.ParseFloat(mx.Value, 64); err == nil {
		me.Value = sql.NullFloat64{Float64: f, Valid: true}
	}
	if mx.Classifier != nil {
		c := uint(*mx.Classifier)
		me.Classifier = &c
	}
	me.Unit = mx.Unit
	me.DataType = mx.DataType

	// The parent envelope, built exactly as the persistence worker builds it — the
	// MESSAGE's time and alternateId, not the sample's, because the parent event IS the
	// message.
	parent := model.Event{
		DeviceToken:  e.SourceDeviceToken,
		OccurredTime: e.OccurredTime,
		Source:       e.Source,
		AltId:        rdb.NullStrOf(e.AltId),
		EventType:    e.EventType,
	}
	eventId, err := model.DeriveEventIdForPayload(tenant, &parent, e.Payload)
	if err != nil {
		return model.MeasurementEvent{}, err
	}
	// Stamped onto the PARENT before the request is built, not merely onto the row: a
	// payload identity is scoped to its event, and it reads that scope from the embedded
	// event. Setting only me.EventId would derive every streamed reading's payload_id
	// under an empty parent — well-formed, stable, and different from the one the
	// persisted row gets.
	parent.EventId = eventId
	me.EventId = eventId

	request := &model.MeasurementEventCreateRequest{
		Event:             parent,
		EntryOccurredTime: entry.OccurredTime,
		Name:              mx.Name,
		Classifier:        me.Classifier,
		Unit:              mx.Unit,
		DataType:          mx.DataType,
	}
	// The value goes into the preimage as the same *float64 the persist path builds. The
	// one input where the two paths differ is a NON-NUMERIC value: persistence rejects it
	// deterministically and stores no row at all, while this stream has always pushed the
	// reading with a null value. So there is no persisted row for such a reading to
	// disagree with — an id derived here is simply the id that row would have had.
	if me.Value.Valid {
		v := me.Value.Float64
		request.Value = &v
	}
	payloadId, err := model.DeriveMeasurementPayloadId(request)
	if err != nil {
		return model.MeasurementEvent{}, err
	}
	me.PayloadId = payloadId
	return me, nil
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
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
	DeviceId *string
	Name     *string
}) (<-chan *MeasurementEventResolver, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}

	live, err := r.GetNats(ctx).SubscribeLive(ctx, tenant, dmconfig.SUBJECT_RESOLVED_EVENTS)
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
			if args.DeviceId != nil && fmt.Sprint(resolved.SourceDeviceId) != *args.DeviceId {
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
					me := measurementFromResolved(resolved, mx)
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

// measurementFromResolved maps a single resolved measurement entry onto the
// MeasurementEvent read model (mirrors the persistence worker's mapping, minus
// the DB round trip), so a streamed event resolves identically to a queried one.
func measurementFromResolved(e *dmmodel.ResolvedEvent, mx dmmodel.ResolvedMeasurementEntry) model.MeasurementEvent {
	me := model.MeasurementEvent{
		DeviceId:     e.SourceDeviceId,
		EventType:    e.EventType,
		OccurredTime: e.OccurredTime,
		Name:         mx.Name,
	}
	if f, err := strconv.ParseFloat(mx.Value, 64); err == nil {
		me.Value = sql.NullFloat64{Float64: f, Valid: true}
	}
	if mx.Classifier != nil {
		c := uint(*mx.Classifier)
		me.Classifier = &c
	}
	return me
}

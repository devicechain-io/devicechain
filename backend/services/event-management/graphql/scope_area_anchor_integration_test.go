// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The shared-area anchor filter (slice S2 of the customer-scoped area dashboards work)
// against a REAL TimescaleDB: the values a scoped caller's chart shows over an area it
// shares, on the raw hypertable path, and the plan the filter gets at scale. Run it with:
//
//	DC_IT_PGPORT=<port> DC_IT_PGPASSWORD=<password> \
//	  go test -tags integration -count=1 ./graphql/... -run SharedArea -v
package graphql

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordAnchored writes one device's measurement event (a BUFFERED reading of base 30 s
// before the envelope and a live one of 2*base) carrying one anchor row per anchor, as the
// persistence worker writes it for a device with several tracked edges.
func recordAnchored(t *testing.T, api *model.Api, device string, at time.Time, base float64, anchors ...[2]string) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), ownershipTenant)
	db := api.RDB.DB(ctx)
	e := model.Event{DeviceToken: device, EventType: eventtype.Measurement, OccurredTime: at, ProcessedTime: at, Source: "mqtt"}
	e.EventId = model.DeriveEventId(ownershipTenant, &e, nil)
	_, err := api.CreateMeasurementEvents(ctx, db, []*model.MeasurementEventCreateRequest{
		{Event: e, EntryOccurredTime: at.Add(-30 * time.Second), Name: "temperature", Value: fp(base)},
		{Event: e, EntryOccurredTime: at, Name: "temperature", Value: fp(2 * base)},
	})
	require.NoError(t, err)
	rows := make([]*model.EventAnchor, 0, len(anchors))
	for _, a := range anchors {
		rows = append(rows, &model.EventAnchor{EventId: e.EventId, DeviceToken: device, EventType: e.EventType,
			OccurredTime: at, AnchorType: a[0], AnchorToken: a[1]})
	}
	require.NoError(t, api.CreateEventAnchors(ctx, db, rows))
}

// Acme's chart over an area it shares with Globex shows Acme's device ALONE, and the value
// equals that device's own per-device read: 3 (1 + 2), not 33. This is the negative control
// for the count the spec calls out - the aggregate is computed by the server over the
// intersection, on the raw path, for scoped callers at a rollup-eligible interval too.
func TestSharedAreaBucketedMeasurementsAreTheCallersOwnAlone(t *testing.T) {
	api := newScopeIntegrationApi(t)
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	cust := func(c string) [2]string { return [2]string{"customer", c} }
	area := func(a string) [2]string { return [2]string{"area", a} }
	recordAnchored(t, api, "acme-pump", at, 1, cust("acme"), area("floor-shared"), area("floor-acme"))
	recordAnchored(t, api, "globex-pump", at, 10, cust("globex"), area("floor-shared"), area("floor-globex"))
	// D3, placement: the mover is on floor A, then floor B.
	recordAnchored(t, api, "mover", at.Add(time.Hour), 100, cust("acme"), area("floor-a"))
	recordAnchored(t, api, "mover", at.Add(2*time.Hour), 200, cust("acme"), area("floor-b"))
	// D3, ownership: sold to Globex after one Acme reading, same shared floor.
	recordAnchored(t, api, "sold", at.Add(3*time.Hour), 1000, cust("acme"), area("floor-shared"))
	recordAnchored(t, api, "sold", at.Add(4*time.Hour), 2000, cust("globex"), area("floor-shared"))

	authorities := []string{string(auth.EventRead)}
	acme := scopedCtx(api, ownershipTenant, []string{"customer:acme"}, authorities...)
	globex := scopedCtx(api, ownershipTenant, []string{"customer:globex"}, authorities...)
	everyone := scopedCtx(api, ownershipTenant, nil, authorities...)
	r := &SchemaResolver{}
	start, end := at.Add(-time.Hour).Format(time.RFC3339), at.Add(24*time.Hour).Format(time.RFC3339)

	type totals struct{ sum, count float64 }
	chart := func(ctx context.Context, interval int32, device *string, anchorType, token string) totals {
		t.Helper()
		in := MeasurementAggregationCriteriaInput{DeviceToken: device, StartTime: &start, EndTime: &end,
			IntervalSeconds: interval}
		if anchorType != "" {
			in.Anchor = &EventAnchorInput{Type: anchorType, Token: token}
		}
		buckets, err := r.BucketedMeasurements(ctx, struct {
			Criteria MeasurementAggregationCriteriaInput
		}{Criteria: in})
		require.NoError(t, err)
		var out totals
		for _, b := range buckets {
			if s := b.Sum(); s != nil {
				out.sum += *s
			}
			out.count += float64(b.Count())
		}
		return out
	}
	acmePump, globexPump := "acme-pump", "globex-pump"
	for _, interval := range []int32{60, 10} {
		// the value equals the per-device read of Acme's device, NOT the sum of both
		perDevice := chart(acme, interval, &acmePump, "", "")
		assert.Equal(t, totals{3, 2}, perDevice, "@%ds per-device read of Acme's device", interval)
		onArea := chart(acme, interval, nil, "area", "floor-shared")
		assert.Equal(t, totals{3003, 4}, onArea, "@%ds Acme's chart over the shared area: acme-pump (3) + Acme period of sold (1000+2000)", interval)
		onAreaDevice := chart(acme, interval, &acmePump, "area", "floor-shared")
		assert.Equal(t, perDevice, onAreaDevice, "@%ds the area read narrowed to the device equals the device read", interval)
		assert.Equal(t, totals{30 + 6000, 4}, chart(globex, interval, nil, "area", "floor-shared"),
			"@%ds Globex's chart over the shared area", interval)
		assert.Equal(t, totals{30, 2}, chart(globex, interval, &globexPump, "area", "floor-shared"))
		// control: the unscoped caller is unchanged and sees everyone
		assert.Equal(t, totals{3 + 30 + 3000 + 6000, 8}, chart(everyone, interval, nil, "area", "floor-shared"),
			"@%ds control: unscoped sees A+B", interval)
		// an area none of Acme's devices is on: empty, not an error
		assert.Equal(t, totals{}, chart(acme, interval, nil, "area", "floor-globex"), "@%ds Acme on Globex's floor", interval)
		// D3: the mover's history stays under the floor it was on
		assert.Equal(t, totals{300, 2}, chart(acme, interval, nil, "area", "floor-a"), "@%ds floor A", interval)
		assert.Equal(t, totals{600, 2}, chart(acme, interval, nil, "area", "floor-b"), "@%ds floor B", interval)
	}

	// the four paged reads over the same area
	page := EventSearchCriteriaInput{PageNumber: 1, PageSize: 100, Anchor: &EventAnchorInput{Type: "area", Token: "floor-shared"}}
	values := func(ctx context.Context) []float64 {
		found, err := r.MeasurementEvents(ctx, struct{ Criteria EventSearchCriteriaInput }{Criteria: page})
		require.NoError(t, err)
		out := []float64{}
		for _, m := range found.Results() {
			out = append(out, *m.Value())
		}
		sort.Float64s(out)
		return out
	}
	assert.Equal(t, []float64{1, 2, 1000, 2000}, values(acme), "measurementEvents: Acme")
	assert.Equal(t, []float64{10, 20, 2000, 4000}, values(globex), "measurementEvents: Globex")
	assert.Equal(t, []float64{1, 2, 10, 20, 1000, 2000, 2000, 4000}, values(everyone), "measurementEvents control")
	found, err := r.Events(acme, struct{ Criteria EventSearchCriteriaInput }{Criteria: page})
	require.NoError(t, err)
	assert.Len(t, found.Results(), 2, "events: Acme's two events on the shared area")
}

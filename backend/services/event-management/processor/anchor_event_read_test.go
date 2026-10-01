// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// TestAnAnchorFilteredEventIsFoundThroughTheWriter writes events through the REAL worker —
// one on its own (the per-message path) and two in one batch (the grouped path) — each
// with readings taken BEFORE the message, and reads them back through the anchor-filtered
// event read.
//
// 🔴 That read matches an anchor to its event on (occurred_time, event_id), because the
// event store's keys lead with time and an event_id alone cannot be sought. It is correct
// only while an anchor row carries its EVENT's instant. A writer that stamped an anchor with
// a reading's instant, or with the time it was written, would make the event vanish from
// "the events for customer X" — a confident empty answer. The readings' instants are kept
// distinct from the message's so a writer that used either of them fails here.
func TestAnAnchorFilteredEventIsFoundThroughTheWriter(t *testing.T) {
	r := newBatchRig(t, 8)
	at := batchT0.Add(2 * time.Hour)

	// A message whose readings were buffered: both entries are earlier than the message.
	buffered := func(dev string, at time.Time) dmodel.ResolvedEvent {
		ev := measurementOf(dev, at, "", 0, "21.5", "22.5")
		p := ev.Payload.(*dmodel.ResolvedMeasurementsPayload)
		for i := range p.Entries {
			p.Entries[i].OccurredTime = at.Add(-time.Duration(5-i) * time.Minute)
		}
		ev.Anchors = []dmodel.ResolvedAnchor{{AnchorType: "customer", AnchorToken: "cust-" + dev}}
		return ev
	}

	// The per-message path: a batch of one.
	r.run([]messaging.Message{consumed(t, r.acks, 0, fenceCostTenant, 1, buffered("dev-a", at))})
	// The grouped path: two messages in one batch.
	r.run(r.consume(t, []sent{
		{fenceCostTenant, buffered("dev-b", at.Add(time.Second))},
		{fenceCostTenant, buffered("dev-c", at.Add(2*time.Second))},
	}))
	if failed := r.reported(); len(failed) != 0 {
		t.Fatalf("persist failures: %+v", failed)
	}

	ctx := core.WithTenant(context.Background(), fenceCostTenant)
	for i, dev := range []string{"dev-a", "dev-b", "dev-c"} {
		anchorType, anchorToken := "customer", "cust-"+dev
		res, err := r.api.Api.Events(ctx, model.EventSearchCriteria{AnchorType: &anchorType, AnchorToken: &anchorToken})
		if err != nil {
			t.Fatalf("%s: anchor-filtered read: %v", dev, err)
		}
		want := at.Add(time.Duration(i) * time.Second)
		if len(res.Results) != 1 || res.Results[0].DeviceToken != dev || !res.Results[0].OccurredTime.Equal(want) ||
			res.Results[0].EventType != esmodel.Measurement {
			t.Errorf("%s: the anchor-filtered read found %+v; want its one event at %v", dev, res.Results, want)
		}
		if res.Pagination.TotalRecords != 1 {
			t.Errorf("%s: the anchor-filtered COUNT is %d; want 1", dev, res.Pagination.TotalRecords)
		}

		// And its readings, at their own instants, through the payload table's filter.
		ms, err := r.api.Api.MeasurementEvents(ctx, model.EventSearchCriteria{AnchorType: &anchorType, AnchorToken: &anchorToken})
		if err != nil {
			t.Fatalf("%s: anchor-filtered readings: %v", dev, err)
		}
		if len(ms.Results) != 2 {
			t.Errorf("%s: %d readings through the anchor; want 2", dev, len(ms.Results))
		}
	}

	// Control: the filter still excludes. Another customer's anchor finds nothing of these.
	other, token := "customer", "cust-nobody"
	res, err := r.api.Api.Events(ctx, model.EventSearchCriteria{AnchorType: &other, AnchorToken: &token})
	if err != nil {
		t.Fatalf("control read: %v", err)
	}
	if len(res.Results) != 0 {
		t.Errorf("an unknown anchor found %+v; want nothing", res.Results)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"gorm.io/gorm"
)

// Stop at the first undeclared wire value, then pin that sentinel by value: appending
// a vocabulary member requires an explicit storage decision in this table.
func TestEveryPublishedEventTypeIsPersistedOrRefusedDeterministically(t *testing.T) {
	var typ eventtype.EventType
	for ; typ.String() != fmt.Sprintf("EventType(%d)", typ); typ++ {
		t.Run(typ.String(), func(t *testing.T) {
			if typ > eventtype.IntegrationUplink {
				t.Fatalf("classify newly published type %s", typ)
			}
			worker := &EventPersistenceWorker{Api: persistingMockApi()}
			ctx := core.WithTenant(context.Background(), "acme")
			ev := *buildResolvedEvent(typ, matchingPayloadFor(typ))
			ev.AltId = nil
			// Exercise dispatch directly for refused types; their payloads need not have
			// canonical identities because they are never stored.
			_, err := worker.writeEvent(ctx, nil, model.Event{EventType: typ}, ev)
			if typ <= eventtype.StateChange {
				if err != nil {
					t.Fatalf("published %s must persist: %v", typ, err)
				}
			} else if !errors.Is(err, ErrDeterministic) {
				t.Fatalf("unsupported %s must be deterministic: %v", typ, err)
			}
		})
	}
	if typ != 8 {
		t.Fatalf("published vocabulary sentinel = %d; classify the new type", typ)
	}
}

func TestBuildRowsRefusesUnsupportedTypesDeterministically(t *testing.T) {
	for _, typ := range []eventtype.EventType{eventtype.NewRelationship, eventtype.StateChange,
		eventtype.CommandInvocation, eventtype.CommandResponse, eventtype.IntegrationUplink, 99} {
		ev := resolvedevent.ResolvedEvent{EventType: typ, Payload: matchingPayloadFor(typ)}
		err := buildRows(model.Event{}, ev, &model.EventRows{})
		if !errors.Is(err, ErrDeterministic) {
			t.Errorf("%s: got %v; want deterministic refusal", typ, err)
		}
	}
}

type envelopeRecordingApi struct {
	*emtest.MockApi
	parents []*model.Event
	anchors []*model.EventAnchor
}

func (a *envelopeRecordingApi) CreateEnvelopeEvents(_ context.Context, _ *gorm.DB, events []*model.Event) error {
	a.parents = append(a.parents, events...)
	return nil
}
func (a *envelopeRecordingApi) CreateEventAnchors(_ context.Context, _ *gorm.DB, anchors []*model.EventAnchor) error {
	a.anchors = append(a.anchors, anchors...)
	return nil
}

func relationshipEvent() resolvedevent.ResolvedEvent {
	ev := *buildResolvedEvent(eventtype.NewRelationship, matchingPayloadFor(eventtype.NewRelationship))
	ev.AltId = nil
	ev.OccurredTime = batchT0
	ev.ProcessedTime = batchT0.Add(1)
	ev.Anchors = []resolvedevent.ResolvedAnchor{
		{AnchorType: "customer", AnchorToken: "cust-1"},
		{AnchorType: "group", AnchorToken: "group-1"},
	}
	return ev
}

func TestANewRelationshipEventIsStoredWithItsAnchors(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrong payload=%v", wrong), func(t *testing.T) {
			api := &envelopeRecordingApi{MockApi: persistingMockApi()}
			worker := &EventPersistenceWorker{Api: api}
			ctx := core.WithTenant(context.Background(), "acme")
			ev := relationshipEvent()
			if wrong {
				ev.Payload = matchingPayloadFor(eventtype.Measurement)
			}
			result, err := worker.PersistEvent(ctx, ev)
			if wrong {
				if !errors.Is(err, ErrDeterministic) {
					t.Fatalf("mismatched payload: %v", err)
				}
				if len(api.parents) != 0 || len(api.anchors) != 0 {
					t.Fatal("mismatched payload wrote rows")
				}
				return
			}
			if err != nil {
				t.Fatalf("persist relationship: %v", err)
			}
			if result == nil || result.Deduped {
				t.Fatal("relationship must reach anchor persistence")
			}
			if len(api.parents) != 1 || len(api.anchors) != 2 {
				t.Fatalf("got %d parents / %d anchors", len(api.parents), len(api.anchors))
			}
			parent := api.parents[0]
			wantID, err := model.DeriveEventIdForPayload("acme", parent, ev.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if parent.EventType != 0 || !bytes.Equal(parent.EventId, wantID) || !parent.OccurredTime.Equal(ev.OccurredTime) {
				t.Fatalf("wrong parent: %+v", parent)
			}
			for i, anchor := range api.anchors {
				if !bytes.Equal(anchor.EventId, wantID) || anchor.EventType != 0 ||
					!anchor.OccurredTime.Equal(ev.OccurredTime) || anchor.DeviceToken != ev.SourceDeviceToken ||
					anchor.AnchorType != ev.Anchors[i].AnchorType || anchor.AnchorToken != ev.Anchors[i].AnchorToken {
					t.Errorf("wrong anchor: %+v", anchor)
				}
			}
		})
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"math/rand"
	"testing"
	"time"
)

// legacyDeriveEventIdForPayload is DeriveEventIdForPayload as it was when it type-switched on
// device-management's measurements payload, kept verbatim so the seam that replaced the switch
// is held to it. Every stored event id was derived by this function (or by its predecessor,
// which it equals on a payload with no derived entry): an input the two disagree on would give
// one event two ids, and a redelivery of it would store twice.
func legacyDeriveEventIdForPayload(tenantId string, event *Event, payload any) ([]byte, error) {
	switch p := payload.(type) {
	case *resolvedevent.ResolvedMeasurementsPayload:
		payload = p.IdentityView()
	case resolvedevent.ResolvedMeasurementsPayload:
		payload = p.IdentityView()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return DeriveEventId(tenantId, event, encoded), nil
}

func parityStrPtr(r *rand.Rand) *string {
	if r.Intn(3) == 0 {
		return nil
	}
	s := fmt.Sprintf("s%d", r.Intn(50))
	return &s
}

func parityMeasurements(r *rand.Rand) *resolvedevent.ResolvedMeasurementsPayload {
	p := &resolvedevent.ResolvedMeasurementsPayload{}
	if r.Intn(10) == 0 {
		return p // no samples at all
	}
	for i := 0; i < 1+r.Intn(4); i++ {
		sample := resolvedevent.ResolvedMeasurementsEntry{OccurredTime: time.Unix(1_700_000_000+int64(r.Intn(1e6)), int64(r.Intn(1e9))).UTC()}
		for j := 0; j < r.Intn(5); j++ {
			e := resolvedevent.ResolvedMeasurementEntry{
				Name:     fmt.Sprintf("m%d", r.Intn(20)),
				Value:    fmt.Sprintf("%g", r.NormFloat64()*100),
				Unit:     parityStrPtr(r),
				DataType: parityStrPtr(r),
				Derived:  r.Intn(3) == 0,
			}
			if r.Intn(2) == 0 {
				c := uint64(r.Intn(5))
				e.Classifier = &c
			}
			sample.Entries = append(sample.Entries, e)
		}
		p.Entries = append(p.Entries, sample)
	}
	return p
}

// parityCorpus is every payload shape that reaches DeriveEventIdForPayload — each resolved kind
// as a pointer, as a value and as a typed nil — plus shapes that never should, which must
// agree too: an untyped nil, a measurements payload nested inside something else, scalars.
func parityCorpus() []any {
	r := rand.New(rand.NewSource(1732))
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	tt, tid := "AREA", uint64(9)
	lat, lon := "51.5", "-0.12"
	corpus := []any{
		nil,
		(*resolvedevent.ResolvedMeasurementsPayload)(nil),
		(*resolvedevent.ResolvedLocationsPayload)(nil),
		(*resolvedevent.ResolvedAlertsPayload)(nil),
		(*resolvedevent.ResolvedStateChangePayload)(nil),
		(*resolvedevent.ResolvedNewRelationshipPayload)(nil),
		resolvedevent.ResolvedMeasurementsPayload{},
		&resolvedevent.ResolvedMeasurementsPayload{},
		&resolvedevent.ResolvedLocationsPayload{Entries: []resolvedevent.ResolvedLocationEntry{{Latitude: &lat, Longitude: &lon, OccurredTime: at}}},
		resolvedevent.ResolvedLocationsPayload{Entries: []resolvedevent.ResolvedLocationEntry{{Latitude: &lat, OccurredTime: at}}},
		&resolvedevent.ResolvedAlertsPayload{Entries: []resolvedevent.ResolvedAlertEntry{{Type: "t", Level: 2, Message: "m", Source: "s", OccurredTime: at}}},
		resolvedevent.ResolvedAlertsPayload{},
		&resolvedevent.ResolvedStateChangePayload{State: string(presence.StateConnected), Reason: "r"},
		resolvedevent.ResolvedStateChangePayload{State: string(presence.StateDemoted)},
		&resolvedevent.ResolvedNewRelationshipPayload{RelationshipTypeId: 3, TargetType: &tt, TargetId: &tid},
		resolvedevent.ResolvedNewRelationshipPayload{RelationshipTypeId: 3},
		"a string",
		42,
		map[string]any{"k": "v"},
		[]any{nil, 1},
	}
	for i := 0; i < 2000; i++ {
		p := parityMeasurements(r)
		corpus = append(corpus, p, *p)
		if i%50 == 0 {
			// Wrapped: the view is not taken through a container, before or after.
			corpus = append(corpus, []any{p}, map[string]any{"p": p}, struct {
				P *resolvedevent.ResolvedMeasurementsPayload
			}{p})
		}
	}
	return corpus
}

// The seam that replaced the type switch derives the SAME event id for every payload the
// switch did. A drift here is a duplicate row for every redelivered event of that shape.
func TestEventIdForPayloadMatchesTheTypeSwitchItReplaced(t *testing.T) {
	corpus := parityCorpus()
	derivedSeen, rawSeen := 0, 0
	for i, payload := range corpus {
		for _, alt := range []*string{nil, ptrStr(""), ptrStr("a-1")} {
			ev := &Event{DeviceToken: "dev-1", EventType: esmodel.Measurement,
				OccurredTime: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC), AltId: rdb.NullStrOf(alt)}
			want, werr := legacyDeriveEventIdForPayload("acme", ev, payload)
			got, gerr := DeriveEventIdForPayload("acme", ev, payload)
			if (werr == nil) != (gerr == nil) {
				t.Fatalf("corpus[%d] %T: errors disagree: legacy %v, now %v", i, payload, werr, gerr)
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("corpus[%d] %T: event id moved\nlegacy %x\nnow    %x", i, payload, want, got)
			}
		}
		if p, ok := payload.(*resolvedevent.ResolvedMeasurementsPayload); ok && p != nil {
			if p.IdentityView() != p {
				derivedSeen++
			} else {
				rawSeen++
			}
		}
	}
	// The corpus is only evidence if it exercised both branches of the view.
	if derivedSeen < 100 || rawSeen < 100 {
		t.Fatalf("corpus too thin: %d payloads with a derived entry, %d without", derivedSeen, rawSeen)
	}
}

func ptrStr(s string) *string { return &s }

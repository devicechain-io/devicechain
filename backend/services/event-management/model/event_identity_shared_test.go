// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity/eventid"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// event-processing's duplicate guard remembers an event by eventid.OfResolved. It must be the id
// this service stores the same event under, including the calculated-values view and an alternate
// id as stored (trimmed, absent when blank), or the two disagree about which events are one.
func TestTheStoredEventIdIsTheOneTheDuplicateGuardUses(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 7, time.UTC)
	alt := " alt-1 "
	for _, withDerived := range []bool{false, true} {
		for _, altId := range []*string{nil, &alt} {
			entries := []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: "20.5"}}
			if withDerived {
				entries = append(entries, resolvedevent.ResolvedMeasurementEntry{Name: "f", Value: "68.9", Derived: true})
			}
			ev := &resolvedevent.ResolvedEvent{
				SourceDeviceToken: "d1", OccurredTime: at, EventType: eventtype.Measurement, AltId: altId,
				Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{OccurredTime: at, Entries: entries}}},
			}
			// What the persistence worker builds (EventPersistenceWorker.baseEvent).
			stored := &Event{DeviceToken: ev.SourceDeviceToken, OccurredTime: ev.OccurredTime, AltId: rdb.NullStrOf(ev.AltId), EventType: ev.EventType}
			want, err := DeriveEventIdForPayload("acme", stored, ev.Payload)
			if err != nil {
				t.Fatal(err)
			}
			got, err := eventid.OfResolved("acme", ev)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("derived=%v alt=%v: the guard's identity differs from the stored event id", withDerived, altId != nil)
			}
		}
	}
}

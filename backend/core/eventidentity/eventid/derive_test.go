// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventid

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

var at = time.Date(2026, 11, 4, 12, 0, 0, 123, time.UTC)

func measurement(withDerived bool) *resolvedevent.ResolvedEvent {
	entries := []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: "20.5"}}
	if withDerived {
		entries = append(entries, resolvedevent.ResolvedMeasurementEntry{Name: "fahrenheit", Value: "68.9", Derived: true})
	}
	return &resolvedevent.ResolvedEvent{
		SourceDeviceToken: "d1", OccurredTime: at, EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{OccurredTime: at, Entries: entries}}},
	}
}

// The fast path equals the json.Marshal derivation it replaces, byte for byte.
func TestDeriveEqualsTheJSONOracle(t *testing.T) {
	for _, withDerived := range []bool{false, true} {
		ev := measurement(withDerived)
		e := Event{DeviceToken: ev.SourceDeviceToken, EventType: uint64(ev.EventType), OccurredTime: ev.OccurredTime}
		got, err := Derive("acme", e, ev.Payload)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(eventidentity.Payload(ev.Payload))
		if want := Hash("acme", e, encoded); !bytes.Equal(got, want) {
			t.Fatalf("derived=%v: the fast path and the json oracle disagree", withDerived)
		}
	}
}

// The calculated values are not part of the identity: one reading keeps one id whether or not a
// formula computed an extra value from it.
func TestCalculatedValuesAreNotPartOfTheIdentity(t *testing.T) {
	plain, _ := OfResolved("acme", measurement(false))
	derived, _ := OfResolved("acme", measurement(true))
	if !bytes.Equal(plain, derived) {
		t.Fatal("a calculated value moved the event identity")
	}
}

// The alternate id is read the way event-management stores it: trimmed, and absent when blank.
func TestOfResolvedReadsTheAlternateIdAsStored(t *testing.T) {
	base, _ := OfResolved("acme", measurement(false))
	blank, trimmed := " ", "  x "
	ev := measurement(false)
	ev.AltId = &blank
	if got, _ := OfResolved("acme", ev); !bytes.Equal(got, base) {
		t.Fatal("a blank alternate id must read as absent")
	}
	ev.AltId = &trimmed
	a, _ := OfResolved("acme", ev)
	x := "x"
	ev.AltId = &x
	if b, _ := OfResolved("acme", ev); !bytes.Equal(a, b) {
		t.Fatal("an alternate id must be read trimmed")
	}
	if bytes.Equal(a, base) {
		t.Fatal("an alternate id must be part of the identity")
	}
}

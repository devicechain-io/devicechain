// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/eventlimit"
)

// ErrTooManyReadings marks an event refused for carrying more readings than
// eventlimit.MaxReadingsPerEvent admits.
//
// It exists so a caller can count an oversized event apart from malformed input: both are
// terminal decode failures on the same path, and "a fleet batching too much into one message"
// asks for different work from "a fleet sending broken payloads" — the first is a firmware
// batching size, the second is a firmware bug.
var ErrTooManyReadings = errors.New("too many readings in one event")

// ReadingCount reports how many readings a payload carries — the unit
// eventlimit.MaxReadingsPerEvent counts — together with the name of its kind, and false for a
// payload kind it does not know.
//
// For measurements it is the metric keys summed over the entries, NOT the entry count: one
// entry carries a map, and one entry holding 300 keys is 300 stored readings. For locations and
// alerts every entry is exactly one reading.
//
// 🔴 THE FALL-THROUGH REFUSES rather than answering 1. Every payload kind is named explicitly,
// including the one that genuinely is a single unit. The difference only shows up for a kind
// added LATER: an entry-carrying payload with no case here would be metered as one reading —
// uncapped, while CheckReadingCount still read as though it were guarding the fan-out.
func ReadingCount(payload interface{}) (kind string, count int, ok bool) {
	switch p := payload.(type) {
	case *UnresolvedMeasurementsPayload:
		n := 0
		for i := range p.Entries {
			n += len(p.Entries[i].Measurements)
		}
		return "measurement", n, true
	case *UnresolvedLocationsPayload:
		return "location", len(p.Entries), true
	case *UnresolvedAlertsPayload:
		return "alert", len(p.Entries), true
	case *UnresolvedNewRelationshipPayload:
		// A relationship carries no entries: it is one indivisible unit of work.
		return "relationship", 1, true
	}
	return "", 0, false
}

// CheckReadingCount refuses a payload that carries more than eventlimit.MaxReadingsPerEvent
// readings. It is the ONE check a producer applies to an event it builds from a client's own
// message; the limit is the ONE number, in core/eventlimit, that the gateway emitter's split
// reads too.
//
// 🔴 IT REFUSES, IT DOES NOT TRUNCATE. The error text is the HTTP 400 body a device reads, so
// it names the count, the limit and the remedy.
func CheckReadingCount(payload interface{}) error {
	kind, count, ok := ReadingCount(payload)
	if !ok {
		// Deliberately NOT ErrTooManyReadings: this is an unmetered payload kind, not an
		// oversized event, and counting it as one would put a code defect into the counter an
		// operator reads as "the fleet is batching too much". It is still terminal.
		return fmt.Errorf("payload of type %T has no reading count, so the per-event limit cannot be "+
			"applied to it; give model.ReadingCount a case for it", payload)
	}
	if count <= eventlimit.MaxReadingsPerEvent {
		return nil
	}
	return fmt.Errorf("%w: %s payload carries %d readings, which is over the platform limit of %d readings "+
		"per event; split it across messages (nothing was stored — the message is refused whole, not truncated)",
		ErrTooManyReadings, kind, count, eventlimit.MaxReadingsPerEvent)
}

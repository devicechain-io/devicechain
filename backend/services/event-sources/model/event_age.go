// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// CheckEventAge applies eventtime.CheckAge to an event's envelope time and to every entry of
// its payload (each entry through eventtime.Reported, so an entry with no time of its own is
// judged by the envelope's). It names the offending entry by kind and index, in the words the
// decoder's other time refusals use, so a device reads one vocabulary.
//
// It is the ONE walker over every payload kind, called by both places that refuse a message
// for its age: the event-sources decoder and device-management resolution. The envelope is
// checked even when every entry has a time of its own, because the event's base row is
// stored at the envelope's time.
//
// A zero processed time disables the check, as it disables eventtime.CheckAge.
//
// 🔴 THE FALL-THROUGH REFUSES, as ReadingCount's does: a payload kind added later with no case
// here would otherwise have its entry times unchecked while this read as though it guarded
// them. The refusal deliberately does NOT wrap eventtime.ErrTooOld: it is a missing case in
// this function, not an old reading, and counting it as one would hide a code defect in the
// counter an operator reads as "devices with bad clocks".
func CheckEventAge(occurred, processed time.Time, payload interface{}) error {
	if processed.IsZero() {
		return nil
	}
	if err := eventtime.CheckAge(occurred, processed); err != nil {
		return fmt.Errorf("envelope occurredTime: %w", err)
	}
	entry := func(kind string, i int, at *time.Time) error {
		if err := eventtime.CheckAge(eventtime.Reported(at, occurred), processed); err != nil {
			return fmt.Errorf("%s entry %d occurredTime: %w", kind, i, err)
		}
		return nil
	}
	switch p := payload.(type) {
	case *UnresolvedMeasurementsPayload:
		for i := range p.Entries {
			if err := entry("measurement", i, p.Entries[i].OccurredTime); err != nil {
				return err
			}
		}
		return nil
	case *UnresolvedLocationsPayload:
		for i := range p.Entries {
			if err := entry("location", i, p.Entries[i].OccurredTime); err != nil {
				return err
			}
		}
		return nil
	case *UnresolvedAlertsPayload:
		for i := range p.Entries {
			if err := entry("alert", i, p.Entries[i].OccurredTime); err != nil {
				return err
			}
		}
		return nil
	case *UnresolvedNewRelationshipPayload, *UnresolvedStateChangePayload:
		// No entry times: a relationship is one unit, and a state change's OccurredTime field
		// is a descriptive copy of the envelope's, which was checked above.
		return nil
	}
	return fmt.Errorf("payload of type %T has no event-time rule, so its age cannot be checked; "+
		"give model.CheckEventAge a case for it", payload)
}

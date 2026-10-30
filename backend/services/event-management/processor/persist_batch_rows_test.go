// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// A calculated metric is an extra ENTRY of the same event, so a message stores one row per
// entry, derived or not; any other event is one row. Rows, not messages, are the unit that
// stays comparable between a profile with formulas and one without.
func TestEventRowsCountsEntriesIncludingDerived(t *testing.T) {
	at := time.Now()
	ev := fenceCostMeasurement("rows", at, 0)
	if got := eventRows(&ev); got != 3 {
		t.Errorf("three raw entries = %d rows; want 3", got)
	}
	sample := &ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0]
	sample.Entries = append(sample.Entries, resolvedevent.ResolvedMeasurementEntry{Name: "f", Value: "1", Derived: true})
	if got := eventRows(&ev); got != 4 {
		t.Errorf("three raw entries and one derived = %d rows; want 4", got)
	}
	other := resolvedevent.ResolvedEvent{}
	if got := eventRows(&other); got != 1 {
		t.Errorf("an event with no measurement payload = %d rows; want 1", got)
	}
}

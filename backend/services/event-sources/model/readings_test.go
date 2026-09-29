// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
)

func TestReadingCountKnowsEveryPayloadKind(t *testing.T) {
	for _, c := range []struct {
		payload interface{}
		kind    string
		count   int
	}{
		{&UnresolvedMeasurementsPayload{Entries: []UnresolvedMeasurementsEntry{
			{Measurements: map[string]string{"a": "1", "b": "2"}},
			{Measurements: map[string]string{"c": "3"}},
		}}, "measurement", 3},
		{&UnresolvedLocationsPayload{Entries: make([]UnresolvedLocationEntry, 2)}, "location", 2},
		{&UnresolvedAlertsPayload{Entries: make([]UnresolvedAlertEntry, 2)}, "alert", 2},
		{&UnresolvedNewRelationshipPayload{}, "relationship", 1},
	} {
		kind, n, ok := ReadingCount(c.payload)
		if !ok || kind != c.kind || n != c.count {
			t.Errorf("ReadingCount(%T) = (%q, %d, %v), want (%q, %d, true)", c.payload, kind, n, ok, c.kind, c.count)
		}
	}
	if _, n, ok := ReadingCount(struct{}{}); ok || n != 0 {
		t.Errorf("an unknown kind must answer (0, false), got (%d, %v)", n, ok)
	}
}

// The fall-through is the part nothing in production reaches today, which is exactly why it
// is pinned: a new entry-carrying kind with no case must be refused, not metered as one
// reading — and not reported as an oversized event, which would send an operator to the
// firmware for a code defect.
func TestAnUnmeteredPayloadKindIsRefusedButNotAsOversized(t *testing.T) {
	type payloadNobodyTaughtItAbout struct{ Entries []int }
	err := CheckReadingCount(&payloadNobodyTaughtItAbout{Entries: make([]int, 5000)})
	if err == nil {
		t.Fatal("an unmetered payload kind passed the limit; the fall-through fails OPEN")
	}
	if errors.Is(err, ErrTooManyReadings) {
		t.Errorf("an unmetered kind was reported as an oversized event: %v", err)
	}
	if err := CheckReadingCount(&UnresolvedNewRelationshipPayload{}); err != nil {
		t.Errorf("a relationship is one reading and must pass: %v", err)
	}
}

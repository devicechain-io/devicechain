// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CheckEventAge walks the envelope and every entry of every payload kind, names the entry it
// refuses, and refuses a payload kind it has no rule for rather than waving it through.
func TestCheckEventAgeWalksEveryEntryAndTheEnvelope(t *testing.T) {
	processed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fresh := processed.Add(-time.Hour)
	ancient := processed.Add(-eventtime.MaxAge - time.Second)
	at := func(t time.Time) *time.Time { return &t }

	refusedAs := func(t *testing.T, err error, want string) {
		t.Helper()
		require.Error(t, err)
		assert.True(t, errors.Is(err, eventtime.ErrTooOld), "an age refusal is an ErrTooOld: %v", err)
		assert.Contains(t, err.Error(), want)
	}

	// The third entry of three is the one named, for every entry-carrying kind.
	refusedAs(t, CheckEventAge(fresh, processed, &UnresolvedMeasurementsPayload{Entries: []UnresolvedMeasurementsEntry{
		{OccurredTime: at(fresh)}, {OccurredTime: nil}, {OccurredTime: at(ancient)}}}), "measurement entry 2 occurredTime")
	refusedAs(t, CheckEventAge(fresh, processed, &UnresolvedLocationsPayload{Entries: []UnresolvedLocationEntry{
		{OccurredTime: at(fresh)}, {OccurredTime: nil}, {OccurredTime: at(ancient)}}}), "location entry 2 occurredTime")
	refusedAs(t, CheckEventAge(fresh, processed, &UnresolvedAlertsPayload{Entries: []UnresolvedAlertEntry{
		{OccurredTime: at(fresh)}, {OccurredTime: nil}, {OccurredTime: at(ancient)}}}), "alert entry 2 occurredTime")

	// An ancient envelope is refused even though every entry carries a fresh time of its own:
	// the base row is stored at the envelope's time.
	refusedAs(t, CheckEventAge(ancient, processed, &UnresolvedMeasurementsPayload{Entries: []UnresolvedMeasurementsEntry{
		{OccurredTime: at(fresh)}}}), "envelope occurredTime")
	// The kinds with no entry times are judged by the envelope alone.
	refusedAs(t, CheckEventAge(ancient, processed, &UnresolvedNewRelationshipPayload{}), "envelope occurredTime")
	refusedAs(t, CheckEventAge(ancient, processed, &UnresolvedStateChangePayload{}), "envelope occurredTime")

	// Accepted: every time fresh, and an entry with no time under a fresh envelope.
	assert.NoError(t, CheckEventAge(fresh, processed, &UnresolvedMeasurementsPayload{Entries: []UnresolvedMeasurementsEntry{
		{OccurredTime: at(fresh)}, {OccurredTime: nil}}}))
	assert.NoError(t, CheckEventAge(fresh, processed, &UnresolvedNewRelationshipPayload{}))
	assert.NoError(t, CheckEventAge(fresh, processed, &UnresolvedStateChangePayload{}))
	// A zero receipt disables the check, as it disables eventtime.CheckAge.
	assert.NoError(t, CheckEventAge(ancient, time.Time{}, &UnresolvedMeasurementsPayload{}))

	// A kind with no rule is refused, and not as an old reading.
	err := CheckEventAge(fresh, processed, struct{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no event-time rule")
	assert.False(t, errors.Is(err, eventtime.ErrTooOld), "a missing case is not an old reading")
}

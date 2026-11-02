// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dropped-measurement record rides on the resolved payload (resolvedevent.DroppedMeasurementEntry)
// but is NOT part of the event: the id of a reading is the same with and without it, and it is
// the id the same reading had before the record existed (meterRawJSON is that document).
func TestADroppedRecordIsOutsideTheEventId(t *testing.T) {
	withDrop := func() *resolvedevent.ResolvedMeasurementsPayload {
		p := meterPayload()
		p.Entries[0].Dropped = []resolvedevent.DroppedMeasurementEntry{
			{Name: "status_text", Reason: resolvedevent.DropUndeclaredNonNumeric, Value: "ok"}}
		return p
	}
	raw, err := DeriveEventIdForPayload("acme", meterEvent(), meterPayload())
	require.NoError(t, err)
	got, err := DeriveEventIdForPayload("acme", meterEvent(), withDrop())
	require.NoError(t, err)
	assert.Equal(t, raw, got, "a drop record moved the event id")
	assert.Equal(t, DeriveEventId("acme", meterEvent(), []byte(meterRawJSON)), got)

	byValue, err := DeriveEventIdForPayload("acme", meterEvent(), *withDrop())
	require.NoError(t, err)
	assert.Equal(t, raw, byValue)

	// Negative control: hashed without the identity view, the record is in the document and
	// the id differs, so it is the view that keeps it out.
	assert.NotEqual(t, raw, DeriveEventId("acme", meterEvent(), mustJSON(t, withDrop())))
}

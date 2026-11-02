// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func failedRecordBytes(t *testing.T, reason resolvedevent.FailureReason, service, errText string, payload []byte) []byte {
	t.Helper()
	b, err := proto.Marshal(&resolvedevent.PFailedEvent{Reason: reason, Service: service, Error: errText, Payload: payload})
	require.NoError(t, err)
	return b
}

func TestParseFailedRecordFromEachWriter(t *testing.T) {
	// device-management archives the unresolved event: the device is the sender's claim.
	un, err := proto.Marshal(&esproto.PUnresolvedEvent{Device: "ghost-1", EventType: 2})
	require.NoError(t, err)
	rec, err := ParseFailedRecord(failedRecordBytes(t, resolvedevent.FailureReason_DeviceNotFound, "device-management", "no such device", un))
	require.NoError(t, err)
	assert.Equal(t, "ghost-1", rec.DeviceToken)
	require.NotNil(t, rec.EventType)
	assert.EqualValues(t, 2, *rec.EventType)
	assert.EqualValues(t, resolvedevent.FailureReason_DeviceNotFound, rec.Reason)
	assert.Equal(t, "no such device", rec.Error)

	// event-management archives the resolved event it could not store.
	re, err := proto.Marshal(&resolvedevent.PResolvedEvent{SourceDeviceToken: "real-1", EventType: 3})
	require.NoError(t, err)
	rec, err = ParseFailedRecord(failedRecordBytes(t, resolvedevent.FailureReason_ApiCallFailed, "event-management", "insert: connection to 10.1.2.3:5432 refused", re))
	require.NoError(t, err)
	assert.Equal(t, "real-1", rec.DeviceToken)
	assert.NotContains(t, rec.Error, "10.1.2.3", "an internal call's text is the platform's wiring, not the tenant's traffic")
	assert.NotEmpty(t, rec.Error)
}

// An Invalid record archives bytes that would not parse: they are never read, and the record
// is still a record.
func TestParseFailedRecordInvalidReadsNoPayload(t *testing.T) {
	junk := []byte{0xff, 0xfe, 0x01}
	rec, err := ParseFailedRecord(failedRecordBytes(t, resolvedevent.FailureReason_Invalid, "event-management", "message could not be parsed", junk))
	require.NoError(t, err)
	assert.Empty(t, rec.DeviceToken)
	assert.Nil(t, rec.EventType)
	assert.EqualValues(t, resolvedevent.FailureReason_Invalid, rec.Reason)
}

// A payload that will not decode loses the device, not the record.
func TestParseFailedRecordSurvivesAMalformedPayload(t *testing.T) {
	rec, err := ParseFailedRecord(failedRecordBytes(t, resolvedevent.FailureReason_Unauthenticated, "device-management", "bad credential", []byte{0xff, 0xff, 0xff}))
	require.NoError(t, err)
	assert.Empty(t, rec.DeviceToken)
	assert.EqualValues(t, resolvedevent.FailureReason_Unauthenticated, rec.Reason)
}

func TestParseFailedRecordRefusesAnEnvelopeThatDoesNotDecode(t *testing.T) {
	_, err := ParseFailedRecord([]byte{0xff, 0xff, 0xff, 0xff})
	assert.Error(t, err)
}

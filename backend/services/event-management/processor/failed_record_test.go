// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"

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

// An event-management Invalid record may archive junk bytes. Decoding them is non-fatal:
// it still records the refusal, without a device or event type.
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

// Match OnUnresolvedEvent's archived payload and failed-event envelope.
func validationRefusalBytes(t *testing.T, device, integration string) []byte {
	t.Helper()
	event := &esmodel.UnresolvedEvent{
		Device: device, IntegrationIdentifier: integration, EventType: eventtype.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{},
	}
	if device == "" && integration != "" {
		event.EventType = eventtype.IntegrationUplink
		event.Payload = &esmodel.UnresolvedIntegrationUplinkPayload{}
	}
	payload, err := esproto.MarshalUnresolvedEvent(event)
	require.NoError(t, err)
	failed, err := resolvedevent.MarshalFailedEvent(resolvedevent.NewFailedEvent(uint(resolvedevent.FailureReason_Invalid),
		areaDeviceManagement, "event could not be resolved", fmt.Errorf("value is not numeric"), payload))
	require.NoError(t, err)
	return failed
}

func TestAValidationRefusalIsIndexedUnderItsDevice(t *testing.T) {
	for _, tc := range []struct{ name, device, integration, want string }{
		{"device", "pump-7", "", "pump-7"},
		{"integration", "", "uplink-7", "uplink-7"},
		{"device takes precedence", "pump-7", "uplink-7", "pump-7"},
		{"overflow marker is sanitized", model.OverflowDeviceToken, "", model.ClaimedDeviceToken(model.OverflowDeviceToken)},
		{"long token is bounded", strings.Repeat("x", 300), "", model.ClaimedDeviceToken(strings.Repeat("x", 300))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := ParseFailedRecord(validationRefusalBytes(t, tc.device, tc.integration))
			require.NoError(t, err)
			assert.Equal(t, tc.want, rec.DeviceToken)
			require.NotNil(t, rec.EventType)
			wantType := eventtype.Measurement
			if tc.device == "" {
				wantType = eventtype.IntegrationUplink
			}
			assert.EqualValues(t, wantType, *rec.EventType)
			assert.True(t, rec.PayloadRetained)
		})
	}
}

func TestParseFailedRecordInvalidWithoutReadableIdentity(t *testing.T) {
	for _, service := range []string{areaDeviceManagement, areaEventManagement} {
		for _, tc := range []struct {
			name    string
			payload []byte
		}{
			{"no payload", nil},
			{"junk", []byte{0xff, 0xfe, 0x01}},
			// An unknown protobuf field decodes successfully to an otherwise empty message.
			{"empty message", []byte{0xf8, 0x07, 0x01}},
		} {
			t.Run(service+"/"+tc.name, func(t *testing.T) {
				rec, err := ParseFailedRecord(failedRecordBytes(t, resolvedevent.FailureReason_Invalid, service, "invalid", tc.payload))
				require.NoError(t, err)
				assert.Empty(t, rec.DeviceToken)
				assert.Nil(t, rec.EventType)
				assert.Equal(t, len(tc.payload) > 0, rec.PayloadRetained)
			})
		}
	}
}

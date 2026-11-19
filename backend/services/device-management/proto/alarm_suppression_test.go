// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
)

func TestAlarmSuppressionRoundTripNanoseconds(t *testing.T) {
	event := compatEvent(nil)
	until := time.Date(2026, 11, 18, 12, 34, 56, 123456789, time.FixedZone("offset", -5*3600))
	event.SuppressedUntil, event.SuppressionWindowToken = &until, "maintenance"
	event.RaisedDuringSuppressionToken = "cycle-window"
	wire, err := MarshalAlarmStateChangeEvent(event)
	require.NoError(t, err)
	got, err := UnmarshalAlarmStateChangeEvent(wire)
	require.NoError(t, err)
	require.NotNil(t, got.SuppressedUntil)
	require.True(t, until.Equal(*got.SuppressedUntil))
	require.Equal(t, 123456789, got.SuppressedUntil.Nanosecond())
	require.Equal(t, "maintenance", got.SuppressionWindowToken)
	require.Equal(t, "cycle-window", got.RaisedDuringSuppressionToken)
}

func TestAlarmSuppressionAbsentFields(t *testing.T) {
	wire, err := MarshalAlarmStateChangeEvent(compatEvent(nil))
	require.NoError(t, err)
	got, err := UnmarshalAlarmStateChangeEvent(wire)
	require.NoError(t, err)
	require.Nil(t, got.SuppressedUntil)
	require.Empty(t, got.SuppressionWindowToken)
	require.Empty(t, got.RaisedDuringSuppressionToken)
}

func TestAlarmSuppressionMalformedUntilDecodesNotSuppressed(t *testing.T) {
	wire, err := MarshalAlarmStateChangeEvent(compatEvent(nil))
	require.NoError(t, err)
	var pb PAlarmStateChangeEvent
	require.NoError(t, gproto.Unmarshal(wire, &pb))
	pb.SuppressedUntil, pb.SuppressionWindowToken = "not-a-time", "window"
	pb.RaisedDuringSuppressionToken = "history"
	wire, err = gproto.Marshal(&pb)
	require.NoError(t, err)
	got, err := UnmarshalAlarmStateChangeEvent(wire)
	require.NoError(t, err, "a malformed optional deadline must not lose the alarm event")
	require.Equal(t, "alarm-1", got.AlarmToken)
	require.Nil(t, got.SuppressedUntil)
	require.Empty(t, got.SuppressionWindowToken)
	require.Equal(t, "history", got.RaisedDuringSuppressionToken)
}

func TestAlarmSuppressionUntilWithoutTokenDecodesNotSuppressed(t *testing.T) {
	event := compatEvent(nil)
	until := event.RaisedTime.Add(time.Hour)
	event.SuppressedUntil = &until
	wire, err := MarshalAlarmStateChangeEvent(event)
	require.NoError(t, err)
	got, err := UnmarshalAlarmStateChangeEvent(wire)
	require.NoError(t, err)
	require.Nil(t, got.SuppressedUntil)
	require.Empty(t, got.SuppressionWindowToken)
}

func TestAlarmSuppressionOldProducerBytes(t *testing.T) {
	// Frozen wire payload containing only tags 1, 2, 9, 14 and 15. No current
	// serializer participates in constructing this pre-suppression producer message.
	wire, err := hex.DecodeString("0a065241495345441207616c61726d2d314a054d414a4f527214323032362d31312d31375431303a30303a30305a7a14323032362d31312d31375431303a30303a30305a")
	require.NoError(t, err)
	got, err := UnmarshalAlarmStateChangeEvent(wire)
	require.NoError(t, err)
	require.Equal(t, "alarm-1", got.AlarmToken)
	require.Nil(t, got.SuppressedUntil)
	require.Empty(t, got.SuppressionWindowToken)
	require.Empty(t, got.RaisedDuringSuppressionToken)
}

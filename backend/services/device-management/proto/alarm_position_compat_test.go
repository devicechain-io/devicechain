// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/devicechain-io/dc-device-management/model"
)

// olderAlarmEventDescriptor is PAlarmStateChangeEvent as a release before position_derived
// compiled it: the current descriptor with field 20 and its synthetic oneof removed.
func olderAlarmEventDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := protodesc.ToFileDescriptorProto((&PAlarmStateChangeEvent{}).ProtoReflect().Descriptor().ParentFile())
	var msg *descriptorpb.DescriptorProto
	for _, m := range fd.MessageType {
		if m.GetName() == "PAlarmStateChangeEvent" {
			msg = m
		}
	}
	require.NotNil(t, msg)
	kept := msg.Field[:0]
	removed := false
	for _, f := range msg.Field {
		if f.GetNumber() == 20 {
			removed = true
			continue
		}
		kept = append(kept, f)
	}
	require.True(t, removed, "field 20 is not position_derived any more")
	msg.Field = kept
	oneofs := msg.OneofDecl[:0]
	for _, o := range msg.OneofDecl {
		if o.GetName() != "_position_derived" {
			oneofs = append(oneofs, o)
		}
	}
	msg.OneofDecl = oneofs
	file, err := protodesc.NewFile(fd, nil)
	require.NoError(t, err)
	return file.Messages().ByName("PAlarmStateChangeEvent")
}

func compatEvent(verdict *bool) *model.AlarmStateChangeEvent {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	return &model.AlarmStateChangeEvent{
		EventType: model.AlarmEventRaised, AlarmToken: "alarm-1", AlarmKey: "fence:yard",
		State: "ACTIVE", Severity: "MAJOR", RaisedTime: at, OccurredTime: at,
		DeviceToken: "pump-1", DeviceTypeToken: "pump", PositionDerived: verdict,
	}
}

// A new producer's event, read by a consumer built before the field: it decodes, every
// field it knows is unchanged, and the verdict rides as an unknown field.
func TestAlarmPositionNewProducerOldConsumer(t *testing.T) {
	yes := true
	wire, err := MarshalAlarmStateChangeEvent(compatEvent(&yes))
	require.NoError(t, err)

	old := dynamicpb.NewMessage(olderAlarmEventDescriptor(t))
	require.NoError(t, gproto.Unmarshal(wire, old))
	fields := old.Descriptor().Fields()
	require.Equal(t, "alarm-1", old.Get(fields.ByName("alarm_token")).String())
	require.Equal(t, "MAJOR", old.Get(fields.ByName("severity")).String())
	require.Equal(t, "pump-1", old.Get(fields.ByName("device_token")).String())
	require.NotEmpty(t, old.GetUnknown(), "the verdict must survive as an unknown field")
}

// An old producer's event, read by this consumer: it decodes, and the verdict is ABSENT
// (unknown), never a plausible false.
func TestAlarmPositionOldProducerNewConsumer(t *testing.T) {
	wire, err := MarshalAlarmStateChangeEvent(compatEvent(nil))
	require.NoError(t, err)
	old := dynamicpb.NewMessage(olderAlarmEventDescriptor(t))
	require.NoError(t, gproto.Unmarshal(wire, old))
	oldWire, err := gproto.Marshal(old)
	require.NoError(t, err)

	got, err := UnmarshalAlarmStateChangeEvent(oldWire)
	require.NoError(t, err)
	require.Nil(t, got.PositionDerived)
	require.Equal(t, "alarm-1", got.AlarmToken)
	require.Equal(t, "pump-1", got.DeviceToken)

	no := false
	wire, err = MarshalAlarmStateChangeEvent(compatEvent(&no))
	require.NoError(t, err)
	got, err = UnmarshalAlarmStateChangeEvent(wire)
	require.NoError(t, err)
	require.NotNil(t, got.PositionDerived, "an explicit false must stay distinguishable from absent")
	require.False(t, *got.PositionDerived)
}

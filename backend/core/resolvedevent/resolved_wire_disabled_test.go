// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"bytes"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func disabledPresence(disabled bool) *ResolvedEvent {
	at := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	return &ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: "dev-1", ProfileVersionToken: "p@1",
		OccurredTime: at, ProcessedTime: at, EventType: eventtype.StateChange,
		SourceDeviceDisabled: disabled,
		Payload:              &ResolvedStateChangePayload{State: "DISCONNECTED", Reason: "close", SessionId: 7},
	}
}

// 🔴 The mark is past field 15, so its TAG is two bytes. The direct writer's bytes, decoded by
// the GENERATED decoder (not by this package's own UnmarshalResolvedEvent, which an
// equal-but-wrong pair could satisfy), must carry it, and in exactly the field-16 varint
// encoding: tag 0x80 0x01, value 0x01. Only the two envelope instants (fields 17 and 18, ten
// bytes each) follow it.
func TestTheDisabledMarkRoundTripsThroughTheGeneratedDecoder(t *testing.T) {
	encoded, err := MarshalResolvedEventAs(disabledPresence(true), eventtime.WireCompact)
	require.NoError(t, err)
	const instants = 2 * envelopeNanosSize
	require.Greater(t, len(encoded), instants+3)
	require.Equal(t, []byte{0x80, 0x01, 0x01}, encoded[len(encoded)-instants-3:len(encoded)-instants],
		"field 16 is not the three bytes before the instants: % x", encoded)

	var pb PResolvedEvent
	require.NoError(t, proto.Unmarshal(encoded, &pb))
	assert.True(t, pb.GetSourceDeviceDisabled(), "proto.Unmarshal lost the mark")
	assert.Empty(t, pb.ProtoReflect().GetUnknown(), "the writer emitted bytes the schema does not declare")

	decoded, err := UnmarshalResolvedEvent(encoded)
	require.NoError(t, err)
	assert.True(t, decoded.SourceDeviceDisabled, "UnmarshalResolvedEvent lost the mark")

	// Unmarked, the field is absent from the wire, as proto3 leaves a false bool.
	plain, err := MarshalResolvedEventAs(disabledPresence(false), eventtime.WireCompact)
	require.NoError(t, err)
	assert.Equal(t, len(encoded)-3, len(plain))
	at := len(encoded) - instants - 3
	_, _, n := protowire.ConsumeField(encoded[at:])
	assert.Equal(t, 3, n)
	assert.Equal(t, plain, append(bytes.Clone(encoded[:at]), encoded[at+3:]...),
		"marked and unmarked differ in the mark alone")
}

// A consumer built before the field existed — every pod still on the previous release during a
// rolling upgrade — decodes a marked event without error and with every other field intact,
// holding the mark as an unknown field it ignores. Built from this file's descriptor with
// field 16 removed, which is what the previous release's generated code is.
func TestAPreviousReleaseDecodesAMarkedEvent(t *testing.T) {
	fdp := protodesc.ToFileDescriptorProto(File_resolvedevent_dc_resolved_events_proto)
	for _, m := range fdp.MessageType {
		if m.GetName() != "PResolvedEvent" {
			continue
		}
		kept := m.Field[:0]
		for _, f := range m.Field {
			if f.GetNumber() != 16 {
				kept = append(kept, f)
			}
		}
		m.Field = kept
	}
	fdp.Name = proto.String("previous-release.proto")
	file, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	require.NoError(t, err)
	md := file.Messages().ByName("PResolvedEvent")
	require.Nil(t, md.Fields().ByNumber(16), "the previous-release descriptor still declares field 16")

	encoded, err := MarshalResolvedEventAs(disabledPresence(true), eventtime.WireCompact)
	require.NoError(t, err)
	old := dynamicpb.NewMessage(md)
	require.NoError(t, proto.Unmarshal(encoded, old))
	assert.Equal(t, "dev-1", old.Get(md.Fields().ByName("source_device_token")).String())
	assert.Equal(t, int64(eventtype.StateChange), old.Get(md.Fields().ByName("event_type")).Int())
	assert.Equal(t, protoreflect.RawFields{0x80, 0x01, 0x01}, old.GetUnknown(), "the mark is not what an old consumer sees as unknown")
}

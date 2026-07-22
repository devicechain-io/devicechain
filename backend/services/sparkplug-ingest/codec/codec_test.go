// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"fmt"
	"sort"
	"testing"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestRoundTripPreservesPayload proves the codec is lossless over a realistic
// NBIRTH-shaped payload: node-level timestamp + seq + a metric set carrying the
// full birth vocabulary (name, alias, per-metric timestamp, datatype, and values
// of several oneof types). Decode∘Encode must reproduce the message exactly.
func TestRoundTripPreservesPayload(t *testing.T) {
	original := &sppb.Payload{
		Timestamp: proto.Uint64(1_700_000_000_123),
		Seq:       proto.Uint64(0),
		Uuid:      proto.String("birth-uuid"),
		Metrics: []*sppb.Payload_Metric{
			{
				Name:      proto.String("temperature"),
				Alias:     proto.Uint64(1),
				Timestamp: proto.Uint64(1_700_000_000_100),
				Datatype:  proto.Uint32(10), // Double
				Value:     &sppb.Payload_Metric_DoubleValue{DoubleValue: 21.5},
			},
			{
				Name:      proto.String("uptime_s"),
				Alias:     proto.Uint64(2),
				Timestamp: proto.Uint64(1_700_000_000_101),
				Datatype:  proto.Uint32(8), // Int64
				Value:     &sppb.Payload_Metric_LongValue{LongValue: 42},
			},
			{
				Name:     proto.String("label"),
				Alias:    proto.Uint64(3),
				Datatype: proto.Uint32(12), // String
				Value:    &sppb.Payload_Metric_StringValue{StringValue: "ok"},
			},
			{
				Name:     proto.String("faulted"),
				Alias:    proto.Uint64(4),
				Datatype: proto.Uint32(11), // Boolean
				Value:    &sppb.Payload_Metric_BooleanValue{BooleanValue: true},
			},
		},
	}

	encoded, err := Encode(original)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := Decode(encoded)
	require.NoError(t, err)

	if !proto.Equal(original, decoded) {
		t.Fatalf("round trip changed the payload:\n original = %v\n decoded  = %v", original, decoded)
	}
	// Spot-check the fields the session machine actually reads, so a regression in
	// getter wiring is legible rather than hiding behind proto.Equal.
	require.Len(t, decoded.GetMetrics(), 4)
	assert.Equal(t, uint64(0), decoded.GetSeq())
	assert.Equal(t, "temperature", decoded.GetMetrics()[0].GetName())
	assert.Equal(t, uint64(1), decoded.GetMetrics()[0].GetAlias())
	assert.Equal(t, 21.5, decoded.GetMetrics()[0].GetDoubleValue())
	assert.Equal(t, int64(42), int64(decoded.GetMetrics()[1].GetLongValue()))
}

// TestDecodeRejectsGarbage pins that a non-protobuf body is an error, not a
// silently-empty payload — the codec must fail loudly so SP2 can rebirth rather
// than ingest nothing.
func TestDecodeRejectsGarbage(t *testing.T) {
	// A byte that starts a length-delimited field claiming more bytes than exist.
	_, err := Decode([]byte{0x0a, 0x7f})
	require.Error(t, err)
}

// TestWireTagsMatchSpecification is the real guard on SP0: protobuf is tag-based,
// so a real Ignition / Cirrus-Link payload decodes ONLY if our schema's field
// NUMBERS match the Sparkplug B specification exactly. A structural round trip
// cannot catch a wrong tag (it is self-consistent with itself). These golden
// single-field encodings pin the load-bearing tags to the spec by hand:
//
//	tag byte = (field_number << 3) | wire_type   (wire_type 0 = varint, 2 = len-delimited)
//
// If any renumbering slips into sparkplug_b.proto, exactly the affected line fails.
func TestWireTagsMatchSpecification(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
		want []byte
	}{
		{
			// Payload.timestamp = field 1, varint. tag = (1<<3)|0 = 0x08.
			name: "payload.timestamp #1",
			msg:  &sppb.Payload{Timestamp: proto.Uint64(9)},
			want: []byte{0x08, 0x09},
		},
		{
			// Payload.seq = field 3, varint. tag = (3<<3)|0 = 0x18.
			name: "payload.seq #3",
			msg:  &sppb.Payload{Seq: proto.Uint64(5)},
			want: []byte{0x18, 0x05},
		},
		{
			// Payload.metrics = field 2 (len-delimited, 0x12) containing a Metric whose
			// name = field 1 (len-delimited, 0x0A), len 1, "x" (0x78).
			name: "payload.metrics #2 + metric.name #1",
			msg:  &sppb.Payload{Metrics: []*sppb.Payload_Metric{{Name: proto.String("x")}}},
			want: []byte{0x12, 0x03, 0x0a, 0x01, 0x78},
		},
		{
			// Metric.alias = field 2, varint. tag = (2<<3)|0 = 0x10.
			name: "metric.alias #2",
			msg:  &sppb.Payload_Metric{Alias: proto.Uint64(7)},
			want: []byte{0x10, 0x07},
		},
		{
			// Metric.timestamp = field 3, varint. tag = (3<<3)|0 = 0x18.
			name: "metric.timestamp #3",
			msg:  &sppb.Payload_Metric{Timestamp: proto.Uint64(3)},
			want: []byte{0x18, 0x03},
		},
		{
			// Metric.datatype = field 4, varint. tag = (4<<3)|0 = 0x20.
			name: "metric.datatype #4",
			msg:  &sppb.Payload_Metric{Datatype: proto.Uint32(8)},
			want: []byte{0x20, 0x08},
		},
		{
			// Metric.long_value = field 11, varint. tag = (11<<3)|0 = 0x58.
			name: "metric.long_value #11",
			msg:  &sppb.Payload_Metric{Value: &sppb.Payload_Metric_LongValue{LongValue: 11}},
			want: []byte{0x58, 0x0b},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := proto.Marshal(tc.msg)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got, "wire tag drifted from the Sparkplug B spec for %s", tc.name)
		})
	}
}

// TestSchemaDescriptorMatchesSpecification pins EVERY field number in the schema,
// not just the seven the byte vectors above sample — the exhaustive form of the
// same guard. It walks the compiled descriptor (what the binary actually decodes
// with) and asserts the complete "<Message>.<field>=<number>" listing. The golden
// list was verified field-by-field against the Eclipse Sparkplug B specification;
// any renumber, added, or removed field in sparkplug_b.proto shows up here as a
// legible diff, including in PropertyValue / DataSet / Template / MetaData, which
// the byte-vector sample does not reach.
func TestSchemaDescriptorMatchesSpecification(t *testing.T) {
	want := []string{
		"DataSet.columns=2",
		"DataSet.num_of_columns=1",
		"DataSet.rows=4",
		"DataSet.types=3",
		"DataSetValue.boolean_value=5",
		"DataSetValue.double_value=4",
		"DataSetValue.extension_value=7",
		"DataSetValue.float_value=3",
		"DataSetValue.int_value=1",
		"DataSetValue.long_value=2",
		"DataSetValue.string_value=6",
		"MetaData.content_type=2",
		"MetaData.description=8",
		"MetaData.file_name=5",
		"MetaData.file_type=6",
		"MetaData.is_multi_part=1",
		"MetaData.md5=7",
		"MetaData.seq=4",
		"MetaData.size=3",
		"Metric.alias=2",
		"Metric.boolean_value=14",
		"Metric.bytes_value=16",
		"Metric.dataset_value=17",
		"Metric.datatype=4",
		"Metric.double_value=13",
		"Metric.extension_value=19",
		"Metric.float_value=12",
		"Metric.int_value=10",
		"Metric.is_historical=5",
		"Metric.is_null=7",
		"Metric.is_transient=6",
		"Metric.long_value=11",
		"Metric.metadata=8",
		"Metric.name=1",
		"Metric.properties=9",
		"Metric.string_value=15",
		"Metric.template_value=18",
		"Metric.timestamp=3",
		"Parameter.boolean_value=7",
		"Parameter.double_value=6",
		"Parameter.extension_value=9",
		"Parameter.float_value=5",
		"Parameter.int_value=3",
		"Parameter.long_value=4",
		"Parameter.name=1",
		"Parameter.string_value=8",
		"Parameter.type=2",
		"Payload.body=5",
		"Payload.metrics=2",
		"Payload.seq=3",
		"Payload.timestamp=1",
		"Payload.uuid=4",
		"PropertySet.keys=1",
		"PropertySet.values=2",
		"PropertySetList.propertyset=1",
		"PropertyValue.boolean_value=7",
		"PropertyValue.double_value=6",
		"PropertyValue.extension_value=11",
		"PropertyValue.float_value=5",
		"PropertyValue.int_value=3",
		"PropertyValue.is_null=2",
		"PropertyValue.long_value=4",
		"PropertyValue.propertyset_value=9",
		"PropertyValue.propertysets_value=10",
		"PropertyValue.string_value=8",
		"PropertyValue.type=1",
		"Row.elements=1",
		"Template.is_definition=5",
		"Template.metrics=2",
		"Template.parameters=3",
		"Template.template_ref=4",
		"Template.version=1",
	}

	var got []string
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		fs := md.Fields()
		for i := 0; i < fs.Len(); i++ {
			f := fs.Get(i)
			got = append(got, fmt.Sprintf("%s.%s=%d", md.Name(), f.Name(), f.Number()))
		}
		ms := md.Messages()
		for i := 0; i < ms.Len(); i++ {
			walk(ms.Get(i))
		}
	}
	walk((&sppb.Payload{}).ProtoReflect().Descriptor())
	sort.Strings(got)

	require.Equal(t, want, got, "the Sparkplug B schema descriptor drifted from the specification")
}

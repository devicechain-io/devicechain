// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// cmdTS is the payload timestamp every golden below was built with.
const cmdTS int64 = 1_700_000_000_000

// 🔴 THE EXPECTED BYTES ARE NOT PRODUCED BY THE CODE UNDER TEST. Each hex string below was built
// by a hand-written protobuf writer (varint / length-delimited / fixed32 / fixed64 from the
// field numbers in sparkplug_b.proto), so a regression in the generated codec or in the
// encoder's carrier choice cannot move the expectation with it.
//
// Reading one: 08 <ts> = payload.timestamp; 12 <len> = a metric; 0a 01 6d = name "m"; 20 <dt> =
// datatype; then the value arm: 50 = int_value (field 10), 58 = long_value (11), 65 = float (12),
// 69 = double (13), 70 = boolean (14), 7a = string (15).
//
// Signed integers: Int8/16/32 ride int_value SIGN-EXTENDED to 32 bits (Int8 -1 is ffffffff0f, a
// five-byte varint, not ff01), Int64 rides long_value as 64-bit two's complement. That is what
// Tahu Java and Tahu C emit; see EncodeMetricValue.
var goldenCommands = []struct {
	name  string
	dt    uint32
	value any
	hex   string
}{
	{"Int8 -1", 1, -1, "0880d095ffbc31120b0a016d200150ffffffff0f"},
	{"Int8 127", 1, 127, "0880d095ffbc3112070a016d2001507f"},
	{"Int8 -128", 1, -128, "0880d095ffbc31120b0a016d20015080ffffff0f"},
	{"Int16 -1", 2, -1, "0880d095ffbc31120b0a016d200250ffffffff0f"},
	{"Int16 -32768", 2, -32768, "0880d095ffbc31120b0a016d2002508080feff0f"},
	{"Int16 32767", 2, 32767, "0880d095ffbc3112090a016d200250ffff01"},
	{"Int32 -1", 3, -1, "0880d095ffbc31120b0a016d200350ffffffff0f"},
	{"Int32 min", 3, -2147483648, "0880d095ffbc31120b0a016d2003508080808008"},
	{"Int32 max", 3, 2147483647, "0880d095ffbc31120b0a016d200350ffffffff07"},
	{"Int64 -1", 4, int64(-1), "0880d095ffbc3112100a016d200458ffffffffffffffffff01"},
	{"Int64 min", 4, "-9223372036854775808", "0880d095ffbc3112100a016d20045880808080808080808001"},
	{"Int64 max", 4, "9223372036854775807", "0880d095ffbc31120f0a016d200458ffffffffffffffff7f"},
	{"UInt8 255", 5, 255, "0880d095ffbc3112080a016d200550ff01"},
	{"UInt16 65535", 6, 65535, "0880d095ffbc3112090a016d200650ffff03"},
	{"UInt32 max", 7, uint64(4294967295), "0880d095ffbc31120b0a016d200750ffffffff0f"},
	{"UInt64 max", 8, "18446744073709551615", "0880d095ffbc3112100a016d200858ffffffffffffffffff01"},
	{"Float 1.5", 9, 1.5, "0880d095ffbc31120a0a016d2009650000c03f"},
	{"Double -2.25", 10, -2.25, "0880d095ffbc31120e0a016d200a6900000000000002c0"},
	{"Boolean true", 11, true, "0880d095ffbc3112070a016d200b7001"},
	{"Boolean false", 11, false, "0880d095ffbc3112070a016d200b7000"},
	{"String", 12, "hi", "0880d095ffbc3112090a016d200c7a026869"},
	{"DateTime", 13, cmdTS, "0880d095ffbc31120c0a016d200d5880d095ffbc31"},
	{"Text", 14, "txt", "0880d095ffbc31120a0a016d200e7a03747874"},
	{"UUID", 15, "123e4567-e89b-12d3-a456-426614174000",
		"0880d095ffbc31122b0a016d200f7a2431323365343536372d653839622d313264332d613435362d343236363134313734303030"},
}

func nameRef(dt uint32) MetricRef {
	return MetricRef{Target: CommandTarget{Group: "g", Node: "n"}, Name: "m", Datatype: dt}
}

// Every datatype's exact bytes, boundaries included. A mutant that puts an Int32 in long_value (or
// an Int8 in a one-byte varint) changes these bytes and fails here.
func TestCommandGoldenBytes(t *testing.T) {
	for _, g := range goldenCommands {
		t.Run(g.name, func(t *testing.T) {
			got, err := BuildMetricCommand(nameRef(g.dt), g.value, cmdTS)
			require.NoError(t, err)
			assert.Equal(t, g.hex, hex.EncodeToString(got))
		})
	}
}

// The 15 scalar datatypes are all covered by a golden above.
func TestCommandGoldenCoversEveryScalar(t *testing.T) {
	seen := map[uint32]bool{}
	for _, g := range goldenCommands {
		seen[g.dt] = true
	}
	for dt := uint32(1); dt <= 15; dt++ {
		assert.True(t, seen[dt], "datatype %d has no golden", dt)
	}
}

// Sparkplug carries a signed value in an unsigned field; the carrier arm is the datatype's, never
// another's (an Int32 in long_value would decode as a huge unsigned number on a 32-bit node).
func TestSignedIntegerCarriers(t *testing.T) {
	for _, c := range []struct {
		dt   uint32
		v    int64
		want uint32
	}{
		{1, -1, 0xFFFFFFFF}, {1, -128, 0xFFFFFF80}, {2, -1, 0xFFFFFFFF}, {2, -32768, 0xFFFF8000},
		{3, -1, 0xFFFFFFFF}, {3, -2147483648, 0x80000000},
	} {
		set, err := EncodeMetricValue(c.dt, c.v)
		require.NoError(t, err)
		m := &sppb.Payload_Metric{}
		set(m)
		arm, ok := m.GetValue().(*sppb.Payload_Metric_IntValue)
		require.True(t, ok, "Int%d must ride int_value", c.dt)
		assert.Equal(t, c.want, arm.IntValue)
		// And the receive side of this very service reads it back as the same signed number.
		assert.Equal(t, float64(c.v), intValueAsFloat(arm.IntValue, c.dt))
	}
	set, err := EncodeMetricValue(4, int64(-5))
	require.NoError(t, err)
	m := &sppb.Payload_Metric{}
	set(m)
	arm, ok := m.GetValue().(*sppb.Payload_Metric_LongValue)
	require.True(t, ok, "Int64 must ride long_value")
	assert.Equal(t, ^uint64(4), arm.LongValue)
	assert.Equal(t, float64(-5), longValueAsFloat(arm.LongValue, 4))
}

// A 64-bit integer above 2^53 only survives as a string of digits; a JSON number that big has
// already been rounded, so it is refused rather than guessed.
func TestSixtyFourBitIntegersNeedExactInput(t *testing.T) {
	_, err := EncodeMetricValue(4, "9007199254740993") // 2^53 + 1, not a float64
	require.NoError(t, err)
	_, err = EncodeMetricValue(4, json.Number("9007199254740993"))
	require.NoError(t, err, "a json.Number of digits is exact")
	_, err = EncodeMetricValue(4, float64(9007199254740993)) // already rounded by the caller's decode
	require.ErrorIs(t, err, ErrValueRefused)
	_, err = EncodeMetricValue(8, 1e19)
	require.ErrorIs(t, err, ErrValueRefused)
}

func TestCommandRefusals(t *testing.T) {
	long := strings.Repeat("x", maxCommandString+1)
	for _, c := range []struct {
		name  string
		dt    uint32
		value any
	}{
		{"Int8 past max", 1, 128}, {"Int8 300", 1, 300}, {"Int8 past min", 1, -129},
		{"Int16 past max", 2, 32768}, {"Int16 past min", 2, -32769},
		{"Int32 past max", 3, 2147483648}, {"Int32 past min", 3, -2147483649},
		{"Int64 past max", 4, "9223372036854775808"}, {"Int64 past min", 4, "-9223372036854775809"},
		{"Int64 not digits", 4, "12x"}, {"Int64 empty string", 4, ""}, {"Int64 float string", 4, "1.5"},
		{"UInt8 negative", 5, -1}, {"UInt8 past max", 5, 256}, {"UInt16 past max", 6, 65536},
		{"UInt32 negative", 7, -1}, {"UInt32 past max", 7, uint64(4294967296)},
		{"UInt64 negative string", 8, "-1"}, {"UInt64 past max", 8, "18446744073709551616"},
		{"1.5 into an integer", 3, 1.5}, {"1.5 json.Number into an integer", 3, json.Number("1.5")},
		{"NaN into an integer", 3, nan()}, {"string into Int32", 3, "5"}, {"bool into Int32", 3, true},
		{"nil into Int32", 3, nil}, {"string into Float", 9, "1.5"}, {"bool into Double", 10, true},
		{"Float overflow", 9, 1e39}, {"Double inf", 10, inf()},
		{"number into Boolean", 11, 1}, {"string into Boolean", 11, "true"}, {"nil into Boolean", 11, nil},
		{"number into String", 12, 5}, {"bool into Text", 14, false},
		{"string too long", 12, long}, {"invalid UTF-8", 12, "\xff\xfe"},
		{"bad UUID", 15, "not-a-uuid"}, {"UUID wrong grouping", 15, "123e4567e89b12d3a456426614174000"},
		{"DateTime negative", 13, -1}, {"DateTime string", 13, "1700000000000"}, {"DateTime fraction", 13, 1.5},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := BuildMetricCommand(nameRef(c.dt), c.value, cmdTS)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrValueRefused)
			assert.Nil(t, got, "a refused value must produce no payload")
		})
	}
}

// Datatypes commands do not carry are refused by their own error, so S3 can word the reason.
func TestUnsupportedDatatypesAreRefused(t *testing.T) {
	for _, dt := range []uint32{0, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 999} {
		_, err := BuildMetricCommand(nameRef(dt), 1, cmdTS)
		require.ErrorIs(t, err, ErrUnsupportedDatatype, "datatype %d", dt)
	}
}

// Accepted inputs that look like refusals: the counterweight to the table above (a refuse-all
// encoder passes the refusals and fails these).
func TestAcceptedInputs(t *testing.T) {
	for _, c := range []struct {
		dt uint32
		v  any
	}{
		{3, 5.0}, {3, json.Number("7")}, {3, json.Number("7.0")}, {1, int8(-3)}, {5, uint8(3)},
		{4, json.Number("-9223372036854775808")}, {9, 3}, {10, json.Number("0.1")}, {10, int64(7)},
		{12, ""}, {12, strings.Repeat("é", maxCommandString/2)}, {15, "123E4567-E89B-12D3-A456-426614174000"},
		{13, 0},
	} {
		_, err := EncodeMetricValue(c.dt, c.v)
		assert.NoError(t, err, "datatype %d value %v", c.dt, c.v)
	}
}

// 🔴 ALIAS-ONLY ([tck-id-payloads-alias-data-cmd-requirement]): when the BIRTH aliased the metric
// the command carries the alias and NO name; a node that does not alias is addressed by name; the
// datatype is set in both; there is a payload timestamp and no seq ([tck-id-payloads-ncmd-seq],
// [tck-id-payloads-dcmd-seq]).
func TestCommandAddressing(t *testing.T) {
	aliased := MetricRef{Target: CommandTarget{Group: "g", Node: "n"}, Name: "Setpoint", Alias: 7, HasAlias: true, Datatype: 1}
	b, err := BuildMetricCommand(aliased, -1, cmdTS)
	require.NoError(t, err)
	assert.Equal(t, "0880d095ffbc31120a1007200150ffffffff0f", hex.EncodeToString(b), "alias 7, no name")
	p, err := codec.Decode(b)
	require.NoError(t, err)
	m := p.GetMetrics()[0]
	assert.Nil(t, m.Name, "the name is EXCLUDED when the BIRTH aliased the metric")
	assert.Equal(t, uint64(7), m.GetAlias())
	assert.Equal(t, uint32(1), m.GetDatatype())
	assert.Nil(t, p.Seq, "no seq")
	assert.Equal(t, uint64(cmdTS), p.GetTimestamp(), "payload timestamp")

	named := nameRef(1)
	b, err = BuildMetricCommand(named, -1, cmdTS)
	require.NoError(t, err)
	assert.Equal(t, "0880d095ffbc31120b0a016d200150ffffffff0f", hex.EncodeToString(b), "name m, no alias")
	p, err = codec.Decode(b)
	require.NoError(t, err)
	assert.Nil(t, p.GetMetrics()[0].Alias)
	assert.Equal(t, "m", p.GetMetrics()[0].GetName())
	assert.Nil(t, p.Seq)
}

// Rebirth is the one command addressed by NAME even on an aliasing node
// ([tck-id-operational-behavior-data-commands-rebirth-name-aliases]); no seq, with a timestamp.
func TestRebirthCommandShape(t *testing.T) {
	b, err := rebirthCommand(cmdTS)
	require.NoError(t, err)
	assert.Equal(t, "0880d095ffbc31121a0a144e6f646520436f6e74726f6c2f52656269727468200b7001", hex.EncodeToString(b))
	p, err := codec.Decode(b)
	require.NoError(t, err)
	assert.Nil(t, p.Seq)
	assert.NotNil(t, p.Timestamp)
	m := p.GetMetrics()[0]
	assert.Equal(t, "Node Control/Rebirth", m.GetName())
	assert.Nil(t, m.Alias)
	assert.True(t, m.GetBooleanValue())
}

func TestCommandTopicsAndFlags(t *testing.T) {
	assert.Equal(t, "spBv1.0/g/NCMD/n", CommandTarget{Group: "g", Node: "n"}.Topic())
	assert.Equal(t, "spBv1.0/g/DCMD/n/d", CommandTarget{Group: "g", Node: "n", Device: "d"}.Topic())
	// [tck-id-topics-ncmd-mqtt] / [tck-id-topics-dcmd-mqtt] / [tck-id-payloads-{n,d}cmd-{qos,retain}]
	assert.Equal(t, byte(0), commandQoS)
	assert.False(t, commandRetain)
	_, err := ParseTopic(CommandTarget{Group: "g", Node: "n", Device: "d"}.Topic())
	require.NoError(t, err)
}

func TestNoNameNoAliasIsRefused(t *testing.T) {
	_, err := BuildMetricCommand(MetricRef{Datatype: 3}, 1, cmdTS)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrValueRefused))
}

func nan() float64 { return math.NaN() }
func inf() float64 { return math.Inf(1) }

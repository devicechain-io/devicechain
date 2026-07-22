// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// Sparkplug datatype codes used in these tests (only the ones without a named
// constant in sample.go).
const (
	datatypeUInt32 = 7
	datatypeFloat  = 9
	datatypeDouble = 10
)

func namedMetric(name string, datatype uint32, value any) *sppb.Payload_Metric {
	m := &sppb.Payload_Metric{Name: proto.String(name), Datatype: proto.Uint32(datatype)}
	setValue(m, value)
	return m
}

func setValue(m *sppb.Payload_Metric, value any) {
	switch v := value.(type) {
	case uint32:
		m.Value = &sppb.Payload_Metric_IntValue{IntValue: v}
	case uint64:
		m.Value = &sppb.Payload_Metric_LongValue{LongValue: v}
	case float32:
		m.Value = &sppb.Payload_Metric_FloatValue{FloatValue: v}
	case float64:
		m.Value = &sppb.Payload_Metric_DoubleValue{DoubleValue: v}
	case bool:
		m.Value = &sppb.Payload_Metric_BooleanValue{BooleanValue: v}
	case string:
		m.Value = &sppb.Payload_Metric_StringValue{StringValue: v}
	}
}

var fixedNow = func() time.Time { return time.Unix(1_700_000_000, 0) }

// oneSample runs a single metric through samplesFrom and returns the resulting
// Sample (or asserts it was skipped when want=false).
func firstSample(t *testing.T, m *sppb.Payload_Metric) (Sample, bool) {
	t.Helper()
	out := samplesFrom(&sppb.Payload{Metrics: []*sppb.Payload_Metric{m}}, map[uint64]string{}, fixedNow)
	if len(out) == 0 {
		return Sample{}, false
	}
	return out[0], true
}

// TestSignedIntegersReinterpretTwosComplement is the §3 sign pin: a signed integer
// rides an UNSIGNED proto field, so a naive float64(uint) would persist a huge
// positive number for a small negative one. Both encoder conventions
// (truncated-to-width and sign-extended-to-32) must decode to the true value.
func TestSignedIntegersReinterpretTwosComplement(t *testing.T) {
	cases := []struct {
		name     string
		datatype uint32
		raw      any
		want     float64
	}{
		{"Int8 -1 width", datatypeInt8, uint32(0xFF), -1},
		{"Int8 -1 sign-extended", datatypeInt8, uint32(0xFFFFFFFF), -1},
		{"Int16 -1 width", datatypeInt16, uint32(0xFFFF), -1},
		{"Int16 -1 sign-extended", datatypeInt16, uint32(0xFFFFFFFF), -1},
		{"Int32 -1", datatypeInt32, uint32(0xFFFFFFFF), -1},
		{"Int32 -42", datatypeInt32, uint32(4294967254), -42},
		{"Int64 -1", datatypeInt64, uint64(0xFFFFFFFFFFFFFFFF), -1},
		{"UInt32 magnitude in int_value", datatypeUInt32, uint32(4294967295), 4294967295},
		{"UInt32 magnitude in long_value (Tahu wart)", datatypeUInt32, uint64(4294967295), 4294967295},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, ok := firstSample(t, namedMetric("m", c.datatype, c.raw))
			assert.True(t, ok, "a numeric metric must produce a sample")
			assert.Equal(t, c.want, s.Value)
		})
	}
}

// TestFloatAndDoublePassThrough pins float/double read straight from their arm.
func TestFloatAndDoublePassThrough(t *testing.T) {
	s, ok := firstSample(t, namedMetric("f", datatypeFloat, float32(1.5)))
	assert.True(t, ok)
	assert.Equal(t, 1.5, s.Value)

	s, ok = firstSample(t, namedMetric("d", datatypeDouble, float64(3.25)))
	assert.True(t, ok)
	assert.Equal(t, 3.25, s.Value)
}

// TestNonNumericAndReservedMetricsAreSkipped pins that booleans, strings, null
// metrics, and the reserved control/sequence metrics never become measurements —
// bdSeq especially, since it IS numeric and would otherwise emit a bogus value.
func TestNonNumericAndReservedMetricsAreSkipped(t *testing.T) {
	_, ok := firstSample(t, namedMetric("flag", datatypeBoolean, true))
	assert.False(t, ok, "boolean is not a measurement")

	_, ok = firstSample(t, namedMetric("label", 12 /*String*/, "hello"))
	assert.False(t, ok, "string is not a measurement")

	null := namedMetric("temp", datatypeDouble, float64(9))
	null.IsNull = proto.Bool(true)
	_, ok = firstSample(t, null)
	assert.False(t, ok, "a null metric is skipped")

	_, ok = firstSample(t, bdSeqM(7))
	assert.False(t, ok, "bdSeq is a reserved sequence metric, never a measurement")

	_, ok = firstSample(t, namedMetric("Node Control/Rebirth", datatypeBoolean, false))
	assert.False(t, ok, "reserved Node Control metric is skipped")
}

// TestMetricTimestampFallbackChain pins the M9 chain: a metric timestamp wins and is
// carried at full millisecond precision (sub-second not truncated); absent, the
// payload timestamp is used; absent both, the receipt clock — never 1970.
func TestMetricTimestampFallbackChain(t *testing.T) {
	// Metric-level timestamp wins, sub-second preserved.
	m := namedMetric("t", datatypeDouble, float64(1))
	m.Timestamp = proto.Uint64(1_700_000_000_123)
	p := &sppb.Payload{Timestamp: proto.Uint64(1_600_000_000_000), Metrics: []*sppb.Payload_Metric{m}}
	out := samplesFrom(p, map[uint64]string{}, fixedNow)
	assert.Equal(t, int64(1_700_000_000_123), out[0].Time, "metric ts wins, sub-second kept")

	// No metric ts → payload ts.
	m2 := namedMetric("t", datatypeDouble, float64(1))
	p2 := &sppb.Payload{Timestamp: proto.Uint64(1_600_000_000_456), Metrics: []*sppb.Payload_Metric{m2}}
	out = samplesFrom(p2, map[uint64]string{}, fixedNow)
	assert.Equal(t, int64(1_600_000_000_456), out[0].Time, "falls back to payload ts")

	// No metric ts, no payload ts → receipt clock, never 1970.
	m3 := namedMetric("t", datatypeDouble, float64(1))
	p3 := &sppb.Payload{Metrics: []*sppb.Payload_Metric{m3}}
	out = samplesFrom(p3, map[uint64]string{}, fixedNow)
	assert.Equal(t, fixedNow().UnixMilli(), out[0].Time, "falls back to receipt time")
	assert.Greater(t, out[0].Time, int64(0), "never a zero/1970 timestamp")
}

// TestAliasResolvesToBirthName pins that a DATA metric sent by alias (no name)
// resolves to the name its birth declared.
func TestAliasResolvesToBirthName(t *testing.T) {
	m := &sppb.Payload_Metric{Alias: proto.Uint64(5), Datatype: proto.Uint32(datatypeDouble),
		Value: &sppb.Payload_Metric_DoubleValue{DoubleValue: 21.5}}
	out := samplesFrom(&sppb.Payload{Metrics: []*sppb.Payload_Metric{m}}, map[uint64]string{5: "temperature"}, fixedNow)
	if assert.Len(t, out, 1) {
		assert.Equal(t, "temperature", out[0].Name)
		assert.Equal(t, 21.5, out[0].Value)
	}
}

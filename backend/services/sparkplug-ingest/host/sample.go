// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"strings"
	"time"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// Sparkplug B datatype codes (Payload.Metric.datatype). Only the signed integer
// codes need naming here: they decide whether the unsigned int_value/long_value
// carrier is reinterpreted as two's-complement. Unsigned ints, floats and doubles
// are read straight from whichever value arm is set; datatypeBoolean lives in
// session.go (the rebirth command uses it).
const (
	datatypeInt8  = 1
	datatypeInt16 = 2
	datatypeInt32 = 3
	datatypeInt64 = 4
)

// samplesFrom extracts the numeric measurements from an ACCEPTED Sparkplug payload:
// one Sample per numeric, non-null, non-reserved metric. It is called only for a
// message the session machine accepted (valid seq, aliases resolve), under the
// tracker lock, so it never races the session state it reads. Each Sample carries
// its own timestamp via the M9 fallback (metric-ts → payload-ts → receipt), never
// truncated and never defaulted to 1970. aliases resolves a metric sent by alias
// (DATA) to the name its birth declared.
func samplesFrom(p *sppb.Payload, aliases map[uint64]string, now func() time.Time) []Sample {
	payloadTs := p.GetTimestamp()
	var out []Sample
	for _, m := range p.GetMetrics() {
		if m.GetIsNull() {
			continue
		}
		name := metricName(m, aliases)
		if name == "" || isReservedMetric(name) {
			continue
		}
		v, ok := numericValue(m)
		if !ok {
			continue // boolean/string/bytes/dataset/template — not a measurement
		}
		out = append(out, Sample{Name: name, Value: v, Time: metricTime(m, payloadTs, now)})
	}
	return out
}

// metricName resolves a metric's name: its explicit name if present, else its
// alias looked up in the session's table (empty if the alias is unknown — but the
// session machine has already rejected a DATA message with an unresolved alias, so
// that path does not reach here).
func metricName(m *sppb.Payload_Metric, aliases map[uint64]string) string {
	if m.GetName() != "" {
		return m.GetName()
	}
	if m.Alias != nil {
		return aliases[m.GetAlias()]
	}
	return ""
}

// isReservedMetric reports whether a metric name is a Sparkplug control/sequence
// metric rather than telemetry. bdSeq is numeric, so without this it would be
// emitted as a bogus measurement; the "Node Control/" and "Device Control/"
// namespaces are reserved for host→node commands a node advertises at birth.
func isReservedMetric(name string) bool {
	return name == bdSeqMetric ||
		strings.HasPrefix(name, "Node Control/") ||
		strings.HasPrefix(name, "Device Control/")
}

// numericValue reads a metric's value as a float64 when it is a numeric Sparkplug
// type, reporting false for every non-numeric type (boolean/string/bytes/dataset/
// template/extension/absent). It keys on WHICH value arm is actually set — not on
// the declared datatype — because encoders disagree on where a UInt32 rides (Tahu
// Java uses long_value, others int_value); the datatype is consulted only to decide
// signedness of an integer carrier.
func numericValue(m *sppb.Payload_Metric) (float64, bool) {
	switch m.GetValue().(type) {
	case *sppb.Payload_Metric_IntValue:
		return intValueAsFloat(m.GetIntValue(), m.GetDatatype()), true
	case *sppb.Payload_Metric_LongValue:
		return longValueAsFloat(m.GetLongValue(), m.GetDatatype()), true
	case *sppb.Payload_Metric_FloatValue:
		return float64(m.GetFloatValue()), true
	case *sppb.Payload_Metric_DoubleValue:
		return m.GetDoubleValue(), true
	default:
		return 0, false
	}
}

// intValueAsFloat reinterprets the 32-bit unsigned int_value carrier per the
// declared datatype's signedness. A signed type is two's-complement at its own
// width; the width-narrowing cast (int8(uint8(v))) yields the correct negative
// value under BOTH common encoder conventions — sign-extended-to-32 and
// truncated-to-width — so e.g. an Int32 of -1 becomes -1, never 4294967295. An
// unsigned or unknown datatype is read as plain magnitude.
func intValueAsFloat(v uint32, datatype uint32) float64 {
	switch datatype {
	case datatypeInt8:
		return float64(int8(uint8(v)))
	case datatypeInt16:
		return float64(int16(uint16(v)))
	case datatypeInt32:
		return float64(int32(v))
	default:
		return float64(v)
	}
}

// longValueAsFloat reinterprets the 64-bit unsigned long_value carrier. Int64 is
// two's-complement; UInt64 (and a UInt32 an encoder placed in long_value) is
// magnitude. A float64 cannot represent an integer magnitude above 2^53 exactly —
// inherent to storing measurements as float64, not fixable here.
func longValueAsFloat(v uint64, datatype uint32) float64 {
	if datatype == datatypeInt64 {
		return float64(int64(v))
	}
	return float64(v)
}

// metricTime resolves one metric's timestamp in ms via the M9 fallback chain:
// the metric's own timestamp, else the payload timestamp, else the receipt time.
// A zero (or absent) timestamp at each level is treated as unset and falls through
// rather than being persisted as 1970 — never truncated to seconds.
func metricTime(m *sppb.Payload_Metric, payloadTs uint64, now func() time.Time) int64 {
	if m.Timestamp != nil && m.GetTimestamp() > 0 {
		return int64(m.GetTimestamp())
	}
	if payloadTs > 0 {
		return int64(payloadTs)
	}
	return now().UnixMilli()
}

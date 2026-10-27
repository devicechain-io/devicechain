// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/eventlimit"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// Sparkplug B datatype codes (Payload.Metric.datatype; the Sparkplug 3.0.0 enum, section
// "Datatype Details"). ONE vocabulary for ingest (which signed-integer codes reinterpret the
// unsigned carrier) and for the command encoder (which scalar types it can carry).
const (
	datatypeInt8     = 1
	datatypeInt16    = 2
	datatypeInt32    = 3
	datatypeInt64    = 4
	datatypeUInt8    = 5
	datatypeUInt16   = 6
	datatypeUInt32   = 7
	datatypeUInt64   = 8
	datatypeFloat    = 9
	datatypeDouble   = 10
	datatypeBoolean  = 11
	datatypeString   = 12
	datatypeDateTime = 13
	datatypeText     = 14
	datatypeUUID     = 15
)

// Reasons a metric of an accepted message produced no Sample, for the samples-skipped counter.
// A closed set: the label is never fed from a metric name or an error string.
const (
	// SkipNonNumeric: the metric carries a boolean, string, bytes, dataset, template or extension
	// value, none of which this service stores as a measurement.
	SkipNonNumeric = "non_numeric"
	// SkipNull: the metric is flagged is_null, so it carries no value this message.
	SkipNull = "null"
	// SkipUnnamed: the metric has no name and no alias that resolves to one.
	SkipUnnamed = "unnamed"
	// SkipOversize: the metric arrived in a message carrying more than MaxMetricsPerMessage
	// metrics, so none of that message's metrics were read.
	SkipOversize = "oversize"
	// SkipUntyped: the metric carries an integer whose signedness cannot be known - it names no
	// datatype (a conformant DATA message omits it) and no BIRTH declared one for it - so reading it
	// would be a guess, and a wrong guess turns a negative reading into a number near 4.29e9.
	SkipUntyped = "untyped"
)

// SkipReasons is every reason a metric is counted as skipped, for pre-creating the counter's
// series at zero.
var SkipReasons = []string{SkipNonNumeric, SkipNull, SkipUnnamed, SkipOversize, SkipUntyped}

// MaxMetricsPerMessage is the most metrics one Sparkplug message may carry and still have its
// readings taken: 64 events' worth at the platform's per-event bound. A message over it has
// NONE of its readings taken, and its metrics are counted as skipped with SkipOversize.
//
// WHY A CAP. Readings are extracted on paho's ordered receive goroutine, which every node
// of the source shares, and a message's readings become one device resolve and a run of
// events published from that same goroutine. Nothing else bounds how many a message carries:
// a BIRTH is not charged against the tenant's ingest ceiling at all, and a DATA message is
// charged only AFTER its readings have been extracted. So one oversized or hostile message
// could hold the goroutine, and with it every other node of the source, for as long as it
// liked.
//
// WHY REFUSED WHOLE, NOT TRUNCATED. A message over the cap is a defect or an attack, not a
// node with a little too much to say: births with hundreds to a few thousand metrics are
// routine, this is several times that. Keeping the first N would store a partial birth that
// reads as a complete one, which is the silent half-success the platform refuses elsewhere
// (a device event over the per-event bound is refused whole, too). The message still drives
// the session machine — a refused birth's sequence, aliases and presence are honoured — so
// refusing its readings does not throw the node into a rebirth loop.
//
// A platform constant, not a setting, as eventlimit.MaxReadingsPerEvent is: it is a bound
// on what one message may cost, and an operator who needed it raised has a node to fix.
const MaxMetricsPerMessage = 64 * eventlimit.MaxReadingsPerEvent

// Skips counts, by reason, the metrics of one accepted message that produced no Sample.
// Reserved control metrics (bdSeq, Node Control/*, Device Control/*) are not counted: they
// are protocol, not telemetry that was lost.
type Skips struct{ NonNumeric, Null, Unnamed, Oversize, Untyped int }

// Total is every skipped metric.
func (k Skips) Total() int { return k.NonNumeric + k.Null + k.Unnamed + k.Oversize + k.Untyped }

// add folds o into k.
func (k *Skips) add(o Skips) {
	k.NonNumeric += o.NonNumeric
	k.Null += o.Null
	k.Unnamed += o.Unnamed
	k.Oversize += o.Oversize
	k.Untyped += o.Untyped
}

// samplesFrom extracts the numeric measurements from an ACCEPTED Sparkplug payload:
// one Sample per numeric, non-null, non-reserved metric. It is called only for a
// message the session machine accepted (valid seq, aliases resolve), under the
// tracker lock, so it never races the session state it reads. Each Sample carries
// its own timestamp via the M9 fallback (metric-ts → payload-ts → receipt), never
// truncated and never defaulted to 1970. aliases resolves a metric sent by alias
// (DATA) to the name its birth declared. Every metric that yields no Sample is accounted
// in the returned Skips rather than vanishing. A message carrying more than
// MaxMetricsPerMessage metrics yields no Sample at all; see MaxMetricsPerMessage.
func samplesFrom(p *sppb.Payload, aliases map[uint64]string, types birthTypes, device string, now func() time.Time) ([]Sample, Skips) {
	if n := len(p.GetMetrics()); n > MaxMetricsPerMessage {
		return nil, Skips{Oversize: n}
	}
	payloadTs := p.GetTimestamp()
	var out []Sample
	var skips Skips
	for _, m := range p.GetMetrics() {
		if m.GetIsNull() {
			skips.Null++
			continue
		}
		name := metricName(m, aliases)
		if name == "" {
			skips.Unnamed++
			continue
		}
		if isReservedMetric(name) {
			continue
		}
		v, ok, untyped := numericValue(m, types.of(device, name))
		if untyped {
			skips.Untyped++
			continue
		}
		if !ok {
			skips.NonNumeric++ // boolean/string/bytes/dataset/template — not a measurement
			continue
		}
		out = append(out, Sample{Name: name, Value: v, Time: metricTime(m, payloadTs, now)})
	}
	return out, skips
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

// birthTypes is the datatype each metric declared at its BIRTH, by (device, name); a metric sent
// by alias is resolved to its name first, through the session's alias table. A conformant DATA message omits the datatype
// ([tck-id-payloads-metric-datatype-not-req]: it SHOULD NOT be included), so the signedness of
// an integer carried in one is known only from here.
type birthTypes struct {
	byName map[typeKey]uint32
}

type typeKey struct{ device, name string }

// of resolves the datatype a BIRTH declared for the metric named name under device. Zero is
// "none declared".
func (t birthTypes) of(device, name string) uint32 {
	return t.byName[typeKey{device, name}]
}

// record folds a BIRTH's declared datatypes in. Metrics that declare none (zero is Unknown) are
// not recorded: they would only mask a later declaration with a guess.
func (t *birthTypes) record(device string, p *sppb.Payload) {
	if t.byName == nil {
		t.byName = map[typeKey]uint32{}
	}
	for _, m := range p.GetMetrics() {
		dt := m.GetDatatype()
		if dt == 0 {
			continue
		}
		if m.GetName() != "" {
			t.byName[typeKey{device, m.GetName()}] = dt
		}
	}
}

// dropDevice forgets a dead device's by-name declarations.
func (t *birthTypes) dropDevice(device string) {
	for k := range t.byName {
		if k.device == device {
			delete(t.byName, k)
		}
	}
}

// numericValue reads a metric's value as a float64 when it is a numeric Sparkplug type,
// reporting false for every non-numeric type (boolean/string/bytes/dataset/template/extension/
// absent). It keys on WHICH value arm is actually set - not on the declared datatype - because
// encoders disagree on where a UInt32 rides (Tahu Java uses long_value, others int_value).
//
// An integer arm needs its datatype for signedness: the metric's own if it names one, else
// declared, the one its BIRTH gave it. With neither, untyped is true and the value is NOT read:
// treating a possibly-signed carrier as magnitude turns every negative reading into ~4.29e9.
func numericValue(m *sppb.Payload_Metric, declared uint32) (v float64, ok bool, untyped bool) {
	dt := m.GetDatatype()
	if dt == 0 {
		dt = declared
	}
	switch m.GetValue().(type) {
	case *sppb.Payload_Metric_IntValue:
		if dt == 0 {
			return 0, false, true
		}
		return intValueAsFloat(m.GetIntValue(), dt), true, false
	case *sppb.Payload_Metric_LongValue:
		if dt == 0 {
			return 0, false, true
		}
		return longValueAsFloat(m.GetLongValue(), dt), true, false
	case *sppb.Payload_Metric_FloatValue:
		return float64(m.GetFloatValue()), true, false
	case *sppb.Payload_Metric_DoubleValue:
		return m.GetDoubleValue(), true, false
	default:
		return 0, false, false
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"time"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// What a node reported (G19 S4).
//
// Sparkplug has no command acknowledgement, so a command is "confirmed" only by a LATER report of
// the requested value (or, for a rebirth or a reboot, by the node's new birth). The session
// machine already sees every accepted BIRTH and DATA message under its lock; this file is what it
// copies out of them for the command path:
//
//   - a Report per accepted, non-historical, non-null metric of a message (resolved to its name,
//     with its value in a form two values can be compared in), and one per accepted NBIRTH;
//   - the last value each metric was reported at, kept in the BIRTH table, which is what lets a
//     command to a value the node already holds be told apart from one the node ignored.
//
// 🔴 THE LOCK ENDS BEFORE THE MATCHING BEGINS. Reports are COPIES built under SessionTracker.mu
// and handed back in the Observation; the dispatcher matches them against its awaiting entries,
// and publishes the responses, after the tracker's lock (and the receive path's decode) are done
// with them. Nothing here, and nothing the hook does, holds tr.mu across a network call.

// ReportKind says what a Report is.
type ReportKind uint8

const (
	// ReportMetric: one metric of an accepted BIRTH or DATA message.
	ReportMetric ReportKind = iota + 1
	// ReportNodeBirth: an accepted NBIRTH, whether it opened a new session or answered a commanded
	// rebirth. A redelivered NBIRTH of the live session is not an accepted birth and yields none.
	ReportNodeBirth
)

// Report is one thing a node said that a command may be waiting on.
type Report struct {
	Kind ReportKind
	// Target is the node (Device empty) or device that reported.
	Target CommandTarget
	// Name is the metric's name, resolved through the session's aliases (ReportMetric only).
	Name string
	// Via is the message type that carried it: NBIRTH, DBIRTH, NDATA or DDATA.
	Via string
	// Value is the metric's value in the form of its BIRTH-declared datatype (ReportMetric only).
	Value WireValue
	// At is when the node says the value held: the metric's own timestamp, else the payload's,
	// else the moment it was received.
	At time.Time
	// ReceivedAt is when the Host received the message, on the Host's clock.
	ReceivedAt time.Time
	// BdSeq and HasBdSeq are the birth/death sequence of the session the NBIRTH opened or
	// re-announced (ReportNodeBirth only).
	BdSeq    uint64
	HasBdSeq bool
}

// WireValue is a metric value in a form that compares with ==: what the datatype means, not which
// protobuf field an encoder happened to put it in. Two values are the same value when the
// WireValues are equal. Floats compare exactly (a Float is compared as the float32 it is), which
// is documented because "the node reports the setpoint I wrote" is exact for a value that was
// written, and an approximate match would confirm a node that rounded it to something else.
type WireValue struct {
	// Kind is 0 (none), 'i' (an integer, in N), 'f' (a float, in F), 'b' (a boolean, in N as 0/1)
	// or 's' (a string, in S).
	Kind byte
	// N is an integer's value as the 64-bit two's complement of its signed value for a signed type,
	// and its magnitude for an unsigned one.
	N uint64
	F float64
	S string
}

// RequestedWire is the WireValue a requested value has once encoded for the BIRTH-declared
// datatype - the form a report of that value will have.
func RequestedWire(datatype uint32, value any) (WireValue, error) {
	set, err := EncodeMetricValue(datatype, value)
	if err != nil {
		return WireValue{}, err
	}
	m := &sppb.Payload_Metric{}
	set(m)
	v, ok := wireValueOf(datatype, m)
	if !ok {
		return WireValue{}, refuse("datatype %d has no comparable form", datatype)
	}
	return v, nil
}

// wireValueOf reads a metric's value as a WireValue of datatype. An integer is read from whichever
// integer field the encoder used (encoders disagree on where a UInt32 rides, and on whether a
// narrow signed value is sign-extended or truncated), narrowed by the datatype's own width and
// sign. A value in a field the datatype does not use is not a report of that datatype.
func wireValueOf(datatype uint32, m *sppb.Payload_Metric) (WireValue, bool) {
	var carrier uint64
	var isInt, isLong bool
	switch v := m.GetValue().(type) {
	case *sppb.Payload_Metric_IntValue:
		carrier, isInt = uint64(v.IntValue), true
	case *sppb.Payload_Metric_LongValue:
		carrier, isLong = v.LongValue, true
	}
	switch datatype {
	case datatypeInt8:
		if isInt || isLong {
			return WireValue{Kind: 'i', N: uint64(int64(int8(uint8(carrier))))}, true
		}
	case datatypeInt16:
		if isInt || isLong {
			return WireValue{Kind: 'i', N: uint64(int64(int16(uint16(carrier))))}, true
		}
	case datatypeInt32:
		if isInt || isLong {
			return WireValue{Kind: 'i', N: uint64(int64(int32(uint32(carrier))))}, true
		}
	case datatypeUInt8:
		if isInt || isLong {
			return WireValue{Kind: 'i', N: carrier & 0xFF}, true
		}
	case datatypeUInt16:
		if isInt || isLong {
			return WireValue{Kind: 'i', N: carrier & 0xFFFF}, true
		}
	case datatypeUInt32:
		if isInt || isLong {
			return WireValue{Kind: 'i', N: carrier & 0xFFFFFFFF}, true
		}
	case datatypeInt64, datatypeUInt64, datatypeDateTime:
		if isLong {
			return WireValue{Kind: 'i', N: carrier}, true
		}
	case datatypeFloat:
		if f, ok := m.GetValue().(*sppb.Payload_Metric_FloatValue); ok {
			return WireValue{Kind: 'f', F: float64(f.FloatValue)}, true
		}
	case datatypeDouble:
		if f, ok := m.GetValue().(*sppb.Payload_Metric_DoubleValue); ok {
			return WireValue{Kind: 'f', F: f.DoubleValue}, true
		}
	case datatypeBoolean:
		if b, ok := m.GetValue().(*sppb.Payload_Metric_BooleanValue); ok {
			n := uint64(0)
			if b.BooleanValue {
				n = 1
			}
			return WireValue{Kind: 'b', N: n}, true
		}
	case datatypeString, datatypeText, datatypeUUID:
		if s, ok := m.GetValue().(*sppb.Payload_Metric_StringValue); ok {
			return WireValue{Kind: 's', S: s.StringValue}, true
		}
	}
	return WireValue{}, false
}

// EnableReports turns on the capture of Reports and last-reported values. A tracker whose source
// does not command anything pays nothing for it.
func (tr *SessionTracker) EnableReports() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.capture = true
}

// stepContext is the message the current step is processing, for the report capture. Set under mu.
type stepContext struct {
	key nodeKey
	mt  MessageType
}

// captureReports copies what an ACCEPTED BIRTH or DATA message said into tr.reports, and moves
// each metric's last-reported value. The caller holds tr.mu, and it is called from extract, which
// runs only for a message the session machine accepted (valid seq, aliases resolve).
//
//   - A metric the sender flagged historical is NOT a report: it is a replay of the past, and a
//     replay that happens to hold the requested value says nothing about the present.
//   - A null metric is not a value: it clears the last value (the node no longer vouches for one)
//     and reports nothing.
//   - The reserved control metrics (bdSeq, Node Control/*) are protocol, never a report.
func (tr *SessionTracker) captureReports(p *sppb.Payload, s *nodeSession, device string) {
	if !tr.capture || len(p.GetMetrics()) > MaxMetricsPerMessage {
		return
	}
	now := tr.now()
	target := CommandTarget{Group: tr.cur.key.group, Node: tr.cur.key.node, Device: device}
	via := string(tr.cur.mt)
	if tr.cur.mt == NBIRTH {
		tr.reports = append(tr.reports, Report{
			Kind: ReportNodeBirth, Target: CommandTarget{Group: target.Group, Node: target.Node}, Via: via,
			ReceivedAt: now, BdSeq: s.bdSeq, HasBdSeq: s.hasBdSeq,
		})
	}
	payloadTs := p.GetTimestamp()
	for _, m := range p.GetMetrics() {
		if m.GetIsHistorical() {
			continue
		}
		name := metricName(m, s.aliases)
		if name == "" || isReservedMetric(name) {
			continue
		}
		key := metricKey{device, name}
		dm, ok := s.metrics[key]
		if !ok {
			continue
		}
		if m.GetIsNull() {
			dm.hasLast = false
			s.metrics[key] = dm
			continue
		}
		dt := dm.datatype
		if dt == 0 {
			dt = m.GetDatatype()
		}
		v, ok := wireValueOf(dt, m)
		if !ok {
			continue
		}
		at := time.UnixMilli(metricTime(m, payloadTs, tr.now))
		dm.last, dm.hasLast, dm.lastAt = v, true, at
		s.metrics[key] = dm
		tr.reports = append(tr.reports, Report{
			Kind: ReportMetric, Target: target, Name: name, Via: via, Value: v, At: at, ReceivedAt: now,
		})
	}
}

// LastReported is the value the metric was last reported at on the table ref was read from, and
// when. It answers false when the table has since been replaced (a rebirth may have changed what
// the node holds) or when no value has been reported.
func (tr *SessionTracker) LastReported(ref MetricRef) (WireValue, time.Time, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s := tr.sessions[nodeKey{ref.Target.Group, ref.Target.Node}]
	if s == nil || !s.online || s.invalidated || s.sessionEpoch != ref.Epoch || s.birthGen != ref.Gen {
		return WireValue{}, time.Time{}, false
	}
	dm, ok := s.metrics[metricKey{ref.Target.Device, ref.Name}]
	if !ok || !dm.hasLast {
		return WireValue{}, time.Time{}, false
	}
	return dm.last, dm.lastAt, true
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// What the session machine copies out for the command path (G19 S4): reports of accepted BIRTH and
// DATA metrics, and each metric's last-reported value.

func reportingHarness() *sessionHarness {
	h := newHarness(defaultRebirthBackoff)
	h.tr.EnableReports(nil)
	return h
}

func intM(alias uint64, v uint32) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Alias: proto.Uint64(alias), Value: &sppb.Payload_Metric_IntValue{IntValue: v}}
}

func reportsOf(obs Observation, kind ReportKind) []Report {
	var out []Report
	for _, r := range obs.Reports {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// bornForReports sends the NBIRTH (bdSeq 5; Setpoint Int8 alias 1, Mode String, Level Double alias 4)
// and a DBIRTH for d1 (Valve Boolean alias 2, Setpoint Int32 alias 3), each carrying values.
func bornForReports(h *sessionHarness) (nb, db Observation) {
	setpoint := declared("Setpoint", datatypeInt8, 1)
	setpoint.Value = &sppb.Payload_Metric_IntValue{IntValue: 1}
	mode := declared("Mode", datatypeString, 0)
	mode.Value = &sppb.Payload_Metric_StringValue{StringValue: "auto"}
	level := declared("Level", datatypeDouble, 4)
	level.Value = &sppb.Payload_Metric_DoubleValue{DoubleValue: 2.5}
	nb = h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), setpoint, mode, level, declared("Node Control/Rebirth", datatypeBoolean, 0)))
	valve := declared("Valve", datatypeBoolean, 2)
	valve.Value = &sppb.Payload_Metric_BooleanValue{BooleanValue: false}
	dsp := declared("Setpoint", datatypeInt32, 3)
	dsp.Value = &sppb.Payload_Metric_IntValue{IntValue: 70}
	db = h.tr.Observe(dTop(DBIRTH, "d1"), pl(1, valve, dsp))
	return nb, db
}

// A tracker that was not asked to capture reports nothing and keeps no last values: a source that
// commands nothing pays nothing.
func TestAnUnarmedTrackerReportsNothing(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	nb, db := bornForReports(h)
	assert.Empty(t, nb.Reports)
	assert.Empty(t, db.Reports)
	ref, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	_, _, ok := h.tr.LastReported(ref)
	assert.False(t, ok)

	// Control: the same stream, armed, reports.
	h2 := reportingHarness()
	nb, _ = bornForReports(h2)
	assert.NotEmpty(t, nb.Reports)
}

func TestABirthReportsItsMetricsAndTheNodeBirth(t *testing.T) {
	h := reportingHarness()
	nb, db := bornForReports(h)

	births := reportsOf(nb, ReportNodeBirth)
	require.Len(t, births, 1)
	assert.Equal(t, nodeT, births[0].Target)
	assert.Equal(t, "NBIRTH", births[0].Via)
	assert.True(t, births[0].HasBdSeq)
	assert.Equal(t, uint64(5), births[0].BdSeq)
	assert.Equal(t, h.now, births[0].ReceivedAt)

	byName := map[string]Report{}
	for _, r := range reportsOf(nb, ReportMetric) {
		byName[r.Name] = r
	}
	require.Len(t, byName, 3, "bdSeq and Node Control/* are protocol, not reports; the rest are")
	assert.Equal(t, WireValue{Kind: 'i', N: 1}, byName["Setpoint"].Value)
	assert.Equal(t, WireValue{Kind: 's', S: "auto"}, byName["Mode"].Value)
	assert.Equal(t, WireValue{Kind: 'f', F: 2.5}, byName["Level"].Value)
	assert.Equal(t, nodeT, byName["Setpoint"].Target)

	// The device's metrics are its own: the same name under another datatype, on the device target.
	dev := map[string]Report{}
	for _, r := range reportsOf(db, ReportMetric) {
		dev[r.Name] = r
	}
	require.Len(t, dev, 2)
	assert.Equal(t, devT, dev["Setpoint"].Target)
	assert.Equal(t, WireValue{Kind: 'i', N: 70}, dev["Setpoint"].Value)
	assert.Equal(t, WireValue{Kind: 'b', N: 0}, dev["Valve"].Value)
	assert.Equal(t, "DBIRTH", dev["Valve"].Via)
	assert.Empty(t, reportsOf(db, ReportNodeBirth), "a DBIRTH is not a node birth")
}

// 🔴 A DATA metric is resolved through the BIRTH's alias to a name and compared in the form of the
// DECLARED datatype: an Int8 of -1 is the same value however the encoder spread it over the integer
// field (sign-extended to 32 bits, or truncated to 8), and it is the value a command for -1 encodes.
func TestADataMetricIsReportedByNameInTheFormOfItsDeclaredDatatype(t *testing.T) {
	want, err := RequestedWire(datatypeInt8, -1)
	require.NoError(t, err)
	assert.Equal(t, WireValue{Kind: 'i', N: ^uint64(0)}, want, "a signed value is its two's complement, sign-extended")
	for name, carrier := range map[string]uint32{"sign-extended": 0xFFFFFFFF, "truncated": 0xFF} {
		t.Run(name, func(t *testing.T) {
			h := reportingHarness()
			bornForReports(h)
			obs := h.tr.Observe(nTop(NDATA), pl(2, intM(1, carrier)))
			r := reportsOf(obs, ReportMetric)
			require.Len(t, r, 1)
			assert.Equal(t, "Setpoint", r[0].Name, "the alias was not resolved to the BIRTH's name")
			assert.Equal(t, "NDATA", r[0].Via)
			assert.Equal(t, want, r[0].Value)
		})
	}
	// Control: a different value is a different WireValue.
	h := reportingHarness()
	bornForReports(h)
	obs := h.tr.Observe(nTop(NDATA), pl(2, intM(1, 0xFE)))
	assert.NotEqual(t, want, reportsOf(obs, ReportMetric)[0].Value)

	// The device's Setpoint (Int32) is not the node's (Int8): 0xFFFFFFFF is -1 there too, but 255
	// is not -1 as an Int32.
	d := h.tr.Observe(dTop(DDATA, "d1"), pl(3, intM(3, 0xFF)))
	want32, _ := RequestedWire(datatypeInt32, -1)
	assert.NotEqual(t, want32, reportsOf(d, ReportMetric)[0].Value)
	assert.Equal(t, devT, reportsOf(d, ReportMetric)[0].Target)
}

// Every scalar datatype reports in the form a command for it encodes to.
func TestEveryDatatypeReportsInTheFormACommandForItEncodesTo(t *testing.T) {
	str := func(s string) *sppb.Payload_Metric_StringValue {
		return &sppb.Payload_Metric_StringValue{StringValue: s}
	}
	for _, tc := range []struct {
		name      string
		datatype  uint32
		requested any
		value     func(m *sppb.Payload_Metric)
	}{
		{"UInt8", datatypeUInt8, 200, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_IntValue{IntValue: 200} }},
		{"Int16 negative", datatypeInt16, -300, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_IntValue{IntValue: 0xFFFFFED4} }},
		{"UInt32 in long_value", datatypeUInt32, 4000000000, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_LongValue{LongValue: 4000000000} }},
		{"Int64 negative", datatypeInt64, -5, func(m *sppb.Payload_Metric) {
			m.Value = &sppb.Payload_Metric_LongValue{LongValue: 0xFFFFFFFFFFFFFFFB}
		}},
		{"UInt64 above 2^53", datatypeUInt64, "18446744073709551615", func(m *sppb.Payload_Metric) {
			m.Value = &sppb.Payload_Metric_LongValue{LongValue: 18446744073709551615}
		}},
		{"Float", datatypeFloat, 1.5, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_FloatValue{FloatValue: 1.5} }},
		{"Double", datatypeDouble, 0.1, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_DoubleValue{DoubleValue: 0.1} }},
		{"Boolean", datatypeBoolean, true, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_BooleanValue{BooleanValue: true} }},
		{"String", datatypeString, "héllo", func(m *sppb.Payload_Metric) { m.Value = str("héllo") }},
		{"DateTime", datatypeDateTime, 1700000000123, func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_LongValue{LongValue: 1700000000123} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := RequestedWire(tc.datatype, tc.requested)
			require.NoError(t, err)
			m := &sppb.Payload_Metric{}
			tc.value(m)
			got, ok := wireValueOf(tc.datatype, m)
			require.True(t, ok)
			assert.Equal(t, want, got)
		})
	}
	// A value in a field the datatype does not use is not a report of that datatype.
	_, ok := wireValueOf(datatypeBoolean, &sppb.Payload_Metric{Value: &sppb.Payload_Metric_IntValue{IntValue: 1}})
	assert.False(t, ok)
	_, ok = wireValueOf(datatypeString, &sppb.Payload_Metric{Value: &sppb.Payload_Metric_LongValue{LongValue: 1}})
	assert.False(t, ok)
	_, ok = wireValueOf(datatypeInt64, &sppb.Payload_Metric{Value: &sppb.Payload_Metric_IntValue{IntValue: 1}})
	assert.False(t, ok, "an Int64 rides long_value")
	// Floats compare exactly: a Float of 0.1 is not the Double 0.1 it was rounded from.
	f32, _ := RequestedWire(datatypeFloat, 0.1)
	f64, _ := RequestedWire(datatypeDouble, 0.1)
	assert.NotEqual(t, f32, f64)
}

// 🔴 A metric flagged historical is a replay of the past, and is NOT a report - even when it holds
// the requested value. A null is not a value. The paired control reports the same value unflagged.
func TestAHistoricalOrNullMetricIsNotAReport(t *testing.T) {
	h := reportingHarness()
	bornForReports(h)

	hist := intM(1, 9)
	hist.IsHistorical = proto.Bool(true)
	obs := h.tr.Observe(nTop(NDATA), pl(2, hist))
	assert.Empty(t, obs.Reports, "a historical metric was reported")
	ref, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	last, _, _ := h.tr.LastReported(ref)
	assert.Equal(t, WireValue{Kind: 'i', N: 1}, last, "a historical metric moved the last-reported value")

	obs = h.tr.Observe(nTop(NDATA), pl(3, intM(1, 9)))
	require.Len(t, obs.Reports, 1, "control: the same value, not historical, is a report")
	last, _, ok := h.tr.LastReported(ref)
	require.True(t, ok)
	assert.Equal(t, WireValue{Kind: 'i', N: 9}, last)

	null := &sppb.Payload_Metric{Alias: proto.Uint64(1), IsNull: proto.Bool(true)}
	obs = h.tr.Observe(nTop(NDATA), pl(4, null))
	assert.Empty(t, obs.Reports, "a null metric was reported as a value")
	_, _, ok = h.tr.LastReported(ref)
	assert.False(t, ok, "a null left a last value standing: the node no longer vouches for it")
}

// A report is timestamped by the metric's own time, else the payload's, else the moment of receipt.
func TestAReportIsTimestampedMetricThenPayloadThenReceipt(t *testing.T) {
	h := reportingHarness()
	bornForReports(h)
	own := intM(1, 2)
	own.Timestamp = proto.Uint64(1_800_000_000_111)
	p := pl(2, own, intM(1, 3))
	p.Timestamp = proto.Uint64(1_800_000_000_222)
	got := h.tr.Observe(nTop(NDATA), p)
	r := reportsOf(got, ReportMetric)
	require.Len(t, r, 2)
	assert.Equal(t, time.UnixMilli(1_800_000_000_111), r[0].At)
	assert.Equal(t, time.UnixMilli(1_800_000_000_222), r[1].At)

	got = h.tr.Observe(nTop(NDATA), pl(3, intM(1, 4)))
	assert.Equal(t, h.now.UnixMilli(), reportsOf(got, ReportMetric)[0].At.UnixMilli(), "no timestamp anywhere: the moment of receipt")
}

// A message the machine REJECTED reports nothing (an unknown alias, a seq gap, data before birth),
// and a redelivered NBIRTH of the live session is not a new birth.
func TestRejectedMessagesAndRedeliveredBirthsReportNothing(t *testing.T) {
	h := reportingHarness()
	bornForReports(h)
	gap := h.tr.Observe(nTop(NDATA), pl(9, intM(1, 5)))
	assert.Empty(t, gap.Reports, "a message with a seq gap was reported")

	h = reportingHarness()
	bornForReports(h)
	unknown := h.tr.Observe(nTop(NDATA), pl(2, intM(77, 5)))
	assert.Empty(t, unknown.Reports, "a message with an unknown alias was reported")

	h = reportingHarness()
	bornForReports(h)
	early := h.tr.Observe(Topic{GroupID: "g", EdgeNodeID: "other", MessageType: NDATA}, pl(1, intM(1, 5)))
	assert.Empty(t, early.Reports, "data before a birth was reported")

	h = reportingHarness()
	bornForReports(h)

	// A QoS-1 redelivery of the NBIRTH (same bdSeq, not asked for) is left alone.
	again := h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Setpoint", datatypeInt8, 1)))
	assert.Empty(t, again.Reports, "a redelivered NBIRTH was reported as a new birth")

	// A commanded rebirth answers with the SAME bdSeq: it is reported (with that bdSeq, so a reboot
	// is not confirmed by it) and carries the current values.
	h.tr.MarkCommandedRebirth("g", "n")
	sp := declared("Setpoint", datatypeInt8, 1)
	sp.Value = &sppb.Payload_Metric_IntValue{IntValue: 4}
	answer := h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), sp))
	births := reportsOf(answer, ReportNodeBirth)
	require.Len(t, births, 1)
	assert.Equal(t, uint64(5), births[0].BdSeq)
	assert.Len(t, reportsOf(answer, ReportMetric), 1)

	// A new session has a new bdSeq.
	fresh := h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(6), sp))
	births = reportsOf(fresh, ReportNodeBirth)
	require.Len(t, births, 1)
	assert.Equal(t, uint64(6), births[0].BdSeq)
}

// The last-reported value is what lets the dispatcher tell "already there" from "ignored", and it
// is dropped with the table it belongs to.
func TestLastReportedIsKeptPerTableAndDroppedWithIt(t *testing.T) {
	h := reportingHarness()
	bornForReports(h)
	ref, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	last, at, ok := h.tr.LastReported(ref)
	require.True(t, ok)
	assert.Equal(t, WireValue{Kind: 'i', N: 1}, last)
	assert.Equal(t, h.now.UnixMilli(), at.UnixMilli())

	h.tr.Observe(nTop(NDATA), pl(2, intM(1, 7)))
	last, _, _ = h.tr.LastReported(ref)
	assert.Equal(t, WireValue{Kind: 'i', N: 7}, last, "DATA does not move the table's generation, and the last value follows it")

	// A rebirth replaces the table: a ref from before it says nothing about what the node holds now.
	h.tr.MarkCommandedRebirth("g", "n")
	again := declared("Setpoint", datatypeInt8, 1)
	again.Value = &sppb.Payload_Metric_IntValue{IntValue: 7}
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), again))
	_, _, ok = h.tr.LastReported(ref)
	assert.False(t, ok, "a last value from a replaced table was trusted")
	fresh, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	last, _, ok = h.tr.LastReported(fresh)
	require.True(t, ok, "control: the same value is read through a ref to the new table")
	assert.Equal(t, WireValue{Kind: 'i', N: 7}, last)

	// And the end of the Host session forgets it.
	cur, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	h.tr.Observe(nTop(NDATA), pl(1, intM(1, 3)))
	_, _, ok = h.tr.LastReported(cur)
	require.True(t, ok)
	h.tr.EndHostSession()
	_, _, ok = h.tr.LastReported(cur)
	assert.False(t, ok)
}

// A message over the metric cap reports nothing, as it yields no samples.
func TestAnOversizeMessageReportsNothing(t *testing.T) {
	h := reportingHarness()
	bornForReports(h)
	ms := make([]*sppb.Payload_Metric, MaxMetricsPerMessage+1)
	for i := range ms {
		ms[i] = intM(1, 1)
	}
	assert.Empty(t, h.tr.Observe(nTop(NDATA), pl(2, ms...)).Reports)
}

// The reserved control metrics are protocol, never a report - even when a node declares one with a
// value (a Reboot trigger sitting at false).
func TestReservedControlMetricsAreNeverReports(t *testing.T) {
	h := reportingHarness()
	reboot := declared("Node Control/Reboot", datatypeBoolean, 0)
	reboot.Value = &sppb.Payload_Metric_BooleanValue{BooleanValue: false}
	scan := declared("Node Control/Scan Rate", datatypeInt32, 0)
	scan.Value = &sppb.Payload_Metric_IntValue{IntValue: 3000}
	ok := declared("Setpoint", datatypeInt8, 1)
	ok.Value = &sppb.Payload_Metric_IntValue{IntValue: 1}
	bd := bdSeqM(5)
	bd.Datatype = proto.Uint32(datatypeInt64)
	obs := h.tr.Observe(nTop(NBIRTH), pl(0, bd, reboot, scan, ok))
	r := reportsOf(obs, ReportMetric)
	require.Len(t, r, 1, "a control metric was reported")
	assert.Equal(t, "Setpoint", r[0].Name)
}

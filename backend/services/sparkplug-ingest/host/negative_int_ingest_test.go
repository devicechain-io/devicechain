// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// 🔴 A CONFORMANT NDATA/DDATA OMITS THE DATATYPE ([tck-id-payloads-metric-datatype-not-req]: it
// SHOULD NOT be included in DATA). Signedness is therefore known only from the BIRTH. Reading an
// integer carrier of a typeless metric as magnitude ingested every negative Int8/Int16/Int32 as a
// number near 4.29e9. These tests drive the real session machine with BIRTHs that declare the
// types and DATA that does not.

func birthTyped(name string, alias uint64, dt uint32) *sppb.Payload_Metric {
	m := &sppb.Payload_Metric{Name: proto.String(name), Datatype: proto.Uint32(dt),
		Value: &sppb.Payload_Metric_IntValue{IntValue: 0}}
	if alias > 0 {
		m.Alias = proto.Uint64(alias)
	}
	return m
}

// untypedInt is a DATA metric: an alias or a name, an int carrier, NO datatype.
func untypedInt(alias uint64, name string, v uint32) *sppb.Payload_Metric {
	m := &sppb.Payload_Metric{Value: &sppb.Payload_Metric_IntValue{IntValue: v}}
	if alias > 0 {
		m.Alias = proto.Uint64(alias)
	}
	if name != "" {
		m.Name = proto.String(name)
	}
	return m
}

func untypedLong(alias uint64, v uint64) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Alias: proto.Uint64(alias), Value: &sppb.Payload_Metric_LongValue{LongValue: v}}
}

func valuesOf(obs Observation) map[string]float64 {
	out := map[string]float64{}
	for _, s := range obs.Samples {
		out[s.Name] = s.Value
	}
	return out
}

func bornTyped(h *sessionHarness) {
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(1),
		birthTyped("t8", 1, datatypeInt8), birthTyped("t16", 2, datatypeInt16), birthTyped("t32", 3, datatypeInt32),
		birthTyped("u32", 4, datatypeUInt32), birthTyped("byname8", 0, datatypeInt8),
		&sppb.Payload_Metric{Name: proto.String("t64"), Alias: proto.Uint64(5), Datatype: proto.Uint32(datatypeInt64),
			Value: &sppb.Payload_Metric_LongValue{LongValue: 0}}))
	h.tr.Observe(dTop(DBIRTH, "d1"), pl(1, birthTyped("dv16", 10, datatypeInt16), birthTyped("byname8", 0, datatypeInt16)))
}

// Both common encoder conventions are read: sign-extended to 32 bits (Tahu Java/C) and truncated
// to the type's width (Tahu Python).
func TestNegativeIntegersWithoutADatatypeAreReadByTheirBirthType(t *testing.T) {
	for _, enc := range []struct {
		name         string
		i8, i16, i32 uint32
	}{
		{"sign-extended", 0xFFFFFF80, 0xFFFF8000, 0x80000000},
		{"width-truncated", 0x80, 0x8000, 0x80000000},
	} {
		t.Run(enc.name, func(t *testing.T) {
			h := newHarness(defaultRebirthBackoff)
			bornTyped(h)
			obs := h.tr.Observe(nTop(NDATA), pl(2,
				untypedInt(1, "", enc.i8), untypedInt(2, "", enc.i16), untypedInt(3, "", enc.i32),
				untypedLong(5, 1<<63)))
			got := valuesOf(obs)
			assert.Equal(t, -128.0, got["t8"])
			assert.Equal(t, -32768.0, got["t16"])
			assert.Equal(t, -2147483648.0, got["t32"])
			assert.Equal(t, -9223372036854775808.0, got["t64"])
			assert.Zero(t, obs.Skipped.Total())
		})
	}
}

// A device's DDATA, aliased and by name; the by-name declaration is device-scoped (the node and
// the device both declare "byname8", with different types).
func TestNegativeIntegersInDDataWithoutADatatype(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	obs := h.tr.Observe(dTop(DDATA, "d1"), pl(2, untypedInt(10, "", 0xFFFFFFFF), untypedInt(0, "byname8", 0xFFFFFFFE)))
	got := valuesOf(obs)
	assert.Equal(t, -1.0, got["dv16"])
	assert.Equal(t, -2.0, got["byname8"], "the device's own Int16 declaration, read by name")

	// The node's metric of the same name keeps its own declaration: Int8, so 0x80 is -128 there.
	obs = h.tr.Observe(nTop(NDATA), pl(3, untypedInt(0, "byname8", 0x80)))
	assert.Equal(t, -128.0, valuesOf(obs)["byname8"])
}

// Controls: what must NOT change. An unsigned type stays magnitude (a fix that sign-reads
// everything would turn this negative), and a Double needs no datatype.
func TestUnsignedAndFloatingPointAreUnchanged(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	obs := h.tr.Observe(nTop(NDATA), pl(2, untypedInt(4, "", 0xFFFFFFFF),
		&sppb.Payload_Metric{Name: proto.String("d"), Value: &sppb.Payload_Metric_DoubleValue{DoubleValue: -1.5}}))
	got := valuesOf(obs)
	assert.Equal(t, 4294967295.0, got["u32"])
	assert.Equal(t, -1.5, got["d"])
	assert.Zero(t, obs.Skipped.Total())
}

// A metric's own datatype, when it names one, is honoured as before.
func TestAnExplicitDatatypeOnDataStillWins(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	m := untypedInt(1, "", 0xFFFFFFFF)
	m.Datatype = proto.Uint32(datatypeInt16)
	assert.Equal(t, -1.0, valuesOf(h.tr.Observe(nTop(NDATA), pl(2, m)))["t8"])
}

// 🔴 NEITHER KNOWN => REFUSED AND COUNTED, NOT GUESSED. A by-name integer the BIRTH never
// declared has no signedness: reading it as magnitude is exactly the old bug.
func TestAnUntypedIntegerWithNoBirthDeclarationIsCountedNotGuessed(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	obs := h.tr.Observe(nTop(NDATA), pl(2, untypedInt(0, "never-declared", 0xFFFFFFFF), untypedInt(1, "", 7)))
	assert.NotContains(t, valuesOf(obs), "never-declared", "no value is invented for it")
	assert.Equal(t, 1, obs.Skipped.Untyped)
	assert.Equal(t, 7.0, valuesOf(obs)["t8"], "control: the declared metric of the same message is read")
	assert.Equal(t, 0, h.rec.count(), "an undeclared name is not a session fault")

	// The 64-bit arm is refused the same way.
	obs = h.tr.Observe(nTop(NDATA), pl(3, &sppb.Payload_Metric{Name: proto.String("never-declared-64"),
		Value: &sppb.Payload_Metric_LongValue{LongValue: ^uint64(0)}}))
	assert.Empty(t, obs.Samples)
	assert.Equal(t, 1, obs.Skipped.Untyped)
}

// A death forgets the declarations: a dead device's by-name type does not survive into a rebirth
// that no longer declares it.
func TestDeclarationsDoNotOutliveTheirBirth(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	h.tr.Observe(dTop(DDEATH, "d1"), pl(2))
	h.tr.Observe(dTop(DBIRTH, "d1"), pl(3, birthTyped("other", 20, datatypeInt8)))
	obs := h.tr.Observe(dTop(DDATA, "d1"), pl(4, untypedInt(0, "byname8", 0xFFFFFFFF)))
	assert.Equal(t, 1, obs.Skipped.Untyped, "the old device's by-name declaration is gone")
	require.NotNil(t, obs)
}

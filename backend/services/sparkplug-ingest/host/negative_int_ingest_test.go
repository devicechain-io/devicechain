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

// A metric's own datatype, when it names one, WINS over the BIRTH's. The two are made to
// disagree, so swapping the precedence changes the answer: t8 was born Int8.
func TestAnExplicitDatatypeOnDataWinsOverTheBirthsType(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	// Explicit UInt32: 0xFFFFFFFF is 4294967295 (the birth's Int8 would read it as -1).
	m := untypedInt(1, "", 0xFFFFFFFF)
	m.Datatype = proto.Uint32(datatypeUInt32)
	assert.Equal(t, 4294967295.0, valuesOf(h.tr.Observe(nTop(NDATA), pl(2, m)))["t8"])
	// Explicit Int16: 0xFF00 is -256 (the birth's Int8 would narrow it to 0).
	m = untypedInt(1, "", 0xFF00)
	m.Datatype = proto.Uint32(datatypeInt16)
	assert.Equal(t, -256.0, valuesOf(h.tr.Observe(nTop(NDATA), pl(3, m)))["t8"])
}

// Signed types narrower than 64 bits that an encoder put in long_value are narrowed by width too.
func TestNarrowSignedTypesInLongValueAreNarrowedByWidth(t *testing.T) {
	for _, c := range []struct {
		dt   uint32
		v    uint64
		want float64
	}{
		{datatypeInt8, 0xFF, -1}, {datatypeInt8, ^uint64(0), -1}, {datatypeInt16, 0xFFFF, -1},
		{datatypeInt32, 0xFFFFFFFF, -1}, {datatypeInt32, 0x80000000, -2147483648}, {datatypeInt8, 5, 5},
		{datatypeInt64, ^uint64(0), -1}, {datatypeUInt32, 0xFFFFFFFF, 4294967295},
	} {
		assert.Equal(t, c.want, longValueAsFloat(c.v, c.dt), "datatype %d value %#x", c.dt, c.v)
	}
}

// A re-BIRTH replaces a scope's declarations: a metric it no longer declares keeps no type.
func TestARebirthDoesNotKeepTypesItNoLongerDeclares(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	h.tr.Observe(dTop(DBIRTH, "d1"), pl(2, birthTyped("other", 30, datatypeInt8)))
	obs := h.tr.Observe(dTop(DDATA, "d1"), pl(3, untypedInt(0, "dv16", 0xFFFF)))
	assert.Equal(t, 1, obs.Skipped.Untyped, "dv16 was dropped from the device's declarations")
	assert.Equal(t, "dv16", obs.Skipped.UntypedFirst)
}

// 🔴 NEITHER KNOWN: READ WHAT CANNOT BE MISREAD, REFUSE AND COUNT WHAT CAN. A by-name integer the
// BIRTH never declared has no signedness. A value that reads the same under every integer type (a
// counter at 1000) is stored as magnitude; one with a high bit set at some width - the bands
// [0x80,0xFF] [0x8000,0xFFFF] [0x80000000,0xFFFFFFFF], and for long_value also >= 2^63 - reads
// differently as signed and unsigned, and is skipped as untyped rather than guessed.
func TestAnUntypedIntegerWithNoBirthDeclarationIsStoredOrSkippedByBand(t *testing.T) {
	stored := []uint32{0, 1, 127, 128 - 1, 256, 1000, 0x7FFF, 0x10000, 0x7FFFFFFF}
	skipped := []uint32{0x80, 255, 0xFF, 0x8000, 0xFF00, 65535, 0x80000000, 0xFFFFFFFE, 0xFFFFFFFF}
	for _, v := range stored {
		h := newHarness(defaultRebirthBackoff)
		bornTyped(h)
		obs := h.tr.Observe(nTop(NDATA), pl(2, untypedInt(0, "counter", v)))
		assert.Equal(t, float64(v), valuesOf(obs)["counter"], "%#x reads the same under every type", v)
		assert.Zero(t, obs.Skipped.Untyped)
	}
	for _, v := range skipped {
		h := newHarness(defaultRebirthBackoff)
		bornTyped(h)
		obs := h.tr.Observe(nTop(NDATA), pl(2, untypedInt(0, "counter", v), untypedInt(1, "", 7)))
		assert.NotContains(t, valuesOf(obs), "counter", "%#x is ambiguous and is not invented", v)
		assert.Equal(t, 1, obs.Skipped.Untyped, "%#x", v)
		assert.Equal(t, "counter", obs.Skipped.UntypedFirst)
		assert.Equal(t, 7.0, valuesOf(obs)["t8"], "control: the declared metric of the same message is read")
		assert.Equal(t, 0, h.rec.count(), "an undeclared name is not a session fault: no rebirth")
	}

	// The 64-bit arm: the same three bands, and everything from 2^63 up.
	for v, wantSkip := range map[uint64]bool{
		1000: false, 0x100000000: false, 1<<63 - 1: false,
		0xFF: true, 0xFFFF: true, 0xFFFFFFFF: true, 1 << 63: true, ^uint64(0): true,
	} {
		h := newHarness(defaultRebirthBackoff)
		bornTyped(h)
		obs := h.tr.Observe(nTop(NDATA), pl(2, &sppb.Payload_Metric{Name: proto.String("big"),
			Value: &sppb.Payload_Metric_LongValue{LongValue: v}}))
		if wantSkip {
			assert.Empty(t, obs.Samples, "%#x", v)
			assert.Equal(t, 1, obs.Skipped.Untyped, "%#x", v)
		} else {
			assert.Equal(t, float64(v), valuesOf(obs)["big"], "%#x", v)
			assert.Zero(t, obs.Skipped.Untyped, "%#x", v)
		}
	}
}

// The untyped warning is due once per window, not once per message.
func TestUntypedWarningIsRateLimited(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := &Client{now: func() time.Time { return now }}
	assert.True(t, c.shouldWarnUntyped(), "the first is due")
	assert.False(t, c.shouldWarnUntyped(), "the next, in the same window, is not")
	now = now.Add(untypedWarnEvery - time.Second)
	assert.False(t, c.shouldWarnUntyped())
	now = now.Add(2 * time.Second)
	assert.True(t, c.shouldWarnUntyped(), "due again once the window has passed")
}

// A death forgets the declarations: a dead device's by-name type does not survive into a rebirth
// that no longer declares it.
func TestDeclarationsDoNotOutliveTheirBirth(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornTyped(h)
	h.tr.Observe(dTop(DDEATH, "d1"), pl(2))
	assert.NotContains(t, h.sess().types.byDevice, "d1", "a dead device's declarations are dropped with it")
	h.tr.Observe(dTop(DBIRTH, "d1"), pl(3, birthTyped("other", 20, datatypeInt8)))
	obs := h.tr.Observe(dTop(DDATA, "d1"), pl(4, untypedInt(0, "byname8", 0xFFFFFFFF)))
	assert.Equal(t, 1, obs.Skipped.Untyped, "the old device's by-name declaration is gone")
	require.NotNil(t, obs)
}

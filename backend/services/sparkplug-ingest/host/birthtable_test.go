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

// declared is a BIRTH metric: name, datatype, and an alias when alias > 0 (0 = the node does not
// alias this metric).
func declared(name string, dt uint32, alias uint64) *sppb.Payload_Metric {
	m := &sppb.Payload_Metric{Name: proto.String(name), Datatype: proto.Uint32(dt)}
	if alias > 0 {
		m.Alias = proto.Uint64(alias)
	}
	return m
}

var (
	nodeT = CommandTarget{Group: "g", Node: "n"}
	devT  = CommandTarget{Group: "g", Node: "n", Device: "d1"}
)

// bornNode sends an NBIRTH (bdSeq 5) and a DBIRTH for d1.
func bornNode(h *sessionHarness) {
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5),
		declared("Setpoint", dtInt8, 1), declared("Mode", dtString, 0), declared("Node Control/Rebirth", dtBoolean, 0)))
	h.tr.Observe(dTop(DBIRTH, "d1"), pl(1, declared("Valve", dtBoolean, 2), declared("Setpoint", dtInt32, 3)))
}

func TestBirthTableCapturesAliasDatatypeAndScope(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)

	r, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	assert.True(t, r.HasAlias)
	assert.Equal(t, uint64(1), r.Alias)
	assert.Equal(t, uint32(dtInt8), r.Datatype)
	assert.Equal(t, uint64(5), r.BdSeq)
	assert.Equal(t, h.sess().sessionEpoch, r.Epoch)

	r, err = h.tr.LookupMetric(nodeT, "Mode")
	require.NoError(t, err)
	assert.False(t, r.HasAlias, "a metric the BIRTH did not alias has no alias")
	assert.Equal(t, uint32(dtString), r.Datatype)

	// Device-scoped: the same name on the device is a different metric with its own alias/type.
	r, err = h.tr.LookupMetric(devT, "Setpoint")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), r.Alias)
	assert.Equal(t, uint32(dtInt32), r.Datatype)

	_, err = h.tr.LookupMetric(nodeT, "Valve")
	assert.ErrorIs(t, err, ErrMetricUnknown, "a device's metric is not the node's")
	_, err = h.tr.LookupMetric(devT, "Mode")
	assert.ErrorIs(t, err, ErrMetricUnknown, "a node's metric is not the device's")
}

func TestBirthTableNotBornTargets(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	_, err := h.tr.LookupMetric(nodeT, "Setpoint")
	assert.ErrorIs(t, err, ErrNotBorn, "never seen")

	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Setpoint", dtInt8, 1)))
	_, err = h.tr.LookupMetric(devT, "Valve")
	assert.ErrorIs(t, err, ErrNotBorn, "the node is born, the device has not sent a DBIRTH")
	_, err = h.tr.LookupMetric(nodeT, "Setpoint")
	assert.NoError(t, err, "control: the node is addressable")
}

// 🔴 EPOCH/GENERATION CONTROL. A commanded rebirth keeps the session (same epoch, same bdSeq) but
// the node may remap every alias. A ref captured before it must not be used after: the re-check
// resolves the metric again BY NAME and returns the NEW alias. The paired mutant (a revalidate
// that returns the ref unchanged) is run against this test.
func TestRevalidateAfterACommandedRebirthUsesTheNewAlias(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)

	// Control: with nothing in between the ref is returned as it is.
	same, err := h.tr.RevalidateMetric(old)
	require.NoError(t, err)
	assert.Equal(t, old, same)

	// A GAP-commanded rebirth (a seq gap makes the Host ask): same bdSeq, same session epoch, seq
	// reset, aliases REMAPPED (Setpoint 1 -> 9).
	h.tr.Observe(nTop(NDATA), pl(99, dataMetric(1)))
	require.Equal(t, 1, h.rec.count(), "the gap commanded a rebirth")
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Setpoint", dtInt8, 9), declared("Mode", dtString, 0)))
	cur, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	assert.Equal(t, uint64(9), cur.Alias)
	assert.Equal(t, old.Epoch, cur.Epoch, "the session is the same: the epoch alone could not have caught this")
	assert.Equal(t, old.BdSeq, cur.BdSeq)
	assert.NotEqual(t, old.Gen, cur.Gen)

	got, err := h.tr.RevalidateMetric(old)
	require.NoError(t, err)
	assert.Equal(t, uint64(9), got.Alias, "the stale alias 1 must not be used")
	assert.Equal(t, cur, got)
}

func TestRevalidateRefusesAMetricThatWentOrChangedType(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)

	// The rebirth re-declares Setpoint as Int16: the value was validated against Int8.
	h.tr.MarkRebirthPending("g", "n")
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Setpoint", dtInt16, 1)))
	_, err = h.tr.RevalidateMetric(old)
	assert.ErrorIs(t, err, ErrDatatypeChanged)

	// The rebirth no longer declares it at all.
	h.tr.MarkRebirthPending("g", "n")
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Other", dtInt8, 1)))
	_, err = h.tr.RevalidateMetric(old)
	assert.ErrorIs(t, err, ErrMetricUnknown)
}

// A rebirth drops the devices' entries until their own DBIRTHs re-declare them: their aliases are
// exactly what must not be trusted in between.
func TestRebirthDropsDeviceEntriesUntilTheirDBirth(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, err := h.tr.LookupMetric(devT, "Valve")
	require.NoError(t, err)

	h.tr.MarkRebirthPending("g", "n")
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Setpoint", dtInt8, 1)))
	_, err = h.tr.LookupMetric(devT, "Valve")
	assert.ErrorIs(t, err, ErrMetricUnknown)
	_, err = h.tr.RevalidateMetric(old)
	assert.ErrorIs(t, err, ErrMetricUnknown, "the old device alias is not used")

	// The device re-births with a remapped alias.
	h.tr.Observe(dTop(DBIRTH, "d1"), pl(1, declared("Valve", dtBoolean, 20)))
	got, err := h.tr.RevalidateMetric(old)
	require.NoError(t, err)
	assert.Equal(t, uint64(20), got.Alias)
}

// An unsolicited same-bdSeq NBIRTH is a QoS-1 redelivery: the session is untouched, and so is its
// table (and generation), so a ref taken before it stays current.
func TestRedeliveredBirthLeavesTheTableAlone(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, _ := h.tr.LookupMetric(nodeT, "Setpoint")
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5), declared("Setpoint", dtInt8, 77)))
	cur, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	assert.Equal(t, old, cur)
}

// A NEW session (a different bdSeq) replaces the table and mints a new epoch; a ref from the old
// session is re-resolved by name, never reused.
func TestNewSessionReplacesTheTable(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, _ := h.tr.LookupMetric(nodeT, "Setpoint")
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(6), declared("Setpoint", dtInt8, 4)))
	cur, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	assert.NotEqual(t, old.Epoch, cur.Epoch)
	assert.Equal(t, uint64(6), cur.BdSeq)
	assert.NotEqual(t, old.Gen, cur.Gen, "generations are unique across sessions")
	got, err := h.tr.RevalidateMetric(old)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), got.Alias)
	_, err = h.tr.LookupMetric(devT, "Valve")
	assert.ErrorIs(t, err, ErrNotBorn, "the old session's devices are gone with it")
}

// 🔴 CLEARED ON DEATH. An NDEATH carrying the live session's bdSeq empties the table; one carrying
// a STALE bdSeq (a will from a prior connection) must not.
func TestTableClearedOnNodeDeath(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, _ := h.tr.LookupMetric(nodeT, "Setpoint")

	h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(4))) // stale will: ignored
	_, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err, "control: a stale NDEATH does not clear the table")

	h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(5)))
	// The lookup is also guarded by "online", so the table itself is read directly: a dead node's
	// declarations must not linger to be addressed by whatever flips it online again.
	assert.Empty(t, h.sess().metrics, "the table is cleared, not merely unreachable")
	_, err = h.tr.LookupMetric(nodeT, "Setpoint")
	assert.ErrorIs(t, err, ErrNotBorn)
	_, err = h.tr.LookupMetric(devT, "Valve")
	assert.ErrorIs(t, err, ErrNotBorn)
	_, err = h.tr.RevalidateMetric(old)
	assert.ErrorIs(t, err, ErrNotBorn, "a dead node cannot be commanded with an old ref")
}

func TestTableClearedOnDeviceDeathOnly(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	h.tr.Observe(dTop(DDEATH, "d1"), pl(2))
	for k := range h.sess().metrics {
		assert.NotEqual(t, "d1", k.device, "the dead device's declarations are dropped, not merely unreachable")
	}
	_, err := h.tr.LookupMetric(devT, "Valve")
	assert.ErrorIs(t, err, ErrNotBorn)
	_, err = h.tr.LookupMetric(nodeT, "Setpoint")
	assert.NoError(t, err, "the node's own entries survive a device's death")
}

func TestTableClearedAtHostSessionEnd(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	bornNode(h)
	old, _ := h.tr.LookupMetric(nodeT, "Setpoint")
	h.tr.EndHostSession()
	_, err := h.tr.LookupMetric(nodeT, "Setpoint")
	assert.ErrorIs(t, err, ErrMetricUnknown, "the node's session is kept, its table is not addressable")
	_, err = h.tr.RevalidateMetric(old)
	assert.Error(t, err)
}

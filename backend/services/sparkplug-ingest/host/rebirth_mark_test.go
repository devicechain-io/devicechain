// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// 🔴 A COMMANDED REBIRTH IS MARKED PENDING BEFORE THE BYTES GO OUT. A node can answer with its
// same-bdSeq NBIRTH before the publish call has returned; an answer that finds no mark is dropped
// as a QoS-1 redelivery of the live session, and the rebirth would never be confirmed. The fake
// connection answers inside Publish, which is the worst case.

type rebirthRig struct {
	c       *Client
	fc      *fakeClient
	mu      sync.Mutex
	reports []Report
}

func newRebirthRig(t *testing.T) *rebirthRig {
	t.Helper()
	cmds := enabledCommands()
	cmds.WritableMetrics = []string{"Setpoint"}
	c, fc, _ := bornClient(t, cmds)
	r := &rebirthRig{c: c, fc: fc}
	c.SetCommandHooks(CommandHooks{Reported: func(_ string, rs []Report) {
		r.mu.Lock()
		r.reports = append(r.reports, rs...)
		r.mu.Unlock()
	}})
	return r
}

func (r *rebirthRig) births() []Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Report
	for _, x := range r.reports {
		if x.Kind == ReportNodeBirth {
			out = append(out, x)
		}
	}
	return out
}

func (r *rebirthRig) pending() bool {
	r.c.sessions.mu.Lock()
	defer r.c.sessions.mu.Unlock()
	return r.c.sessions.sessions[nodeKey{"plant-a", wireNode}].rebirthPending
}

// answerInPublish makes the fake connection deliver the node's same-bdSeq NBIRTH from inside the NCMD
// publish, before Publish returns.
func (r *rebirthRig) answerInPublish(t *testing.T) {
	r.fc.mu.Lock()
	r.fc.onPublish = func(topic string) {
		if topic == wireNodeT.Topic() {
			r.c.onMessage(nil, fakeMessage{topic: wireNBirth, payload: nbirthWire(t, 9)})
		}
	}
	r.fc.mu.Unlock()
}

func TestAnAnswerThatBeatsThePublishCallBackStillConfirmsTheRebirth(t *testing.T) {
	r := newRebirthRig(t)
	require.False(t, r.pending())
	r.answerInPublish(t)

	_, err := r.c.PublishRebirth(context.Background(), wireNodeT, nil)
	require.NoError(t, err)
	b := r.births()
	require.Len(t, b, 1, "the node's answer to a commanded rebirth was dropped as a redelivery")
	assert.Equal(t, uint64(7), b[0].BdSeq, "a rebirth keeps the session's bdSeq")
	assert.False(t, r.pending(), "the answer must have consumed the mark")

	// Control: the same NBIRTH with no rebirth commanded IS a redelivery and reports nothing.
	r.fc.mu.Lock()
	r.fc.onPublish = nil
	r.fc.mu.Unlock()
	r.c.onMessage(nil, fakeMessage{topic: wireNBirth, payload: nbirthWire(t, 9)})
	assert.Len(t, r.births(), 1)
}

func TestAnIndeterminateRebirthIsStillConfirmedByTheAnswer(t *testing.T) {
	r := newRebirthRig(t)
	r.answerInPublish(t)
	r.fc.pubErr = errors.New("PUBACK not received")

	_, err := r.c.PublishRebirth(context.Background(), wireNodeT, nil)
	var ind *PublishIndeterminateError
	require.ErrorAs(t, err, &ind, "the publish should have an unknown outcome")
	assert.Len(t, r.births(), 1, "the answer to a rebirth whose publish call failed was dropped")
}

// A rebirth that was definitely not sent gives the mark back, and leaves a pending request the
// session machine had made of its own alone.
func TestARebirthThatWasNotSentGivesTheMarkBack(t *testing.T) {
	r := newRebirthRig(t)
	r.c.mu.Lock()
	live := r.c.mc
	r.c.mc = nil // the broker session is gone: nothing can be published
	r.c.mu.Unlock()

	_, err := r.c.PublishRebirth(context.Background(), wireNodeT, nil)
	require.ErrorIs(t, err, ErrSessionDown)
	assert.False(t, r.pending(), "a rebirth that never went out left the node marked as asked")

	// A request the machine itself had made stays.
	r.c.sessions.mu.Lock()
	r.c.sessions.sessions[nodeKey{"plant-a", wireNode}].rebirthPending = true
	r.c.sessions.mu.Unlock()
	_, err = r.c.PublishRebirth(context.Background(), wireNodeT, nil)
	require.ErrorIs(t, err, ErrSessionDown)
	assert.True(t, r.pending(), "the session machine's own pending rebirth was cleared")

	r.c.mu.Lock()
	r.c.mc = live
	r.c.mu.Unlock()
}

// A value an integer type cannot hold is not a report of that type: it must not be narrowed into
// one that happens to equal what was asked for.
func TestAnOutOfRangeIntegerIsNotAReport(t *testing.T) {
	intM := func(v uint32) *sppb.Payload_Metric {
		return &sppb.Payload_Metric{Value: &sppb.Payload_Metric_IntValue{IntValue: v}}
	}
	longM := func(v uint64) *sppb.Payload_Metric {
		return &sppb.Payload_Metric{Value: &sppb.Payload_Metric_LongValue{LongValue: v}}
	}
	for _, tc := range []struct {
		name string
		dt   uint32
		m    *sppb.Payload_Metric
		ok   bool
	}{
		{"Int8 300", datatypeInt8, intM(300), false},
		{"Int8 255 truncated -1", datatypeInt8, intM(0xFF), true},
		{"Int8 sign-extended -1", datatypeInt8, intM(0xFFFFFFFF), true},
		{"Int8 sign-extended -128", datatypeInt8, intM(0xFFFFFF80), true},
		{"Int8 just below -128", datatypeInt8, intM(0xFFFFFF7F), false},
		{"Int8 0x1234", datatypeInt8, intM(0x1234), false},
		{"Int8 64-bit sign-extended -2 in long_value", datatypeInt8, longM(0xFFFFFFFFFFFFFFFE), true},
		{"Int16 70000", datatypeInt16, intM(70000), false},
		{"Int16 sign-extended -2", datatypeInt16, intM(0xFFFFFFFE), true},
		{"Int32 any 32-bit pattern", datatypeInt32, intM(0x80000000), true},
		{"Int32 beyond 32 bits", datatypeInt32, longM(1 << 33), false},
		{"UInt8 256", datatypeUInt8, intM(256), false},
		{"UInt8 255", datatypeUInt8, intM(255), true},
		{"UInt16 65536", datatypeUInt16, intM(65536), false},
		{"UInt32 in long_value", datatypeUInt32, longM(4000000000), true},
		{"UInt32 past 2^32", datatypeUInt32, longM(1 << 32), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := wireValueOf(tc.dt, tc.m)
			assert.Equal(t, tc.ok, ok)
		})
	}
	// And through the tracker: the out-of-range DATA is no report and moves no last value.
	h := reportingHarness()
	bornForReports(h)
	oob := intM(300)
	oob.Alias = proto.Uint64(1)
	obs := h.tr.Observe(nTop(NDATA), pl(2, oob))
	assert.Empty(t, obs.Reports)
}

// Only the metrics a write may target are captured: a fleet's worth of values is not kept for
// metrics nothing can be sent to.
func TestOnlyWritableMetricsAreCaptured(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.EnableReports(func(name string) bool { return name == "Setpoint" })
	nb, _ := bornForReports(h)
	var names []string
	for _, r := range reportsOf(nb, ReportMetric) {
		names = append(names, r.Name)
	}
	assert.Equal(t, []string{"Setpoint"}, names, "metrics outside the allowlist were captured")
	assert.Len(t, reportsOf(nb, ReportNodeBirth), 1, "the node birth is captured whatever the allowlist")
	ref, err := h.tr.LookupMetric(nodeT, "Mode")
	require.NoError(t, err)
	_, _, ok := h.tr.LastReported(ref)
	assert.False(t, ok, "a last value is kept for a metric no write may target")
	ref, _ = h.tr.LookupMetric(nodeT, "Setpoint")
	_, _, ok = h.tr.LastReported(ref)
	assert.True(t, ok, "control: the allowlisted metric keeps one")
}

// A node that dies mid-window has no table: what it last reported says nothing about it, and an
// "unchanged" decided from it would be wrong.
func TestANodeDeathEndsTheLastReportedValue(t *testing.T) {
	h := reportingHarness()
	bornForReports(h)
	ref, err := h.tr.LookupMetric(nodeT, "Setpoint")
	require.NoError(t, err)
	_, _, ok := h.tr.LastReported(ref)
	require.True(t, ok)
	h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(5)))
	_, _, ok = h.tr.LastReported(ref)
	assert.False(t, ok, "a dead node still vouches for its last value")
}

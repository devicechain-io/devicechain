// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The emitter is the boundary every transport producer crosses, so a time it will not
// emit has to be refused HERE. Two guards, and both existed only half way.

// TestEveryEntryTimeIsFloored is the per-entry half. The envelope's guard fires only when
// NO sample carried a positive time — `latest` starts at zero and a non-positive time can
// never raise it — so an all-negative batch was stamped "now" on the envelope over entries
// every one of which was dated before 1970, and a MIXED batch put pre-1970 entries under a
// perfectly ordinary envelope where nothing would look at them twice.
func TestEveryEntryTimeIsFloored(t *testing.T) {
	epoch := fixedNow().UnixMilli()
	good := int64(1_700_000_000_123)

	for _, tc := range []struct {
		name    string
		samples []Sample
		// wantEnvelope is the envelope's occurred time in millis.
		wantEnvelope int64
		wantEntries  []int64
	}{
		{
			name: "every time is negative",
			samples: []Sample{
				{Name: "/3303/0/5700", Value: 1, Time: -5},
				{Name: "/3303/0/5701", Value: 2, Time: -1_000_000},
			},
			wantEnvelope: epoch,
			wantEntries:  []int64{epoch, epoch},
		},
		{
			name: "a zero time is the same sentinel",
			samples: []Sample{
				{Name: "/3303/0/5700", Value: 1, Time: 0},
			},
			wantEnvelope: epoch,
			wantEntries:  []int64{epoch},
		},
		{
			// The one an envelope guard cannot see at all: the envelope is correct and
			// only some entries are pre-1970.
			name: "a mixed batch keeps its real envelope",
			samples: []Sample{
				{Name: "/3303/0/5700", Value: 1, Time: good},
				{Name: "/3303/0/5701", Value: 2, Time: -42},
			},
			wantEnvelope: good,
			wantEntries:  []int64{good, epoch},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeWriter{}
			e := NewEmitter(w, fixedNow, "lw", true)
			require.NoError(t, e.Emit(context.Background(), "acme", "lwm2m", "dev-1", tc.samples))
			require.Len(t, w.msgs, 1)

			ev, err := esproto.UnmarshalUnresolvedEvent(w.msgs[0].Value)
			require.NoError(t, err)
			assert.Equal(t, tc.wantEnvelope, ev.OccurredTime.UnixMilli(), "envelope occurred time")

			p, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
			require.True(t, ok)
			require.Len(t, p.Entries, len(tc.wantEntries))
			for i, want := range tc.wantEntries {
				require.NotNil(t, p.Entries[i].OccurredTime, "entry %d carries no time", i)
				got := p.Entries[i].OccurredTime.UnixMilli()
				assert.Equal(t, want, got, "entry %d occurred time", i)
				assert.False(t, p.Entries[i].OccurredTime.Before(time.Unix(0, 0)),
					"entry %d is dated before 1970", i)
			}
		})
	}
}

// TestTheEnvelopeGuardStillHoldsForAnEmptyBatch is the counterweight to the per-entry
// floor: with every entry floored, the only way `latest` can still be zero is a batch with
// no samples at all, and the envelope guard is what covers it.
func TestTheEnvelopeGuardStillHoldsForAnEmptyBatch(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "lw", true)
	require.NoError(t, e.Emit(context.Background(), "acme", "lwm2m", "dev-1", nil))
	require.Len(t, w.msgs, 1)

	ev, err := esproto.UnmarshalUnresolvedEvent(w.msgs[0].Value)
	require.NoError(t, err)
	assert.Equal(t, fixedNow().UnixMilli(), ev.OccurredTime.UnixMilli())
}

// TestFlooringAnEntryDoesNotMoveTheDedupID. The floor repairs what is STORED and must not
// touch idempotency: the dedup id is built from the raw sample times, so a retry of the
// identical batch — including its bad times — still dedups at JetStream.
func TestFlooringAnEntryDoesNotMoveTheDedupID(t *testing.T) {
	samples := []Sample{{Name: "/3303/0/5700", Value: 1, Time: -42}}

	first := &fakeWriter{}
	require.NoError(t, NewEmitter(first, fixedNow, "lw", true).
		Emit(context.Background(), "acme", "lwm2m", "dev-1", samples))
	second := &fakeWriter{}
	require.NoError(t, NewEmitter(second, fixedNow, "lw", true).
		Emit(context.Background(), "acme", "lwm2m", "dev-1", samples))

	require.Len(t, first.msgs, 1)
	require.Len(t, second.msgs, 1)
	assert.Equal(t, first.msgs[0].DedupID, second.msgs[0].DedupID,
		"a retry of the identical batch must still carry the same dedup id")
}

// TestAnUnstampedStateChangeIsFloored is the presence half, and the demotion case is the
// one with teeth: the resolver REFUSES a demotion carrying no occurred time, so an
// unstamped one is dead-lettered every time and the row it was releasing stays asserted
// and frozen — the exact condition the demotion exists to end. The guard lives in the
// emitter rather than in its callers because a caller added later inherits it there.
func TestAnUnstampedStateChangeIsFloored(t *testing.T) {
	t.Run("demotion", func(t *testing.T) {
		w := &fakeWriter{}
		e := NewEmitter(w, fixedNow, "bp", true)
		require.NoError(t, e.EmitPresenceDemotion(context.Background(), "acme", "mqtt1", "dev-1",
			DemotionEvent{SessionId: 7, Reason: "source-release"}))
		require.Len(t, w.msgs, 1)

		ev, err := esproto.UnmarshalUnresolvedEvent(w.msgs[0].Value)
		require.NoError(t, err)
		require.False(t, ev.OccurredTime.IsZero(),
			"the resolver refuses a demotion with no occurred time; it must never leave here unstamped")
		assert.Equal(t, fixedNow().UTC(), ev.OccurredTime)

		p, ok := ev.Payload.(*esmodel.UnresolvedStateChangePayload)
		require.True(t, ok)
		require.NotNil(t, p.OccurredTime)
		assert.Equal(t, fixedNow().UTC().Format(time.RFC3339Nano), *p.OccurredTime,
			"the payload's descriptive copy must agree with the envelope, not restate the zero instant")
	})

	t.Run("presence transition", func(t *testing.T) {
		w := &fakeWriter{}
		e := NewEmitter(w, fixedNow, "bp", true)
		require.NoError(t, e.EmitPresence(context.Background(), "acme", "mqtt1", "dev-1",
			PresenceEvent{Connected: true, SessionId: 9}))
		require.Len(t, w.msgs, 1)

		ev, err := esproto.UnmarshalUnresolvedEvent(w.msgs[0].Value)
		require.NoError(t, err)
		assert.Equal(t, fixedNow().UTC(), ev.OccurredTime)
	})
}

// TestAStampedStateChangeIsUntouched is the counterweight. Flooring the zero instant is
// only safe while a producer that DID stamp a clock keeps the instant it chose — a guard
// that quietly rewrote every presence time to "now" would flatten a reconciliation's
// day-old connection start onto the repair pass.
func TestAStampedStateChangeIsUntouched(t *testing.T) {
	stamped := time.UnixMilli(1_600_000_000_500).UTC()
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "bp", true)
	require.NoError(t, e.EmitPresenceDemotion(context.Background(), "acme", "mqtt1", "dev-1",
		DemotionEvent{SessionId: 7, OccurredAt: stamped, Reason: "source-release"}))
	require.Len(t, w.msgs, 1)

	ev, err := esproto.UnmarshalUnresolvedEvent(w.msgs[0].Value)
	require.NoError(t, err)
	assert.Equal(t, stamped, ev.OccurredTime, "a producer that stamped a clock keeps the instant it chose")
}

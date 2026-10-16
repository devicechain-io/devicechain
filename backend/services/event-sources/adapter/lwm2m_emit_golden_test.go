// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// LwM2M publishes through the synchronous Emit, Ingest and EmitPresence, and those are now
// built on the BuildMeasurements/BuildPresence split the Sparkplug host uses. These goldens pin
// what the LwM2M path WRITES, end to end and field for field (every Message field, the tenant
// the write is made under, the drop count): the literals were computed from the implementation
// BEFORE the split, so their survival is the proof the split changed no byte LwM2M sends. A
// test that compares Emit with BuildMeasurements cannot show that, because both sides moved.
//
// The digests were re-captured once, deliberately, when the inbound event's wire format
// changed (each instant a count of nanoseconds rather than an RFC 3339 string); every dedup id
// pinned beside them is unchanged.

// writtenDigest hashes every message a writer received, with the tenant each write was made
// under. %#v prints every field, maps in key order.
func writtenDigest(t *testing.T, w *fakeWriter) string {
	t.Helper()
	h := sha256.New()
	for i, ctx := range w.ctxs {
		tenant, _ := core.TenantFromContext(ctx)
		fmt.Fprintf(h, "write %d tenant=%q\n", i, tenant)
	}
	for _, m := range w.msgs {
		fmt.Fprintf(h, "%#v\n", m)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestGoldenLwm2mEmitWritesUnchanged(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "lw", true)
	in := numberedSamples(300)                                  // two pieces
	in = append(in, Sample{Name: "ancient", Value: 1, Time: 1}) // dropped for age
	tooOld, err := e.Emit(context.Background(), "acme", "lwm2m-src", "lw-dev-1", in)
	require.NoError(t, err)
	assert.Equal(t, 1, tooOld)
	require.Len(t, w.ctxs, 1, "ONE write call for every piece")
	require.Len(t, w.msgs, 2)
	assert.Equal(t, "lwih3ffszln5b9", w.msgs[0].DedupID)
	assert.Equal(t, "lw3v4hl6gazr6qt", w.msgs[1].DedupID)
	assert.Equal(t, "e39c29945c8fbb64c3ce7501f5da63b4c55d554455cdba94e68866cd60fc3c3f", writtenDigest(t, w),
		"the bytes LwM2M's Emit writes must not drift")
}

func TestGoldenLwm2mEmitPresenceWritesUnchanged(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "lw", true)
	for _, connected := range []bool{true, false} {
		require.NoError(t, e.EmitPresence(context.Background(), "acme", "lwm2m-src", "lw-dev-1", PresenceEvent{
			ExternalId: "ep-1", Connected: connected, Reason: "registration",
			SessionId: 1_700_000_000_123456789, OccurredAt: time.Unix(1_700_000_000, 0),
		}))
	}
	require.Len(t, w.msgs, 2)
	assert.Equal(t, "lw2qmgm3hd1ei3w", w.msgs[0].DedupID)
	assert.Equal(t, "lw2qmq169wjn4ut", w.msgs[1].DedupID)
	assert.Equal(t, "85b768b46ab8468cf2622db4e6ef110438ff6058be1843ec2a5ebd01e76b1863", writtenDigest(t, w),
		"the bytes LwM2M's EmitPresence writes must not drift")
}

// The error paths LwM2M's Notify counts on: a writer failure comes back from Emit with the age
// count still reported, and from Ingest with nothing counted as emitted; a resolve failure is
// returned before anything is written.
func TestGoldenLwm2mIngestErrorPaths(t *testing.T) {
	boom := errors.New("broker down")
	w := &fakeWriter{err: boom}
	e := NewEmitter(w, fixedNow, "lw", true)
	tooOld, err := e.Emit(context.Background(), "acme", "s", "lw-dev-1",
		[]Sample{{Name: "t", Value: 1, Time: fixedNowMs}, {Name: "old", Value: 1, Time: 1}})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, tooOld, "the age drop is reported even when the write fails")

	m, read := newIngestMetrics()
	hit := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	ing := NewIngester(NewRegistrar(hit, "url", "lw-", nil), NewEmitter(w, fixedNow, "lw", true), m)
	err = ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s"}, "ep-1", []Sample{{Name: "t", Value: 1, Time: fixedNowMs}})
	assert.ErrorIs(t, err, boom)
	assert.Zero(t, read("emitted"), "nothing counted as emitted when the write failed")

	ok := &fakeWriter{}
	down := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return nil, errors.New("dm down") }}
	ing = NewIngester(NewRegistrar(down, "url", "lw-", nil), NewEmitter(ok, fixedNow, "lw", true), m)
	err = ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s"}, "ep-1", []Sample{{Name: "t", Value: 1, Time: fixedNowMs}})
	assert.Error(t, err)
	assert.Empty(t, ok.msgs, "a resolve failure writes nothing")

	ing = NewIngester(NewRegistrar(hit, "url", "lw-", nil), NewEmitter(ok, fixedNow, "lw", true), m)
	require.NoError(t, ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s"}, "ep-1",
		[]Sample{{Name: "t", Value: 1, Time: fixedNowMs}, {Name: "u", Value: 2, Time: fixedNowMs}}))
	assert.Equal(t, 2.0, read("emitted"))
	require.Len(t, ok.msgs, 1)
	assert.Equal(t, "8b0676c42de0e6b66e838a48d865b2580ed0d814c9f3529b6821d6a8aa2e4187", writtenDigest(t, ok),
		"the bytes LwM2M's Ingest writes must not drift")
}

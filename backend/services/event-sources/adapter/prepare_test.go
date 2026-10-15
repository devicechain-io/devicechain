// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

func knownDevice() *fakeGraphQL {
	return &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
}

// The messages BuildMeasurements returns are exactly the ones Emit writes, split and stamped
// the same way: Emit is built on it, and a caller that publishes through a writer of its own
// must get the shipped messages, not a near copy.
func TestBuildMeasurementsIsWhatEmitWrites(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	in := numberedSamples(600)

	built, tooOld, err := e.BuildMeasurements("acme", "src", "dev-1", in)
	require.NoError(t, err)
	assert.Zero(t, tooOld)
	require.Len(t, built, 3)
	assert.Empty(t, w.msgs, "building must publish nothing")

	_, err = e.Emit(context.Background(), "acme", "src", "dev-1", in)
	require.NoError(t, err)
	require.Len(t, w.msgs, len(built))
	for i := range built {
		assert.Equal(t, w.msgs[i].DedupID, built[i].DedupID, "piece %d", i)
		assert.Equal(t, w.msgs[i].Key, built[i].Key, "piece %d", i)
		assert.Equal(t, w.msgs[i].Value, built[i].Value, "piece %d", i)
	}
}

// Building twice from a clock that moves gives messages with DIFFERENT bytes but the SAME dedup
// ids: which is why a retry must resubmit what was built (identical bytes) and not rebuild.
func TestBuildMeasurementsRestampsButKeepsTheDedupID(t *testing.T) {
	ticks := 0
	moving := func() time.Time { ticks++; return fixedNow().Add(time.Duration(ticks) * time.Second) }
	e := NewEmitter(&fakeWriter{}, moving, "sp", true)
	in := []Sample{{Name: "t", Value: 1, Time: fixedNowMs}}

	a, _, err := e.BuildMeasurements("acme", "src", "dev-1", in)
	require.NoError(t, err)
	b, _, err := e.BuildMeasurements("acme", "src", "dev-1", in)
	require.NoError(t, err)
	assert.Equal(t, a[0].DedupID, b[0].DedupID, "the dedup id is a function of content")
	assert.NotEqual(t, a[0].Value, b[0].Value, "the stamped times differ between builds")
}

func TestBuildPresenceIsWhatEmitPresenceWrites(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	ev := PresenceEvent{ExternalId: "g/n", Connected: true, SessionId: 9, OccurredAt: fixedNow()}

	built, err := e.BuildPresence("acme", "src", "dev-1", ev)
	require.NoError(t, err)
	require.NoError(t, e.EmitPresence(context.Background(), "acme", "src", "dev-1", ev))
	require.Len(t, w.msgs, 1)
	assert.Equal(t, w.msgs[0], built)
	assert.True(t, built.BypassBackpressure, "a presence transition is admitted past a closed gate")
}

// PrepareIngest resolves and builds but publishes nothing, hands back a Publication whose
// counters run only when the caller says the messages were stored, and reports a definitive
// drop as (nil, nil).
func TestPrepareIngest(t *testing.T) {
	w := &fakeWriter{}
	m, read := newIngestMetrics()
	ing := NewIngester(NewRegistrar(knownDevice(), "url", "sp-", nil), NewEmitter(w, fixedNow, "sp", false), m)

	pub, err := ing.PrepareIngest(context.Background(), "acme", IngestPolicy{Source: "s"}, "g/n",
		[]Sample{{Name: "t", Value: 1, Time: fixedNowMs}, {Name: "u", Value: 2, Time: fixedNowMs}})
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Equal(t, "acme", pub.Tenant)
	assert.Len(t, pub.Messages, 1)
	assert.Empty(t, w.msgs, "preparing must publish nothing")
	assert.Zero(t, read("emitted"), "counted as emitted before it was stored")

	pub.Delivered()
	assert.Equal(t, 2.0, read("emitted"))
}

func TestPrepareIngestDropsAndErrorsLikeIngest(t *testing.T) {
	m, read := newIngestMetrics()
	miss := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupMiss(), nil }}
	ing := NewIngester(NewRegistrar(miss, "url", "sp-", nil), NewEmitter(&fakeWriter{}, fixedNow, "sp", false), m)
	pub, err := ing.PrepareIngest(context.Background(), "acme", IngestPolicy{}, "g/n", []Sample{{Name: "t", Value: 1, Time: fixedNowMs}})
	require.NoError(t, err, "a definitive drop is handled, not an error")
	assert.Nil(t, pub)
	assert.Equal(t, 1.0, read("dropped"))

	down := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return nil, errors.New("dm down") }}
	ing = NewIngester(NewRegistrar(down, "url", "sp-", nil), NewEmitter(&fakeWriter{}, fixedNow, "sp", false), m)
	_, err = ing.PrepareIngest(context.Background(), "acme", IngestPolicy{}, "g/n", []Sample{{Name: "t", Value: 1, Time: fixedNowMs}})
	assert.Error(t, err, "a resolve failure is the retryable error")

	// Every sample too old: nothing to publish, and not an error.
	ing = NewIngester(NewRegistrar(knownDevice(), "url", "sp-", nil), NewEmitter(&fakeWriter{}, fixedNow, "sp", false), m)
	pub, err = ing.PrepareIngest(context.Background(), "acme", IngestPolicy{}, "g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	require.NoError(t, err)
	assert.Nil(t, pub)
}

// PreparePresence builds one message per ingestible event, in order, as one Publication, and
// skips an event whose device is not ingestible without failing the rest.
func TestPreparePresence(t *testing.T) {
	gql := &fakeGraphQL{responder: func(_ string, vars map[string]any) (any, error) {
		ids, _ := vars["externalIds"].([]string)
		if len(ids) == 1 && ids[0] == "g/unknown" {
			return lookupMiss(), nil
		}
		return lookupHit("dev-1"), nil
	}}
	m := IngestMetrics{}
	ing := NewIngester(NewRegistrar(gql, "url", "sp-", nil), NewEmitter(&fakeWriter{}, fixedNow, "sp", false), m)
	events := []PresenceEvent{
		{ExternalId: "g/n", Connected: true, SessionId: 1, OccurredAt: fixedNow()},
		{ExternalId: "g/unknown", Connected: true, SessionId: 2, OccurredAt: fixedNow()},
		{ExternalId: "g/n/d", Connected: false, SessionId: 3, OccurredAt: fixedNow()},
	}
	pub, err := ing.PreparePresence(core.WithTenant(context.Background(), "acme"), "acme", IngestPolicy{Source: "s"}, events)
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Len(t, pub.Messages, 2, "the unknown device is dropped, the others are kept in order")
	for _, msg := range pub.Messages {
		assert.True(t, msg.BypassBackpressure)
	}
	pub.Delivered()

	none, err := ing.PreparePresence(context.Background(), "acme", IngestPolicy{Source: "s"}, nil)
	require.NoError(t, err)
	assert.Nil(t, none)
}

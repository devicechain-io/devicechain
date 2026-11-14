// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type previewFenceArchive struct {
	t     *testing.T
	set   *geofence.FenceSet
	reads int
}

func (a *previewFenceArchive) FenceSetAt(_ context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	require.Equal(a.t, "acme", tenant)
	require.EqualValues(a.t, 7, version, "the replay must request the event's historical version")
	a.reads++
	return a.set, nil
}

type countedFenceReplay struct {
	*messaging.NatsManager
	opens int
}

func (r *countedFenceReplay) NewReplayReaderFromTime(suffix string, start time.Time) (messaging.ReplayReader, time.Time, error) {
	r.opens++
	return r.NatsManager.NewReplayReaderFromTime(suffix, start)
}

// The earlier source-availability tests stop before replay. This one reaches the actual
// preview.Run call over retained JetStream history: replacing its r.FenceSets argument
// with nil must lose the expected edges and fail, even though the availability gate passes.
func TestPreviewPassesItsFenceSourceIntoBrokerReplay(t *testing.T) {
	const instance = "previewfence"
	b := permtest.Start(t, instance)
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing", Deployed: true}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = b.NatsConfiguration("event-processing")
	manager := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, manager.Initialize(ctx))
	t.Cleanup(func() { require.NoError(t, manager.Stop(ctx)) })
	require.NoError(t, manager.Start(ctx))
	opener := &countedFenceReplay{NatsManager: manager}

	js, err := b.Connect(t, "device-management").JetStream()
	require.NoError(t, err)
	start := time.Now().UTC().Truncate(time.Second)
	insideAt, outsideAt := start.Add(time.Minute), start.Add(2*time.Minute)
	for i, at := range []time.Time{insideAt, outsideAt} {
		lat, lon := "0.5", "0.5"
		if i == 1 {
			lat, lon = "5", "5"
		}
		data, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
			Source: "mqtt1", SourceDeviceToken: "d1", ProfileVersionToken: "p@1",
			OccurredTime: at, ProcessedTime: at, EventType: eventtype.Location, FenceSetVersion: 7,
			Payload: &resolvedevent.ResolvedLocationsPayload{Entries: []resolvedevent.ResolvedLocationEntry{
				{Latitude: &lat, Longitude: &lon, OccurredTime: at},
			}},
		})
		require.NoError(t, err)
		_, err = js.Publish(messaging.ConcreteSubjectFor(instance, "acme", streams.ResolvedEvents, "d1"), data)
		require.NoError(t, err)
	}
	archive := &previewFenceArchive{t: t, set: geofence.NewFenceSet(7, []geofence.SnapshotFence{{
		Token:    "yard",
		Geometry: []byte(`{"kind":"` + geofence.KindPolygon2D + `","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}}`),
	}})}
	resolver := &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: opener, FenceSets: archive}
	input := previewInput(fenceDraft, start.Format(time.RFC3339), start.Add(3*time.Minute).Format(time.RFC3339))
	request := previewCtx(auth.DeviceRead, auth.LocationRead)

	res, err := resolver.PreviewRule(request, input)
	require.NoError(t, err)
	require.True(t, res.Ok())
	require.EqualValues(t, 2, res.Stats().EventsScanned())
	require.Zero(t, res.Stats().EvalErrors())
	require.Len(t, res.Firings(), 2, "a wired archive must produce edges, not a plausible empty preview")
	require.Equal(t, "raised", res.Firings()[0].Signal())
	require.Equal(t, insideAt.Format(time.RFC3339Nano), res.Firings()[0].OccurredAt())
	require.Equal(t, "resolved", res.Firings()[1].Signal())
	require.Equal(t, outsideAt.Format(time.RFC3339Nano), res.Firings()[1].OccurredAt())
	require.Equal(t, 1, archive.reads, "one historical version is memoized across the replay")
	require.Equal(t, 1, opener.opens)

	// With the same valid window and retained events, an unwired archive is refused
	// before opening a reader, rather than replaying into predicate errors.
	resolver.FenceSets = nil
	res, err = resolver.PreviewRule(request, input)
	require.NoError(t, err)
	require.True(t, res.Ok())
	require.NotNil(t, res.Degraded())
	require.Contains(t, *res.Degraded(), "geofence archive")
	require.Empty(t, res.Firings())
	require.Zero(t, res.Stats().EventsScanned())
	require.Equal(t, 1, opener.opens, "an unavailable archive must not open another replay")
	b.RequireNoViolations(t)
}

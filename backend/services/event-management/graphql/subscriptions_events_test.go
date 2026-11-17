// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

// measurementFromResolved maps a resolved entry onto the same MeasurementEvent
// shape the query returns, so a streamed event and a queried one resolve
// identically. This locks the proto->model mapping (device id, occurred time,
// name, parsed value, classifier) against a drift in the resolved-event shape.
func TestMeasurementFromResolved(t *testing.T) {
	// The sample's instant is DIFFERENT from the message's, and that difference is the
	// point of the test: with both set to the same value, the streamed event would look
	// correct whether it read the sample's time or the envelope's — which is exactly how
	// this surface stamped the wrong one while its own comment promised otherwise.
	envelope := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sample := time.Date(2026, 7, 1, 11, 58, 30, 0, time.UTC)
	classifier := uint64(3)
	resolved := &resolvedevent.ResolvedEvent{
		SourceDeviceToken: "device-4",
		OccurredTime:      envelope,
		EventType:         eventtype.Measurement,
	}
	entry := resolvedevent.ResolvedMeasurementsEntry{OccurredTime: sample}

	me, err := measurementFromResolved("tenant1", resolved, entry, resolvedevent.ResolvedMeasurementEntry{
		Name:       "temperature",
		Value:      "21.5",
		Classifier: &classifier,
	})
	require.NoError(t, err)

	assert.Equal(t, "device-4", me.DeviceToken)
	assert.Equal(t, eventtype.Measurement, me.EventType)
	assert.Equal(t, sample, me.OccurredTime,
		"a streamed reading must carry the SAMPLE's instant, which is what the historian stores for it")
	assert.Equal(t, "temperature", me.Name)
	require.True(t, me.Value.Valid)
	assert.InDelta(t, 21.5, me.Value.Float64, 1e-9)
	require.NotNil(t, me.Classifier)
	assert.Equal(t, uint(3), *me.Classifier)
}

// A non-numeric measurement value leaves Value null rather than erroring, so one
// bad reading does not drop the stream.
func TestMeasurementFromResolvedNonNumeric(t *testing.T) {
	me, err := measurementFromResolved("tenant1", &resolvedevent.ResolvedEvent{SourceDeviceToken: "device-1"},
		resolvedevent.ResolvedMeasurementsEntry{}, resolvedevent.ResolvedMeasurementEntry{
			Name:  "state",
			Value: "OPEN",
		})
	require.NoError(t, err)
	assert.Equal(t, "state", me.Name)
	assert.False(t, me.Value.Valid)
	assert.Nil(t, me.Classifier)
}

// Counts the work at the real resolver boundary, including scoped and filtered peers.
func TestMeasurementStreamSharedFeed(t *testing.T) {
	nmgr := &messaging.NatsManager{}
	upstream := make(chan messaging.Message, 20)
	var opens, decodes atomic.Int64
	var upstreamCtx context.Context
	oldOpen, oldDecode := measurementLiveOpener, measurementDecode
	measurementLiveOpener = func(*messaging.NatsManager) messaging.LiveOpener {
		return func(ctx context.Context, tenant string) (<-chan messaging.Message, error) {
			opens.Add(1)
			upstreamCtx = ctx
			require.Equal(t, "tenant1", tenant)
			return upstream, nil
		}
	}
	measurementDecode = func(msg messaging.Message, tenant string) []messaging.Keyed[*resolvedevent.ResolvedEvent] {
		decodes.Add(1)
		return decodeMeasurements(msg, tenant)
	}
	defer func() {
		measurementLiveOpener, measurementDecode = oldOpen, oldDecode
		measurementHubs.Lock()
		delete(measurementHubs.hubs, nmgr)
		measurementHubs.Unlock()
	}()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribe := func(device, name *string, anchors []string) <-chan *MeasurementEventResolver {
		ctx := scopedCtx(nil, "tenant1", anchors, string(auth.EventRead))
		// Preserve claims while sharing one cancellation tree.
		ctx, stop := context.WithCancel(ctx)
		go func() { <-base.Done(); stop() }()
		ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr)
		ch, err := (&SchemaResolver{}).MeasurementStream(ctx, struct {
			DeviceToken *string
			Name        *string
		}{device, name})
		require.NoError(t, err)
		return ch
	}
	chans := make([]<-chan *MeasurementEventResolver, 50)
	for i := range chans {
		device := fmt.Sprintf("device-%d", i)
		chans[i] = subscribe(&device, nil, nil)
	}
	scoped := subscribe(nil, nil, []string{"customer:acme"})
	temp := "temperature"
	named := subscribe(nil, &temp, nil)
	all := subscribe(nil, nil, nil)
	require.Equal(t, int64(1), opens.Load())
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var members []eventenvelope.Event
	for i := 0; i < 10; i++ {
		owner := "globex"
		if i%2 == 0 {
			owner = "acme"
		}
		e := &resolvedevent.ResolvedEvent{SourceDeviceToken: fmt.Sprintf("device-%d", i), EventType: eventtype.Measurement, OccurredTime: at,
			Anchors: []resolvedevent.ResolvedAnchor{{AnchorType: "customer", AnchorToken: owner}},
			Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: fmt.Sprint(i)}, {Name: "humidity", Value: "60"}}}}}}
		data, err := resolvedevent.MarshalResolvedEvent(e)
		require.NoError(t, err)
		if i == 0 {
			upstream <- messaging.Message{Value: data}
		} else {
			members = append(members, eventenvelope.Event{Index: uint32(i - 1), Bytes: data})
		}
	}
	// Queue an envelope burst before reading any subscriber, preserving the same
	// scope, name and device filtering assertions for every event in the batch.
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "tenant1"}, members)
	require.NoError(t, err)
	upstream <- messaging.NewConsumedMessage("test.tenant1.resolved-events", body, 0,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, nil)
	receive := func(ch <-chan *MeasurementEventResolver) *MeasurementEventResolver {
		select {
		case v, ok := <-ch:
			require.True(t, ok)
			return v
		case <-time.After(5 * time.Second):
			t.Fatal("missing reading")
			return nil
		}
	}
	for i := 0; i < 10; i++ {
		for _, name := range []string{"temperature", "humidity"} {
			v := receive(chans[i])
			require.Equal(t, fmt.Sprintf("device-%d", i), v.M.DeviceToken)
			require.Equal(t, name, v.M.Name)
			if name == "temperature" {
				require.Equal(t, float64(i), v.M.Value.Float64)
			}
		}
	}
	for i := 0; i < 10; i++ {
		v := receive(named)
		require.Equal(t, "temperature", v.M.Name)
		require.Equal(t, fmt.Sprintf("device-%d", i), v.M.DeviceToken)
	}
	for i := 0; i < 10; i++ {
		for range 2 {
			v := receive(all)
			require.Equal(t, fmt.Sprintf("device-%d", i), v.M.DeviceToken)
		}
	}
	for i := 0; i < 10; i += 2 {
		for range 2 {
			v := receive(scoped)
			require.Equal(t, fmt.Sprintf("device-%d", i), v.M.DeviceToken)
		}
	}
	require.Equal(t, int64(2), decodes.Load())
	// Drain to natural completion so cancellation cannot hide an unwanted reading.
	close(upstream)
	for _, ch := range append(chans, scoped, named, all) {
		select {
		case _, ok := <-ch:
			require.False(t, ok, "unexpected reading after expected matches")
		case <-time.After(5 * time.Second):
			t.Fatal("resolver did not close")
		}
	}
	select {
	case <-upstreamCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shared upstream did not cancel")
	}
}

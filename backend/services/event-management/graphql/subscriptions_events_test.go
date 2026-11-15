// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	graphql "github.com/graph-gophers/graphql-go"
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
	}()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribe := func(device, name *string, tokens *[]string, anchors []string) <-chan *MeasurementEventResolver {
		ctx := scopedCtx(nil, "tenant1", anchors, string(auth.EventRead))
		// Preserve claims while sharing one cancellation tree.
		ctx, stop := context.WithCancel(ctx)
		go func() { <-base.Done(); stop() }()
		ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr)
		ch, err := (&SchemaResolver{}).MeasurementStream(ctx, struct {
			DeviceToken  *string
			DeviceTokens *[]string
			Name         *string
		}{device, tokens, name})
		require.NoError(t, err)
		return ch
	}
	chans := make([]<-chan *MeasurementEventResolver, 50)
	for i := range chans {
		device := fmt.Sprintf("device-%d", i)
		chans[i] = subscribe(&device, nil, nil, nil)
	}
	scoped := subscribe(nil, nil, nil, []string{"customer:acme"})
	temp := "temperature"
	named := subscribe(nil, &temp, nil, nil)
	all := subscribe(nil, nil, nil, nil)
	// Reverse the requested keys to prove delivery follows publication order.
	tokens := []string{"device-3", "device-1"}
	multi := subscribe(nil, nil, &tokens, nil)
	scopedTokens := []string{"device-0", "device-3"}
	multiScoped := subscribe(nil, nil, &scopedTokens, []string{"customer:acme"})
	// Exactly the limit is accepted, and repeated keys still deliver each reading once.
	duplicates := make([]string, rdb.MaxLookupKeys)
	for i := range duplicates {
		duplicates[i] = "device-1"
	}
	duplicate := subscribe(nil, nil, &duplicates, nil)
	// Execute the new argument through the same parsed schema the service serves.
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	schemaCtx := scopedCtx(nil, "tenant1", nil, string(auth.EventRead))
	schemaCtx = context.WithValue(schemaCtx, gqlcore.ContextNatsKey, nmgr)
	schemaCtx, schemaCancel := context.WithCancel(schemaCtx)
	defer schemaCancel()
	wire, err := schema.Subscribe(schemaCtx,
		`subscription($tokens: [String!]) { measurementStream(deviceTokens: $tokens, name: "temperature") { deviceToken name } }`,
		"", map[string]any{"tokens": []any{"device-3", "device-1"}})
	require.NoError(t, err)
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
	for _, device := range []string{"device-1", "device-3"} {
		for _, name := range []string{"temperature", "humidity"} {
			v := receive(multi)
			require.Equal(t, device, v.M.DeviceToken)
			require.Equal(t, name, v.M.Name)
		}
		select {
		case response := <-wire:
			resp, ok := response.(*graphql.Response)
			require.True(t, ok)
			require.Empty(t, resp.Errors)
			want, err := json.Marshal(map[string]any{"measurementStream": map[string]string{"deviceToken": device, "name": "temperature"}})
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(resp.Data))
		case <-time.After(5 * time.Second):
			t.Fatal("missing schema subscription reading")
		}
	}
	for _, name := range []string{"temperature", "humidity"} {
		v := receive(multiScoped)
		require.Equal(t, "device-0", v.M.DeviceToken)
		require.Equal(t, name, v.M.Name)
		v = receive(duplicate)
		require.Equal(t, "device-1", v.M.DeviceToken)
		require.Equal(t, name, v.M.Name)
	}
	require.Equal(t, int64(1), opens.Load())
	require.Equal(t, int64(2), decodes.Load())
	// Drain to natural completion so cancellation cannot hide an unwanted reading.
	close(upstream)
	for _, ch := range append(chans, scoped, named, all, multi, multiScoped, duplicate) {
		select {
		case _, ok := <-ch:
			require.False(t, ok, "unexpected reading after expected matches")
		case <-time.After(5 * time.Second):
			t.Fatal("resolver did not close")
		}
	}
	select {
	case response, ok := <-wire:
		require.False(t, ok, "unexpected schema subscription reading: %v", response)
	case <-time.After(5 * time.Second):
		t.Fatal("schema subscription did not close")
	}
	select {
	case <-upstreamCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shared upstream did not cancel")
	}
}

// Invalid lists are refused before looking up a NATS manager or opening a feed.
func TestMeasurementStreamDeviceTokensRefusals(t *testing.T) {
	device := "device-1"
	empty := []string{}
	tokens := []string{device}
	overLimit := make([]string, rdb.MaxLookupKeys+1)
	for i := range overLimit {
		overLimit[i] = device // The cap bounds raw input, before deduplication.
	}
	for _, tc := range []struct {
		name   string
		device *string
		tokens *[]string
		code   string
	}{
		{"both arguments", &device, &tokens, "INVALID_VALUE"},
		{"both with empty list", &device, &empty, "INVALID_VALUE"},
		{"empty list", nil, &empty, "INVALID_VALUE"},
		{"over limit", nil, &overLimit, "LIMIT_EXCEEDED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := scopedCtx(nil, "tenant1", nil, string(auth.EventRead))
			ch, err := (&SchemaResolver{}).MeasurementStream(ctx, struct {
				DeviceToken  *string
				DeviceTokens *[]string
				Name         *string
			}{DeviceToken: tc.device, DeviceTokens: tc.tokens})
			require.Nil(t, ch)
			require.Error(t, err)
			if tc.code == limit.Code {
				var refusal *limit.Error
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, tc.code, refusal.Extensions()["code"])
				require.Equal(t, rdb.MaxLookupKeys+1, refusal.Got)
				require.Equal(t, rdb.MaxLookupKeys, refusal.Max)
			} else {
				var refusal *integrity.Refusal
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, integrity.ClassInvalid, refusal.Class())
				require.Equal(t, tc.code, refusal.Extensions()["code"])
			}
		})
	}
}

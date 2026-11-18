// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func detectionRule(token, definition string) model.DetectRule {
	return model.DetectRule{
		RuleId: runtime.PublishedRuleID("acme", "p@1", token), Tenant: "acme",
		ProfileVersionToken: "p@1", RuleToken: token, Definition: definition,
	}
}

// Drive the actual subscription loop, including both edges: a withheld resolve is just as
// sensitive as a raise. The finite live feed closes so absence assertions need no sleeps.
func detectionFeed(t *testing.T, store *model.DetectRuleStore, authorities ...auth.Authority) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(previewCtx(authorities...), 5*time.Second)
	defer cancel()
	live := make(chan messaging.Message, 12)
	for _, token := range []string{"fence", "threshold", "unknown", "invalid", "undecodable"} {
		for _, edge := range []string{"raised", "resolved"} {
			body, err := json.Marshal(runtime.DerivedEvent{
				RuleID: runtime.PublishedRuleID("acme", "p@1", token), Series: "device-1",
				Edge: edge, OccurredTime: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatal(err)
			}
			live <- messaging.Message{Value: body}
		}
	}
	close(live)
	r := &SchemaResolver{DetectRules: store, Live: func(_ context.Context, tenant, stream string) (<-chan messaging.Message, error) {
		if tenant != "acme" || stream != streams.DerivedEvents {
			t.Fatalf("unexpected live scope: %s/%s", tenant, stream)
		}
		return live, nil
	}}
	out, err := r.DetectionStream(ctx, struct{ ProfileToken string }{ProfileToken: "p"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, ev.RuleToken()+":"+ev.Edge())
		case <-ctx.Done():
			t.Fatal("subscription did not finish")
		}
	}
}

func TestDetectionStreamWithholdsFenceDetectionsWithoutLocationRead(t *testing.T) {
	store, _ := evStores(t)
	if err := store.Upsert(previewCtx(auth.DeviceRead), []model.DetectRule{
		detectionRule("fence", fenceDraft), detectionRule("threshold", plainDraft),
		detectionRule("invalid", `{"name":"broken","type":"threshold","when":{"cel":"missingFunction()"}}`),
		detectionRule("undecodable", `{"inputRule":"fence"}`),
	}); err != nil {
		t.Fatal(err)
	}
	got := detectionFeed(t, store, auth.DeviceRead)
	want := []string{"threshold:raised", "threshold:resolved"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detections = %v, want %v (fence, unknown and invalid rules must be withheld)", got, want)
	}
}

func TestDetectionStreamWithLocationReadReceivesAllDetections(t *testing.T) {
	store, _ := evStores(t)
	if err := store.Upsert(previewCtx(auth.DeviceRead), []model.DetectRule{
		detectionRule("fence", fenceDraft), detectionRule("threshold", plainDraft),
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"fence:raised", "fence:resolved", "threshold:raised", "threshold:resolved",
		"unknown:raised", "unknown:resolved", "invalid:raised", "invalid:resolved",
		"undecodable:raised", "undecodable:resolved",
	}
	for _, tc := range []struct {
		name  string
		store *model.DetectRuleStore
	}{{"stored rules", store}, {"no store", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil store also proves location-authorized callers skip rule lookups.
			got := detectionFeed(t, tc.store, auth.DeviceRead, auth.LocationRead)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("detections = %v, want %v", got, want)
			}
		})
	}
}

func TestDetectionPositionFilterCachesAndBoundsDecisions(t *testing.T) {
	store, _ := evStores(t)
	ctx := previewCtx(auth.DeviceRead)
	rows := []model.DetectRule{
		detectionRule("fence", fenceDraft), detectionRule("threshold", plainDraft),
		detectionRule("another-threshold", plainDraft),
	}
	if err := store.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	f := positionFilter{store: store}
	if f.allow(ctx, rows[0].RuleId) || !f.allow(ctx, rows[1].RuleId) {
		t.Fatal("wrong initial decisions")
	}
	f.store = nil // Cached decisions must survive without another store access.
	if f.allow(ctx, rows[0].RuleId) || !f.allow(ctx, rows[1].RuleId) {
		t.Fatal("decisions were not cached")
	}
	for i := len(f.allowed); i < maxPositionFilterEntries; i++ {
		id := fmt.Sprintf("unknown-%d", i)
		f.allowed[id] = positionDecision{allowed: false, at: time.Now()}
		f.order = append(f.order, id)
	}
	f.store = store
	if !f.allow(ctx, rows[2].RuleId) || len(f.allowed) != maxPositionFilterEntries {
		t.Fatal("cache capacity must permit safe rules without growing")
	}
	if !f.allow(ctx, rows[1].RuleId) {
		t.Fatal("capacity must not change a cached decision")
	}
	if f.allow(ctx, rows[0].RuleId) || len(f.allowed) != maxPositionFilterEntries {
		t.Fatal("an evicted fence decision must be recomputed and withheld")
	}
}

func TestDetectionStreamWithholdsOnRuleLoadError(t *testing.T) {
	store, _ := evStores(t)
	ctx, cancel := context.WithCancel(previewCtx(auth.DeviceRead))
	cancel() // A cancelled DB operation is an error, never evidence a rule is safe.
	f := positionFilter{store: store}
	if f.allow(ctx, "acme/p@1/threshold") {
		t.Fatal("rule load error must withhold the event")
	}
}

// A rule id is not unique for all time: a profile deleted and created again under the same
// token restarts its versions, so "{profile}@1/{rule}" can come back holding a geofence rule.
// A cached "allowed" must therefore expire, and the re-read must see the fence.
func TestDetectionPositionDecisionExpiresWhenARuleIdIsReused(t *testing.T) {
	store, _ := evStores(t)
	ctx := previewCtx(auth.DeviceRead)
	row := detectionRule("reused", plainDraft)
	if err := store.Upsert(ctx, []model.DetectRule{row}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	f := positionFilter{store: store, now: func() time.Time { return now }}
	if !f.allow(ctx, row.RuleId) {
		t.Fatal("a threshold rule must be delivered")
	}
	fence := detectionRule("reused", fenceDraft)
	if fence.RuleId != row.RuleId {
		t.Fatalf("test setup: ids differ %q %q", fence.RuleId, row.RuleId)
	}
	if err := store.Upsert(ctx, []model.DetectRule{fence}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(positionDecisionTTL)
	if f.allow(ctx, row.RuleId) {
		t.Fatal("an expired decision was trusted after the rule id came back as a geofence rule")
	}
}

func TestDetectionStreamSharedFeed(t *testing.T) {
	nmgr := &messaging.NatsManager{}
	upstream := make(chan messaging.Message, 32)
	var opens, decodes atomic.Int64
	var upstreamCtx context.Context
	oldOpen, oldDecode := detectionLiveOpener, detectionDecode
	detectionLiveOpener = func(*messaging.NatsManager) messaging.LiveOpener {
		return func(ctx context.Context, tenant string) (<-chan messaging.Message, error) {
			opens.Add(1)
			upstreamCtx = ctx
			require.Equal(t, "acme", tenant)
			return upstream, nil
		}
	}
	detectionDecode = func(msg messaging.Message, tenant string) []messaging.Keyed[liveDetection] {
		decodes.Add(1)
		return decodeDetections(msg, tenant)
	}
	defer func() { detectionLiveOpener, detectionDecode = oldOpen, oldDecode }()
	// Every firing reaching every subscriber is a location:read caller's view; the
	// withholding path, on this same shared feed, is TestDetectionStreamSharedFeedFiltersPerSubscriber.
	ctx, cancel := context.WithCancel(core.WithTenant(authedContext(auth.DeviceRead, auth.LocationRead), "acme"))
	defer cancel()
	ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr)
	chans := make([]<-chan *DetectionEventResolver, 20)
	for i := range chans {
		ch, err := (&SchemaResolver{}).DetectionStream(ctx, struct{ ProfileToken string }{fmt.Sprintf("profile-%d", i%4)})
		require.NoError(t, err)
		chans[i] = ch
	}
	require.Equal(t, int64(1), opens.Load())
	for i := 0; i < 24; i++ {
		data, err := json.Marshal(runtime.DerivedEvent{RuleID: fmt.Sprintf("acme/profile-%d@v1/rule-%d", i%4, i), Series: fmt.Sprint(i)})
		require.NoError(t, err)
		upstream <- messaging.Message{Value: data}
	}
	// Neither malformed JSON nor an unparseable rule id reaches any subscriber.
	upstream <- messaging.Message{Value: []byte("{")}
	upstream <- messaging.Message{Value: []byte(`{"ruleId":"invalid"}`)}
	close(upstream)
	for i, ch := range chans {
		for j := i % 4; j < 24; j += 4 {
			select {
			case got, ok := <-ch:
				require.True(t, ok)
				require.Equal(t, fmt.Sprintf("acme/profile-%d@v1/rule-%d", i%4, j), got.RuleId())
				require.Equal(t, fmt.Sprintf("rule-%d", j), got.RuleToken())
				require.Equal(t, fmt.Sprint(j), got.Series())
			case <-time.After(5 * time.Second):
				t.Fatal("missing detection")
			}
		}
		select {
		case _, ok := <-ch:
			require.False(t, ok, "unexpected detection after expected profile matches")
		case <-time.After(5 * time.Second):
			t.Fatal("resolver did not close")
		}
	}
	require.Equal(t, int64(26), decodes.Load())
	select {
	case <-upstreamCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shared upstream did not cancel")
	}
}

// One shared tenant feed, two subscribers with different authorities: the hub fans out by
// profile only, and each subscription applies its own caller's location:read filter, so the
// caller without it is withheld the fence firings while the caller with it still gets them.
func TestDetectionStreamSharedFeedFiltersPerSubscriber(t *testing.T) {
	store, _ := evStores(t)
	if err := store.Upsert(previewCtx(auth.DeviceRead), []model.DetectRule{
		detectionRule("fence", fenceDraft), detectionRule("threshold", plainDraft),
	}); err != nil {
		t.Fatal(err)
	}
	nmgr := &messaging.NatsManager{}
	upstream := make(chan messaging.Message, 8)
	var opens atomic.Int64
	oldOpen := detectionLiveOpener
	detectionLiveOpener = func(*messaging.NatsManager) messaging.LiveOpener {
		return func(context.Context, string) (<-chan messaging.Message, error) {
			opens.Add(1)
			return upstream, nil
		}
	}
	defer func() { detectionLiveOpener = oldOpen }()
	subscribe := func(authorities ...auth.Authority) <-chan *DetectionEventResolver {
		ctx, cancel := context.WithTimeout(previewCtx(authorities...), 5*time.Second)
		t.Cleanup(cancel)
		ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr)
		out, err := (&SchemaResolver{DetectRules: store}).DetectionStream(ctx, struct{ ProfileToken string }{ProfileToken: "p"})
		require.NoError(t, err)
		return out
	}
	located := subscribe(auth.DeviceRead, auth.LocationRead)
	unlocated := subscribe(auth.DeviceRead)
	require.Equal(t, int64(1), opens.Load(), "both subscriptions must share one feed")
	for _, token := range []string{"fence", "threshold"} {
		for _, edge := range []string{"raised", "resolved"} {
			body, err := json.Marshal(runtime.DerivedEvent{RuleID: runtime.PublishedRuleID("acme", "p@1", token), Series: "device-1", Edge: edge})
			require.NoError(t, err)
			upstream <- messaging.Message{Value: body}
		}
	}
	close(upstream)
	drain := func(ch <-chan *DetectionEventResolver) []string {
		var got []string
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return got
				}
				got = append(got, ev.RuleToken()+":"+ev.Edge())
			case <-time.After(5 * time.Second):
				t.Fatal("subscription did not finish")
			}
		}
	}
	require.Equal(t, []string{"fence:raised", "fence:resolved", "threshold:raised", "threshold:resolved"}, drain(located))
	require.Equal(t, []string{"threshold:raised", "threshold:resolved"}, drain(unlocated))
}

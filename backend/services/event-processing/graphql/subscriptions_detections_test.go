// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
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
		f.allowed[id] = false
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

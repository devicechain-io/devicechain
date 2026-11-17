// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	dccore "github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

func TestRestartHydratesHistoricalFenceGeometryBeforeReplay(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateGeoFence(dmCtx, "yard", &dmmodel.GeoFenceUpdateRequest{
		Geometry: gqlcore.OptionalStringOf(fenceBox(10, 10, 11, 11))}); err != nil {
		t.Fatal(err)
	}
	src := newSchemaFenceSource(t, api)
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, src)
	rp.VersionedFenceSets = src
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rp.fenceView.RetainedVersions("acme"); !reflect.DeepEqual(got, []int32{1, 2}) {
		t.Fatalf("after restart: versions %v, want [1 2] without facts or preview", got)
	}
	// The historical geometry contains the point; today's geometry does not.
	rp.handle(locatedMsg(t, 1, "acme", "old-device", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.handle(locatedMsg(t, 2, "acme", "new-device", "p@1", 2, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("historical and current events produced %d detections, want 1", w.writes)
	}
}

type hydrationSource struct {
	current int32
	calls   []int32
	load    func(int32) (*geofence.FenceSet, error)
}

func (s *hydrationSource) CurrentFenceSet(context.Context, string) (*geofence.FenceSet, error) {
	return geofence.EmptyFenceSet(s.current), nil
}

func (s *hydrationSource) FenceSetAt(_ context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	if tenant != "acme" {
		return nil, fmt.Errorf("unexpected tenant %q", tenant)
	}
	s.calls = append(s.calls, version)
	if s.load != nil {
		return s.load(version)
	}
	return geofence.EmptyFenceSet(version), nil
}

func TestStartupFenceHydrationIsBoundedAndSkipsHeldVersions(t *testing.T) {
	for _, current := range []int32{0, 1, 2, 6, 2147483647} {
		t.Run(fmt.Sprint(current), func(t *testing.T) {
			src := &hydrationSource{current: current}
			rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, src)
			rp.VersionedFenceSets = src
			if err := rp.startFenceView(context.Background()); err != nil {
				t.Fatal(err)
			}
			var want []int32
			for i := int32(1); i < 4 && i < current; i++ {
				want = append(want, current-i)
			}
			if !reflect.DeepEqual(src.calls, want) {
				t.Fatalf("archive calls %v, want %v", src.calls, want)
			}
			if err := rp.reconcileFenceView(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(src.calls, want) {
				t.Fatalf("second reconcile reloaded held versions: %v", src.calls)
			}
			if got := rp.fenceView.RetainedVersions("acme"); len(got) > runtime.MaxRetainedFenceSetVersions {
				t.Fatalf("unbounded retained versions: %v", got)
			}
			if rp.fenceView.For("other", current) != nil && current > 0 {
				t.Fatal("hydration leaked across tenants")
			}
		})
	}
}

func TestStartupFenceHydrationFailsClosedAndRetriesMissingVersions(t *testing.T) {
	for _, failure := range []string{"error", "nil", "wrong version"} {
		t.Run(failure, func(t *testing.T) {
			src := &hydrationSource{current: 6}
			src.load = func(version int32) (*geofence.FenceSet, error) {
				if version == 5 {
					switch failure {
					case "error":
						return geofence.EmptyFenceSet(5), fmt.Errorf("archive unavailable")
					case "nil":
						return nil, nil
					default:
						return geofence.EmptyFenceSet(2), nil
					}
				}
				return geofence.EmptyFenceSet(version), nil
			}
			rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, src)
			rp.VersionedFenceSets = src
			if err := rp.startFenceView(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := rp.fenceView.RetainedVersions("acme"); !reflect.DeepEqual(got, []int32{3, 4, 6}) {
				t.Fatalf("failed hydration installed incorrect versions or skipped healthy ones: %v", got)
			}
			if rp.fenceView.For("acme", 5) != nil {
				t.Fatal("failed read became a known fence set")
			}
			src.load = nil
			if err := rp.reconcileFenceView(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(src.calls, []int32{5, 4, 3, 5}) || rp.fenceView.For("acme", 5) == nil {
				t.Fatalf("missing version was not retried independently: %v", src.calls)
			}
		})
	}
}

func TestStartupFenceHydrationStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &hydrationSource{current: 6}
	src.load = func(version int32) (*geofence.FenceSet, error) {
		cancel()
		return geofence.EmptyFenceSet(version), nil
	}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, src)
	rp.VersionedFenceSets = src
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(src.calls, []int32{5}) {
		t.Fatalf("archive calls continued after cancellation: %v", src.calls)
	}
}

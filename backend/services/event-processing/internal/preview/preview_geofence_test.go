// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// boxFenceSet builds a frozen fence set of one axis-aligned lon/lat box.
func boxFenceSet(version int32, token string, lonMin, latMin, lonMax, latMax float64) *geofence.FenceSet {
	geom, err := json.Marshal(map[string]any{
		"kind": geofence.KindPolygon2D,
		"geometry": map[string]any{
			"type": "Polygon",
			"coordinates": [][][2]float64{{
				{lonMin, latMin}, {lonMax, latMin}, {lonMax, latMax}, {lonMin, latMax}, {lonMin, latMin},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return geofence.NewFenceSet(version, []geofence.SnapshotFence{{Token: token, Geometry: geom}})
}

// archiveSource stands in for device-management's frozen fence-set snapshots: every version ever
// minted, readable by version, exactly as GeoFenceSetSnapshotAt serves them.
type archiveSource struct {
	byTenant map[string]map[int32]*geofence.FenceSet
	asked    []int32
}

func (a *archiveSource) FenceSetAt(_ context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	a.asked = append(a.asked, version)
	fs, ok := a.byTenant[tenant][version]
	if !ok {
		return nil, errors.New("no such fence set version")
	}
	return fs, nil
}

// locMsg builds a consumed resolved LOCATION message stamped with a fence-set version.
func locMsg(t *testing.T, seq uint64, tenant, device, profileVersion string, fenceSetVersion int32,
	lon, lat float64, occurred time.Time) messaging.Message {
	t.Helper()
	lonS := strconv.FormatFloat(lon, 'f', -1, 64)
	latS := strconv.FormatFloat(lat, 'f', -1, 64)
	ev := &dmmodel.ResolvedEvent{
		Source:              "mqtt1",
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		ProcessedTime:       occurred,
		EventType:           esmodel.Location,
		FenceSetVersion:     fenceSetVersion,
		Payload: &dmmodel.ResolvedLocationsPayload{Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: &latS, Longitude: &lonS, OccurredTime: occurred},
		}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

// fenceReg is a registry holding one draft rule whose leaf is pure containment.
func fenceReg(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	cr, err := rules0.Compile(rules0.Rule{
		ID: "acme/p@1/inyard", Name: "in the yard", Type: rules0.TypeThreshold,
		Severity: rules0.SeverityCritical,
		When:     rules0.Condition{CEL: `geo.inFence("yard")`},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

// TestPreviewResolvesTheHISTORICALFenceSet is the point of wiring a source into the preview at all.
//
// The yard used to be at [0,1]² (fence-set version 7) and was later moved to [10,11]² (version 9).
// The replayed history is two events from BEFORE the move, both stamped 7: one inside the old yard,
// one outside it. A correct preview raises and then resolves, because it evaluates each event
// against the fence set that event NAMES.
//
// 🔴 THE ASSERTION THAT MATTERS IS THAT THIS IS NOT ZERO. The attr projection degrades to nil in a
// preview, so a dynamic-threshold rule previews as never-firing — and copying that for fences was
// the obvious, cheap, and wrong thing to do here. Both events sit outside the CURRENT yard, so an
// implementation that resolved the current fence set (or none at all) produces a perfectly
// plausible empty timeline for the headline feature. The version the source was ASKED for is
// asserted too, so a preview that happened to read the right geometry for the wrong reason fails.
func TestPreviewResolvesTheHISTORICALFenceSet(t *testing.T) {
	src := &archiveSource{byTenant: map[string]map[int32]*geofence.FenceSet{"acme": {
		7: boxFenceSet(7, "yard", 0, 0, 1, 1),
		9: boxFenceSet(9, "yard", 10, 10, 11, 11),
	}}}
	op := &fakeOpener{msgs: []messaging.Message{
		locMsg(t, 1, "acme", "d1", "p@1", 7, 0.5, 0.5, base),              // inside the OLD yard → RAISE
		locMsg(t, 2, "acme", "d1", "p@1", 7, 5, 5, base.Add(time.Minute)), // outside it → RESOLVE
	}}

	res, err := Run(context.Background(), op, "resolved-events", fenceReg(t), "acme", "p@1", window(), 0, 0, 0, src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.EvalErrors != 0 {
		t.Errorf("eval errors = %d; the historical fence set was resolvable, so nothing should have errored",
			res.Stats.EvalErrors)
	}
	if len(res.Firings) != 2 {
		t.Fatalf("want 2 firings (raise, resolve) against the historical geometry, got %d: %+v",
			len(res.Firings), res.Firings)
	}
	if !res.Firings[0].Raise || res.Firings[1].Raise {
		t.Errorf("firings are %v/%v (raise flags), want raise then resolve",
			res.Firings[0].Raise, res.Firings[1].Raise)
	}
	for _, v := range src.asked {
		if v != 7 {
			t.Errorf("the preview asked the archive for version %d; the events are stamped 7 and nothing "+
				"else may be consulted", v)
		}
	}
	if len(src.asked) != 1 {
		t.Errorf("the archive was read %d times for one version; the resolver must memoize", len(src.asked))
	}
}

// TestPreviewWithNoFenceSourceIsHonestlyDegraded: with no source wired, a geofence rule previews as
// zero firings WITH eval errors and a degraded note — not as a quiet, healthy, never-firing rule.
// The distinction is the whole reason inFence errors rather than answering false.
//
// The control is the test above: the identical rule and history, with a source, fires twice. So
// "zero firings" here is a statement about the missing source rather than about a rule that could
// never fire.
func TestPreviewWithNoFenceSourceIsHonestlyDegraded(t *testing.T) {
	op := &fakeOpener{msgs: []messaging.Message{
		locMsg(t, 1, "acme", "d1", "p@1", 7, 0.5, 0.5, base),
		locMsg(t, 2, "acme", "d1", "p@1", 7, 5, 5, base.Add(time.Minute)),
	}}
	res, err := Run(context.Background(), op, "resolved-events", fenceReg(t), "acme", "p@1", window(), 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 0 {
		t.Errorf("got %d firings with no fence source", len(res.Firings))
	}
	if res.Stats.EvalErrors != 2 {
		t.Errorf("eval errors = %d, want 2 (one per in-scope event)", res.Stats.EvalErrors)
	}
	if res.Degraded == "" {
		t.Error("the result is not marked degraded; an unresolvable fence set must not look like a quiet rule")
	}
}

// TestPreviewFenceSetsAreTenantScoped: the resolver is bound to the previewed tenant, so a replayed
// event cannot steer it into another tenant's archive however it is stamped. The archive here holds
// a version 7 for BOTH tenants, at opposite ends of the world.
func TestPreviewFenceSetsAreTenantScoped(t *testing.T) {
	src := &archiveSource{byTenant: map[string]map[int32]*geofence.FenceSet{
		"acme":   {7: boxFenceSet(7, "yard", 0, 0, 1, 1)},
		"globex": {7: boxFenceSet(7, "yard", 100, 40, 101, 41)},
	}}
	// The device sits at (100.5, 40.5) — inside GLOBEX's yard, far outside acme's.
	op := &fakeOpener{msgs: []messaging.Message{
		locMsg(t, 1, "acme", "d1", "p@1", 7, 100.5, 40.5, base),
	}}
	res, err := Run(context.Background(), op, "resolved-events", fenceReg(t), "acme", "p@1", window(), 0, 0, 0, src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 0 {
		t.Errorf("an acme preview fired at coordinates inside GLOBEX's yard: %+v", res.Firings)
	}
	if res.Stats.EvalErrors != 0 {
		t.Errorf("eval errors = %d; acme's own version 7 was resolvable, so the non-firing above must be "+
			"a real 'outside' answer rather than a failure to resolve", res.Stats.EvalErrors)
	}
	// Control: acme's own yard does fire, so the tenant scoping is not just a broken lookup.
	op2 := &fakeOpener{msgs: []messaging.Message{locMsg(t, 1, "acme", "d1", "p@1", 7, 0.5, 0.5, base)}}
	res2, err := Run(context.Background(), op2, "resolved-events", fenceReg(t), "acme", "p@1", window(), 0, 0, 0, src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res2.Firings) != 1 || !res2.Firings[0].Raise {
		t.Fatalf("acme's device inside acme's own yard did not raise: %+v (errors %d)", res2.Firings, res2.Stats.EvalErrors)
	}
}

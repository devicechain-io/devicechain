// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

// profileResolutionRig is a CachedApi over SQLite whose per-type resolution cache is a
// counting store, wired as the Api's evictor the way main.go wires it, with one device
// type "dt" adopting profile "p", published once with metric "temp" in unit Cel.
type profileResolutionRig struct {
	api    *Api
	capi   *CachedApi
	kv     *msgtest.MemoryKV
	ctx    context.Context
	typeId uint
}

func newProfileResolutionRig(t *testing.T) *profileResolutionRig {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...),
		&GeoFence{}, &GeoFenceSetVersion{}, &GeoFenceGeometryBlob{})...)
	ctx := core.WithTenant(context.Background(), partialUpdateTenant)
	kv := msgtest.NewMemoryKV()
	capi := NewCachedApi(api, &Caches{ProfileResolutionByType: kv.NewCache()})
	api.CacheEvictor = capi

	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "p"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	unit := "Cel"
	if _, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
		Token: "temp-def", DeviceProfileToken: "p", MetricKey: "temp", DataType: "DOUBLE", Unit: &unit,
	}); err != nil {
		t.Fatalf("seed metric: %v", err)
	}
	if _, err := capi.PublishDeviceProfile(ctx, "p", nil, nil, "t"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	profile := "p"
	dt, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt", ProfileToken: &profile})
	if err != nil {
		t.Fatalf("seed type: %v", err)
	}
	return &profileResolutionRig{api: api, capi: capi, kv: kv, ctx: ctx, typeId: dt.ID}
}

func (r *profileResolutionRig) read(t *testing.T) *ProfileResolution {
	t.Helper()
	res, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
	if err != nil {
		t.Fatalf("read resolution: %v", err)
	}
	return res
}

func unitOf(t *testing.T, res *ProfileResolution) string {
	t.Helper()
	m, ok := res.MetricsByKey()["temp"]
	if !ok {
		return ""
	}
	if m.Unit == nil {
		return "<none>"
	}
	return *m.Unit
}

// editUnitAndPublish changes the draft unit of "temp" and publishes it, through the
// cached decorator the GraphQL publish mutation takes.
func (r *profileResolutionRig) editUnitAndPublish(t *testing.T, unit string) {
	t.Helper()
	if err := r.api.RDB.DB(r.ctx).Model(&MetricDefinition{}).Where("token = ?", "temp-def").
		Update("unit", sql.NullString{String: unit, Valid: true}).Error; err != nil {
		t.Fatalf("edit draft unit: %v", err)
	}
	if _, err := r.capi.PublishDeviceProfile(r.ctx, "p", nil, nil, "t"); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// The per-type resolution entry is dropped by every mutation that changes what it holds,
// and the next read rebuilds it with the NEW value. A trigger that forgot to evict would
// leave the entry in place, and the read after it would be a hit on the old value until the
// TTL: exactly the staleness this cache is allowed only by accident, never by design.
//
// 🔑 The fence-set mint is the trigger that changed on purpose. It used to drop only the
// scope; the scope and the metric definitions now share one entry, so a fence edit also
// makes the next event of each type re-read its definitions.
func TestTheProfileResolutionEntryIsDroppedByEveryTrigger(t *testing.T) {
	cases := []struct {
		name    string
		trigger func(t *testing.T, r *profileResolutionRig)
		check   func(t *testing.T, before, after *ProfileResolution)
	}{
		{
			name: "publish",
			trigger: func(t *testing.T, r *profileResolutionRig) {
				r.editUnitAndPublish(t, "K")
			},
			check: func(t *testing.T, before, after *ProfileResolution) {
				if after.Scope.ProfileVersionToken != "p@2" || unitOf(t, after) != "K" {
					t.Errorf("after publish: %q with unit %q, want p@2 with K",
						after.Scope.ProfileVersionToken, unitOf(t, after))
				}
			},
		},
		{
			name: "rollback",
			trigger: func(t *testing.T, r *profileResolutionRig) {
				// Move to v2 (which evicts), re-warm the entry on v2, then roll back to v1:
				// the rollback is the only thing left that can drop the v2 entry.
				r.editUnitAndPublish(t, "K")
				r.read(t)
				if _, err := r.capi.RollbackDeviceProfile(r.ctx, "p", 1); err != nil {
					t.Fatalf("rollback: %v", err)
				}
			},
			check: func(t *testing.T, before, after *ProfileResolution) {
				if after.Scope.ProfileVersionToken != "p@1" || unitOf(t, after) != "Cel" {
					t.Errorf("after rollback: %q with unit %q, want p@1 with Cel",
						after.Scope.ProfileVersionToken, unitOf(t, after))
				}
			},
		},
		{
			name: "device type re-profiled",
			trigger: func(t *testing.T, r *profileResolutionRig) {
				if _, err := r.capi.UpdateDeviceType(r.ctx, "dt", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.ClearedString()}); err != nil {
					t.Fatalf("detach profile: %v", err)
				}
			},
			check: func(t *testing.T, before, after *ProfileResolution) {
				if after.Scope.ProfileVersionToken != "" || len(after.Metrics) != 0 {
					t.Errorf("after detaching the profile: %q with %d metrics, want none",
						after.Scope.ProfileVersionToken, len(after.Metrics))
				}
			},
		},
		{
			name: "fence-set version minted",
			trigger: func(t *testing.T, r *profileResolutionRig) {
				if _, err := r.api.CreateGeoFence(r.ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
					t.Fatalf("create fence: %v", err)
				}
			},
			check: func(t *testing.T, before, after *ProfileResolution) {
				if after.Scope.FenceSetVersion == before.Scope.FenceSetVersion {
					t.Errorf("fence-set version still %d after a fence was created", after.Scope.FenceSetVersion)
				}
				if unitOf(t, after) != "Cel" {
					t.Errorf("the re-read lost the metric definitions: unit %q", unitOf(t, after))
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProfileResolutionRig(t)
			before := r.read(t)
			if unitOf(t, before) != "Cel" || before.Scope.ProfileVersionToken != "p@1" {
				t.Fatalf("warm read: %q with unit %q, want p@1 with Cel", before.Scope.ProfileVersionToken, unitOf(t, before))
			}
			if r.kv.Puts != 1 || r.kv.Len() != 1 {
				t.Fatalf("warm read stored %d entries in %d puts, want 1 in 1", r.kv.Len(), r.kv.Puts)
			}

			tc.trigger(t, r)
			if r.kv.Len() != 0 {
				t.Fatalf("the entry survived the trigger (%d held); the next event would read the old value", r.kv.Len())
			}

			gets, puts := r.kv.Gets, r.kv.Puts
			after := r.read(t)
			if r.kv.Gets != gets+1 || r.kv.Puts != puts+1 {
				t.Errorf("the re-read was %d gets and %d puts, want a miss then a fill (1 and 1)",
					r.kv.Gets-gets, r.kv.Puts-puts)
			}
			tc.check(t, before, after)
		})
	}
}

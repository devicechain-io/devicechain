// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// DistinctAnchorRefs returns the distinct entity references a tenant's anchors hold:
// each source device and each (anchor_type, anchor_id) target, deduped.
func TestDistinctAnchorRefs(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)
	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		{DeviceToken: "device-4", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
		{DeviceToken: "device-4", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "area", AnchorToken: "area-9"},
		{DeviceToken: "device-7", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"}, // dup target
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	refs, err := api.DistinctAnchorRefs(ctx)
	assert.NoError(t, err)

	set := make(map[string]bool)
	for _, r := range refs {
		set[r.Type+"|"+r.Token] = true
	}
	// two distinct targets + two distinct source devices
	assert.True(t, set["customer|cust-3"], "customer 3 target")
	assert.True(t, set["area|area-9"], "area 9 target")
	assert.True(t, set["device|device-4"], "device 4 source")
	assert.True(t, set["device|device-7"], "device 7 source")
	assert.Len(t, set, 4, "customer 3 appears once despite two anchors")
}

// DistinctAnchorTenants is a cross-tenant read: given a system context it returns
// every tenant with anchors.
func TestDistinctAnchorTenants(t *testing.T) {
	api := newPersistenceTestApi(t)
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)
	for _, tenant := range []string{"acme", "globex"} {
		tctx := core.WithTenant(context.Background(), tenant)
		if err := api.CreateEventAnchors(tctx, api.RDB.DB(tctx), []*EventAnchor{
			{DeviceToken: "device-1", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-1"},
		}); err != nil {
			t.Fatalf("seed %s: %v", tenant, err)
		}
	}

	tenants, err := api.DistinctAnchorTenants(core.WithSystemContext(context.Background()))
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"acme", "globex"}, tenants)
}

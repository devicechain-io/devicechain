// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// A geofence is an authored tenant resource, so it takes the same authorities as the
// profiles and rules it sits beside: device:write to change, device:read to read.
//
// The reads matter as much as the writes here. A fence's geometry is where a tenant's
// sites are — a yard, a depot, a customer's plant — so an unauthenticated read is a map
// of the tenant's operations.

const authTestGeometry = `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
	`[[[-84.3881,33.7490],[-84.3875,33.7492],[-84.3872,33.7486],[-84.3881,33.7490]]]}}`

// geoFenceTestCtx builds a context carrying a real sqlite-backed Api and a tenant, with
// NO claims — how an unauthenticated request arrives. The Api is real so an authorized
// call runs the whole path and returns rows, rather than proving only that it got past
// the gate.
func geoFenceTestCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.GeoFence{}, &model.GeoFenceSetVersion{}, &model.GeoFenceGeometryBlob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// callGeoFenceReads invokes every geofence read door and hands each (name, error) pair
// to check. Keeping them in one table matters: they are four doors onto the same
// material, and a gate applied to three of the four is exactly the defect this catches.
func callGeoFenceReads(t *testing.T, ctx context.Context, check func(name string, err error)) {
	t.Helper()
	r := &SchemaResolver{}

	_, err := r.GeoFencesById(ctx, struct{ Ids []string }{Ids: []string{"1"}})
	check("geoFencesById", err)

	_, err = r.GeoFencesByToken(ctx, struct{ Tokens []string }{Tokens: []string{"yard"}})
	check("geoFencesByToken", err)

	_, err = r.GeoFences(ctx, struct {
		Criteria model.GeoFenceSearchCriteria
	}{Criteria: model.GeoFenceSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}}})
	check("geoFences", err)

	_, err = r.CurrentFenceSetVersion(ctx)
	check("currentFenceSetVersion", err)

	// The frozen-snapshot doors carry the SAME material as the live fences — a fence's
	// geometry is where the tenant's sites are — so they take the same gate. They are the
	// ones a cross-service caller uses, which makes an ungated one a map of the tenant's
	// operations reachable by anything that can reach the pod.
	//
	// geoFenceSetSnapshot is asked for version 0, which is the ONE version that resolves
	// without a row (the known-empty set). That is deliberate: an authorized caller must get
	// past the gate and return a real result here, so the "device:read is admitted" half of
	// the table is not passing merely because the query errored for a different reason.
	_, err = r.GeoFenceSetSnapshot(ctx, struct{ Version int32 }{Version: 0})
	check("geoFenceSetSnapshot", err)

	_, err = r.CurrentGeoFenceSet(ctx)
	check("currentGeoFenceSet", err)
}

// callGeoFenceWrites invokes every geofence write door.
func callGeoFenceWrites(t *testing.T, ctx context.Context, token string, check func(name string, err error)) {
	t.Helper()
	r := &SchemaResolver{}
	request := model.GeoFenceCreateRequest{Token: token, Geometry: authTestGeometry}

	_, err := r.CreateGeoFence(ctx, struct {
		Request model.GeoFenceCreateRequest
	}{Request: request})
	check("createGeoFence", err)

	_, err = r.UpdateGeoFence(ctx, struct {
		Token   string
		Request model.GeoFenceCreateRequest
	}{Token: token, Request: request})
	check("updateGeoFence", err)

	_, err = r.DeleteGeoFence(ctx, struct{ Token string }{Token: token})
	check("deleteGeoFence", err)
}

// An unauthenticated caller is refused by every geofence door, read and write.
func TestGeoFenceAuthoritiesRefuseUnauthenticated(t *testing.T) {
	ctx := geoFenceTestCtx(t)
	refuse := func(name string, err error) {
		if err == nil {
			t.Errorf("%s admitted a caller with no claims", name)
		}
	}
	callGeoFenceReads(t, ctx, refuse)
	callGeoFenceWrites(t, ctx, "yard", refuse)
}

// device:read opens the reads and NOT the writes: a viewer can see the fences and cannot
// move them. Both halves are asserted from the same context, so "reads work" is not
// being read off a caller who happens to hold everything.
func TestGeoFenceReadAuthorityDoesNotGrantWrite(t *testing.T) {
	ctx := withAuthorities(geoFenceTestCtx(t), auth.DeviceRead)

	callGeoFenceReads(t, ctx, func(name string, err error) {
		if err != nil {
			t.Errorf("%s refused a caller holding device:read: %v", name, err)
		}
	})
	callGeoFenceWrites(t, ctx, "yard", func(name string, err error) {
		if err == nil {
			t.Errorf("%s admitted a caller holding only device:read", name)
		}
	})
}

// device:write plus location:read opens the writes, and the whole create → update → delete
// path really runs (a real Api behind it), so this is not passing merely because a stub
// returned nil. location:read is in the set because MINTING or MOVING a fence takes it — see
// the test below for why.
func TestGeoFenceWriteAuthorityAdmitsMutations(t *testing.T) {
	ctx := withAuthorities(geoFenceTestCtx(t), auth.DeviceRead, auth.DeviceWrite, auth.LocationRead)

	callGeoFenceWrites(t, ctx, "yard", func(name string, err error) {
		if err != nil {
			t.Errorf("%s refused a caller holding device:write and location:read: %v", name, err)
		}
	})
}

// 🔴🔴 AUTHORING A FENCE TAKES location:read, BECAUSE SHAPING THE QUESTION EXTRACTS THE ANSWER.
// A fence is a question about where a device is. A caller holding device:write but NOT
// location:read could otherwise mint a fence a few metres across, run a containment rule against
// it, read the raise/resolve edge, and repeat — binary-searching a device's actual coordinates
// out of a platform that had just refused to show them one. That recovers POSITION ITSELF, not
// merely containment against a region someone else drew, and it left device:write strictly
// out-reading the authority invented to gate position.
//
// DELETE is deliberately NOT in that set, and the asymmetry is the point of this test: deleting
// removes a question rather than constructing one, and delete-then-recreate is no way around the
// gate because the recreate is the gated half. Asserting both halves from ONE context is what
// makes this more than a restatement of the code — a gate accidentally applied to all three
// writes, or to none, fails here.
func TestFenceAuthoringNeedsThePositionAuthorityButDeletingDoesNot(t *testing.T) {
	ctx := withAuthorities(geoFenceTestCtx(t), auth.DeviceRead, auth.DeviceWrite)
	r := &SchemaResolver{}
	request := model.GeoFenceCreateRequest{Token: "yard", Geometry: authTestGeometry}

	if _, err := r.CreateGeoFence(ctx, struct {
		Request model.GeoFenceCreateRequest
	}{Request: request}); err == nil {
		t.Error("createGeoFence admitted a caller without location:read; a fence author can " +
			"binary-search a position out of repeated containment answers")
	}

	if _, err := r.UpdateGeoFence(ctx, struct {
		Token   string
		Request model.GeoFenceCreateRequest
	}{Token: "yard", Request: request}); err == nil {
		t.Error("updateGeoFence admitted a caller without location:read; reshaping an existing " +
			"fence is the same question-shaping primitive as minting one")
	}

	// The counterweight. Without it, a gate bolted onto every write would pass the two
	// assertions above while quietly making fence cleanup need a position authority it has no
	// reason to need.
	if _, err := r.DeleteGeoFence(ctx, struct{ Token string }{Token: "yard"}); err != nil {
		t.Errorf("deleteGeoFence must stay on device:write alone — removing a fence constructs "+
			"no question and answers none; got: %v", err)
	}
}

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
	if err := db.AutoMigrate(&model.GeoFence{}, &model.GeoFenceSetVersion{}); err != nil {
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

// device:write opens the writes, and the whole create → update → delete path really runs
// (a real Api behind it), so this is not passing merely because a stub returned nil.
func TestGeoFenceWriteAuthorityAdmitsMutations(t *testing.T) {
	ctx := withAuthorities(geoFenceTestCtx(t), auth.DeviceRead, auth.DeviceWrite)

	callGeoFenceWrites(t, ctx, "yard", func(name string, err error) {
		if err != nil {
			t.Errorf("%s refused a caller holding device:write: %v", name, err)
		}
	})
}

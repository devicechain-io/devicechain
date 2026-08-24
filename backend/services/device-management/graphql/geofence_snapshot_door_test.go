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

// The frozen fence-set doors are what event-processing reads a historical fence set through —
// a preview replaying last week, a cold start re-seeding its live projection. They are
// cross-service doors, so their tenancy is not a UI concern: the caller is another service
// holding a service token, and the only thing standing between one tenant's fence geometry
// and another is that the request cannot name a tenant at all.
//
// These tests drive the resolvers over ONE shared database holding two tenants' fences, which
// is the arrangement that can actually fail. A per-tenant database would pass whatever the
// resolver did.

// twoTenantFenceCtx builds two tenant contexts over one shared sqlite-backed Api, each
// carrying device:read, and seeds one geofence per tenant so both have something to find.
func twoTenantFenceCtx(t *testing.T) (acme, globex context.Context) {
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
	api := model.NewApi(&rdb.RdbManager{Database: db})

	build := func(tenant string) context.Context {
		ctx := core.WithTenant(context.Background(), tenant)
		ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
		return withAuthorities(ctx, auth.DeviceRead)
	}
	acme, globex = build("acme"), build("globex")

	if _, err := api.CreateGeoFence(acme, &model.GeoFenceCreateRequest{
		Token: "acme-yard", Geometry: authTestGeometry}); err != nil {
		t.Fatalf("seed acme fence: %v", err)
	}
	if _, err := api.CreateGeoFence(globex, &model.GeoFenceCreateRequest{
		Token: "globex-depot", Geometry: authTestGeometry}); err != nil {
		t.Fatalf("seed globex fence: %v", err)
	}
	return acme, globex
}

// allSnapshotFences reads a frozen fence set's fences through the REAL paginated field, paging
// until the reported total is in hand. The tests below assert about the whole set, and the field
// hands out one page — a helper that asked for page 1 and stopped would quietly turn "the set has
// one fence" into "the first page has one fence", which is the exact confusion pagination
// introduces and the exact thing these tenancy assertions must not inherit.
func allSnapshotFences(t *testing.T, res *GeoFenceSetSnapshotResolver) []*GeoFenceSnapshotEntryResolver {
	t.Helper()
	all := make([]*GeoFenceSnapshotEntryResolver, 0)
	for page := int32(1); ; page++ {
		got := res.Fences(struct{ Pagination PaginationInput }{
			Pagination: PaginationInput{PageNumber: page, PageSize: 2},
		})
		all = append(all, got.Results()...)
		total := got.Pagination().TotalRecords()
		if total == nil {
			t.Fatalf("fences page %d reported no totalRecords", page)
		}
		if int32(len(all)) >= *total {
			return all
		}
		if len(got.Results()) == 0 {
			t.Fatalf("fences page %d was empty with %d of %d read", page, len(all), *total)
		}
	}
}

// Each tenant's CURRENT fence set holds its OWN fence and only its own. Both directions are
// asserted, so this cannot pass by the door returning nothing to anybody.
func TestCurrentGeoFenceSetDoorIsTenantScoped(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	only := func(ctx context.Context, who string) string {
		res, err := r.CurrentGeoFenceSet(ctx)
		if err != nil {
			t.Fatalf("%s currentGeoFenceSet: %v", who, err)
		}
		fences := allSnapshotFences(t, res)
		if len(fences) != 1 {
			t.Fatalf("%s currentGeoFenceSet returned %d fences, want exactly 1 (its own)", who, len(fences))
		}
		return fences[0].Token()
	}

	if got := only(acme, "acme"); got != "acme-yard" {
		t.Errorf("acme's current fence set holds %q, want acme-yard", got)
	}
	if got := only(globex, "globex"); got != "globex-depot" {
		t.Errorf("globex's current fence set holds %q — it can see another tenant's fence geometry", got)
	}
}

// Version numbers are PER TENANT, so both tenants own a "version 1". Naming another tenant's
// version must not reach it: the two version-1s are different rows and the request carries no
// way to select the other one.
//
// The owner's read of the same number is the counterweight in the same test — otherwise
// "globex got acme's fence" not happening would be indistinguishable from version 1 being
// unreadable by anyone.
func TestGeoFenceSetSnapshotDoorCannotReachAnotherTenantsVersion(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	res, err := r.GeoFenceSetSnapshot(acme, struct{ Version int32 }{Version: 1})
	if err != nil {
		t.Fatalf("acme geoFenceSetSnapshot(1): %v", err)
	}
	fences := allSnapshotFences(t, res)
	if len(fences) != 1 || fences[0].Token() != "acme-yard" {
		t.Fatalf("control: acme's own version 1 must resolve to its own fence, got %d fences", len(fences))
	}

	res, err = r.GeoFenceSetSnapshot(globex, struct{ Version int32 }{Version: 1})
	if err != nil {
		// globex has a version 1 of its own, so this must resolve — to ITS fence.
		t.Fatalf("globex geoFenceSetSnapshot(1): %v", err)
	}
	fences = allSnapshotFences(t, res)
	if len(fences) != 1 {
		t.Fatalf("globex's own version 1 returned %d fences, want 1", len(fences))
	}
	if fences[0].Token() != "globex-depot" {
		t.Errorf("globex asking for version 1 got fence %q — version numbers are per-tenant and "+
			"this read crossed the boundary", fences[0].Token())
	}
}

// A version that exists for one tenant and NOT for another is an error for the second, never
// an empty set. Answering "empty" would tell the caller the tenant has no fences at that
// version, which is a statement about another tenant's history dressed up as its own.
func TestGeoFenceSetSnapshotDoorRefusesAVersionTheTenantDoesNotHave(t *testing.T) {
	acme, globex := twoTenantFenceCtx(t)
	r := &SchemaResolver{}

	// Advance acme to version 2; globex stays at 1. The authoring authorities here are a
	// FIXTURE, not the subject — this test is about the snapshot door's tenant scoping — so it
	// carries whatever authoring currently takes (location:read included, since minting or
	// moving a fence shapes a question about where a device is).
	if _, err := r.UpdateGeoFence(withAuthorities(acme, auth.DeviceRead, auth.DeviceWrite, auth.LocationRead), struct {
		Token   string
		Request model.GeoFenceCreateRequest
	}{Token: "acme-yard", Request: model.GeoFenceCreateRequest{Token: "acme-yard", Geometry: authTestGeometry}}); err != nil {
		t.Fatalf("advance acme: %v", err)
	}

	if _, err := r.GeoFenceSetSnapshot(acme, struct{ Version int32 }{Version: 2}); err != nil {
		t.Fatalf("control: acme's own version 2 must resolve: %v", err)
	}
	if _, err := r.GeoFenceSetSnapshot(globex, struct{ Version int32 }{Version: 2}); err == nil {
		t.Error("globex resolved version 2, which only acme ever minted; an unknown version must " +
			"be an error, never an empty fence set")
	}
}

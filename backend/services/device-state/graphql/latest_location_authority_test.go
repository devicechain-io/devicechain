// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// viewerBaseline is the authority set every enabled tenant member receives whether or
// not they hold any role — user-management's read-only baseline
// (identity.viewerAuthorities). It is spelled out here rather than imported because it
// lives in another module; a test over there asserts the baseline carries no write
// authority, and another asserts it does NOT carry location:read, which is what keeps
// this list an accurate stand-in for "a read-only user".
//
// 🔴 THE ABSENCE OF auth.LocationRead FROM THIS LIST IS THE POINT. It is why the
// location queries can be refused to a caller who is otherwise fully able to read the
// device, its telemetry history, its live state, its commands and its alarms.
var viewerBaseline = []auth.Authority{
	auth.DeviceRead, auth.EventRead, auth.StateRead, auth.CommandRead, auth.AlarmRead,
	auth.DashboardRead,
}

// locationTestCtx builds a context carrying a REAL sqlite-backed device-state Api and a
// tenant — what the location resolvers read out of context once past the gate. It
// carries NO claims, which is how an unauthenticated request arrives; authorities are
// layered on with withAuthorities. The Api is real so an authorized call runs the whole
// query path and returns a row, rather than proving only that it got past the gate.
func locationTestCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.LatestLocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// withAuthorities layers a claims set onto ctx, as the GraphQL auth middleware would
// populate it from a validated bearer token.
func withAuthorities(ctx context.Context, authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return auth.WithClaims(ctx, &auth.Claims{Authorities: strs})
}

// seedLocation writes one projection row — the material the gate exists to protect.
// Elevation is deliberately left unreported so the resolver's null mapping is covered
// by the same fixture.
func seedLocation(t *testing.T, ctx context.Context) {
	t.Helper()
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if err := api.MergeLatestLocations(ctx, "dozer-01", []model.LatestLocationInput{{
		Latitude:     sql.NullFloat64{Float64: 41.87231954, Valid: true},
		Longitude:    sql.NullFloat64{Float64: -87.62819473, Valid: true},
		Speed:        sql.NullFloat64{Float64: 12.9053, Valid: true},
		OccurredTime: time.Date(2026, 8, 9, 14, 32, 17, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("seed location: %v", err)
	}
}

// callBothDoors invokes each location query against ctx and hands each (name, error)
// pair to check. Keeping them in one table matters: they are two doors onto the same
// material, and a gate applied to one of the two is the exact shape of the defect this
// test exists to prevent.
func callBothDoors(t *testing.T, ctx context.Context, check func(name string, err error)) {
	t.Helper()
	r := &SchemaResolver{}

	_, err := r.LatestLocation(ctx, struct{ DeviceToken string }{DeviceToken: "dozer-01"})
	check("latestLocation", err)

	_, err = r.LatestLocations(ctx, struct{ DeviceTokens []string }{DeviceTokens: []string{"dozer-01"}})
	check("latestLocations", err)
}

// TestLocationQueriesRefuseTheViewerBaseline is the property the separate authority
// exists for. The caller here is not a stranger: they hold event:read and state:read, so
// they can already read every measurement this device has ever produced and its live
// connectivity. They still must not be told WHERE it is. The caller is given the FULL
// viewer baseline, not a bare state:read, so the test cannot pass merely because some
// other read authority happened to be missing.
func TestLocationQueriesRefuseTheViewerBaseline(t *testing.T) {
	ctx := withAuthorities(locationTestCtx(t), viewerBaseline...)
	seedLocation(t, ctx)

	callBothDoors(t, ctx, func(name string, err error) {
		if err == nil {
			t.Errorf("%s admitted a caller holding the read-only baseline: event:read/state:read now leak position", name)
			return
		}
		if err != auth.ErrForbidden {
			t.Errorf("%s refused the viewer with %v, want ErrForbidden", name, err)
		}
	})
}

// TestLocationQueriesRefuseAnAnonymousCaller: an unauthenticated caller is refused too,
// and is distinguishable from one who authenticated but lacks the authority.
func TestLocationQueriesRefuseAnAnonymousCaller(t *testing.T) {
	// No claims layered on at all, as an unauthenticated request arrives.
	ctx := locationTestCtx(t)
	seedLocation(t, ctx)

	callBothDoors(t, ctx, func(name string, err error) {
		if err != auth.ErrUnauthenticated {
			t.Errorf("%s answered an anonymous caller with %v, want ErrUnauthenticated", name, err)
		}
	})
}

// TestLocationQueriesAdmitLocationRead is the counterweight, and without it the gate
// tests above are satisfied by a resolver that refuses everyone. A caller holding
// location:read gets past the gate AND gets the row back, with its values intact.
func TestLocationQueriesAdmitLocationRead(t *testing.T) {
	ctx := withAuthorities(locationTestCtx(t), auth.LocationRead)
	seedLocation(t, ctx)

	callBothDoors(t, ctx, func(name string, err error) {
		if err != nil {
			t.Errorf("%s refused a caller holding location:read: %v", name, err)
		}
	})

	r := &SchemaResolver{}
	one, err := r.LatestLocation(ctx, struct{ DeviceToken string }{DeviceToken: "dozer-01"})
	if err != nil {
		t.Fatalf("latestLocation: %v", err)
	}
	if one == nil {
		t.Fatal("latestLocation returned no row for a device that has one")
	}
	if one.DeviceToken() != "dozer-01" {
		t.Errorf("deviceToken = %q, want dozer-01", one.DeviceToken())
	}
	if lat := one.Latitude(); lat == nil || *lat != 41.87231954 {
		t.Errorf("latitude = %v, want 41.87231954", lat)
	}
	if lon := one.Longitude(); lon == nil || *lon != -87.62819473 {
		t.Errorf("longitude = %v, want -87.62819473", lon)
	}
	if spd := one.Speed(); spd == nil || *spd != 12.9053 {
		t.Errorf("speed = %v, want 12.9053", spd)
	}
	if one.OccurredTime() == "" {
		t.Error("occurredTime came back empty")
	}
	// An unreported coordinate stays null through the resolver — it must not surface as
	// 0, which a consumer would read as sea level.
	if ele := one.Elevation(); ele != nil {
		t.Errorf("unreported elevation surfaced as %v, want null", *ele)
	}

	many, err := r.LatestLocations(ctx, struct{ DeviceTokens []string }{
		DeviceTokens: []string{"dozer-01", "never-located"}})
	if err != nil {
		t.Fatalf("latestLocations: %v", err)
	}
	if len(many) != 1 {
		t.Fatalf("latestLocations returned %d rows, want 1 (a never-located device has none)", len(many))
	}
}

// TestLatestLocationIsNullForAnUnlocatedDevice: the single-device door answers null
// rather than erroring or inventing a row, which is what lets a caller distinguish
// "never located" from "located".
func TestLatestLocationIsNullForAnUnlocatedDevice(t *testing.T) {
	ctx := withAuthorities(locationTestCtx(t), auth.LocationRead)

	r := &SchemaResolver{}
	got, err := r.LatestLocation(ctx, struct{ DeviceToken string }{DeviceToken: "never-located"})
	if err != nil {
		t.Fatalf("latestLocation: %v", err)
	}
	if got != nil {
		t.Fatalf("latestLocation invented a row for a device that has never been located: %+v", got.M)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// newLocationTestApi is newTestApi plus the latest_locations table — the projection
// under test. It is migrated from the LIVE model, which is what production reads and
// writes; the frozen migration snapshot is exercised separately by the golden schema
// diff.
func newLocationTestApi(t *testing.T) *Api {
	t.Helper()
	api := newTestApi(t)
	if err := api.RDB.Database.AutoMigrate(&LatestLocation{}); err != nil {
		t.Fatalf("migrate latest_locations: %v", err)
	}
	return api
}

// nf is a present coordinate.
func nf(f float64) sql.NullFloat64 { return sql.NullFloat64{Float64: f, Valid: true} }

// loadLocation reads the single projection row for a device, failing if absent.
func loadLocation(t *testing.T, api *Api, ctx context.Context, token string) LatestLocation {
	t.Helper()
	var l LatestLocation
	if err := api.RDB.DB(ctx).Where("device_token = ?", token).First(&l).Error; err != nil {
		t.Fatalf("load latest location for %s: %v", token, err)
	}
	return l
}

// countLocations counts the projection rows for a device — the "upsert, not insert"
// instrument.
func countLocations(t *testing.T, api *Api, ctx context.Context, token string) int64 {
	t.Helper()
	var n int64
	if err := api.RDB.DB(ctx).Model(&LatestLocation{}).Where("device_token = ?", token).Count(&n).Error; err != nil {
		t.Fatalf("count latest locations for %s: %v", token, err)
	}
	return n
}

// TestLatestLocationRoundTripsEveryField asserts each of the seven fields INDIVIDUALLY,
// from values chosen to be mutually distinguishable: no two are equal, none is a round
// number, none is a plausible default, and the pairs that share a SQL type (elevation /
// accuracy / speed) differ in magnitude. That is what makes the assertions able to catch
// a transposed mapping — with 0/1/2-style values, swapping speed and heading in the
// upsert would still pass every check.
func TestLatestLocationRoundTripsEveryField(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 8, 9, 14, 32, 17, 0, time.UTC)

	if err := api.MergeLatestLocations(ctx, "dozer-01", []LatestLocationInput{{
		Latitude:     nf(41.87231954),
		Longitude:    nf(-87.62819473),
		Elevation:    nf(179.6231),
		Accuracy:     nf(3.8417),
		Speed:        nf(12.9053),
		Heading:      nf(217.6384),
		OccurredTime: t0,
	}}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := loadLocation(t, api, ctx, "dozer-01")
	for _, f := range []struct {
		name string
		got  sql.NullFloat64
		want float64
	}{
		{"latitude", got.Latitude, 41.87231954},
		{"longitude", got.Longitude, -87.62819473},
		{"elevation", got.Elevation, 179.6231},
		{"accuracy", got.Accuracy, 3.8417},
		{"speed", got.Speed, 12.9053},
		{"heading", got.Heading, 217.6384},
	} {
		if !f.got.Valid {
			t.Errorf("%s came back NULL, want %v", f.name, f.want)
			continue
		}
		if f.got.Float64 != f.want {
			t.Errorf("%s = %v, want %v", f.name, f.got.Float64, f.want)
		}
	}
	if !got.OccurredTime.Equal(t0) {
		t.Errorf("occurredTime = %v, want %v", got.OccurredTime, t0)
	}
	if got.DeviceToken != "dozer-01" {
		t.Errorf("deviceToken = %q, want %q", got.DeviceToken, "dozer-01")
	}
}

// TestLatestLocationUpsertsRatherThanInserts: a second fix REPLACES the row. If this
// appended instead, "where is it now?" would become a scan over a table that grows with
// history — the exact thing the projection exists to avoid — and First() would keep
// answering with the oldest fix.
func TestLatestLocationUpsertsRatherThanInserts(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	if err := api.MergeLatestLocations(ctx, "dozer-01", []LatestLocationInput{
		{Latitude: nf(41.87231954), Longitude: nf(-87.62819473), OccurredTime: t0},
	}); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if err := api.MergeLatestLocations(ctx, "dozer-01", []LatestLocationInput{
		{Latitude: nf(41.90118427), Longitude: nf(-87.61042956), OccurredTime: t0.Add(90 * time.Second)},
	}); err != nil {
		t.Fatalf("second merge: %v", err)
	}

	if n := countLocations(t, api, ctx, "dozer-01"); n != 1 {
		t.Fatalf("expected exactly 1 projection row per device, got %d", n)
	}
	got := loadLocation(t, api, ctx, "dozer-01")
	if got.Latitude.Float64 != 41.90118427 || got.Longitude.Float64 != -87.61042956 {
		t.Fatalf("row was not advanced to the second fix: %+v", got)
	}
}

// TestLatestLocationNeverGoesBackwards is the out-of-order / redelivery case, and it is
// the reason the merge compares occurred time rather than trusting arrival order.
//
// 🔴 This is not hypothetical. The resolved-events stream redelivers any message left
// unacked, and five merge workers drain it in parallel, so a fix that occurred EARLIER
// routinely arrives LATER. Under last-write-wins the projection would teleport the
// device back to a position it has already left — and nothing downstream could tell that
// from the device having genuinely returned there.
func TestLatestLocationNeverGoesBackwards(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	newer := LatestLocationInput{
		Latitude: nf(41.90118427), Longitude: nf(-87.61042956),
		Speed: nf(9.4271), OccurredTime: t0.Add(10 * time.Minute),
	}
	older := LatestLocationInput{
		Latitude: nf(41.87231954), Longitude: nf(-87.62819473),
		Speed: nf(0.7318), OccurredTime: t0,
	}

	if err := api.MergeLatestLocations(ctx, "dozer-01", []LatestLocationInput{newer}); err != nil {
		t.Fatalf("newer merge: %v", err)
	}
	// The older fix arrives second — a delayed or redelivered message.
	if err := api.MergeLatestLocations(ctx, "dozer-01", []LatestLocationInput{older}); err != nil {
		t.Fatalf("older merge: %v", err)
	}

	got := loadLocation(t, api, ctx, "dozer-01")
	if got.Latitude.Float64 != 41.90118427 || got.Longitude.Float64 != -87.61042956 {
		t.Fatalf("an OLDER fix overwrote a newer one: device rolled back to %v,%v",
			got.Latitude.Float64, got.Longitude.Float64)
	}
	if got.Speed.Float64 != 9.4271 {
		t.Fatalf("an older fix overwrote the newer speed: %v", got.Speed.Float64)
	}
	if !got.OccurredTime.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("an older fix rolled occurredTime back to %v", got.OccurredTime)
	}

	// An EXACT redelivery of the stored fix is a no-op too: the guard is strictly
	// newer, so an equal timestamp does not rewrite the row.
	if err := api.MergeLatestLocations(ctx, "dozer-01", []LatestLocationInput{newer}); err != nil {
		t.Fatalf("redelivery merge: %v", err)
	}
	if n := countLocations(t, api, ctx, "dozer-01"); n != 1 {
		t.Fatalf("redelivery produced %d rows, want 1", n)
	}

	// And the guard survives a batch that arrives newest-first WITHIN one event: the
	// second entry is older than the first, and must not win by being applied last.
	if err := api.MergeLatestLocations(ctx, "dozer-02", []LatestLocationInput{newer, older}); err != nil {
		t.Fatalf("out-of-order batch merge: %v", err)
	}
	if got := loadLocation(t, api, ctx, "dozer-02"); !got.OccurredTime.Equal(newer.OccurredTime) {
		t.Fatalf("an out-of-order batch left the OLDER entry winning: %v", got.OccurredTime)
	}
}

// TestLatestLocationNullOptionalsStayNull: a fix that reports only a position must leave
// the four optional columns NULL. Zero is a claim — sea level, perfect accuracy,
// stationary, due north — and a consumer cannot tell a stored 0 from a reported one.
func TestLatestLocationNullOptionalsStayNull(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	if err := api.MergeLatestLocations(ctx, "bare-fix", []LatestLocationInput{
		{Latitude: nf(41.87231954), Longitude: nf(-87.62819473), OccurredTime: t0},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := loadLocation(t, api, ctx, "bare-fix")
	for _, f := range []struct {
		name string
		got  sql.NullFloat64
	}{
		{"elevation", got.Elevation},
		{"accuracy", got.Accuracy},
		{"speed", got.Speed},
		{"heading", got.Heading},
	} {
		if f.got.Valid {
			t.Errorf("unreported %s stored as %v instead of NULL", f.name, f.got.Float64)
		}
	}

	// Read the raw column too, so the assertion does not rest solely on the driver's
	// scan: a stored 0.0 and a NULL are indistinguishable to anything that coerces.
	var raw sql.NullFloat64
	if err := api.RDB.DB(ctx).Raw(
		`select elevation from latest_locations where device_token = ?`, "bare-fix").Scan(&raw).Error; err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if raw.Valid {
		t.Fatalf("elevation column holds %v, want SQL NULL", raw.Float64)
	}

	// A later fix that DOES report elevation fills it in, so "stays NULL" is a property
	// of the absent value rather than of the column never being written.
	if err := api.MergeLatestLocations(ctx, "bare-fix", []LatestLocationInput{
		{Latitude: nf(41.87231954), Longitude: nf(-87.62819473), Elevation: nf(179.6231), OccurredTime: t0.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if got := loadLocation(t, api, ctx, "bare-fix"); !got.Elevation.Valid || got.Elevation.Float64 != 179.6231 {
		t.Fatalf("a reported elevation did not land: %+v", got.Elevation)
	}
}

// TestLatestLocationIsTenantScoped: two tenants reusing one device token (which ADR-042
// permits — a token is unique only within a tenant) keep separate positions, and neither
// merge is visible to the other.
func TestLatestLocationIsTenantScoped(t *testing.T) {
	api := newLocationTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	globex := core.WithTenant(context.Background(), "globex")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	if err := api.MergeLatestLocations(acme, "shared-token", []LatestLocationInput{
		{Latitude: nf(41.87231954), Longitude: nf(-87.62819473), OccurredTime: t0},
	}); err != nil {
		t.Fatalf("acme merge: %v", err)
	}
	// Globex's fix is OLDER than acme's. If tenant scoping leaked, the newer-wins guard
	// would silently discard it and this would look like a working merge.
	if err := api.MergeLatestLocations(globex, "shared-token", []LatestLocationInput{
		{Latitude: nf(51.50336218), Longitude: nf(-0.11957384), OccurredTime: t0.Add(-time.Hour)},
	}); err != nil {
		t.Fatalf("globex merge: %v", err)
	}

	if got := loadLocation(t, api, acme, "shared-token"); got.Latitude.Float64 != 41.87231954 {
		t.Fatalf("acme's position was overwritten across the tenant boundary: %+v", got)
	}
	if got := loadLocation(t, api, globex, "shared-token"); got.Latitude.Float64 != 51.50336218 {
		t.Fatalf("globex's position is wrong (tenant leak): %+v", got)
	}

	// The batch read is tenant-scoped as well.
	found, err := api.LatestLocationsByDeviceToken(acme, []string{"shared-token"})
	if err != nil {
		t.Fatalf("batch read: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("batch read returned %d rows across tenants, want 1", len(found))
	}
}

// TestLatestLocationsByDeviceTokenOmitsNeverLocated: a device with no fix is absent
// rather than present-with-nulls, so a caller can tell "never located" from "located".
func TestLatestLocationsByDeviceTokenOmitsNeverLocated(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	if err := api.MergeLatestLocations(ctx, "located-1", []LatestLocationInput{
		{Latitude: nf(41.87231954), Longitude: nf(-87.62819473), OccurredTime: t0},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	found, err := api.LatestLocationsByDeviceToken(ctx, []string{"located-1", "never-located"})
	if err != nil {
		t.Fatalf("batch read: %v", err)
	}
	if len(found) != 1 || found[0].DeviceToken != "located-1" {
		t.Fatalf("expected only the located device, got %d rows: %+v", len(found), found)
	}
}

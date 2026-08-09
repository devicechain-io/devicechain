// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newGeoFenceTestApi builds a sqlite-backed Api with the geofence tables and the
// device-type/profile chain ProfileScopeByDeviceType walks, so the stamp can be
// followed all the way from a fence write to what the resolve path would read.
func newGeoFenceTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&GeoFence{}, &GeoFenceSetVersion{}, &DeviceType{}, &DeviceProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// polygonGeometry renders a POLYGON_2D geometry document over the given ring, which is
// written as flat [lon, lat, lon, lat, …] pairs. The ring is closed by the caller — a
// helper that closed it silently would hide the closure check this package enforces.
func polygonGeometry(coords ...float64) string {
	positions := make([]string, 0, len(coords)/2)
	for i := 0; i+1 < len(coords); i += 2 {
		positions = append(positions, fmt.Sprintf("[%g,%g]", coords[i], coords[i+1]))
	}
	return `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[` +
		strings.Join(positions, ",") + `]]}}`
}

// yardGeometry is a small, deliberately NON-round, NON-symmetric quadrilateral. Distinct
// and irregular on purpose: a round square would let a coordinate mapping that swapped
// longitude and latitude, or that dropped a position, round-trip unnoticed.
const yardGeometry = `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
	`[[[-84.3881,33.7490],[-84.3875,33.7492],[-84.3872,33.7486],[-84.3879,33.7483],[-84.3881,33.7490]]]}}`

// decodePolygonRing pulls the exterior ring out of a stored geometry document so a test
// can compare coordinates as numbers rather than as bytes — a byte comparison would pass
// on a document that was stored verbatim but is not the polygon it claims to be.
func decodePolygonRing(t *testing.T, raw string) [][]float64 {
	t.Helper()
	var doc struct {
		Kind     string `json:"kind"`
		Geometry struct {
			Type        string        `json:"type"`
			Coordinates [][][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode geometry: %v", err)
	}
	if doc.Kind != GeoFenceKindPolygon2D {
		t.Fatalf("geometry kind = %q, want %q", doc.Kind, GeoFenceKindPolygon2D)
	}
	if doc.Geometry.Type != "Polygon" {
		t.Fatalf("geojson type = %q, want Polygon", doc.Geometry.Type)
	}
	if len(doc.Geometry.Coordinates) != 1 {
		t.Fatalf("ring count = %d, want 1", len(doc.Geometry.Coordinates))
	}
	return doc.Geometry.Coordinates[0]
}

// A fence survives create → read → update → delete with its geometry intact, compared
// position by position rather than as an opaque blob.
func TestGeoFenceRoundTrip(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	name, desc := "North Yard", "the gravel yard north of the shop"
	created, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "north-yard", Name: &name, Description: &desc, Geometry: yardGeometry,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	read, err := api.GeoFencesByToken(ctx, []string{"north-yard"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 1 {
		t.Fatalf("read %d fences, want 1", len(read))
	}
	if read[0].ID != created.ID {
		t.Errorf("read id = %d, want %d", read[0].ID, created.ID)
	}
	if read[0].Name.String != name || read[0].Description.String != desc {
		t.Errorf("read name/description = %q/%q, want %q/%q",
			read[0].Name.String, read[0].Description.String, name, desc)
	}
	if read[0].Kind() != GeoFenceKindPolygon2D {
		t.Errorf("read kind = %q, want %q", read[0].Kind(), GeoFenceKindPolygon2D)
	}

	// Field by field: every position of the stored ring must be exactly what was authored.
	want := [][]float64{
		{-84.3881, 33.7490}, {-84.3875, 33.7492}, {-84.3872, 33.7486},
		{-84.3879, 33.7483}, {-84.3881, 33.7490},
	}
	got := decodePolygonRing(t, string(read[0].Geometry))
	if len(got) != len(want) {
		t.Fatalf("ring has %d positions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			t.Errorf("position %d = [%v %v], want [%v %v]", i, got[i][0], got[i][1], want[i][0], want[i][1])
		}
	}

	// Update: a genuinely different quadrilateral, so the edit cannot pass by leaving
	// the original geometry in place.
	moved := polygonGeometry(-84.40, 33.75, -84.39, 33.76, -84.38, 33.74, -84.40, 33.75)
	renamed := "North Yard (resurveyed)"
	updated, err := api.UpdateGeoFence(ctx, "north-yard", &GeoFenceCreateRequest{
		Token: "north-yard", Name: &renamed, Geometry: moved,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name.String != renamed {
		t.Errorf("updated name = %q, want %q", updated.Name.String, renamed)
	}
	reread, err := api.GeoFencesByToken(ctx, []string{"north-yard"})
	if err != nil || len(reread) != 1 {
		t.Fatalf("re-read: %v (%d rows)", err, len(reread))
	}
	ring := decodePolygonRing(t, string(reread[0].Geometry))
	if len(ring) != 4 {
		t.Fatalf("updated ring has %d positions, want 4", len(ring))
	}
	if ring[1][0] != -84.39 || ring[1][1] != 33.76 {
		t.Errorf("updated ring position 1 = [%v %v], want [-84.39 33.76]", ring[1][0], ring[1][1])
	}

	// Delete, and the token is free again — hard delete is this area's uniform semantics.
	deleted, err := api.DeleteGeoFence(ctx, "north-yard")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("delete reported no row removed")
	}
	after, err := api.GeoFencesByToken(ctx, []string{"north-yard"})
	if err != nil {
		t.Fatalf("read after delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("fence survived delete: %+v", after)
	}
	// Deleting a token that names nothing changes nothing and says so.
	again, err := api.DeleteGeoFence(ctx, "north-yard")
	if err != nil || again {
		t.Errorf("second delete = (%v, %v), want (false, nil)", again, err)
	}
}

// 🔴 EVERY fence change mints a new fence-set version — create, edit AND delete — and
// one tenant's changes never move another tenant's version. This is the property the
// whole stamp rests on: a change that did not mint would leave events pointing at a
// snapshot that no longer describes the fences.
func TestGeoFenceChangeMintsNewFenceSetVersion(t *testing.T) {
	api := newGeoFenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "globex")

	version := func(t *testing.T, ctx context.Context) int32 {
		t.Helper()
		v, err := api.CurrentFenceSetVersion(ctx)
		if err != nil {
			t.Fatalf("current version: %v", err)
		}
		return v
	}

	// A tenant with no fence has never minted a version. 0 is a real answer, and it is
	// distinct from the non-zero version an emptied fence set sits at (asserted below).
	if v := version(t, acme); v != 0 {
		t.Fatalf("initial version = %d, want 0", v)
	}

	// Seed the other tenant so it has a version of its own that must not move.
	if _, err := api.CreateGeoFence(other, &GeoFenceCreateRequest{Token: "their-yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("seed other tenant: %v", err)
	}
	otherBefore := version(t, other)
	if otherBefore == 0 {
		t.Fatal("the other tenant's create minted no version")
	}

	// CREATE mints.
	if _, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	afterCreate := version(t, acme)
	if afterCreate == 0 {
		t.Fatal("create minted no version")
	}

	// EDIT mints.
	edited := polygonGeometry(-84.40, 33.75, -84.39, 33.76, -84.38, 33.74, -84.40, 33.75)
	if _, err := api.UpdateGeoFence(acme, "yard", &GeoFenceCreateRequest{Token: "yard", Geometry: edited}); err != nil {
		t.Fatalf("update: %v", err)
	}
	afterEdit := version(t, acme)
	if afterEdit == afterCreate {
		t.Errorf("edit did not mint a new version (still %d)", afterEdit)
	}

	// DELETE mints — the case most easily forgotten, and the one where a stale stamp
	// would keep a removed fence live for every event resolved afterwards.
	deleted, err := api.DeleteGeoFence(acme, "yard")
	if err != nil || !deleted {
		t.Fatalf("delete = (%v, %v)", deleted, err)
	}
	afterDelete := version(t, acme)
	if afterDelete == afterEdit {
		t.Errorf("delete did not mint a new version (still %d)", afterDelete)
	}
	// And an emptied fence set is NOT back at 0 — "never had a fence" and "had fences,
	// now has none" stay distinguishable.
	if afterDelete == 0 {
		t.Error("an emptied fence set fell back to version 0")
	}

	// Three changes, three strictly increasing versions.
	if !(afterCreate < afterEdit && afterEdit < afterDelete) {
		t.Errorf("versions not strictly increasing: create=%d edit=%d delete=%d",
			afterCreate, afterEdit, afterDelete)
	}

	// The other tenant sat still through all of it.
	if got := version(t, other); got != otherBefore {
		t.Errorf("the other tenant's version moved from %d to %d", otherBefore, got)
	}
}

// The snapshot frozen at a version holds the geometry AS IT WAS, so previewing a rule
// against last week's events evaluates them against last week's fences. This also pins
// the mint ORDER: minting before the mutation would file the pre-change fence set under
// the post-change version number, which every "the number went up" assertion would miss.
func TestGeoFenceSetSnapshotFreezesGeometry(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	original, err := api.CurrentFenceSetVersion(ctx)
	if err != nil {
		t.Fatalf("current version: %v", err)
	}

	moved := polygonGeometry(-10, 10, -11, 11, -12, 10, -10, 10)
	if _, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{Token: "yard", Geometry: moved}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// The ORIGINAL version still describes the original polygon.
	snap, err := api.GeoFenceSetSnapshotAt(ctx, original)
	if err != nil {
		t.Fatalf("snapshot at %d: %v", original, err)
	}
	if len(snap.Fences) != 1 || snap.Fences[0].Token != "yard" {
		t.Fatalf("snapshot at %d = %+v, want one fence 'yard'", original, snap.Fences)
	}
	ring := decodePolygonRing(t, string(snap.Fences[0].Geometry))
	if ring[0][0] != -84.3881 || ring[0][1] != 33.7490 {
		t.Errorf("historical snapshot carries the EDITED geometry (%v); the fence set at a "+
			"version must be the one that was live then", ring[0])
	}

	// The CURRENT version describes the edited polygon.
	current, err := api.CurrentFenceSetVersion(ctx)
	if err != nil {
		t.Fatalf("current version: %v", err)
	}
	nowSnap, err := api.GeoFenceSetSnapshotAt(ctx, current)
	if err != nil {
		t.Fatalf("snapshot at %d: %v", current, err)
	}
	nowRing := decodePolygonRing(t, string(nowSnap.Fences[0].Geometry))
	if nowRing[0][0] != -10 || nowRing[0][1] != 10 {
		t.Errorf("current snapshot did not follow the edit: %v", nowRing[0])
	}
}

// 🔴 The resolve path reads the fence-set version through ProfileScopeByDeviceType, so
// this is the seam between "a fence changed" and "a location event gets a new stamp".
// Without it the processor-level stamp test would be asserting against a fiction.
func TestProfileScopeCarriesCurrentFenceSetVersion(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	dt := &DeviceType{}
	dt.Token = "excavator"
	if err := api.RDB.DB(ctx).Create(dt).Error; err != nil {
		t.Fatalf("seed device type: %v", err)
	}

	// No fences yet: the scope resolves, and its fence version is 0.
	scope, err := api.ProfileScopeByDeviceType(ctx, dt.ID)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if scope.DeviceTypeToken != "excavator" {
		t.Fatalf("scope device type = %q, want excavator", scope.DeviceTypeToken)
	}
	if scope.FenceSetVersion != 0 {
		t.Fatalf("fence set version = %d before any fence, want 0", scope.FenceSetVersion)
	}

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	afterCreate, err := api.ProfileScopeByDeviceType(ctx, dt.ID)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if afterCreate.FenceSetVersion == 0 {
		t.Fatal("the scope still reports version 0 after a fence was created")
	}

	edited := polygonGeometry(-10, 10, -11, 11, -12, 10, -10, 10)
	if _, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{Token: "yard", Geometry: edited}); err != nil {
		t.Fatalf("update: %v", err)
	}
	afterEdit, err := api.ProfileScopeByDeviceType(ctx, dt.ID)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if afterEdit.FenceSetVersion == afterCreate.FenceSetVersion {
		t.Errorf("the scope did not follow the fence edit (still %d)", afterEdit.FenceSetVersion)
	}

	// A device whose TYPE is unknown still gets the tenant's real fence version — it
	// reports positions like any other, and stamping 0 would claim the tenant has no
	// fences.
	orphan, err := api.ProfileScopeByDeviceType(ctx, 999999)
	if err != nil {
		t.Fatalf("orphan scope: %v", err)
	}
	if orphan.FenceSetVersion != afterEdit.FenceSetVersion {
		t.Errorf("orphan-type scope fence version = %d, want %d",
			orphan.FenceSetVersion, afterEdit.FenceSetVersion)
	}
}

// Every fence mutation drops the caches holding the fence-set version, and a mutation
// that changed nothing does not. The version rides in the cached ProfileScope, so a
// missed eviction keeps stamping the previous version for a whole TTL.
func TestGeoFenceMutationsEvictFenceSetVersion(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ev := &captureEvictor{}
	api.CacheEvictor = ev
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if ev.fenceSetEvicts != 1 {
		t.Errorf("evictions after create = %d, want 1", ev.fenceSetEvicts)
	}
	edited := polygonGeometry(-10, 10, -11, 11, -12, 10, -10, 10)
	if _, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{Token: "yard", Geometry: edited}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if ev.fenceSetEvicts != 2 {
		t.Errorf("evictions after update = %d, want 2", ev.fenceSetEvicts)
	}
	if _, err := api.DeleteGeoFence(ctx, "yard"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ev.fenceSetEvicts != 3 {
		t.Errorf("evictions after delete = %d, want 3", ev.fenceSetEvicts)
	}
	// A delete that removed nothing minted nothing, so there is nothing to evict.
	if _, err := api.DeleteGeoFence(ctx, "yard"); err != nil {
		t.Fatalf("no-op delete: %v", err)
	}
	if ev.fenceSetEvicts != 3 {
		t.Errorf("a no-op delete evicted (count now %d, want 3)", ev.fenceSetEvicts)
	}
}

// The vertex bound is enforced at authoring, on create AND on update — and a legal
// fence is still accepted. The acceptance half is the counterweight: a limit that
// rejected everything would satisfy every rejection case here.
func TestGeoFenceVertexBound(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	// A closed ring of exactly n positions, sitting on a circle so no two coincide.
	ring := func(n int) string {
		coords := make([]float64, 0, n*2)
		for i := 0; i < n-1; i++ {
			lon := -84.0 + float64(i)*0.0001
			lat := 33.0 + float64(i%7)*0.0001
			coords = append(coords, lon, lat)
		}
		coords = append(coords, -84.0, 33.0) // close on the first position
		return polygonGeometry(coords...)
	}

	// AT the limit: accepted. This is what stops the rejections below from being
	// satisfied by a validator that says no to everything.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "at-limit", Geometry: ring(MaxGeoFenceVertices),
	}); err != nil {
		t.Fatalf("a fence at exactly the %d-position limit was rejected: %v", MaxGeoFenceVertices, err)
	}
	// A small, ordinary fence: also accepted.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "ordinary", Geometry: yardGeometry,
	}); err != nil {
		t.Fatalf("an ordinary 5-position fence was rejected: %v", err)
	}

	// ONE over the limit: refused on create.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "over-limit", Geometry: ring(MaxGeoFenceVertices + 1),
	}); err == nil {
		t.Errorf("a fence with %d positions was accepted; the limit is %d",
			MaxGeoFenceVertices+1, MaxGeoFenceVertices)
	}
	// And refused on update, so the bound cannot be walked around by editing a legal
	// fence into an illegal one.
	if _, err := api.UpdateGeoFence(ctx, "ordinary", &GeoFenceCreateRequest{
		Token: "ordinary", Geometry: ring(MaxGeoFenceVertices + 1),
	}); err == nil {
		t.Errorf("an update to %d positions was accepted; the limit is %d",
			MaxGeoFenceVertices+1, MaxGeoFenceVertices)
	}
	// The over-limit update must not have landed.
	after, err := api.GeoFencesByToken(ctx, []string{"ordinary"})
	if err != nil || len(after) != 1 {
		t.Fatalf("re-read: %v (%d rows)", err, len(after))
	}
	if len(decodePolygonRing(t, string(after[0].Geometry))) != 5 {
		t.Error("the rejected update changed the stored geometry")
	}
}

// The fences-per-tenant bound is enforced at authoring, and one tenant filling up does
// not block another. Everything up to the limit is accepted — the counterweight again.
func TestGeoFencesPerTenantBound(t *testing.T) {
	api := newGeoFenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "globex")

	for i := 0; i < MaxGeoFencesPerTenant; i++ {
		if _, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{
			Token: fmt.Sprintf("fence-%d", i), Geometry: yardGeometry,
		}); err != nil {
			t.Fatalf("fence %d of %d rejected below the limit: %v", i+1, MaxGeoFencesPerTenant, err)
		}
	}
	if _, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{
		Token: "one-too-many", Geometry: yardGeometry,
	}); err == nil {
		t.Errorf("fence %d was accepted; the limit is %d", MaxGeoFencesPerTenant+1, MaxGeoFencesPerTenant)
	}
	// The bound is per tenant, not global.
	if _, err := api.CreateGeoFence(other, &GeoFenceCreateRequest{
		Token: "their-first", Geometry: yardGeometry,
	}); err != nil {
		t.Errorf("another tenant's first fence was refused because acme is full: %v", err)
	}
	// Deleting frees a slot, so the bound is a live count and not a high-water mark.
	if _, err := api.DeleteGeoFence(acme, "fence-0"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{
		Token: "replacement", Geometry: yardGeometry,
	}); err != nil {
		t.Errorf("a fence was refused after a delete freed a slot: %v", err)
	}
}

// One tenant's fences are invisible to another, through every read door.
func TestGeoFenceTenantIsolation(t *testing.T) {
	api := newGeoFenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "globex")

	mine, err := api.CreateGeoFence(acme, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if found, err := api.GeoFencesByToken(other, []string{"yard"}); err != nil || len(found) != 0 {
		t.Errorf("by-token leaked across tenants: %v (%d rows)", err, len(found))
	}
	if found, err := api.GeoFencesById(other, []uint{mine.ID}); err != nil || len(found) != 0 {
		t.Errorf("by-id leaked across tenants: %v (%d rows)", err, len(found))
	}
	results, err := api.GeoFences(other, GeoFenceSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results.Results) != 0 {
		t.Errorf("list leaked %d fences across tenants", len(results.Results))
	}
	// The owning tenant does see it, so the assertions above are not passing because
	// every read returns nothing.
	if found, err := api.GeoFencesByToken(acme, []string{"yard"}); err != nil || len(found) != 1 {
		t.Errorf("the owning tenant cannot read its own fence: %v (%d rows)", err, len(found))
	}
	// The other tenant cannot delete it either.
	if deleted, err := api.DeleteGeoFence(other, "yard"); err != nil || deleted {
		t.Errorf("cross-tenant delete = (%v, %v), want (false, nil)", deleted, err)
	}
	if found, _ := api.GeoFencesByToken(acme, []string{"yard"}); len(found) != 1 {
		t.Error("the fence was removed by another tenant's delete")
	}
}

// Geometry validation: the malformed and out-of-contract documents are refused, the
// well-formed one is accepted, and the RESERVED kinds are refused rather than stored.
// Storing a reserved kind would let a rule name a fence whose containment nothing can
// evaluate — a rule that silently never fires, which is worse than a rejected write.
func TestValidateGeoFenceGeometry(t *testing.T) {
	closed := `[[-84.0,33.0],[-84.1,33.1],[-84.2,33.0],[-84.0,33.0]]`
	cases := []struct {
		name  string
		doc   string
		valid bool
	}{
		{"valid polygon", yardGeometry, true},
		{"valid with hole", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[` +
			closed + `,[[-84.05,33.02],[-84.06,33.03],[-84.07,33.02],[-84.05,33.02]]]}}`, true},
		{"empty", "", false},
		{"malformed json", `{"kind":`, false},
		{"array", `[{"kind":"POLYGON_2D"}]`, false},
		{"literal null", `null`, false},
		{"no kind", `{"geometry":{"type":"Polygon","coordinates":[` + closed + `]}}`, false},
		{"unknown kind", `{"kind":"HEXAGON","geometry":{"type":"Polygon","coordinates":[` + closed + `]}}`, false},
		{"reserved 2.5D", `{"kind":"POLYGON_2_5D","geometry":{"type":"Polygon","coordinates":[` + closed + `]}}`, false},
		{"reserved 3D", `{"kind":"VOXEL_3D","geometry":{"type":"Polygon","coordinates":[` + closed + `]}}`, false},
		{"no geometry", `{"kind":"POLYGON_2D"}`, false},
		{"wrong geojson type", `{"kind":"POLYGON_2D","geometry":{"type":"Point","coordinates":[-84.0,33.0]}}`, false},
		{"no rings", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[]}}`, false},
		{"ring too short", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
			`[[[-84.0,33.0],[-84.1,33.1],[-84.0,33.0]]]}}`, false},
		{"ring not closed", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
			`[[[-84.0,33.0],[-84.1,33.1],[-84.2,33.0],[-84.3,33.2]]]}}`, false},
		{"longitude out of range", polygonGeometry(-181, 33, -84.1, 33.1, -84.2, 33.0, -181, 33), false},
		{"latitude out of range", polygonGeometry(-84.0, 91, -84.1, 33.1, -84.2, 33.0, -84.0, 91), false},
		{"one-dimensional position", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
			`[[[-84.0],[-84.1,33.1],[-84.2,33.0],[-84.0]]]}}`, false},
	}
	for _, tc := range cases {
		_, err := validateGeoFenceGeometry(tc.doc)
		if tc.valid && err != nil {
			t.Errorf("%s: rejected a valid geometry: %v", tc.name, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("%s: accepted an invalid geometry", tc.name)
		}
	}
}

// A RESERVED kind is refused as reserved, not as unknown — and the difference is in the
// message an author reads. "reserved but not yet supported" says the name is right and
// the engine is not ready; "unsupported" says the name is wrong. Telling an author to fix
// a typo they did not make is the failure this pins.
//
// It also stops the reserved-kind cases in the table above from being satisfied by the
// default branch: with only a valid/invalid assertion, deleting the reserved branch
// entirely changes nothing observable, so the branch would be untested.
func TestReservedGeometryKindsAreRefusedAsReserved(t *testing.T) {
	closed := `[[-84.0,33.0],[-84.1,33.1],[-84.2,33.0],[-84.0,33.0]]`
	doc := func(kind string) string {
		return `{"kind":"` + kind + `","geometry":{"type":"Polygon","coordinates":[` + closed + `]}}`
	}
	for _, kind := range []string{GeoFenceKindPolygon25D, GeoFenceKindVoxel3D} {
		_, err := validateGeoFenceGeometry(doc(kind))
		if err == nil {
			t.Errorf("%s: a reserved kind was accepted; a rule could then name a fence whose "+
				"containment nothing can evaluate", kind)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: rejected as %q; a reserved kind must say it is reserved, not that it "+
				"is unknown", kind, err)
		}
	}
	// The counterweight: a genuinely unknown kind is NOT described as reserved.
	_, err := validateGeoFenceGeometry(doc("HEXAGON"))
	if err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if strings.Contains(err.Error(), "reserved") {
		t.Errorf("an unknown kind was reported as reserved: %v", err)
	}
}

// A geofence token must satisfy the platform token grammar: a rule names a fence by it,
// so it ends up inside a compiled rule body carried on a per-tenant subject.
func TestGeoFenceTokenValidation(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, bad := range []string{"", "with space", "with/slash", "with.dot"} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: bad, Geometry: yardGeometry}); err == nil {
			t.Errorf("token %q was accepted", bad)
		}
	}
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "north-yard-1", Geometry: yardGeometry}); err != nil {
		t.Errorf("a legal token was rejected: %v", err)
	}
}

// GeoFenceSetSnapshotAt distinguishes "before any fence existed" from "a version that
// is not on record". Answering an empty snapshot for an unknown version would read as
// "there were no fences" when the truth is "we cannot know".
func TestGeoFenceSetSnapshotAtUnknownVersion(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	snap, err := api.GeoFenceSetSnapshotAt(ctx, 0)
	if err != nil {
		t.Fatalf("snapshot at 0: %v", err)
	}
	if len(snap.Fences) != 0 {
		t.Errorf("snapshot at 0 has %d fences, want 0", len(snap.Fences))
	}
	if _, err := api.GeoFenceSetSnapshotAt(ctx, 42); err == nil {
		t.Error("an unknown fence-set version answered a snapshot instead of erroring")
	}
}

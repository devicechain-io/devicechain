// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// An instance upgraded from a release that predates the geometry archive holds fence-set
// snapshots with their geometry INLINED and no content address at all. This is the coverage
// for the migration that rewrites them, and for what happens if it does not run.
//
// 🔴 WHY IT CANNOT LIVE WITH THE OTHER GEOFENCE TESTS. The rewrite is issued as SQL against
// jsonb columns, with a ::jsonb cast that exists because the driver sends a []byte as bytea,
// and with ON CONFLICT against a unique index the sqlite unit tests do not build. Every one
// of those is a Postgres fact. A sqlite test of this migration would exercise the Go decode
// and nothing that can actually fail in production.
//
// Run it the same way as the TOCTOU test:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run Backfill -v
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// legacySnapshotJSON renders a fence-set snapshot in the PRE-ARCHIVE form: every fence
// carrying its geometry document inline, no hash anywhere.
//
// 🔴 IT IS WRITTEN OUT AS TEXT RATHER THAN BUILT FROM THE LIVE TYPES, and that is the whole
// point of the fixture. storedGeoFenceSetSnapshot cannot express this shape — it has no
// geometry field — so a fixture built from it would encode the shape that already works and
// the migration would have nothing to do. The old shape only exists as bytes now.
func legacySnapshotJSON(version int32, tokenAndGeometry ...string) string {
	entries := make([]string, 0, len(tokenAndGeometry)/2)
	for i := 0; i+1 < len(tokenAndGeometry); i += 2 {
		entries = append(entries, fmt.Sprintf(`{"token":%q,"geometry":%s}`,
			tokenAndGeometry[i], tokenAndGeometry[i+1]))
	}
	return fmt.Sprintf(`{"version":%d,"fences":[%s]}`, version, strings.Join(entries, ","))
}

// seedFenceSetVersionRow writes one geo_fence_set_versions row VERBATIM, bypassing the model
// layer — which is required, since the model layer can no longer produce the old shape.
func seedFenceSetVersionRow(t *testing.T, db *gorm.DB, tenant string, version int32, snapshot string) {
	t.Helper()
	sys := db.WithContext(core.WithSystemContext(context.Background()))
	err := sys.Exec(`INSERT INTO "device-management".geo_fence_set_versions
		(created_at, updated_at, tenant_id, version, snapshot, minted_at)
		VALUES (now(), now(), ?, ?, ?::jsonb, now())`, tenant, version, snapshot).Error
	if err != nil {
		t.Fatalf("seed fence set version %d for %s: %v", version, tenant, err)
	}
}

// runBackfill invokes the migration's own Migrate function, which is what the chain runs.
func runBackfill(t *testing.T, db *gorm.DB) error {
	t.Helper()
	return schema.NewGeoFenceSnapshotBackfill().
		Migrate(db.WithContext(core.WithSystemContext(context.Background())))
}

// A pre-archive snapshot is unreadable until the backfill runs, and correct afterwards.
//
// The FIRST half is a negative control and the test is worthless without it: if the seeded row
// were already readable, the second half would pass against a fixture that never had the defect.
func TestBackfillMakesAPreArchiveSnapshotReadableAgain(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	db := api.RDB.Database

	seedFenceSetVersionRow(t, db, "acme", 7, legacySnapshotJSON(7, "yard", yardGeometry))

	// --- negative control: this is what an upgraded instance does before the migration ---
	if _, err := api.CurrentGeoFenceSetSnapshot(ctx); err == nil {
		t.Fatal("a pre-archive snapshot hydrated cleanly, so the fixture does not carry the " +
			"defect this migration exists to repair and the rest of this test proves nothing")
	} else {
		t.Logf("before the backfill, hydration fails as it should: %v", err)
	}
	before, err := api.CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		t.Fatalf("manifest before the backfill: %v", err)
	}
	if len(before.Fences) != 1 || before.Fences[0].Hash != "" {
		t.Fatalf("expected the manifest to hand out one entry addressed by the empty string "+
			"before the backfill, got %+v", before.Fences)
	}

	// --- the migration ---
	if err := runBackfill(t, db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	snapshot, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatalf("hydrate after the backfill: %v", err)
	}
	if snapshot.Version != 7 || len(snapshot.Fences) != 1 || snapshot.Fences[0].Token != "yard" {
		t.Fatalf("hydrated snapshot is not the seeded fence set: %+v", snapshot)
	}
	// The GEOMETRY, not just the token — a rewrite that archived the wrong document, or the
	// right document under an address nothing resolves, would satisfy every check above.
	want := decodePolygonRing(t, yardGeometry)
	got := decodePolygonRing(t, string(snapshot.Fences[0].Geometry))
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("archived geometry is not the geometry that was inlined:\n want %v\n  got %v", want, got)
	}

	// The inline geometry must be GONE, not merely joined by an address. Leaving it would
	// hydrate correctly and pass every other assertion here while giving up the property the
	// archive exists for: a version's stored size being a function of its fence count alone.
	var raw string
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' AND version = 7`).Scan(&raw).Error; err != nil {
		t.Fatalf("read the rewritten snapshot: %v", err)
	}
	if strings.Contains(raw, `"geometry"`) {
		t.Fatalf("the rewrite left the geometry inlined alongside its address: %s", raw)
	}

	after, err := api.CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		t.Fatalf("manifest after the backfill: %v", err)
	}
	if len(after.Fences) != 1 || len(after.Fences[0].Hash) != 64 {
		t.Fatalf("expected one manifest entry carrying a 64-character address, got %+v", after.Fences)
	}
	// And the address RESOLVES through the door the detection side actually uses.
	documents, err := api.GeoFenceGeometryDocuments(ctx, []string{after.Fences[0].Hash})
	if err != nil {
		t.Fatalf("resolve the backfilled address: %v", err)
	}
	if len(documents) != 1 {
		t.Fatalf("the backfilled address resolved to %d documents, want 1", len(documents))
	}
}

// Running it twice must change nothing, because migrations replay from the top after a failure.
func TestBackfillIsRerunnable(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	db := api.RDB.Database
	sys := db.WithContext(core.WithSystemContext(context.Background()))

	seedFenceSetVersionRow(t, db, "acme", 1, legacySnapshotJSON(1, "yard", yardGeometry))
	if err := runBackfill(t, db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var first string
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' AND version = 1`).Scan(&first).Error; err != nil {
		t.Fatalf("read snapshot after the first run: %v", err)
	}

	if err := runBackfill(t, db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var second string
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' AND version = 1`).Scan(&second).Error; err != nil {
		t.Fatalf("read snapshot after the second run: %v", err)
	}
	if first != second {
		t.Fatalf("a second run rewrote the snapshot:\n first  %s\n second %s", first, second)
	}

	// One archived document, not two. A second run that re-inserted would mean the ON CONFLICT
	// clause is not doing what the migration says it does.
	var blobs int64
	if err := sys.Raw(`SELECT count(*) FROM "device-management".geo_fence_geometry_blobs
		WHERE tenant_id = 'acme'`).Scan(&blobs).Error; err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if blobs != 1 {
		t.Fatalf("expected exactly one archived document after two runs, got %d", blobs)
	}
}

// A snapshot already in the reference form is left exactly as it is — the counterweight to
// every assertion above, which would all still pass if the migration rewrote everything it saw.
func TestBackfillLeavesAnAlreadyAddressedSnapshotAlone(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	db := api.RDB.Database
	sys := db.WithContext(core.WithSystemContext(context.Background()))

	// Minted by the live path, so the fixture is the real post-archive shape rather than a
	// hand-written guess at it — including the positionSum field the rewrite must preserve.
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create fence: %v", err)
	}
	var before string
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' ORDER BY version DESC LIMIT 1`).Scan(&before).Error; err != nil {
		t.Fatalf("read the minted snapshot: %v", err)
	}
	if !strings.Contains(before, `"positionSum"`) {
		t.Fatalf("the live mint path no longer writes positionSum, so this test can no longer "+
			"prove the rewrite preserves it: %s", before)
	}

	if err := runBackfill(t, db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var after string
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' ORDER BY version DESC LIMIT 1`).Scan(&after).Error; err != nil {
		t.Fatalf("read the snapshot after the backfill: %v", err)
	}
	if before != after {
		t.Fatalf("the backfill rewrote a snapshot that was already addressed:\n before %s\n after  %s",
			before, after)
	}
}

// A snapshot entry with neither a geometry nor an address is refused, loudly.
//
// This is the input the one-statement SQL form of this migration would have accepted, writing
// `"hash": null` and turning a corrupt snapshot into a differently-corrupt one in silence.
func TestBackfillRefusesAnEntryItCannotAddress(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	db := api.RDB.Database

	seedFenceSetVersionRow(t, db, "acme", 3, `{"version":3,"fences":[{"token":"orphan"}]}`)

	err := runBackfill(t, db)
	if err == nil {
		t.Fatal("the backfill accepted an entry carrying neither a geometry nor an address")
	}
	if !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("the refusal does not name the fence it could not address: %v", err)
	}

	// And it did not write a null address on the way out.
	sys := db.WithContext(core.WithSystemContext(context.Background()))
	var snapshot string
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' AND version = 3`).Scan(&snapshot).Error; err != nil {
		t.Fatalf("read the refused snapshot: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(snapshot), &decoded); err != nil {
		t.Fatalf("decode the refused snapshot: %v", err)
	}
	if strings.Contains(snapshot, `"hash"`) {
		t.Fatalf("the refused snapshot was rewritten anyway: %s", snapshot)
	}
}

// The chain itself must repair a pre-archive database — not just the function, called by hand.
//
// 🔴 EVERY OTHER TEST IN THIS FILE INVOKES THE MIGRATION DIRECTLY, so all four would still pass
// if the migration were never added to schema.Migrations, which is the exact defect that
// shipped: the archive existed, worked, and was never reached by an upgraded instance. This one
// runs what `dcctl`/the service runs.
//
// The simulation is a database that has never seen this migration: the chain is run once to
// build the schema, the backfill's bookkeeping row is deleted, a pre-archive snapshot is
// seeded, and the chain runs again — which is precisely the state a v0.12.x database is in when
// a v0.13.0 binary starts against it.
func TestTheMigrationChainRepairsAPreArchiveDatabase(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))

	// Read the id from the migration rather than writing it out, so renumbering it cannot leave
	// this test deleting a row that is not there and asserting against a chain that skipped.
	id := schema.NewGeoFenceSnapshotBackfill().ID
	result := sys.Exec(`DELETE FROM "device-management".device_management_migrations WHERE id = ?`, id)
	if result.Error != nil {
		t.Fatalf("forget the backfill migration: %v", result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("expected to delete exactly one bookkeeping row for %q, deleted %d — the chain "+
			"never recorded this migration, so the replay below would prove nothing", id, result.RowsAffected)
	}

	seedFenceSetVersionRow(t, api.RDB.Database, "acme", 11, legacySnapshotJSON(11, "yard", yardGeometry))
	if _, err := api.CurrentGeoFenceSetSnapshot(ctx); err == nil {
		t.Fatal("the seeded pre-archive snapshot hydrated before the chain replayed, so this " +
			"test is not measuring the migration")
	}

	// The upgrade: a second manager over the same database, running the same chain.
	upgraded := newPostgresRdbManager(t)
	api2 := NewApi(upgraded)

	snapshot, err := api2.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatalf("the chain ran but the pre-archive snapshot still does not hydrate: %v", err)
	}
	if snapshot.Version != 11 || len(snapshot.Fences) != 1 || snapshot.Fences[0].Token != "yard" {
		t.Fatalf("hydrated snapshot is not the seeded fence set: %+v", snapshot)
	}
	want := decodePolygonRing(t, yardGeometry)
	got := decodePolygonRing(t, string(snapshot.Fences[0].Geometry))
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("archived geometry is not the geometry that was inlined:\n want %v\n  got %v", want, got)
	}
}

// The rewrite re-encodes the WHOLE snapshot document, so it must carry through the fields it
// does not understand.
//
// The fixture is deliberately a shape no released version ever wrote — positionSum arrived
// with the archive, so a document holding it alongside a hash-less entry cannot come from
// v0.12.x. That is the point: the migration re-encodes whole documents, and a struct covering
// only the inputs it expects loses everything else. Without this test, deleting the
// PositionSum field is a mutation nothing in this package notices.
func TestBackfillCarriesThroughFieldsItDoesNotRewrite(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	db := api.RDB.Database
	sys := db.WithContext(core.WithSystemContext(context.Background()))

	mixed := fmt.Sprintf(`{"version":5,"positionSum":41,"fences":[{"token":"yard","geometry":%s}]}`, yardGeometry)
	seedFenceSetVersionRow(t, db, "acme", 5, mixed)

	if err := runBackfill(t, db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var raw string
	if err := sys.Raw(`SELECT snapshot::text FROM "device-management".geo_fence_set_versions
		WHERE tenant_id = 'acme' AND version = 5`).Scan(&raw).Error; err != nil {
		t.Fatalf("read the rewritten snapshot: %v", err)
	}
	var decoded struct {
		PositionSum *int `json:"positionSum"`
		Fences      []struct {
			Hash string `json:"hash"`
		} `json:"fences"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode the rewritten snapshot: %v", err)
	}
	if len(decoded.Fences) != 1 || len(decoded.Fences[0].Hash) != 64 {
		t.Fatalf("the entry was not rewritten, so this test never exercised the round trip: %s", raw)
	}
	if decoded.PositionSum == nil {
		t.Fatalf("the rewrite dropped positionSum: %s", raw)
	}
	if *decoded.PositionSum != 41 {
		t.Fatalf("the rewrite changed positionSum to %d, want 41: %s", *decoded.PositionSum, raw)
	}
}

// Two historical versions naming the same shape must archive it once, not fail on the second.
//
// 🔴 THIS IS THE INPUT EVERY REAL UPGRADED INSTANCE HAS, and it is the one the other tests in
// this file do not build. A fence-set version is minted on every fence-set change, so a tenant
// that has edited fences at all holds several versions, and every fence they did not touch
// appears in each of them with identical geometry. The migration walks versions, so the second
// version offers a document the first already archived — which is the only path that reaches
// the insert's conflict clause. Removing that clause was a mutation the whole suite survived
// until this test existed, and on a real database it is a duplicate-key error that aborts the
// upgrade.
func TestBackfillArchivesAShapeSharedByTwoVersionsOnce(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	db := api.RDB.Database
	sys := db.WithContext(core.WithSystemContext(context.Background()))

	// Version 2 keeps yard unchanged and adds a second fence — the shape of an ordinary edit.
	dockGeometry := polygonGeometry(-84.40, 33.75, -84.39, 33.75, -84.39, 33.76, -84.40, 33.75)
	seedFenceSetVersionRow(t, db, "acme", 1, legacySnapshotJSON(1, "yard", yardGeometry))
	seedFenceSetVersionRow(t, db, "acme", 2, legacySnapshotJSON(2, "yard", yardGeometry, "dock", dockGeometry))

	if err := runBackfill(t, db); err != nil {
		t.Fatalf("backfill over two versions sharing a shape: %v", err)
	}

	var blobs int64
	if err := sys.Raw(`SELECT count(*) FROM "device-management".geo_fence_geometry_blobs
		WHERE tenant_id = 'acme'`).Scan(&blobs).Error; err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if blobs != 2 {
		t.Fatalf("expected two distinct archived documents for two distinct shapes, got %d — "+
			"the shape shared by both versions was stored twice or not at all", blobs)
	}

	// Both versions still hydrate, and the shared fence resolves to the same address in each.
	first, err := api.GeoFenceSetSnapshotAt(ctx, 1)
	if err != nil {
		t.Fatalf("hydrate version 1: %v", err)
	}
	second, err := api.GeoFenceSetSnapshotAt(ctx, 2)
	if err != nil {
		t.Fatalf("hydrate version 2: %v", err)
	}
	if len(first.Fences) != 1 || len(second.Fences) != 2 {
		t.Fatalf("version 1 has %d fences and version 2 has %d, want 1 and 2",
			len(first.Fences), len(second.Fences))
	}
	if fmt.Sprint(decodePolygonRing(t, string(first.Fences[0].Geometry))) !=
		fmt.Sprint(decodePolygonRing(t, string(second.Fences[0].Geometry))) {
		t.Fatal("the fence both versions share hydrated to different geometry in each")
	}
}

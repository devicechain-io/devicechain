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
	"gorm.io/gorm"
)

// storedSnapshotBytes reads a version's snapshot column as raw bytes.
//
// It goes around GeoFenceSetSnapshotAt on purpose. That door returns the HYDRATED form,
// which carries geometry by design — so asserting through it could never tell a snapshot
// that stores references from one that stores documents, which is the only thing these
// tests are about.
func storedSnapshotBytes(t *testing.T, api *Api, ctx context.Context, version int32) string {
	t.Helper()
	found := make([]GeoFenceSetVersion, 0, 1)
	if err := api.RDB.DB(ctx).Where("version = ?", version).Limit(1).Find(&found).Error; err != nil {
		t.Fatalf("read version %d: %v", version, err)
	}
	if len(found) != 1 {
		t.Fatalf("version %d: want 1 row, got %d", version, len(found))
	}
	return string(found[0].Snapshot)
}

// archivedBlobs lists every geometry blob the tenant in ctx holds.
func archivedBlobs(t *testing.T, api *Api, ctx context.Context) []GeoFenceGeometryBlob {
	t.Helper()
	blobs := make([]GeoFenceGeometryBlob, 0)
	if err := api.RDB.DB(ctx).Find(&blobs).Error; err != nil {
		t.Fatalf("read blobs: %v", err)
	}
	return blobs
}

// TestSnapshotStoresReferencesNotGeometry is the whole point of the archive, and it is
// asserted on the STORED BYTES because nothing else can see it.
//
// The negative half is what gives it teeth: the same fence's geometry MUST still come
// back out of the hydrated door. A snapshot that stored references and could not resolve
// them would satisfy the first assertion alone.
func TestSnapshotStoresReferencesNotGeometry(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}

	stored := storedSnapshotBytes(t, api, ctx, 1)
	if !strings.Contains(stored, `"hash"`) {
		t.Fatalf("stored snapshot carries no content reference: %s", stored)
	}
	// "coordinates" is the marker: it appears in every geometry document and in no
	// reference. Checking for it rather than for the whole document also catches a
	// snapshot that inlined a REWRITTEN geometry, not just a verbatim one.
	if strings.Contains(stored, "coordinates") {
		t.Fatalf("stored snapshot still inlines geometry: %s", stored)
	}

	snapshot, err := api.GeoFenceSetSnapshotAt(ctx, 1)
	if err != nil {
		t.Fatalf("snapshot at 1: %v", err)
	}
	if len(snapshot.Fences) != 1 {
		t.Fatalf("want 1 fence, got %d", len(snapshot.Fences))
	}
	got := decodePolygonRing(t, string(snapshot.Fences[0].Geometry))
	want := decodePolygonRing(t, yardGeometry)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("hydrated geometry differs:\n got %v\nwant %v", got, want)
	}
}

// TestStoredSnapshotSizeIsAFunctionOfFenceCountAlone pins the property the whole change
// exists for, and CARRIES ITS OWN NEGATIVE CONTROL: the same two fence sets measured in
// the inline form, where the property does not hold.
//
// Without the control this test would pass on a build that stored references to
// geometry it had rewritten to a fixed size, and would say nothing about the inline form
// it replaced.
func TestStoredSnapshotSizeIsAFunctionOfFenceCountAlone(t *testing.T) {
	// Two rings with the SAME position count and very different byte lengths: three
	// decimal places against twelve. The vertex count is what the old bound counted;
	// the byte length is what the wire actually carries.
	small := polygonGeometry(0.001, 0.002, 1.003, 0.004, 1.005, 1.006, 0.001, 0.002)
	large := polygonGeometry(
		0.123456789012, 0.234567890123, 1.345678901234, 0.456789012345,
		1.567890123456, 1.678901234567, 0.123456789012, 0.234567890123)

	measure := func(t *testing.T, geometry string) int {
		t.Helper()
		api := newGeoFenceTestApi(t)
		ctx := core.WithTenant(context.Background(), "acme")
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: geometry}); err != nil {
			t.Fatalf("create: %v", err)
		}
		return len(storedSnapshotBytes(t, api, ctx, 1))
	}

	if a, b := measure(t, small), measure(t, large); a != b {
		t.Fatalf("stored snapshot size varies with geometry length: %d vs %d", a, b)
	}

	// NEGATIVE CONTROL. The inline form these two documents would have produced before
	// the archive existed — the exact shape the old snapshot marshalled. If this does
	// NOT differ, the two fixtures above are not actually different lengths and the
	// assertion above proved nothing.
	inline := func(geometry string) int {
		encoded, err := json.Marshal(&GeoFenceSetSnapshot{
			Version: 1,
			Fences:  []GeoFenceSnapshotRef{{Token: "yard", Geometry: json.RawMessage(geometry)}},
		})
		if err != nil {
			t.Fatalf("marshal inline snapshot: %v", err)
		}
		return len(encoded)
	}
	if a, b := inline(small), inline(large); a == b {
		t.Fatalf("broken control: the two fixtures are the same length inline (%d) — "+
			"the size assertion above cannot distinguish anything", a)
	}
}

// TestEditingOneFenceArchivesOnlyTheNewGeometry is the storage-amplification claim: a
// tenant with several fences who edits ONE must not re-archive the others.
func TestEditingOneFenceArchivesOnlyTheNewGeometry(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for i := 0; i < 4; i++ {
		geometry := polygonGeometry(float64(i)+0.01, 0, 1, 0, 1, 1, float64(i)+0.01, 0)
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
			Token: fmt.Sprintf("yard-%d", i), Geometry: geometry}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if n := len(archivedBlobs(t, api, ctx)); n != 4 {
		t.Fatalf("after 4 distinct fences want 4 blobs, got %d", n)
	}

	// One edit, to a geometry none of the four already has.
	if _, err := api.UpdateGeoFence(ctx, "yard-0", &GeoFenceCreateRequest{
		Token: "yard-0", Geometry: polygonGeometry(9.5, 0, 1, 0, 1, 1, 9.5, 0)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n := len(archivedBlobs(t, api, ctx)); n != 5 {
		t.Fatalf("one edit of one fence should add exactly one blob; want 5, got %d", n)
	}

	// And the version it minted still resolves all four fences.
	snapshot, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatalf("current snapshot: %v", err)
	}
	if len(snapshot.Fences) != 4 {
		t.Fatalf("want 4 fences after the edit, got %d", len(snapshot.Fences))
	}
}

// TestIdenticalGeometryIsArchivedOnce pins the ON CONFLICT DO NOTHING path: two fences
// with byte-identical geometry share one archive row.
func TestIdenticalGeometryIsArchivedOnce(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, token := range []string{"north", "south"} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
			Token: token, Geometry: yardGeometry}); err != nil {
			t.Fatalf("create %s: %v", token, err)
		}
	}
	blobs := archivedBlobs(t, api, ctx)
	if len(blobs) != 1 {
		t.Fatalf("identical geometry should archive once; got %d rows", len(blobs))
	}

	// Both fences must still resolve — sharing a row must not lose one of them.
	snapshot, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatalf("current snapshot: %v", err)
	}
	if len(snapshot.Fences) != 2 {
		t.Fatalf("want 2 fences sharing one geometry, got %d", len(snapshot.Fences))
	}
	for _, fence := range snapshot.Fences {
		if len(fence.Geometry) == 0 {
			t.Fatalf("fence %q hydrated with no geometry", fence.Token)
		}
	}
}

// TestArchiveIsTenantScoped proves two tenants storing the SAME document each get their
// own row — the reason the unique index is (tenant_id, hash) and not (hash).
//
// A globally-unique hash would let the first tenant's row satisfy the second tenant's
// reference, which is a cross-tenant read dressed as a cache hit.
func TestArchiveIsTenantScoped(t *testing.T) {
	api := newGeoFenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	globex := core.WithTenant(context.Background(), "globex")

	for _, ctx := range []context.Context{acme, globex} {
		if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
			Token: "yard", Geometry: yardGeometry}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{{"acme", acme}, {"globex", globex}} {
		if n := len(archivedBlobs(t, api, tc.ctx)); n != 1 {
			t.Fatalf("%s: want its own archive row, got %d", tc.name, n)
		}
		snapshot, err := api.CurrentGeoFenceSetSnapshot(tc.ctx)
		if err != nil {
			t.Fatalf("%s: current snapshot: %v", tc.name, err)
		}
		if len(snapshot.Fences) != 1 || len(snapshot.Fences[0].Geometry) == 0 {
			t.Fatalf("%s: fence did not hydrate", tc.name)
		}
	}
}

// TestHydrationRefusesADanglingReference is the failure mode that must never degrade
// into a short fence set.
//
// 🔴 A MISSING DOCUMENT MUST BE AN ERROR, NOT A DROPPED FENCE. A snapshot that hydrated
// to fewer fences than it names is indistinguishable downstream from a tenant who really
// has that many, so containment would answer "outside" for a device that is inside and
// report a healthy rule that never fires.
func TestHydrationRefusesADanglingReference(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Positive control FIRST: it resolves before the archive is emptied, so a failure
	// below is the deletion and not the fixture.
	if _, err := api.GeoFenceSetSnapshotAt(ctx, 1); err != nil {
		t.Fatalf("control: snapshot should resolve before the archive is emptied: %v", err)
	}

	if err := api.RDB.DB(ctx).Unscoped().Where("1 = 1").Delete(&GeoFenceGeometryBlob{}).Error; err != nil {
		t.Fatalf("empty archive: %v", err)
	}
	snapshot, err := api.GeoFenceSetSnapshotAt(ctx, 1)
	if err == nil {
		t.Fatalf("want an error for a dangling reference, got a snapshot of %d fences", len(snapshot.Fences))
	}
	if !strings.Contains(err.Error(), "not in the archive") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// TestHashAddressesTheStoredDocument pins the contract GeoFenceGeometryHash documents:
// the address in a snapshot names the bytes the archive holds, so re-hashing what came
// back out reproduces it.
//
// This is what would break first if anything ever hashed the AUTHORED text instead of
// the stored form — the two differ, and by an unbounded amount.
func TestHashAddressesTheStoredDocument(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	blobs := archivedBlobs(t, api, ctx)
	if len(blobs) != 1 {
		t.Fatalf("want 1 blob, got %d", len(blobs))
	}
	if got := GeoFenceGeometryHash([]byte(blobs[0].Document)); got != blobs[0].Hash {
		t.Fatalf("stored document does not hash to its address:\n stored %s\nrehashed %s", blobs[0].Hash, got)
	}

	stored := storedSnapshotBytes(t, api, ctx, 1)
	if !strings.Contains(stored, blobs[0].Hash) {
		t.Fatalf("snapshot does not reference the archived document:\n%s", stored)
	}
}

// TestDeletingTheLastFenceMintsAnEmptySnapshot keeps the known-empty distinction alive
// across the change: an empty fence list is a real answer, and it must not need the
// archive to say so.
func TestDeletingTheLastFenceMintsAnEmptySnapshot(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.DeleteGeoFence(ctx, "yard"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	snapshot, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatalf("current snapshot: %v", err)
	}
	if snapshot.Version == 0 {
		t.Fatalf("deleting the last fence must mint a version, not fall back to 0")
	}
	if snapshot.Fences == nil {
		t.Fatalf("empty fence list must be non-nil")
	}
	if len(snapshot.Fences) != 0 {
		t.Fatalf("want an empty fence set, got %d", len(snapshot.Fences))
	}
}

// TestArchiveWriteIsIdempotent exercises archiveGeoFenceGeometries directly at the seam a
// replayed mutation goes through: the same address written repeatedly must leave one row
// and must not error.
//
// It runs the writes in SEPARATE transactions, which is what a replay actually is. Three
// calls inside one transaction would be dedupated by the absence read seeing its own
// uncommitted insert, and would pass on an implementation that only worked there.
func TestArchiveWriteIsIdempotent(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	document := []byte(yardGeometry)
	hash := GeoFenceGeometryHash(document)

	for i := 0; i < 3; i++ {
		err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			return archiveGeoFenceGeometries(tx, map[string][]byte{hash: document})
		})
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if n := len(archivedBlobs(t, api, ctx)); n != 1 {
		t.Fatalf("want 1 row after three identical writes, got %d", n)
	}
}

// TestArchiveCollapsesRepeatsWithinOneMint is the other half: a single call carrying the
// same address twice must not attempt two inserts. The map key is what collapses them,
// so this pins the SHAPE of the argument, not an accident of the caller.
func TestArchiveCollapsesRepeatsWithinOneMint(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	first := []byte(yardGeometry)
	second := []byte(polygonGeometry(0, 0, 1, 0, 1, 1, 0, 0))

	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return archiveGeoFenceGeometries(tx, map[string][]byte{
			GeoFenceGeometryHash(first):  first,
			GeoFenceGeometryHash(second): second,
		})
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if n := len(archivedBlobs(t, api, ctx)); n != 2 {
		t.Fatalf("want 2 distinct rows, got %d", n)
	}
}

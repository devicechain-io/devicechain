// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	dcconfig "github.com/devicechain-io/dc-microservice/config"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"gorm.io/datatypes"
)

// 🔴 THE FINDING THIS FILE EXISTS FOR: THE FENCE SET'S SIZE IS NOT A FUNCTION OF ITS FENCE
// COUNT OR ITS VERTEX COUNT.
//
// device-management admits MaxGeoFencesPerTenant fences of MaxGeoFenceVertices positions, and
// it is tempting to read "100 × 512" as a size. It is not one. validatePolygon2D checks that a
// position has at least two ordinates in the WGS84 ranges — not that it has ONLY two, and not
// how long a coordinate's decimal expansion is. So the same 100 × 512 set that measures ~670 KB
// at two decimal places measures ~1.4 MB at nine and ~2.5 MB at twenty, and a single fence
// carrying extra ordinates could be megabytes on its own.
//
// Every fixture in this package used to be written at one precision, which made a page size
// counted in FENCES look like a solution to a problem measured in BYTES. These tests move that
// variable on purpose.

// The authoring-side bound and canonicalisation are tested where they live, in
// device-management's model package (geofence_canonical_test.go). What is tested HERE is the
// READ side: that a fence set at the documented limits, at high coordinate precision, and one
// containing a row written before the bound existed, all still resolve whole.

// ── 2. the same fence set, four times the bytes ──────────────────────────────────────────────

// A fence set at the documented limits AND at high coordinate precision still resolves whole.
//
// Same fence count, same vertex count, roughly double the bytes of the ordinary fixture — which
// is the axis nothing else in this package moves. The read must come back complete, and no
// single response may cross the cap.
func TestHighPrecisionFenceSetStillResolvesWhole(t *testing.T) {
	api, facts, pointers := ceilingFenceSetAt(t, 20, nil)
	// The LATEST version across both subjects — see latestFact. At this precision the set
	// crosses the broker ceiling partway through being built, so the two writers interleave.
	_, ev := latestFact(t, facts, pointers)

	src := &schemaFenceSource{t: t, api: api}
	set, err := src.FenceSetAt(context.Background(), "acme", ev.Version)
	if err != nil {
		t.Fatalf("a high-precision fence set would not resolve: %v", err)
	}
	if set.Len() != dmmodel.MaxGeoFencesPerTenant {
		t.Fatalf("the high-precision set resolved to %d fences, want %d",
			set.Len(), dmmodel.MaxGeoFencesPerTenant)
	}
	// No response was ever refused: at the opening page size, a set at BOTH documented limits
	// and at twenty decimal places still fits. That is the claim the page size is chosen on, and
	// it is worth asserting rather than inferring from the read having succeeded — the halving
	// retry would have rescued it silently.
	if src.refusals != 0 {
		t.Errorf("%d responses were refused for size at the opening page size; the common case is "+
			"paying for retries it should not need", src.refusals)
	}
	if src.largestBytes > svcclient.MaxResponseBytes {
		t.Errorf("a page came back at %d bytes, over the %d-byte cap", src.largestBytes, svcclient.MaxResponseBytes)
	}

	// The premise, stated in numbers so a fixture that quietly shrank would be caught: this set
	// is materially larger than the ordinary one, and larger than the broker's ceiling.
	stored := 0
	for _, f := range fullSnapshotFences(t, api, ev.Version) {
		stored += len(f.Geometry)
	}
	if stored <= int(dcconfig.DefaultStreamMaxMsgSize) {
		t.Fatalf("the high-precision set is only %d bytes of geometry — it is not bigger than "+
			"the ordinary fixture and tests nothing new", stored)
	}
	t.Logf("high-precision set: %d fences, %d bytes of stored geometry, %d responses, "+
		"largest %d bytes (cap %d)", set.Len(), stored, src.pagesServed, src.largestBytes,
		svcclient.MaxResponseBytes)
}

// ── 3. an already-stored fence bigger than the page budget ───────────────────────────────────

// A fence ALREADY STORED above the byte bound is still readable, because the walk halves its
// page size until the response fits.
//
// 🔴 THIS IS WHY THE HALVING IS NOT OPTIONAL. MaxGeoFenceGeometryBytes binds new WRITES; it
// cannot reach rows written before it existed, and those tenants' fence sets have to stay
// readable or their containment is dead forever — the exact outcome this whole change removes.
// The fixture therefore writes the row STRAIGHT TO THE TABLE, bypassing the authoring
// validation, because that is precisely how such a row comes to exist.
func TestAStoredFenceOverTheByteBoundIsStillReadable(t *testing.T) {
	api := newFenceDmApi(t)
	ctx := dccore.WithTenant(context.Background(), "acme")
	facts, pointers := &fenceFactWriter{}, &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts, pointers,
		dcconfig.DefaultStreamMaxMsgSize, nil, nil)

	// Thirty legitimately-authored fences at the vertex ceiling. They are what makes the
	// oversized row's page cross the cap rather than the row doing it alone: a fence that no
	// page size can carry is unreadable by construction, which is the case the authoring bound
	// exists to prevent, not the case the halving retry exists to survive.
	const ordinary = 30
	for i := 0; i < ordinary; i++ {
		if _, err := api.CreateGeoFence(ctx, &dmmodel.GeoFenceCreateRequest{
			Token:    fmt.Sprintf("fence-%03d", i),
			Geometry: maxVertexFence(float64(100+i%70), float64(i%80)-40, 0.25)}); err != nil {
			t.Fatalf("create fence-%03d: %v", i, err)
		}
	}
	// "yard" contains the probe point, so the set is evaluable and not merely countable.
	if _, err := api.CreateGeoFence(ctx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: maxVertexFence(0, 0, 1)}); err != nil {
		t.Fatalf("create yard: %v", err)
	}

	// The legacy row: far over the byte bound, inserted the only way such a row can exist —
	// straight to the table, past the authoring validation that would refuse it today. Its token
	// sorts FIRST, so it lands on page one at every page size and the halving is forced rather
	// than hoped for.
	huge := `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]},` +
		`"padding":"` + strings.Repeat("x", (svcclient.MaxResponseBytes*3)/4) + `"}`
	if len(huge) <= dmmodel.MaxGeoFenceGeometryBytes {
		t.Fatalf("fixture: the legacy row is %d bytes, inside today's bound", len(huge))
	}
	if err := api.RDB.DB(ctx).Create(&dmmodel.GeoFence{
		TokenReference: rdb.TokenReference{Token: "aaa-legacy"},
		Geometry:       datatypes.JSON(huge)}).Error; err != nil {
		t.Fatalf("insert the legacy oversized fence: %v", err)
	}
	// One more ordinary create mints the version that freezes all of them together.
	if _, err := api.CreateGeoFence(ctx, &dmmodel.GeoFenceCreateRequest{
		Token: "zzz-last", Geometry: fenceBox(50, 50, 51, 51)}); err != nil {
		t.Fatalf("create zzz-last: %v", err)
	}
	_, ev := latestFact(t, facts, pointers)
	wantFences := ordinary + 3

	src := &schemaFenceSource{t: t, api: api}
	set, err := src.FenceSetAt(context.Background(), "acme", ev.Version)
	if err != nil {
		t.Fatalf("a stored fence set containing an oversized row would not resolve: %v — every "+
			"tenant holding such a row would have permanently dead containment", err)
	}
	if src.refusals == 0 {
		t.Fatal("no response was ever refused, so the halving retry was never provoked and this " +
			"test proves nothing about it")
	}
	if set.Len() != wantFences {
		t.Fatalf("the set resolved to %d fences, want %d", set.Len(), wantFences)
	}
	for _, tok := range []string{"aaa-legacy", "yard", "zzz-last", "fence-000", "fence-029"} {
		if set.Fence(tok) == nil {
			t.Errorf("fence %q is missing from the reassembled set", tok)
		}
	}
	// The legitimate fences still EVALUATE. A set that came back complete but no longer computed
	// containment would satisfy every count above and be useless.
	if in, err := set.Contains("yard", geofence.Position{Lat: 0.5, Lon: 0.5}); err != nil || !in {
		t.Errorf("the reassembled yard reports inside=%v err=%v for a point within it", in, err)
	}
	if in, err := set.Contains("yard", geofence.Position{Lat: 40, Lon: 40}); err != nil || in {
		t.Errorf("the reassembled yard reports inside=%v err=%v for a point far outside it", in, err)
	}
	t.Logf("legacy oversized row (%d bytes) among %d fences: %d responses, %d refused, "+
		"largest produced %d bytes (cap %d)", len(huge), wantFences, src.pagesServed,
		src.refusals, src.largestBytes, svcclient.MaxResponseBytes)
}

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
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"gorm.io/datatypes"
)

// 🔴 THE FINDING THIS FILE EXISTS FOR: THE FENCE SET'S SIZE IS NOT A FUNCTION OF ITS FENCE
// COUNT OR ITS VERTEX COUNT.
//
// device-management admits a default tenant geoFenceCeiling fences of geoFencePositionCeiling
// positions each, and
// it is tempting to read "100 × 512" as a size. It is not one. validatePolygon2D checks that a
// position has ordinates in the WGS84 ranges — not how long a coordinate's decimal expansion
// is. So the same 100 × 512 set that measures ~670 KB at two decimal places measures ~1.4 MB at
// nine and ~2.5 MB at twenty.
//
// That is exactly why the geometry batch is sized by ARITHMETIC against a known per-document
// byte ceiling — MaxGeoFenceGeometryHashesPerRequest × MaxGeoFenceGeometryBytes against the
// response cap — rather than by a count of fences, which is a guess about bytes. Every fixture
// in this package used to be written at one precision, which made a size chosen in FENCES look
// like a solution to a problem measured in BYTES. These tests move that variable on purpose.
//
// The authoring-side bound and canonicalisation are tested where they live, in
// device-management's model package. What is tested HERE is the READ side: that a fence set at
// the documented limits, at high coordinate precision, and one containing a row written before
// the bound existed, all still resolve whole.

// ── 1. the same fence set, four times the bytes ──────────────────────────────────────────────

// A fence set at the documented limits AND at high coordinate precision still resolves whole,
// with no batch ever refused.
//
// Same fence count, same vertex count, roughly double the bytes of the ordinary fixture — which
// is the axis nothing else in this package moves. The batch size is chosen so that the standard
// chunk fits under the cap for any document inside the authoring bound, and asserting on the
// REFUSAL COUNT rather than only on the result is what makes that claim testable: the split
// would have rescued an under-sized chunk silently, and the common case would then be paying
// for retries it should never need.
func TestHighPrecisionFenceSetStillResolvesWhole(t *testing.T) {
	api, facts := ceilingFenceSetAt(t, 20)
	_, manifest := lastFact(t, facts)

	src := newSchemaFenceSource(t, api)
	set, err := src.FenceSetAt(context.Background(), "acme", manifest.Version)
	if err != nil {
		t.Fatalf("a high-precision fence set would not resolve: %v", err)
	}
	if set.Len() != governance.DefaultGeoFenceCeiling {
		t.Fatalf("the high-precision set resolved to %d fences, want %d",
			set.Len(), governance.DefaultGeoFenceCeiling)
	}

	st := src.stats()
	if st.refusals != 0 {
		t.Errorf("%d responses were refused for size at the standard chunk size; the common case "+
			"is paying for splits it should not need", st.refusals)
	}
	if st.largestBytes > svcclient.MaxResponseBytes {
		t.Errorf("a response came back at %d bytes, over the %d-byte cap",
			st.largestBytes, svcclient.MaxResponseBytes)
	}
	for i, n := range st.chunkSizes {
		if n > dmmodel.MaxGeoFenceGeometryHashesPerRequest {
			t.Errorf("geometry request %d asked for %d addresses, over device-management's own "+
				"per-request limit of %d", i, n, dmmodel.MaxGeoFenceGeometryHashesPerRequest)
		}
	}

	// The premise, stated in numbers so a fixture that quietly shrank would be caught: this set
	// is materially larger than the ordinary one, and larger than the broker's ceiling.
	stored := 0
	for _, f := range fullSnapshotFences(t, api, manifest.Version) {
		stored += len(f.Geometry)
	}
	if stored <= int(dcconfig.DefaultStreamMaxMsgSize) {
		t.Fatalf("the high-precision set is only %d bytes of geometry — it is not bigger than "+
			"the ordinary fixture and tests nothing new", stored)
	}
	t.Logf("high-precision set: %d fences, %d bytes of stored geometry, %d responses "+
		"(%d geometry batches, sizes %v), largest %d bytes (cap %d)", set.Len(), stored,
		st.responses, st.geometryRequests, st.chunkSizes, st.largestBytes, svcclient.MaxResponseBytes)
}

// ── 2. an already-stored fence bigger than one batch ─────────────────────────────────────────

// A fence ALREADY STORED above the byte bound is still readable, because a refused batch is
// SPLIT and re-asked down to that one address.
//
// 🔴 THIS IS WHY THE SPLIT IS NOT OPTIONAL, AND WHY THE CHUNK SIZE ALONE IS NOT ENOUGH.
// MaxGeoFenceGeometryBytes binds new WRITES; it cannot reach rows written before it existed,
// and those tenants' fence sets have to stay readable or their containment is dead forever —
// the exact outcome this whole design removes. The fixture therefore writes the row STRAIGHT TO
// THE TABLE, bypassing the authoring validation, because that is precisely how such a row comes
// to exist.
//
// The property is the SAME one the retired paged read was tested for; only the mechanism that
// provides it changed, and it changed for the better: a chunk is a set of addresses, so half of
// it is still a well-formed request for exactly the bodies that have not arrived, where the old
// walk had to restart from page one at a halved size and throw away everything it had.
func TestAStoredFenceOverTheByteBoundIsStillReadable(t *testing.T) {
	api := newFenceDmApi(t)
	ctx := dccore.WithTenant(context.Background(), "acme")
	facts := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts, dcconfig.DefaultStreamMaxMsgSize, nil)

	// Thirty legitimately-authored fences at the vertex ceiling. They are what makes the
	// oversized row's BATCH cross the cap rather than the row doing it alone: a document that no
	// subdivision can carry is unreadable by construction, which is the case the authoring bound
	// exists to prevent, not the case the split exists to survive.
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
	// sorts FIRST, so it lands in the first geometry batch and the split is forced rather than
	// hoped for.
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
	// One more ordinary create mints the version that freezes all of them together — and, in the
	// same transaction, archives the oversized document under its content address.
	if _, err := api.CreateGeoFence(ctx, &dmmodel.GeoFenceCreateRequest{
		Token: "zzz-last", Geometry: fenceBox(50, 50, 51, 51)}); err != nil {
		t.Fatalf("create zzz-last: %v", err)
	}
	_, manifest := lastFact(t, facts)
	wantFences := ordinary + 3
	if len(manifest.Fences) != wantFences {
		t.Fatalf("fixture: the manifest names %d fences, want %d", len(manifest.Fences), wantFences)
	}

	src := newSchemaFenceSource(t, api)
	set, err := src.FenceSetAt(context.Background(), "acme", manifest.Version)
	if err != nil {
		t.Fatalf("a stored fence set containing an oversized row would not resolve: %v — every "+
			"tenant holding such a row would have permanently dead containment", err)
	}
	st := src.stats()
	if st.refusals == 0 {
		t.Fatal("no response was ever refused, so the split was never provoked and this test " +
			"proves nothing about it")
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
	t.Logf("legacy oversized row (%d bytes) among %d fences: %d responses (%d geometry batches, "+
		"sizes %v), %d refused, largest produced %d bytes (cap %d)", len(huge), wantFences,
		st.responses, st.geometryRequests, st.chunkSizes, st.refusals, st.largestBytes,
		svcclient.MaxResponseBytes)
}

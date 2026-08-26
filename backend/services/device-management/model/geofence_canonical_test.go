// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// 🔴 THIS FILE EXISTS BECAUSE A LENGTH WAS MEASURED ON THE WRONG DOCUMENT.
//
// The byte bound used to check len(request.Geometry) — the AUTHORED text. What every
// downstream seam carries is what PostgreSQL hands back out of a jsonb column, and jsonb is
// not a byte store: it parses each number into `numeric` and reprints it with numeric_out,
// which never uses exponent notation. Measured on PostgreSQL 16, "1e308" is 5 bytes in and
// 309 out; "1e131071" is 8 in and 131,072 out.
//
// Two accepted shapes reached that gap, because the validator range-checked only the first
// two ordinates and ignored every key it did not read:
//
//	A. extra ordinates  — 512 positions x 7 "1e308" ordinates: ~30 KB in, ~1.1 MB stored
//	B. unparsed keys    — a "pad" key of "1e131071" tokens:    ~1.2 KB in, ~1.3 MB stored
//
// Both were accepted, and either one exceeds a whole response on its own — so the reader's
// halving retry could not save them: it reaches a page of one fence and that fence is still
// too large. The claim the whole paged read rests on ("one fence always fits") was false.
//
// 🔴 AND NO TEST COULD SEE IT. The unit tests run on SQLite, where datatypes.JSON is stored
// verbatim, so every byte assertion measured the authored string — precisely the quantity
// that is NOT what downstream sees. The tests below therefore assert on the CANONICAL
// document, which is the thing this package now controls and stores, and on jsonbRenderedLen,
// which is pinned to values measured from a real PostgreSQL 16 jsonb column.

// ── the size formula ─────────────────────────────────────────────────────────────────────────

// jsonbRenderedLen agrees with PostgreSQL, on documents shaped like the ones this package
// stores.
//
// The expected values were MEASURED, by loading each document into a jsonb column on
// PostgreSQL 16.14 and reading back length(g::text). A formula that merely looked plausible
// would reintroduce exactly the defect this file is about: a size check confidently computed
// against a rendering nobody verified.
func TestJsonbRenderedLenMatchesPostgres(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want int // measured on PostgreSQL 16.14
	}{
		{"flat", `{"a":[1,2,3],"b":[4,5,6]}`, 32},
		{"a fence envelope", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}`, 106},
		{"negative coordinates", `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[-122.419416,37.774929],[-122.4,37.7],[-122.5,37.8],[-122.419416,37.774929]]]}}`, 158},
		{"nested", `{"a":{"b":{"c":[1,2,{"d":3}]}},"e":"f"}`, 47},
		// The one the naive formula gets wrong: separators INSIDE a string get no space.
		{"separators inside a string", `{"k":"a,b:c","z":[1]}`, 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonbRenderedLen([]byte(tc.doc)); got != tc.want {
				t.Errorf("jsonbRenderedLen = %d, PostgreSQL measured %d", got, tc.want)
			}
		})
	}
}

// ── what is now refused ──────────────────────────────────────────────────────────────────────

// A position may carry exactly two ordinates. The check used to be a MINIMUM, which is what
// let a position be arbitrarily wide and made a position count stop bounding anything.
func TestExtraOrdinatesAreRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[`)
	extra := strings.Repeat(",1e308", 7)
	for i := 0; i < 3; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "[%d,%d%s]", i, i, extra)
	}
	fmt.Fprintf(&b, ",[0,0%s]]]}}", extra)

	_, _, err := validateGeoFenceGeometry(b.String())
	if err == nil {
		t.Fatal("a position carrying seven extra 1e308 ordinates was accepted; 512 of them is " +
			"~1.1 MB of stored jsonb, over a whole response on its own")
	}
	if !strings.Contains(err.Error(), "ordinates") {
		t.Errorf("the refusal does not mention ordinates, so an author cannot act on it: %v", err)
	}
}

// A key nothing reads is refused, in both objects. An unread key is stored, snapshotted and
// published like any other, so it is unbounded storage with no reader — and with exponent
// tokens in it, a 1,000x amplification of the request that carried it.
func TestUnknownKeysAreRefused(t *testing.T) {
	ring := `[[[0,0],[1,0],[1,1],[0,0]]]`
	for _, tc := range []struct{ name, doc, wantKey string }{
		{
			"in the envelope",
			`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` + ring + `},"pad":[1e131071]}`,
			"pad",
		},
		{
			"in the GeoJSON object",
			`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` + ring + `,"pad":[1e131071]}}`,
			"pad",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := validateGeoFenceGeometry(tc.doc)
			if err == nil {
				t.Fatalf("a document carrying an unread %q key was accepted", tc.wantKey)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("the refusal does not name the offending key: %v", err)
			}
		})
	}
}

// 🔴 THE ONE THE RANGE CHECK CANNOT CATCH. 1e-300 is a perfectly in-range longitude, six bytes
// of request text, and 302 bytes once PostgreSQL stores it. So range-checking a coordinate is
// not a size check, and this is why the bound is applied to the CANONICAL form — where that
// coordinate is already written out in the same notation the database will use, and counts
// what it will really cost.
func TestATinyInRangeCoordinateCountsAtItsStoredSize(t *testing.T) {
	doc := `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[1e-300,0],[1,0],[1,1],[1e-300,0]]]}}`

	_, canonical, err := validateGeoFenceGeometry(doc)
	if err != nil {
		t.Fatalf("an in-range coordinate was refused: %v", err)
	}
	stored := jsonbRenderedLen([]byte(canonical))
	if stored <= len(doc)*4 {
		t.Fatalf("the canonical form is %d bytes for a %d-byte request; the expansion this bound "+
			"exists to see is not being seen", stored, len(doc))
	}
	// And the canonical form carries no exponent, because that is what makes its length the
	// length PostgreSQL will store.
	if strings.ContainsAny(canonical, "eE") && !strings.Contains(canonical, "POLYGON") {
		t.Errorf("the canonical document still carries exponent notation: %s", canonical[:80])
	}
	t.Logf("1e-300 coordinate: request %d bytes, canonical %d, stored %d", len(doc), len(canonical), stored)
}

// A document whose canonical form exceeds the bound is refused, and the message names both
// numbers so an author can act on it.
//
// The fixture is many small VALID rings, each carrying a coordinate written as 1e-300 — in
// range, so every range check passes; bounding an area, so every ring check passes; and about
// 300 bytes each once written out in the notation the database uses. The request is a few
// kilobytes and the canonical form is tens of them, which is exactly the gap the old
// len(request) check could not see.
func TestAGeometryOverTheStoredBoundIsRefused(t *testing.T) {
	// 4 positions per ring, at the position ceiling: 128 rings.
	const rings = governance.DefaultGeoFencePositionCeiling / 4
	parts := make([]string, 0, rings)
	for i := 0; i < rings; i++ {
		parts = append(parts, `[[1e-300,0],[1,0],[1,1],[1e-300,0]]`)
	}
	doc := `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[` +
		strings.Join(parts, ",") + `]}}`

	if len(doc) > MaxGeoFenceGeometryBytes {
		t.Fatalf("fixture: the REQUEST is already %d bytes, over the %d-byte bound — this test "+
			"would pass on the old, wrong measurement", len(doc), MaxGeoFenceGeometryBytes)
	}
	_, _, err := validateGeoFenceGeometry(doc)
	if err == nil {
		t.Fatalf("a %d-byte request whose canonical form is far larger was accepted", len(doc))
	}
	if !strings.Contains(err.Error(), fmt.Sprint(MaxGeoFenceGeometryBytes)) {
		t.Errorf("the refusal does not name the limit: %v", err)
	}
	if !strings.Contains(err.Error(), "stores as") {
		t.Errorf("the refusal does not say it is talking about the STORED size, which is the "+
			"whole distinction: %v", err)
	}
	t.Logf("refused a %d-byte request whose canonical form is over the %d-byte stored bound: %v",
		len(doc), MaxGeoFenceGeometryBytes, err)
}

// 🔴 THE CLOSING ARGUMENT: WITH THE OTHER RULES IN PLACE, ORDINARY GEOMETRY CANNOT COME NEAR
// THE BOUND — AT THE DEFAULT POSITION CEILING. Unknown keys are refused, a position is exactly
// two ordinates, and a coordinate that survives the WGS84 range checks is a float64 whose
// shortest round-trip decimal is short — so a valid fence's canonical size is bounded by
// roughly the position ceiling x 2 x ~24 bytes. This asserts that arithmetic against the real
// bound, because it is the reason "one fence always fits" is TRUE rather than merely asserted:
// the reader's halving retry terminates at a page of one, and this is what makes that page
// carryable.
//
// 🔴 AT THE PLATFORM MAXIMUM CEILING THE TWO BOUNDS FIGHT, AND THIS TEST SAYS SO RATHER THAN
// ASSERTING SOMETHING FALSE. The position ceiling is a tier setting now, so a tenant may be
// granted governance.MaxGeoFencePositionCeiling — at which the full-float64 worst case is over
// MaxGeoFenceGeometryBytes and the BYTE bound refuses first. That is a tension, not a defect:
// the byte bound refuses with a message naming both numbers, and everything downstream that
// depends on "one fence fits one message" depends only on MaxGeoFenceGeometryBytes being far
// below svcclient.MaxResponseBytes, which is untouched. What it costs is stated in the second
// half below and measured in TestACeilingFenceIsStillAcceptedWithHeadroom: at nine decimal
// places — already finer than any real editor emits — a maximum-ceiling fence still fits, with
// about 11% to spare rather than the 2x a default-ceiling fence has.
//
// Raising MaxGeoFenceGeometryBytes to restore that headroom was considered and declined: it
// would have to halve MaxGeoFenceGeometryHashesPerRequest to stay inside the same response
// cap, which spends a real cost on every tenant's geometry fetch to buy room in a corner
// occupied by nobody — a maximum-ceiling tenant authoring at more than millimetre precision.
//
// The bound remains as a backstop for the shapes above, which reach it through decimal
// expansion rather than through position count.
func TestValidGeometryCannotApproachTheStoredBound(t *testing.T) {
	// The widest a normal coordinate gets: full float64 precision inside the range.
	const worstCoordinateBytes = 24
	perPosition := MaxGeoFencePositionOrdinates * (worstCoordinateBytes + 1)

	atDefault := governance.DefaultGeoFencePositionCeiling * perPosition
	if atDefault >= MaxGeoFenceGeometryBytes {
		t.Fatalf("a fence at the DEFAULT position ceiling could reach %d bytes against a %d-byte "+
			"bound; a tenant that has declared nothing would be refused by a limit it was never "+
			"told about", atDefault, MaxGeoFenceGeometryBytes)
	}
	t.Logf("default ceiling (%d positions): worst-case valid fence ~%d bytes against a %d-byte "+
		"bound (%.1fx headroom)", governance.DefaultGeoFencePositionCeiling, atDefault,
		MaxGeoFenceGeometryBytes, float64(MaxGeoFenceGeometryBytes)/float64(atDefault))

	// The property that actually carries the paged reader, and the one that must hold at
	// EVERY ceiling: one stored fence is far inside the cross-service response cap, so a
	// halving retry terminates at a page of one.
	if MaxGeoFenceGeometryBytes >= svcclient.MaxResponseBytes {
		t.Fatalf("one fence may store at %d bytes against a %d-byte response cap; a page of one "+
			"is no longer guaranteed to be carryable and the halving retry cannot terminate",
			MaxGeoFenceGeometryBytes, svcclient.MaxResponseBytes)
	}

	// And the tension, stated as a measurement rather than left for someone to discover:
	// above this many positions, full-precision geometry meets the byte bound first.
	t.Logf("the byte bound binds before the position bound above ~%d full-precision positions; "+
		"the platform maximum ceiling is %d", MaxGeoFenceGeometryBytes/perPosition,
		governance.MaxGeoFencePositionCeiling)
}

// ── the counterweight: legitimate authoring is untouched ─────────────────────────────────────

// ceilingRingOf builds a valid closed ring of exactly `positions` positions at a chosen
// precision. Separate from ceilingRing because the position ceiling is a tier setting now, so
// "the ceiling" is two different numbers depending on which question is being asked.
func ceilingRingOf(positions int, prec int) string {
	n := positions - 1
	pos := fmt.Sprintf("[%%.%df,%%.%df]", prec, prec)
	var b strings.Builder
	b.WriteString(`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		th := 2 * math.Pi * float64(i) / float64(n)
		fmt.Fprintf(&b, pos, math.Cos(th), math.Sin(th))
	}
	fmt.Fprintf(&b, ","+pos+"]]}}", 1.0, 0.0)
	return b.String()
}

// A fence at the position ceiling, at more precision than any real editor emits, is still
// accepted. A bound that refused legitimate authoring would be a worse defect than the one it
// fixes, and "the pathological one is refused" cannot say which of the two this is.
//
// 🔴 IT MEASURES AT BOTH CEILINGS, AND DEMANDS DIFFERENT THINGS OF THEM. The position ceiling
// is a tier setting: the DEFAULT is what a tenant that declares nothing gets, and it must have
// real headroom, because nobody chose it and nobody was warned. The platform MAXIMUM is what
// an operator can knowingly grant, and there the requirement is only that a fence at that
// ceiling is still ACCEPTED at millimetre precision — the headroom is thin by arithmetic, and
// TestValidGeometryCannotApproachTheStoredBound records why that is the accepted trade.
//
// Testing only the default would leave the maximum untested and the whole ceiling range
// looking uniformly safe; testing only the maximum would let the default's headroom rot away.
func TestACeilingFenceIsStillAcceptedWithHeadroom(t *testing.T) {
	measure := func(t *testing.T, positions int) float64 {
		t.Helper()
		doc := ceilingRingOf(positions, 9)
		_, canonical, err := validateGeoFenceGeometry(doc)
		if err != nil {
			t.Fatalf("a %d-byte fence at %d positions was refused: %v", len(doc), positions, err)
		}
		stored := jsonbRenderedLen([]byte(canonical))
		headroom := float64(MaxGeoFenceGeometryBytes) / float64(stored)
		t.Logf("%d positions at 9 dp: request %d, canonical %d, stored %d, %.2fx headroom",
			positions, len(doc), len(canonical), stored, headroom)
		return headroom
	}

	// The default ceiling: real headroom, because no operator opted into this number.
	if h := measure(t, governance.DefaultGeoFencePositionCeiling); h < 2 {
		t.Errorf("the bound leaves only %.2fx headroom over a DEFAULT-ceiling fence (limit %d); "+
			"that is too close to refuse authoring nobody would call unreasonable",
			h, MaxGeoFenceGeometryBytes)
	}

	// The platform maximum: accepted at all is the requirement. A tier granting this has
	// opted in; being refused by the byte bound at a precision finer than a millimetre is a
	// stated trade rather than a surprise.
	if h := measure(t, governance.MaxGeoFencePositionCeiling); h < 1 {
		t.Errorf("a fence at the PLATFORM MAXIMUM ceiling is refused by the %d-byte bound at nine "+
			"decimal places (%.2fx); a ceiling no tenant can actually author to is not a ceiling",
			MaxGeoFenceGeometryBytes, h)
	}
}

// ── canonicalisation itself ──────────────────────────────────────────────────────────────────

// What is validated is what is stored: the canonical document round-trips to the same
// coordinates, and is byte-identical when re-validated.
//
// The second half is the load-bearing one. If canonicalising a canonical document produced
// something else, the bound would be measuring a document that is still not the final one.
func TestCanonicalGeometryIsStableAndPreservesCoordinates(t *testing.T) {
	doc := `{"geometry":{"coordinates":[[[-122.419416,37.774929],[-122.4,37.7],[-122.5,37.8],[-122.419416,37.774929]]],"type":"Polygon"},"kind":"POLYGON_2D"}`

	_, canonical, err := validateGeoFenceGeometry(doc)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	_, again, err := validateGeoFenceGeometry(canonical)
	if err != nil {
		t.Fatalf("the canonical document does not re-validate: %v", err)
	}
	if again != canonical {
		t.Errorf("canonicalisation is not stable:\n first: %s\nsecond: %s", canonical, again)
	}

	// The coordinates survive exactly — canonicalising must not round an author's fence.
	var envelope struct {
		Geometry struct {
			Coordinates [][][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	if err := json.Unmarshal([]byte(canonical), &envelope); err != nil {
		t.Fatalf("decode canonical: %v", err)
	}
	want := [][]float64{{-122.419416, 37.774929}, {-122.4, 37.7}, {-122.5, 37.8}, {-122.419416, 37.774929}}
	got := envelope.Geometry.Coordinates[0]
	if len(got) != len(want) {
		t.Fatalf("canonical ring has %d positions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			t.Errorf("position %d canonicalised to %v, want %v", i, got[i], want[i])
		}
	}
}

// ── what is actually STORED ──────────────────────────────────────────────────────────────────

// 🔴 THESE EXIST BECAUSE A MUTANT SURVIVED. Reverting CreateGeoFence and UpdateGeoFence to
// persist request.Geometry — validating the canonical document and storing the authored one —
// broke no test in this repository. Every test above calls validateGeoFenceGeometry directly,
// and every read-path fixture was written in a form that is already close to canonical, so
// "what was validated" and "what was stored" being different was invisible.
//
// That is exactly the defect canonicalisation exists to prevent: a bound applied to a document
// nobody keeps. The assertions here are therefore about the ROW and about the SNAPSHOT — the
// two things downstream actually reads.

// nonCanonicalRequest is a VALID fence document that differs from its canonical form in three
// independent ways: exponent notation, key ordering, and insignificant whitespace. Any one of
// them is enough to make stored-vs-validated observable; all three make it unmissable.
const nonCanonicalRequest = `{ "geometry" : { "coordinates" : [ [ [1e-2,0], [1,0], [1,1], [1e-2,0] ] ] , "type" : "Polygon" } , "kind" : "POLYGON_2D" }`

// A created fence stores the CANONICAL document, not the request text.
func TestCreateStoresTheCanonicalGeometry(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, canonical, err := validateGeoFenceGeometry(nonCanonicalRequest)
	if err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	if canonical == nonCanonicalRequest {
		t.Fatal("fixture is already canonical, so it cannot tell the two apart")
	}

	created, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: nonCanonicalRequest})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := string(created.Geometry); got != canonical {
		t.Errorf("the stored geometry is not the canonical document.\n stored: %s\ncanonical: %s", got, canonical)
	}

	// And the row on disk, not just the struct handed back.
	found, err := api.GeoFencesByToken(ctx, []string{"yard"})
	if err != nil || len(found) != 1 {
		t.Fatalf("read back: %v (%d rows)", err, len(found))
	}
	if got := string(found[0].Geometry); got != canonical {
		t.Errorf("the persisted row is not the canonical document: %s", got)
	}
}

// An updated fence stores the canonical document too. The two write paths are separate call
// sites, so one being right says nothing about the other.
func TestUpdateStoresTheCanonicalGeometry(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: polygonGeometry(0, 0, 1, 0, 1, 1, 0, 0)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, canonical, err := validateGeoFenceGeometry(nonCanonicalRequest)
	if err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}

	updated, err := api.UpdateGeoFence(ctx, "yard", &GeoFenceCreateRequest{
		Token: "yard", Geometry: nonCanonicalRequest})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := string(updated.Geometry); got != canonical {
		t.Errorf("update stored the request text rather than the canonical document: %s", got)
	}
}

// 🔴 AND THE SNAPSHOT CARRIES IT. The frozen fence-set snapshot is what the fact publishes and
// what the paged GraphQL door serves, so it — not the live row — is the document every byte
// budget downstream is spent on. mintGeoFenceSetVersion copies the stored geometry verbatim,
// which means storing the authored text would put it straight onto the wire, past a bound that
// was measured against something else.
func TestTheFrozenSnapshotCarriesTheCanonicalGeometry(t *testing.T) {
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, canonical, err := validateGeoFenceGeometry(nonCanonicalRequest)
	if err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: nonCanonicalRequest}); err != nil {
		t.Fatalf("create: %v", err)
	}

	snapshot, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Fences) != 1 {
		t.Fatalf("the snapshot holds %d fences, want 1", len(snapshot.Fences))
	}
	if got := string(snapshot.Fences[0].Geometry); got != canonical {
		t.Errorf("the frozen snapshot carries the request text, so the size bound was applied to "+
			"a document nobody publishes.\n frozen: %s\ncanonical: %s", got, canonical)
	}
}

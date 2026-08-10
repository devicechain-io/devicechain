// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package basemap

import (
	"math"
	"strings"
	"testing"
)

func s(v string) *string   { return &v }
func f(v float64) *float64 { return &v }

const (
	osmTiles = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
	osmCred  = `© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors`
	// A second, DIFFERENT provider. The pairing of one provider's tiles with the
	// other's credit line is the defect Merge exists to prevent, so the tests need
	// two of everything.
	cartoTiles = "https://a.basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png"
	cartoCred  = `© CARTO`
)

// ---- Merge: the tile source is atomic -------------------------------------

// 🔴 This is the headline property of the whole package. A uniform per-field merge
// passes every other test in this file and fails only this one.
func TestATenantTileUrlNeverInheritsTheOperatorAttribution(t *testing.T) {
	operator := Basemap{TileURL: s(cartoTiles), Attribution: s(cartoCred)}
	tenant := Basemap{TileURL: s(osmTiles)} // chose a provider, did not restate a credit

	got := Merge(tenant, operator)

	if got.TileURL == nil || *got.TileURL != osmTiles {
		t.Fatalf("tenant tile source should win, got %v", got.TileURL)
	}
	if got.Attribution != nil {
		t.Fatalf("attribution must NOT be inherited from the operator when the tenant sets its own tile URL: "+
			"that credits %q for tiles served by %q, which is a licence violation manufactured by the merge; got %q",
			cartoCred, osmTiles, *got.Attribution)
	}
}

// The mirror of the case above, and the reason it cannot be fixed by "never inherit
// attribution": a tenant that sets NOTHING must inherit the operator's source WITH
// its credit line, or the instance default renders uncredited.
func TestATenantThatSetsNothingInheritsBothHalvesOfTheTileSource(t *testing.T) {
	operator := Basemap{TileURL: s(cartoTiles), Attribution: s(cartoCred)}

	got := Merge(Basemap{}, operator)

	if got.TileURL == nil || *got.TileURL != cartoTiles {
		t.Fatalf("tile URL should be inherited, got %v", got.TileURL)
	}
	if got.Attribution == nil || *got.Attribution != cartoCred {
		t.Fatalf("attribution should be inherited WITH its tile URL, got %v", got.Attribution)
	}
}

func TestATenantThatSetsBothHalvesKeepsBoth(t *testing.T) {
	operator := Basemap{TileURL: s(cartoTiles), Attribution: s(cartoCred)}
	tenant := Basemap{TileURL: s(osmTiles), Attribution: s(osmCred)}

	got := Merge(tenant, operator)

	if *got.TileURL != osmTiles || *got.Attribution != osmCred {
		t.Fatalf("tenant should win both halves, got %v / %v", *got.TileURL, *got.Attribution)
	}
}

// ---- Merge: the camera merges per field ------------------------------------

func TestTheCameraMergesPerFieldSoATenantNeedNotRestateAProviderToRefineTheView(t *testing.T) {
	operator := Basemap{TileURL: s(cartoTiles), Attribution: s(cartoCred), CenterLat: f(33.75), CenterLon: f(-84.39), Zoom: f(10)}
	tenant := Basemap{Zoom: f(14)} // "same map, closer in"

	got := Merge(tenant, operator)

	if got.TileURL == nil || *got.TileURL != cartoTiles {
		t.Fatalf("tile source should still be inherited, got %v", got.TileURL)
	}
	if got.Zoom == nil || *got.Zoom != 14 {
		t.Fatalf("tenant zoom should win, got %v", got.Zoom)
	}
	if got.CenterLat == nil || *got.CenterLat != 33.75 || got.CenterLon == nil || *got.CenterLon != -84.39 {
		t.Fatalf("operator centre should carry when the tenant sets none, got %v / %v", got.CenterLat, got.CenterLon)
	}
}

// Half a coordinate names no point, so the pair moves together for the same reason
// the tile source does.
func TestACoordinateMovesAsAPairSoNoTierSuppliesHalfOfAPointTheOtherDidNotChoose(t *testing.T) {
	operator := Basemap{CenterLat: f(33.75), CenterLon: f(-84.39)}
	tenant := Basemap{CenterLat: f(41.90)} // Rome's latitude, no longitude

	got := Merge(tenant, operator)

	if got.CenterLat == nil || *got.CenterLat != 41.90 {
		t.Fatalf("tenant latitude should win, got %v", got.CenterLat)
	}
	if got.CenterLon != nil {
		t.Fatalf("longitude must not be inherited beside a different tier's latitude: "+
			"41.90/%v is a point in neither Atlanta nor Rome; got %v", *got.CenterLon, *got.CenterLon)
	}
}

func TestMergingTwoEmptyOverridesResolvesToNothing(t *testing.T) {
	got := Merge(Basemap{}, Basemap{})
	if IsSet(got) {
		t.Fatalf("an unset basemap at every tier must resolve to no tile source, got %+v", got)
	}
	if got.TileURL != nil || got.Attribution != nil || got.CenterLat != nil || got.CenterLon != nil || got.Zoom != nil {
		t.Fatalf("expected a wholly empty result, got %+v", got)
	}
}

// ---- Validate: the tile source is atomic -----------------------------------

func TestAValidBasemapIsAccepted(t *testing.T) {
	b := Basemap{TileURL: s(osmTiles), Attribution: s(osmCred), CenterLat: f(33.75), CenterLon: f(-84.39), Zoom: f(11.5)}
	if err := Validate(b); err != nil {
		t.Fatalf("a well-formed basemap must be accepted, got %v", err)
	}
}

// The counterweight to every rejection below: refusing bad input is only safe while
// ordinary input still passes untouched.
func TestAnEmptyOverrideIsValidBecauseEveryFieldIsOptional(t *testing.T) {
	if err := Validate(Basemap{}); err != nil {
		t.Fatalf("an empty override clears everything and must be valid, got %v", err)
	}
}

func TestATileUrlWithNoAttributionIsRefused(t *testing.T) {
	err := Validate(Basemap{TileURL: s(osmTiles)})
	if err == nil {
		t.Fatal("a tile source with no credit line must be refused")
	}
	if !strings.Contains(err.Error(), "attribution") {
		t.Fatalf("the message must name the missing field, got %q", err)
	}
}

func TestABlankAttributionIsRefusedTheSameAsAMissingOne(t *testing.T) {
	if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s("   ")}); err == nil {
		t.Fatal("whitespace is not a credit line")
	}
}

func TestAnAttributionWithNoTileUrlIsRefused(t *testing.T) {
	err := Validate(Basemap{Attribution: s(osmCred)})
	if err == nil {
		t.Fatal("an attribution alone would be applied to a tile source it does not describe")
	}
}

// ---- Validate: the tile URL --------------------------------------------------

func TestAnHttpTileUrlIsRefused(t *testing.T) {
	err := Validate(Basemap{TileURL: s("http://tile.example.invalid/{z}/{x}/{y}.png"), Attribution: s(osmCred)})
	if err == nil {
		t.Fatal("http is blocked as mixed content by any console served over TLS, so it must not be storable")
	}
}

func TestATileUrlWithNoPlaceholderIsRefused(t *testing.T) {
	for _, url := range []string{
		"https://tiles.example.invalid/style.json",  // pasted a style URL
		"https://tiles.example.invalid/1/2/3.png",   // pasted one tile
		"https://tiles.example.invalid/{ratio}.png", // a modifier, not a scheme
	} {
		if err := Validate(Basemap{TileURL: s(url), Attribution: s(osmCred)}); err == nil {
			t.Fatalf("%q requests the same image for every tile and must be refused", url)
		}
	}
}

func TestTheNonXyzTileSchemesAreAccepted(t *testing.T) {
	for _, url := range []string{
		"https://wms.example.invalid/?bbox={bbox-epsg-3857}&width=256",
		"https://tiles.example.invalid/{quadkey}.png",
	} {
		if err := Validate(Basemap{TileURL: s(url), Attribution: s(osmCred)}); err != nil {
			t.Fatalf("%q is a valid tile template, got %v", url, err)
		}
	}
}

func TestATileUrlCarryingAProviderKeyIsAccepted(t *testing.T) {
	// The credential-bearing case is the ordinary one, not an exception — this is the
	// whole reason the value is per tenant (ADR-079).
	url := "https://api.maptiler.com/maps/streets/{z}/{x}/{y}.png?key=abc123DEF"
	if err := Validate(Basemap{TileURL: s(url), Attribution: s(osmCred)}); err != nil {
		t.Fatalf("a query-string API key must be accepted, got %v", err)
	}
}

func TestAnOverlongTileUrlIsRefused(t *testing.T) {
	long := "https://t.example.invalid/{z}/{x}/{y}.png?k=" + strings.Repeat("a", MaxTileURLLen)
	if err := Validate(Basemap{TileURL: s(long), Attribution: s(osmCred)}); err == nil {
		t.Fatal("an over-long tile URL must be refused")
	}
}

// ---- Validate: the attribution allow-list ------------------------------------

func TestAnAttributionMayCarryAPlainHttpsLinkBecauseLicencesRequireOne(t *testing.T) {
	if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s(osmCred)}); err != nil {
		t.Fatalf("OpenStreetMap's own required credit line must be accepted, got %v", err)
	}
}

func TestPlainTextAttributionIsAccepted(t *testing.T) {
	if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s("© CARTO, © OpenStreetMap contributors")}); err != nil {
		t.Fatalf("plain text must be accepted, got %v", err)
	}
}

func TestAttributionMarkupOutsideTheAllowListIsRefused(t *testing.T) {
	cases := map[string]string{
		"a script tag":            `© <script>alert(1)</script> Tiles`,
		"an image":                `<img src="https://x.invalid/a.png"> Tiles`,
		"an inline handler":       `<a href="https://x.invalid" onclick="alert(1)">Tiles</a>`,
		"a javascript href":       `<a href="javascript:alert(1)">Tiles</a>`,
		"an http href":            `<a href="http://x.invalid">Tiles</a>`,
		"single quotes":           `<a href='https://x.invalid'>Tiles</a>`,
		"an extra attribute":      `<a href="https://x.invalid" target="_blank">Tiles</a>`,
		"a styled span":           `<span style="color:red">Tiles</span>`,
		"an unclosed link":        `<a href="https://x.invalid">Tiles`,
		"a stray closing tag":     `Tiles</a>`,
		"nested links":            `<a href="https://x.invalid"><a href="https://y.invalid">T</a></a>`,
		"an svg":                  `<svg onload="alert(1)"/>`,
		"an uppercase script tag": `<SCRIPT>alert(1)</SCRIPT>`,
	}
	for name, attr := range cases {
		if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s(attr)}); err == nil {
			t.Errorf("%s must be refused: %q", name, attr)
		}
	}
}

func TestAttributionRejectsControlCharacters(t *testing.T) {
	if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s("© Tiles\x00")}); err == nil {
		t.Fatal("a control character must be refused")
	}
}

func TestAnOverlongAttributionIsRefused(t *testing.T) {
	long := strings.Repeat("©", MaxAttributionLen+1)
	if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s(long)}); err == nil {
		t.Fatal("an over-long attribution must be refused")
	}
	// Counted in RUNES, not bytes: "©" is two bytes, so a byte-length check would
	// refuse a credit line half the documented limit.
	atLimit := strings.Repeat("©", MaxAttributionLen)
	if err := Validate(Basemap{TileURL: s(osmTiles), Attribution: s(atLimit)}); err != nil {
		t.Fatalf("an attribution of exactly %d runes must be accepted, got %v", MaxAttributionLen, err)
	}
}

// ---- Validate: the camera ----------------------------------------------------

func TestHalfACoordinateIsRefused(t *testing.T) {
	if err := Validate(Basemap{CenterLat: f(33.75)}); err == nil {
		t.Fatal("a latitude with no longitude names no point")
	}
	if err := Validate(Basemap{CenterLon: f(-84.39)}); err == nil {
		t.Fatal("a longitude with no latitude names no point")
	}
}

func TestOutOfRangeCoordinatesAreRefused(t *testing.T) {
	cases := []Basemap{
		{CenterLat: f(91), CenterLon: f(0)},
		{CenterLat: f(-91), CenterLon: f(0)},
		{CenterLat: f(0), CenterLon: f(181)},
		{CenterLat: f(0), CenterLon: f(-181)},
	}
	for _, b := range cases {
		if err := Validate(b); err == nil {
			t.Errorf("out-of-range coordinate must be refused: %v/%v", *b.CenterLat, *b.CenterLon)
		}
	}
}

func TestTheCoordinateExtremesAreAccepted(t *testing.T) {
	if err := Validate(Basemap{CenterLat: f(90), CenterLon: f(180)}); err != nil {
		t.Fatalf("the poles and the antimeridian are real places, got %v", err)
	}
	if err := Validate(Basemap{CenterLat: f(-90), CenterLon: f(-180)}); err != nil {
		t.Fatalf("the poles and the antimeridian are real places, got %v", err)
	}
}

func TestNonFiniteCoordinatesAreRefused(t *testing.T) {
	inf := math.Inf(1)
	nan := math.NaN()
	for _, b := range []Basemap{
		{CenterLat: &nan, CenterLon: f(0)},
		{CenterLat: f(0), CenterLon: &inf},
		{Zoom: &nan},
	} {
		if err := Validate(b); err == nil {
			t.Errorf("a non-finite value must be refused: %+v", b)
		}
	}
}

func TestOutOfRangeZoomIsRefused(t *testing.T) {
	for _, z := range []float64{-1, 25} {
		if err := Validate(Basemap{Zoom: f(z)}); err == nil {
			t.Errorf("zoom %v is outside MapLibre's usable range and must be refused", z)
		}
	}
	for _, z := range []float64{MinZoom, MaxZoom, 11.5} {
		if err := Validate(Basemap{Zoom: f(z)}); err != nil {
			t.Errorf("zoom %v must be accepted (fractional zoom is ordinary), got %v", z, err)
		}
	}
}

// ---- IsSet -------------------------------------------------------------------

func TestIsSetReportsATileSourceRatherThanAnyFieldAtAll(t *testing.T) {
	if IsSet(Basemap{CenterLat: f(33.75), CenterLon: f(-84.39), Zoom: f(10)}) {
		t.Fatal("a camera fallback with no tile source has nothing to render")
	}
	if !IsSet(Basemap{TileURL: s(osmTiles), Attribution: s(osmCred)}) {
		t.Fatal("a tile source is set")
	}
	if IsSet(Basemap{TileURL: s("")}) {
		t.Fatal("an empty tile URL is not a tile source")
	}
}

// ---- Blank is not a tile source (the review finding) ------------------------
//
// 🔴 These exist because Merge USED to read a pointer-to-blank as SET. The comment on
// trimmedValue claimed an emptied field "must clear the override, not store a blank
// one" — and nothing anywhere did that, so a non-console client sending "" to mean
// "clear" masked the operator's instance default with an empty tile source.

func TestABlankTenantTileUrlDoesNotMaskTheOperatorDefault(t *testing.T) {
	operator := Basemap{TileURL: s(cartoTiles), Attribution: s(cartoCred)}

	for name, tenant := range map[string]Basemap{
		"an empty string":  {TileURL: s(""), Attribution: s("")},
		"whitespace":       {TileURL: s("   "), Attribution: s("  ")},
		"a blank URL only": {TileURL: s("")},
	} {
		got := Merge(tenant, operator)
		if got.TileURL == nil || *got.TileURL != cartoTiles {
			t.Errorf("%s: a blank tenant tile URL means UNSET, so the operator default must carry; got %v", name, got.TileURL)
		}
		if got.Attribution == nil || *got.Attribution != cartoCred {
			t.Errorf("%s: the inherited tile source must arrive with its credit line; got %v", name, got.Attribution)
		}
	}
}

func TestNormalizeTurnsABlankFieldIntoAClearedOne(t *testing.T) {
	got := Normalize(Basemap{TileURL: s("   "), Attribution: s(""), Zoom: f(10)})

	if got.TileURL != nil {
		t.Errorf("a whitespace tile URL must normalize to nil, got %q", *got.TileURL)
	}
	if got.Attribution != nil {
		t.Errorf("an empty attribution must normalize to nil, got %q", *got.Attribution)
	}
	// The camera is untouched: 0 is a real coordinate and a real zoom, so there is no
	// "blank" for a number to collapse to.
	if got.Zoom == nil || *got.Zoom != 10 {
		t.Errorf("normalizing must not disturb the camera, got %v", got.Zoom)
	}
}

// The stored value must be byte-identical to the validated one. Without the trim,
// "  https://…  " validated cleanly and was written back with its padding, leaving
// every renderer to trim it independently and forever.
func TestNormalizeTrimsAValueRatherThanStoringItsPadding(t *testing.T) {
	got := Normalize(Basemap{TileURL: s("  " + osmTiles + "  "), Attribution: s("  " + osmCred + " ")})

	if got.TileURL == nil || *got.TileURL != osmTiles {
		t.Errorf("expected the trimmed tile URL, got %v", got.TileURL)
	}
	if got.Attribution == nil || *got.Attribution != osmCred {
		t.Errorf("expected the trimmed attribution, got %v", got.Attribution)
	}
}

func TestIsSetTreatsWhitespaceAsNoTileSource(t *testing.T) {
	if IsSet(Basemap{TileURL: s("   ")}) {
		t.Fatal("whitespace is not a tile source")
	}
}

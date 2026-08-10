// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/basemap"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func sp(v string) *string   { return &v }
func fp(v float64) *float64 { return &v }

const (
	tenantTiles = "https://tenant.example.invalid/{z}/{x}/{y}.png"
	tenantCred  = "© Tenant Tiles"
	opTiles     = "https://operator.example.invalid/{z}/{x}/{y}.png"
	opCred      = "© Operator Tiles"
)

// ---- validateBasemapDefault: the operator tier's mint-point gate ------------

// The counterpart of TestValidateBrandingDefault: a value that would be rejected on
// the tenant path must not be storable via setSetting either, because this one is
// served to every non-overriding TENANT.
func TestValidateBasemapDefault(t *testing.T) {
	// The shipped code default must always pass its own gate.
	var shipped string
	for _, d := range settings.Definitions() {
		if d.Key == settings.KeyBasemapDefault {
			shipped = string(d.Default)
		}
	}
	require.NotEmpty(t, shipped, "basemap.default must be a registered setting")
	require.NoError(t, validateBasemapDefault(shipped), "the shipped code default must pass its own gate")

	valid := []string{
		`{}`,
		`{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© Example"}`,
		`{"centerLat":33.75,"centerLon":-84.39,"zoom":10}`,
		`{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"<a href=\"https://e.invalid\">Example</a>"}`,
	}
	for _, v := range valid {
		require.NoErrorf(t, validateBasemapDefault(v), "valid basemap.default rejected: %s", v)
	}

	invalid := map[string]string{
		"a tile URL with no credit line": `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png"}`,
		"a credit line with no tiles":    `{"attribution":"© Example"}`,
		"http":                           `{"tileUrl":"http://t.example.invalid/{z}/{x}/{y}.png","attribution":"© E"}`,
		"not a template":                 `{"tileUrl":"https://t.example.invalid/style.json","attribution":"© E"}`,
		"script in the attribution":      `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"<script>alert(1)</script>"}`,
		"out-of-range latitude":          `{"centerLat":91,"centerLon":0}`,
		"half a coordinate":              `{"centerLat":33.75}`,
		// 🔴 The typo case is the reason DisallowUnknownFields is set: without it an
		// operator stores `tile_url`, setSetting reports success, and every map in the
		// instance stays blank behind a stored value that looks correct.
		"an unknown key": `{"tile_url":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© E"}`,
		"not json":       `nope`,
	}
	for name, v := range invalid {
		require.Errorf(t, validateBasemapDefault(v), "%s must be refused: %s", name, v)
	}
}

// 🔴 MEASURED, not assumed: encoding/json matches field names CASE-INSENSITIVELY, so
// DisallowUnknownFields does NOT reject `tileURL` — it binds to TileURL and works.
// Pinned here because the natural reading of "unknown fields are rejected" says the
// opposite, and someone will eventually write a validator that depends on the strict
// reading. Only a genuinely different key (see "an unknown key" above) is refused.
func TestKeyCasingIsAcceptedBecauseTheJsonDecoderIsCaseInsensitive(t *testing.T) {
	v := `{"tileURL":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© E"}`
	require.NoError(t, validateBasemapDefault(v),
		"case variation binds to the right field, so it is accepted rather than silently ignored")
}

// ---- setSetting actually CALLS that validator -------------------------------

// 🔴 This pair exists because a mutation pass found the gap: deleting the whole
// basemap branch from SetSetting left every test in this package green.
// TestValidateBasemapDefault proves the validator WORKS; it says nothing about
// whether anything calls it. A validator nobody invokes is a comment.

func newSettingsCtx(t *testing.T, authorities ...string) (context.Context, *settings.Service) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&settings.SystemSetting{}))
	svc := settings.NewService(settings.NewStore(&rdb.RdbManager{Database: db}))

	// settings:write is SYSTEM tier, so it rides an identity token — a tenant access
	// token cannot satisfy it however many authorities it lists (ADR-065).
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		Username:    "operator@devicechain.local",
		TokenType:   auth.TokenTypeIdentity,
		Authorities: authorities,
	})
	return context.WithValue(ctx, ContextSettingsKey, svc), svc
}

func TestSetSettingRefusesAnInvalidBasemapDefault(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	r := &SettingsResolver{}

	// A tile source with no credit line: valid JSON, valid shape, refused by the rules.
	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: settings.KeyBasemapDefault, Value: `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png"}`})

	require.Error(t, err, "setSetting must run the basemap rules, not just store opaque JSON")
	require.Contains(t, err.Error(), "attribution")

	// And nothing was stored — a refused write must not land.
	eff, err := svc.Get(ctx, settings.KeyBasemapDefault)
	require.NoError(t, err)
	require.False(t, eff.Overridden, "a refused value must not be persisted")
}

// The positive control: a VALID basemap default must still be storable. Without this,
// a validator that refused everything would pass the test above.
func TestSetSettingStoresAValidBasemapDefault(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	r := &SettingsResolver{}

	value := `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© Example","zoom":9}`
	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: settings.KeyBasemapDefault, Value: value})
	require.NoError(t, err)

	eff, err := svc.Get(ctx, settings.KeyBasemapDefault)
	require.NoError(t, err)
	require.True(t, eff.Overridden)
	require.JSONEq(t, value, string(eff.Value))
}

func TestSetSettingRequiresSettingsWriteForTheBasemapDefault(t *testing.T) {
	// A holder of basemap:write may set its OWN tenant's basemap; the INSTANCE default
	// is an operator surface and stays behind settings:write.
	ctx, _ := newSettingsCtx(t, string(auth.BasemapWrite))
	r := &SettingsResolver{}

	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: settings.KeyBasemapDefault, Value: `{}`})

	require.ErrorIs(t, err, auth.ErrForbidden)
}

// ---- resolveBasemap: the cascade wiring ------------------------------------

// 🔴 The argument order is the defect this test exists for. Swap the two arguments in
// resolveBasemap and the OPERATOR default silently wins over every tenant's own
// choice — every other test in this package, and every test in the basemap package,
// still passes.
func TestTheTenantOverrideWinsOverTheOperatorDefault(t *testing.T) {
	stored := []byte(`{"tileUrl":"` + opTiles + `","attribution":"` + opCred + `","zoom":8}`)
	tenant := basemap.Basemap{TileURL: sp(tenantTiles), Attribution: sp(tenantCred)}

	got := resolveBasemap(tenant, stored)

	require.NotNil(t, got.TileURL)
	require.Equal(t, tenantTiles, *got.TileURL, "the tenant's own tile source must win over the operator default")
	require.NotNil(t, got.Attribution)
	require.Equal(t, tenantCred, *got.Attribution)
	// The camera still cascades: the tenant set none, so the operator's carries.
	require.NotNil(t, got.Zoom)
	require.Equal(t, float64(8), *got.Zoom)
}

func TestATenantWithNoOverrideInheritsTheOperatorDefault(t *testing.T) {
	stored := []byte(`{"tileUrl":"` + opTiles + `","attribution":"` + opCred + `"}`)

	got := resolveBasemap(basemap.Basemap{}, stored)

	require.NotNil(t, got.TileURL)
	require.Equal(t, opTiles, *got.TileURL)
	require.NotNil(t, got.Attribution)
	require.Equal(t, opCred, *got.Attribution, "an inherited tile source must arrive WITH its credit line")
}

// The end-to-end statement of the licence rule, through the resolver's own fold
// rather than through basemap.Merge directly.
func TestATenantTileSourceNeverPicksUpTheOperatorCreditLine(t *testing.T) {
	stored := []byte(`{"tileUrl":"` + opTiles + `","attribution":"` + opCred + `"}`)
	tenant := basemap.Basemap{TileURL: sp(tenantTiles)}

	got := resolveBasemap(tenant, stored)

	require.Nil(t, got.Attribution,
		"a tenant that named its own tile source must not inherit %q — that credits the operator's provider for the tenant's tiles", opCred)
}

// An operator who deliberately stores `{}` as the instance default lands here. It is
// no longer the shipped state (see TestShippedBasemapDefault) but it is still a
// reachable one, and the renderers' plain-panel path exists for it.
//
// 🔴 "Stores {}", not "clears": settings.Clear REMOVES the override and reverts to the
// code default, which is now a real tile source. The two words mean opposite things
// here, and the console's button is the one labelled "Reset to default".
func TestAnEmptiedOperatorDefaultResolvesToNoBasemapAtAll(t *testing.T) {
	got := resolveBasemap(basemap.Basemap{}, []byte(`{}`))
	require.False(t, basemap.IsSet(got),
		"an instance default stored as {} must yield no tile source, not a stale one")
}

// TestShippedBasemapDefault checks the registered default through the CASCADE FOLD,
// on a tenant that overrides nothing.
//
// 🔴 Asserting the constant's bytes would not be enough: the value has to survive JSON
// unmarshalling, basemap.Validate and the fold before a console ever sees it, and a
// default that is rejected by one of those degrades to nothing (resolveBasemap
// swallows it by design) — a blank map again, with a log line the only evidence.
//
// It stops at the fold, though, and that gap is covered separately: this hands
// `shipped` to resolveBasemap directly, so it cannot see a resolver that stopped
// consulting the settings store at all. TestABareTenantResolvesToTheShippedDefault
// drives that half through TenantResolver.Basemap.
func TestShippedBasemapDefault(t *testing.T) {
	var shipped []byte
	for _, d := range settings.Definitions() {
		if d.Key == settings.KeyBasemapDefault {
			shipped = d.Default
		}
	}
	require.NotEmpty(t, shipped, "basemap.default must be a registered setting")

	got := resolveBasemap(basemap.Basemap{}, shipped)

	require.True(t, basemap.IsSet(got),
		"a new install must draw a map: an empty canvas is the defect this default exists to fix")
	require.NotNil(t, got.TileURL)
	require.NotNil(t, got.Attribution, "the shipped tile source must arrive with its credit line")

	// The specific provider is a decision, not an accident: OpenStreetMap needs no
	// credentials, which is what makes it viable as a zero-config default and is the
	// parity bar. Changing it is fine — changing it without also changing the credit
	// line below is not, which is what the cross-check after this enforces.
	require.Equal(t, "https://tile.openstreetmap.org/{z}/{x}/{y}.png", *got.TileURL)
	require.Contains(t, *got.Attribution, "https://www.openstreetmap.org/copyright",
		"OSM's attribution guidelines require the credit to link to the copyright page")
	require.Contains(t, *got.Attribution, "OpenStreetMap",
		"OSM's attribution guidelines require the credit to name OpenStreetMap")
}

// TestABareTenantResolvesToTheShippedDefault drives the whole read path a console's
// boot query drives: TenantResolver.Basemap → the settings store → the code default →
// the fold. No override at either tier, which is what a fresh install looks like.
//
// 🔴 This exists because every other test in this file hands resolveBasemap its
// operator default as an argument. That makes them blind to the ONE mutation that
// reproduces the defect this whole change fixes: a resolver that stops consulting the
// settings store and folds against nothing. Rewriting Basemap's body to
// `resolveBasemap(basemapFromTenant(r.t), []byte("{}"))` leaves every other test in
// the module green and every fresh install blank.
func TestABareTenantResolvesToTheShippedDefault(t *testing.T) {
	// No authorities: reading a basemap is deliberately ungated, because gating it
	// blanks the map for exactly the people who need to see one.
	ctx, _ := newSettingsCtx(t)
	r := &TenantResolver{t: &iam.Tenant{}, svc: &SchemaResolver{}}

	got, err := r.Basemap(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.TileUrl(),
		"a tenant that overrides nothing must inherit the instance default — with no override and no default reaching it, a new install is blank")
	require.Equal(t, "https://tile.openstreetmap.org/{z}/{x}/{y}.png", *got.TileUrl())
	require.NotNil(t, got.Attribution(), "the inherited tile source must arrive with its credit line")
	require.Contains(t, *got.Attribution(), "OpenStreetMap")
}

// The licence property, at the one tier that reaches every tenant on the instance.
//
// 🔴 This is the failure mode worth a test rather than a comment: repointing the
// default at another provider is a one-line edit to a URL, and the credit line
// sitting on the next line is easy not to think about. The result ships a licence
// violation, prefilled and trusted, to every install that takes the default — the
// exact defect basemap.Merge refuses to manufacture, reintroduced by hand.
//
// The check is that the host serving the tiles is credited BY A LINK IN THE
// ATTRIBUTION. Deliberately scoped to the shipped default and not generalised: a
// composited basemap legitimately credits a data source it does not fetch from
// (Carto's tiles come from cartocdn.com and credit OpenStreetMap), so this exact
// rule would be wrong applied to the whole provider catalog. It is right here,
// where there is one provider and it serves its own credit page.
func TestTheShippedDefaultCreditsTheHostItFetchesTilesFrom(t *testing.T) {
	var shipped []byte
	for _, d := range settings.Definitions() {
		if d.Key == settings.KeyBasemapDefault {
			shipped = d.Default
		}
	}
	got := resolveBasemap(basemap.Basemap{}, shipped)
	require.NotNil(t, got.TileURL)
	require.NotNil(t, got.Attribution)

	tileHost, err := url.Parse(*got.TileURL)
	require.NoError(t, err)
	want := registrableDomain(tileHost.Host)
	require.NotEmpty(t, want)

	var credited []string
	for _, href := range hrefsIn(*got.Attribution) {
		u, err := url.Parse(href)
		require.NoErrorf(t, err, "attribution link is not a URL: %s", href)
		credited = append(credited, registrableDomain(u.Host))
	}
	require.Containsf(t, credited, want,
		"the default fetches tiles from %s but its credit line links only to %v — "+
			"a tile source and the attribution its licence requires are ONE value, "+
			"and this is the tier that reaches every tenant", tileHost.Host, credited)
}

// registrableDomain reduces a host to its last two labels, so tile.example.org and
// www.example.org compare equal. Crude by intent — it is not a public-suffix list and
// would be wrong for a host under a multi-label suffix (foo.co.uk), which is why its
// only caller is the single-provider check above.
func registrableDomain(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	parts := strings.Split(strings.ToLower(host), ".")
	if len(parts) < 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// hrefsIn pulls the href values out of an attribution. It relies on the shape the
// validator already enforces — links written exactly as <a href="https://…"> — so it
// needs no HTML parser and cannot drift from what is storable.
func hrefsIn(attr string) []string {
	var out []string
	const open = `<a href="`
	for {
		i := strings.Index(attr, open)
		if i < 0 {
			return out
		}
		attr = attr[i+len(open):]
		j := strings.IndexByte(attr, '"')
		if j < 0 {
			return out
		}
		out = append(out, attr[:j])
		attr = attr[j:]
	}
}

// ---- resolveBasemap: degrade, never fail -----------------------------------

func TestAMalformedStoredDefaultDegradesRatherThanBreakingTheBootQuery(t *testing.T) {
	tenant := basemap.Basemap{TileURL: sp(tenantTiles), Attribution: sp(tenantCred)}

	for name, stored := range map[string]string{
		"not json":            `{`,
		"wrong type":          `{"zoom":"ten"}`,
		"rule-invalid":        `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png"}`, // no attribution
		"an empty value":      ``,
		"a json null":         `null`,
		"an out-of-range lat": `{"centerLat":9000,"centerLon":0}`,
	} {
		got := resolveBasemap(tenant, []byte(stored))
		require.NotNilf(t, got.TileURL, "%s: the tenant's own basemap must survive a bad operator default", name)
		require.Equalf(t, tenantTiles, *got.TileURL, "%s", name)
	}
}

// 🔴 The rule-invalid default must be DROPPED, not merely tolerated. A stored default
// carrying a tile URL with no credit line would otherwise be inherited by every
// non-overriding tenant and render uncredited — which is the exact outcome validation
// exists to prevent, arriving through the back door of a value stored before the gate.
func TestARuleInvalidStoredDefaultIsNotInheritedByATenantWithNoOverride(t *testing.T) {
	stored := []byte(`{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png"}`)

	got := resolveBasemap(basemap.Basemap{}, stored)

	require.False(t, basemap.IsSet(got),
		"an uncredited operator default must not be served to tenants; got %v", got.TileURL)
}

// ---- basemapFromTenant: the column wiring ----------------------------------

// 🔴 Every value here is DISTINCT, and that is the point. A fixture where the fields
// share a value (or are all null) cannot see crossed wires — lat read from lon, the
// credit line read from the tile URL — which is the defect class that got through in
// #704 and had to be caught in review.
func TestBasemapFromTenantReadsEachColumnFromItsOwnField(t *testing.T) {
	tenant := &iam.Tenant{
		BasemapTileURL:     sp(tenantTiles),
		BasemapAttribution: sp(tenantCred),
		BasemapCenterLat:   fp(33.75),
		BasemapCenterLon:   fp(-84.39),
		BasemapZoom:        fp(11.5),
	}

	got := basemapFromTenant(tenant)

	require.Equal(t, tenantTiles, *got.TileURL)
	require.Equal(t, tenantCred, *got.Attribution)
	require.Equal(t, 33.75, *got.CenterLat)
	require.Equal(t, -84.39, *got.CenterLon)
	require.Equal(t, 11.5, *got.Zoom)
}

func TestBasemapFromTenantLeavesUnsetColumnsNil(t *testing.T) {
	got := basemapFromTenant(&iam.Tenant{})
	require.Nil(t, got.TileURL)
	require.Nil(t, got.Attribution)
	require.Nil(t, got.CenterLat)
	require.Nil(t, got.CenterLon)
	require.Nil(t, got.Zoom)
}

// ---- the input mapping ------------------------------------------------------

// The mirror of the test above, on the way IN. Distinct values for the same reason.
func TestTheInputMapsEachFieldToItsOwnColumn(t *testing.T) {
	in := tenantBasemapInput{
		TileUrl:     sp(tenantTiles),
		Attribution: sp(tenantCred),
		CenterLat:   fp(33.75),
		CenterLon:   fp(-84.39),
		Zoom:        fp(11.5),
	}

	got := in.toBasemap()

	require.Equal(t, tenantTiles, *got.TileURL)
	require.Equal(t, tenantCred, *got.Attribution)
	require.Equal(t, 33.75, *got.CenterLat)
	require.Equal(t, -84.39, *got.CenterLon)
	require.Equal(t, 11.5, *got.Zoom)
}

// A null field CLEARS that override — it does not leave the stored value in place.
// The mutation is a full replace precisely because the tile source is atomic.
func TestAnEmptyInputClearsEveryOverride(t *testing.T) {
	got := tenantBasemapInput{}.toBasemap()
	require.Nil(t, got.TileURL)
	require.Nil(t, got.Attribution)
	require.Nil(t, got.CenterLat)
	require.Nil(t, got.CenterLon)
	require.Nil(t, got.Zoom)
	require.NoError(t, basemap.Validate(got), "clearing everything is always a legal write")
}

// ---- the authority gate ------------------------------------------------------

func accessClaims(authorities ...string) *auth.Claims {
	return &auth.Claims{
		Tenant:      "acme",
		Username:    "someone@acme.example",
		TokenType:   auth.TokenTypeAccess,
		Authorities: authorities,
	}
}

// 🔴 The whole reason basemap:write exists as its own authority (ADR-079 §6). A
// holder of branding:write can restyle the console; it must NOT be able to rewrite
// the tile URL, because that URL carries the tenant's provider credential.
//
// Delete the auth.Authorize call in SetTenantBasemap and this is the test that
// notices — nothing else in the package does.
func TestSetTenantBasemapRefusesAHolderOfBrandingWriteAlone(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), accessClaims(string(auth.BrandingWrite)))
	r := &SchemaResolver{}

	_, err := r.SetTenantBasemap(ctx, struct{ Input tenantBasemapInput }{
		Input: tenantBasemapInput{TileUrl: sp(tenantTiles), Attribution: sp(tenantCred)},
	})

	require.ErrorIs(t, err, auth.ErrForbidden,
		"branding:write must not carry the map key with it")
}

func TestSetTenantBasemapRefusesAnUnauthenticatedCaller(t *testing.T) {
	r := &SchemaResolver{}
	_, err := r.SetTenantBasemap(context.Background(), struct{ Input tenantBasemapInput }{})
	require.Error(t, err, "an unauthenticated caller must never reach the write")
}

// 🔴 The POSITIVE CONTROL, and without it the two tests above prove only that the
// method can fail. A holder of basemap:write must get PAST the authority gate — shown
// here by feeding it an input that fails VALIDATION, so the error it returns is the
// validation error rather than the authorization one. A gate that refused everyone
// would fail this test.
func TestSetTenantBasemapAdmitsAHolderOfBasemapWriteAndThenValidates(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), accessClaims(string(auth.BasemapWrite)))
	r := &SchemaResolver{}

	_, err := r.SetTenantBasemap(ctx, struct{ Input tenantBasemapInput }{
		Input: tenantBasemapInput{TileUrl: sp(tenantTiles)}, // no attribution
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, auth.ErrForbidden, "basemap:write must be admitted by the gate")
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.Contains(t, err.Error(), "attribution",
		"having passed the gate, the caller must be stopped by validation instead")
}

// ---- the resolver's own field mapping ---------------------------------------

func TestTheResolverExposesEachFieldFromItsOwnValue(t *testing.T) {
	r := &TenantBasemapResolver{b: basemap.Basemap{
		TileURL:     sp(tenantTiles),
		Attribution: sp(tenantCred),
		CenterLat:   fp(33.75),
		CenterLon:   fp(-84.39),
		Zoom:        fp(11.5),
	}}

	require.Equal(t, tenantTiles, *r.TileUrl())
	require.Equal(t, tenantCred, *r.Attribution())
	require.Equal(t, 33.75, *r.CenterLat())
	require.Equal(t, -84.39, *r.CenterLon())
	require.Equal(t, 11.5, *r.Zoom())
}

// BasemapOverride is the RAW override — no cascade, no settings lookup. The editor
// depends on that difference to show set-vs-inherited, so a version that quietly
// folded in the operator default would make every tenant look as if it had chosen
// the operator's provider itself.
func TestBasemapOverrideReportsTheRawColumnsWithNoCascade(t *testing.T) {
	r := &TenantResolver{t: &iam.Tenant{}}
	got := r.BasemapOverride()
	require.Nil(t, got.TileUrl(), "an inheriting tenant's RAW override is null, whatever the operator has set")
	require.Nil(t, got.Attribution())
	require.Nil(t, got.Zoom())
}

// ---- prepareBasemapWrite: normalize and validate are one step ---------------

// 🔴 From the review: a client that means "clear this" may send "" rather than null.
// A stored blank is NOT an absent one — it wins the cascade and masks the operator's
// instance default, so the tenant gets a blank map on an instance that has one.
func TestABlankSubmissionIsStoredAsClearedRatherThanAsAnEmptyString(t *testing.T) {
	got, err := prepareBasemapWrite(tenantBasemapInput{
		TileUrl:     sp("   "),
		Attribution: sp(""),
	})

	require.NoError(t, err, "clearing every field is always a legal write")
	require.Nil(t, got.TileURL, "a whitespace tile URL must be stored as NULL, not as text")
	require.Nil(t, got.Attribution)
}

// The stored bytes must be the bytes that were validated.
func TestASubmittedValueIsStoredTrimmed(t *testing.T) {
	got, err := prepareBasemapWrite(tenantBasemapInput{
		TileUrl:     sp("  " + tenantTiles + "  "),
		Attribution: sp(" " + tenantCred + " "),
	})

	require.NoError(t, err)
	require.Equal(t, tenantTiles, *got.TileURL)
	require.Equal(t, tenantCred, *got.Attribution)
}

// The counterweight: normalizing must not swallow a real refusal.
func TestPreparingStillRefusesAnInvalidWrite(t *testing.T) {
	_, err := prepareBasemapWrite(tenantBasemapInput{TileUrl: sp(tenantTiles)})
	require.Error(t, err, "a tile URL with no credit line must still be refused")
	require.Contains(t, err.Error(), "attribution")
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The areas and assets a customer user sees, and the devices on them (ADR-081 decision 12).
// The fixture (scope_wire_test.go seedPlaces) puts acme-pump and globex-pump in ONE shared
// area and ONE shared asset, each in a place of its own, and reaches one more area through an
// UNTRACKED edge and leaves one empty. Every case that says "acme never sees globex" has an
// unscoped control beside it that DOES see globex, so a fixture that placed nothing could not
// pass.

func tokensOf(t *testing.T, s served, op string) []string {
	t.Helper()
	requireServed(t, s, op)
	return tokensAt(t, s.data, op, "results")
}

func total(t *testing.T, s served, op string) float64 {
	t.Helper()
	return s.data[op].(map[string]any)["pagination"].(map[string]any)["totalRecords"].(float64)
}

const (
	listAreas  = `{ areas(criteria: {pageNumber: 1, pageSize: 50}) { results { token } pagination { totalRecords } } }`
	listAssets = `{ assets(criteria: {pageNumber: 1, pageSize: 50}) { results { token } pagination { totalRecords } } }`
)

func TestScopedAreasListsOnlyTheAreasItsDevicesAreIn(t *testing.T) {
	f := newScopeFixture(t)

	acme := serve(t, f.member("customer:acme"), listAreas, nil)
	if got, want := tokensOf(t, acme, "areas"), []string{"floor-acme", "floor-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's member listed areas %v, want %v", got, want)
	}
	if n := total(t, acme, "areas"); n != 2 {
		t.Fatalf("acme's totalRecords = %v, want 2 (the areas it reaches, not the tenant's)", n)
	}
	globex := serve(t, f.member("customer:globex"), listAreas, nil)
	if got, want := tokensOf(t, globex, "areas"), []string{"floor-globex", "floor-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("globex's member listed areas %v, want %v", got, want)
	}
	// Positive control: the operator sees all five, including the untracked and the idle one.
	op := serve(t, f.operator(), listAreas, nil)
	if got := tokensOf(t, op, "areas"); len(got) != 5 {
		t.Fatalf("control: the operator listed %v, want all five areas", got)
	}
	if n := total(t, op, "areas"); n != 5 {
		t.Fatalf("control: the operator's totalRecords = %v, want 5", n)
	}
}

func TestScopedAssetsListsOnlyTheAssetsItsDevicesAreAssignedTo(t *testing.T) {
	f := newScopeFixture(t)

	acme := serve(t, f.member("customer:acme"), listAssets, nil)
	if got, want := tokensOf(t, acme, "assets"), []string{"asset-acme", "asset-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's member listed assets %v, want %v", got, want)
	}
	if n := total(t, acme, "assets"); n != 2 {
		t.Fatalf("acme's totalRecords = %v, want 2", n)
	}
	op := serve(t, f.operator(), listAssets, nil)
	if got := tokensOf(t, op, "assets"); len(got) != 3 {
		t.Fatalf("control: the operator listed %v, want all three assets", got)
	}
}

// By-token and by-id reads answer by OMISSION: an area outside the member's reach is absent,
// as a missing one is, and never FORBIDDEN, so a refusal never confirms it exists.
func TestScopedAreaAndAssetLookupsAnswerByOmission(t *testing.T) {
	f := newScopeFixture(t)
	ids := map[string]string{}
	for _, tok := range []string{"floor-acme", "floor-globex", "floor-shared", "floor-untracked", "floor-idle", "no-such-floor"} {
		got := f.api
		found, err := got.AreasByToken(f.operator(), []string{tok})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) == 1 {
			ids[tok] = fmt.Sprint(found[0].ID)
		}
	}
	const byToken = `{ areasByToken(tokens: ["floor-acme","floor-globex","floor-shared","floor-untracked","floor-idle","no-such-floor"]) { token } }`
	acme := serve(t, f.member("customer:acme"), byToken, nil)
	requireServed(t, acme, "areasByToken")
	if got, want := tokensAt(t, acme.data, "areasByToken"), []string{"floor-acme", "floor-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's areasByToken = %v, want %v", got, want)
	}
	if got := tokensAt(t, serve(t, f.operator(), byToken, nil).data, "areasByToken"); len(got) != 5 {
		t.Fatalf("control: the operator's areasByToken = %v, want the five that exist", got)
	}
	byID := fmt.Sprintf(`{ areasById(ids: [%q,%q,%q]) { token } }`, ids["floor-acme"], ids["floor-globex"], ids["floor-shared"])
	acmeByID := serve(t, f.member("customer:acme"), byID, nil)
	requireServed(t, acmeByID, "areasById")
	if got, want := tokensAt(t, acmeByID.data, "areasById"), []string{"floor-acme", "floor-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's areasById = %v, want %v", got, want)
	}
	if got := tokensAt(t, serve(t, f.operator(), byID, nil).data, "areasById"); len(got) != 3 {
		t.Fatalf("control: the operator's areasById = %v, want all three", got)
	}
	// Assets: the same two lookups.
	const assetsByToken = `{ assetsByToken(tokens: ["asset-acme","asset-globex","asset-shared"]) { token } }`
	if got, want := tokensAt(t, serve(t, f.member("customer:acme"), assetsByToken, nil).data, "assetsByToken"),
		[]string{"asset-acme", "asset-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acme's assetsByToken = %v, want %v", got, want)
	}
	found, err := f.api.AssetsByToken(f.operator(), []string{"asset-acme", "asset-globex"})
	if err != nil || len(found) != 2 {
		t.Fatalf("assets: %v %v", found, err)
	}
	byAssetID := fmt.Sprintf(`{ assetsById(ids: [%q,%q]) { token } }`, fmt.Sprint(found[0].ID), fmt.Sprint(found[1].ID))
	got := tokensAt(t, serve(t, f.member("customer:acme"), byAssetID, nil).data, "assetsById")
	if !reflect.DeepEqual(got, []string{"asset-acme"}) {
		t.Fatalf("acme's assetsById = %v, want [asset-acme]", got)
	}
}

// D2: the operator's free-form metadata (and an asset's property document) is withheld from a
// scoped member as NULL, while the rest of the row and the type it points at are shown.
func TestScopedAreaAndAssetMetadataIsWithheld(t *testing.T) {
	f := newScopeFixture(t)
	const area = `{ areasByToken(tokens: ["floor-shared"]) { token name metadata areaType { token } } }`
	const asset = `{ assetsByToken(tokens: ["asset-shared"]) { token name metadata properties assetType { token } } }`

	row := func(s served, op string) map[string]any {
		t.Helper()
		requireServed(t, s, op)
		rows := s.data[op].([]any)
		if len(rows) != 1 {
			t.Fatalf("%s: %v", op, rows)
		}
		return rows[0].(map[string]any)
	}
	a := row(serve(t, f.member("customer:acme"), area, nil), "areasByToken")
	if a["metadata"] != nil {
		t.Fatalf("a scoped member was shown area metadata: %v", a["metadata"])
	}
	if a["name"] != "Name of floor-shared" || a["areaType"].(map[string]any)["token"] != "floor" {
		t.Fatalf("a scoped member lost the visible fields: %v", a)
	}
	// Positive control: the operator's metadata is intact.
	if op := row(serve(t, f.operator(), area, nil), "areasByToken"); op["metadata"] == nil {
		t.Fatalf("control: the operator's area metadata is gone: %v", op)
	}
	b := row(serve(t, f.member("customer:acme"), asset, nil), "assetsByToken")
	if b["metadata"] != nil || b["properties"] != nil {
		t.Fatalf("a scoped member was shown asset metadata or properties: %v", b)
	}
	if b["name"] != "Name of asset-shared" || b["assetType"].(map[string]any)["token"] != "machine" {
		t.Fatalf("a scoped member lost the visible fields: %v", b)
	}
	if op := row(serve(t, f.operator(), asset, nil), "assetsByToken"); op["metadata"] == nil || op["properties"] == nil {
		t.Fatalf("control: the operator's asset metadata or properties are gone: %v", op)
	}
}

// Area and asset types are shared configuration, readable by a scoped member in full.
func TestScopedMemberReadsAreaAndAssetTypes(t *testing.T) {
	f := newScopeFixture(t)
	for _, q := range []struct{ query, op, want string }{
		{`{ areaTypes(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`, "areaTypes", "floor"},
		{`{ areaTypesByToken(tokens: ["floor"]) { token } }`, "areaTypesByToken", "floor"},
		{`{ assetTypes(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`, "assetTypes", "machine"},
		{`{ assetTypesByToken(tokens: ["machine"]) { token } }`, "assetTypesByToken", "machine"},
	} {
		s := serve(t, f.member("customer:acme"), q.query, nil)
		requireServed(t, s, q.op)
		var got []string
		if _, paged := s.data[q.op].(map[string]any); paged {
			got = tokensAt(t, s.data, q.op, "results")
		} else {
			got = tokensAt(t, s.data, q.op)
		}
		if !reflect.DeepEqual(got, []string{q.want}) {
			t.Fatalf("%s: %v, want [%s]", q.op, got, q.want)
		}
	}
}

// The writes, the relationship graph and the device anchors stay closed to a scoped member.
func TestScopedMemberStillCannotWriteAreasOrReadTheGraph(t *testing.T) {
	f := newScopeFixture(t)
	writer := f.withClaimsScoped("customer:acme", "device:read", "device:write")
	for name, q := range map[string]string{
		"createArea":          `mutation { createArea(request: {token: "x", areaTypeToken: "floor"}) { token } }`,
		"updateArea":          `mutation { updateArea(token: "floor-shared", request: {name: "x"}) { token } }`,
		"deleteArea":          `mutation { deleteArea(token: "floor-shared") }`,
		"createAreaType":      `mutation { createAreaType(request: {token: "x"}) { token } }`,
		"createAsset":         `mutation { createAsset(request: {token: "x", assetTypeToken: "machine"}) { token } }`,
		"deleteAsset":         `mutation { deleteAsset(token: "asset-shared") }`,
		"entityRelationships": `{ entityRelationships(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`,
		"deviceScopeAnchors":  `{ deviceScopeAnchors(deviceTokens: ["acme-pump"]) { deviceToken } }`,
	} {
		res := serve(t, writer, q, nil)
		if len(res.errs) == 0 {
			t.Errorf("%s was served to a scoped member: %v", name, res.data)
			continue
		}
		if !isScopeRefusal(res.errs[0]) {
			// GraphQL-level mismatches (an argument shape) prove nothing: the refusal must be the scope's.
			t.Errorf("%s was refused, but not by the scope: %v", name, res.errs)
		}
	}
}

func (f scopeFixture) withClaimsScoped(anchor string, authorities ...string) context.Context {
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant, Authorities: authorities, ScopeAnchors: []string{anchor}})
}

const anchorDevicesQuery = `query($type: String!, $target: String!, $rel: String) {
  anchorDevices(criteria: {pageNumber: 1, pageSize: 50, targetType: $type, target: $target, relationshipType: $rel}) {
    results { token } pagination { totalRecords } } }`

type anchorAnswer struct {
	tokens []string
	total  float64
	code   string
	// scope: the refusal was the scope's (FORBIDDEN with the SCOPE_UNSUPPORTED reason).
	scope bool
}

func askAnchor(t *testing.T, ctx context.Context, kind, target string, rel *string) anchorAnswer {
	t.Helper()
	vars := map[string]any{"type": kind, "target": target, "rel": nil}
	if rel != nil {
		vars["rel"] = *rel
	}
	s := serve(t, ctx, anchorDevicesQuery, vars)
	if len(s.errs) > 0 {
		code, _ := s.errs[0].Extensions["code"].(string)
		if s.data != nil && s.data["anchorDevices"] != nil {
			t.Fatalf("anchorDevices answered data beside an error: %v", s.data)
		}
		if code == "" {
			code = "UNCODED: " + s.errs[0].Message
		}
		return anchorAnswer{code: code, scope: isScopeRefusal(s.errs[0])}
	}
	page := s.data["anchorDevices"].(map[string]any)
	return anchorAnswer{tokens: tokensAt(t, s.data, "anchorDevices", "results"), total: page["pagination"].(map[string]any)["totalRecords"].(float64)}
}

func str(s string) *string { return &s }

// The negative controls of the spec: in the SHARED area, acme never sees globex's device, nor
// a count or total that includes it; the operator does see both.
func TestAnchorDevicesCountsOnlyTheCallersOwnDevices(t *testing.T) {
	f := newScopeFixture(t)

	for _, kind := range []struct{ kind, shared string }{{"area", "floor-shared"}, {"asset", "asset-shared"}} {
		acme := askAnchor(t, f.member("customer:acme"), kind.kind, kind.shared, nil)
		if !reflect.DeepEqual(acme.tokens, []string{"acme-pump"}) || acme.total != 1 {
			t.Fatalf("acme in %s: %+v, want exactly acme-pump and totalRecords 1", kind.shared, acme)
		}
		globex := askAnchor(t, f.member("customer:globex"), kind.kind, kind.shared, nil)
		if !reflect.DeepEqual(globex.tokens, []string{"globex-pump"}) || globex.total != 1 {
			t.Fatalf("globex in %s: %+v, want exactly globex-pump and totalRecords 1", kind.shared, globex)
		}
		// Positive control: unscoped, both, total 2.
		op := askAnchor(t, f.operator(), kind.kind, kind.shared, nil)
		if !reflect.DeepEqual(op.tokens, []string{"acme-pump", "globex-pump"}) && !reflect.DeepEqual(op.tokens, []string{"globex-pump", "acme-pump"}) || op.total != 2 {
			t.Fatalf("control: the operator in %s: %+v, want both devices and totalRecords 2", kind.shared, op)
		}
	}
	// An anchor with none of acme's devices on it is empty for acme: no tokens, total 0.
	for _, other := range []struct{ kind, token string }{{"area", "floor-globex"}, {"area", "floor-idle"}, {"asset", "asset-globex"}, {"area", "no-such-floor"}} {
		got := askAnchor(t, f.member("customer:acme"), other.kind, other.token, nil)
		if len(got.tokens) != 0 || got.total != 0 || got.code != "" {
			t.Fatalf("acme in %s: %+v, want an empty page", other.token, got)
		}
	}
	if op := askAnchor(t, f.operator(), "area", "floor-globex", nil); !reflect.DeepEqual(op.tokens, []string{"globex-pump"}) {
		t.Fatalf("control: the operator in floor-globex: %+v", op)
	}
}

func TestAnchorDevicesOfACustomerStaysRefusedOutsideTheScope(t *testing.T) {
	f := newScopeFixture(t)
	acme := f.member("customer:acme")
	if got := askAnchor(t, acme, "customer", "acme", nil); !reflect.DeepEqual(got.tokens, []string{"acme-pump"}) {
		t.Fatalf("acme's own customer anchor: %+v", got)
	}
	if got := askAnchor(t, acme, "customer", "globex", nil); got.code != "FORBIDDEN" {
		t.Fatalf("acme asking about globex: %+v, want FORBIDDEN", got)
	}
	// An existing and a non-existent customer are refused alike.
	if got := askAnchor(t, acme, "customer", "nobody", nil); got.code != "FORBIDDEN" {
		t.Fatalf("acme asking about a nonexistent customer: %+v, want FORBIDDEN", got)
	}
	// Control: the operator may ask about globex.
	if got := askAnchor(t, f.operator(), "customer", "globex", nil); !reflect.DeepEqual(got.tokens, []string{"globex-pump"}) {
		t.Fatalf("control: the operator about globex: %+v", got)
	}
	// A kind with no scoped semantics is refused, never answered empty; an unscoped caller is
	// told the kind is not one it can ask about.
	for _, kind := range []string{"group", "device", "bogus"} {
		if got := askAnchor(t, acme, kind, "x", nil); !got.scope {
			t.Fatalf("a scoped %s anchor: %+v, want the scope's refusal", kind, got)
		}
		if got := askAnchor(t, f.operator(), kind, "x", nil); got.code == "" || got.scope {
			t.Fatalf("an unscoped %s anchor: %+v, want a plain refusal", kind, got)
		}
	}
}

// Only an ownership (tracked) edge reaches an area for a scoped caller; the unscoped control
// sees the untracked one.
func TestAnchorDevicesIgnoresUntrackedEdgesForAScopedCaller(t *testing.T) {
	f := newScopeFixture(t)
	acme := f.member("customer:acme")
	if got := askAnchor(t, acme, "area", "floor-untracked", nil); len(got.tokens) != 0 || got.total != 0 {
		t.Fatalf("an area reached only by an untracked edge: %+v", got)
	}
	// A scoped caller may not name a relationship type at all: refused, tracked or not, so
	// it cannot probe which types link its devices to an anchor.
	for _, rel := range []string{"monitors", "assigned", "no-such-type"} {
		if got := askAnchor(t, acme, "area", "floor-shared", str(rel)); !got.scope {
			t.Fatalf("a scoped relationshipType %q: %+v, want the scope's refusal", rel, got)
		}
	}
	// Control: unscoped, the untracked edge is there, with or without the type.
	if got := askAnchor(t, f.operator(), "area", "floor-untracked", nil); !reflect.DeepEqual(got.tokens, []string{"acme-pump"}) {
		t.Fatalf("control: the operator without a type: %+v", got)
	}
	if got := askAnchor(t, f.operator(), "area", "floor-untracked", str("monitors")); !reflect.DeepEqual(got.tokens, []string{"acme-pump"}) {
		t.Fatalf("control: the operator with the untracked type: %+v", got)
	}
}

// Visibility is current: a device moved off an area, or sold to another customer, changes the
// answer on the very next read.
func TestAreaVisibilityFollowsPlacementAndOwnership(t *testing.T) {
	f := newScopeFixture(t)
	ctx := f.operator()
	acme := f.member("customer:acme")
	areas := func(c context.Context) []string { return tokensOf(t, serve(t, c, listAreas, nil), "areas") }

	if got := areas(acme); !reflect.DeepEqual(got, []string{"floor-acme", "floor-shared"}) {
		t.Fatalf("before: %v", got)
	}
	// The device leaves floor-acme.
	if _, err := f.api.RemoveEntityRelationship(ctx, "acme-pump-in-floor-acme"); err != nil {
		t.Fatal(err)
	}
	if got := areas(acme); !reflect.DeepEqual(got, []string{"floor-shared"}) {
		t.Fatalf("after the device left floor-acme: %v, want [floor-shared]", got)
	}
	if got := askAnchor(t, acme, "area", "floor-acme", nil); len(got.tokens) != 0 {
		t.Fatalf("anchorDevices of the area it left: %+v", got)
	}
	// It is then SOLD to globex: acme owns no device, so it reaches no area at all, and globex
	// reaches the shared floor with both devices of its own.
	if _, err := f.api.RemoveEntityRelationship(ctx, "acme-pump-to-acme"); err != nil {
		t.Fatal(err)
	}
	assigned, err := f.api.EnsureAssignmentType(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.CreateEntityRelationship(ctx, &model.EntityRelationshipCreateRequest{
		Token: "acme-pump-to-globex", SourceType: "device", Source: "acme-pump",
		TargetType: "customer", Target: "globex", RelationshipType: assigned.Token}); err != nil {
		t.Fatal(err)
	}
	if got := areas(acme); len(got) != 0 {
		t.Fatalf("after the sale acme still reaches %v", got)
	}
	if got := askAnchor(t, acme, "area", "floor-shared", nil); len(got.tokens) != 0 || got.total != 0 {
		t.Fatalf("after the sale acme still sees devices on floor-shared: %+v", got)
	}
	globex := f.member("customer:globex")
	if got := askAnchor(t, globex, "area", "floor-shared", nil); got.total != 2 {
		t.Fatalf("after the sale globex sees %+v on floor-shared, want both devices", got)
	}
}

// A member limited to a GROUP reaches the places of the group's devices through the Device
// rule alone: north holds acme-pump, so it sees floor-shared and floor-acme and only that
// device on floor-shared.
func TestGroupScopedMemberReachesTheAreasOfItsGroupsDevices(t *testing.T) {
	f := newGroupScopeFixture(t)
	north := f.member("group:north")
	if got, want := tokensOf(t, serve(t, north, listAreas, nil), "areas"), []string{"floor-acme", "floor-shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group:north listed %v, want %v", got, want)
	}
	if got := askAnchor(t, north, "area", "floor-shared", nil); !reflect.DeepEqual(got.tokens, []string{"acme-pump"}) || got.total != 1 {
		t.Fatalf("group:north on floor-shared: %+v, want only acme-pump", got)
	}
	if got := askAnchor(t, north, "area", "floor-globex", nil); len(got.tokens) != 0 {
		t.Fatalf("group:north on floor-globex: %+v", got)
	}
}

// LOW-1: a type's free-form metadata is the operator's note about it, withheld from a scoped
// member as NULL like an area's, while its token, name and look stay readable. The unscoped
// control reads the metadata, so the null is the scope's doing. An asset type's propertySchema
// is the SHAPE of an asset's property document (field names and types), which a customer's
// console needs to render the asset it may see, and is deliberately left visible.
func TestScopedTypeMetadataIsWithheld(t *testing.T) {
	f := newScopeFixture(t)
	for _, q := range []struct{ query, op string }{
		{`{ areaTypesByToken(tokens: ["floor"]) { token metadata } }`, "areaTypesByToken"},
		{`{ assetTypesByToken(tokens: ["machine"]) { token metadata propertySchema } }`, "assetTypesByToken"},
		{`{ customerTypesByToken(tokens: ["retail"]) { token metadata } }`, "customerTypesByToken"},
	} {
		row := func(c context.Context) map[string]any {
			s := serve(t, c, q.query, nil)
			requireServed(t, s, q.op)
			return s.data[q.op].([]any)[0].(map[string]any)
		}
		if got := row(f.member("customer:acme")); got["metadata"] != nil || got["token"] == nil {
			t.Fatalf("%s: a scoped member was shown type metadata, or lost the token: %v", q.op, got)
		}
		if got := row(f.operator()); got["metadata"] == nil {
			t.Fatalf("control, %s: the operator's type metadata is gone: %v", q.op, got)
		}
	}
}

// INFO-2: the withholding test fails CLOSED. A context carrying no claims is treated as
// scoped, so a wiring fault withholds rather than discloses.
func TestIsScopedCallerFailsClosedWithoutClaims(t *testing.T) {
	if !isScopedCaller(context.Background()) {
		t.Fatal("a context with no claims was treated as unscoped")
	}
	f := newScopeFixture(t)
	if isScopedCaller(f.operator()) {
		t.Fatal("control: the operator was treated as scoped")
	}
	if !isScopedCaller(f.member("customer:acme")) {
		t.Fatal("control: a scoped member was treated as unscoped")
	}
	// And through a resolver: the metadata of an area served on a claimless context is null.
	areas, err := f.api.AreasByToken(f.operator(), []string{"floor-shared"})
	if err != nil || len(areas) != 1 {
		t.Fatalf("areas: %v %v", areas, err)
	}
	if got := (&AreaResolver{M: *areas[0]}).Metadata(context.Background()); got != nil {
		t.Fatalf("an area served without claims showed metadata: %v", *got)
	}
}

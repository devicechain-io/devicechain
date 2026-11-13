// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"

	"github.com/devicechain-io/dc-device-management/model"
)

// Installed positions are served through the REAL schema against a REAL sqlite-backed Api, as the
// scope tests are: the properties under test are authority checks on a resolver and a scope rule on
// a row, and a test that called the resolver method would measure its own fixture.

func (f scopeFixture) caller(authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant, Authorities: strs})
}

func (f scopeFixture) scopedCaller(anchor string, authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: strs, ScopeAnchors: []string{anchor}})
}

func (f scopeFixture) install(t *testing.T, family, token string, lat, lon float64) {
	t.Helper()
	src := "SURVEY"
	if _, err := f.api.SetInstalledPosition(f.operator(), family, token,
		model.InstalledPositionInput{Latitude: lat, Longitude: lon, Source: &src}); err != nil {
		t.Fatalf("install %s %s: %v", family, token, err)
	}
}

func migrateDetection(t *testing.T, f scopeFixture) {
	t.Helper()
	if err := f.db.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&model.DetectionRule{}, &model.DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate detection rules: %v", err)
	}
}

func codeOf(s served) string {
	if len(s.errs) == 0 {
		return ""
	}
	code, _ := s.errs[0].Extensions["code"].(string)
	if code == "" {
		return "UNCODED: " + s.errs[0].Message
	}
	return code
}

const devicePositionQuery = `query($t: [String!]!) { devicesByToken(tokens: $t) {
  token positionWithheld installedPosition { latitude longitude elevation accuracy source updatedAt } } }`

// 🔴 THE ACCEPTANCE TEST. A caller holding ONLY location:installed:read, on a device that declares
// it reports its position and has an installed position, is handed the installed position and
// nothing else. device-management holds no reported fix to fall back to, and the schema serves no
// field that would choose one: a position that can be a live fix belongs to the slice that wires a
// reader for reported fixes, under location:read.
func TestInstalledOnlyCallerSeesTheInstalledPositionNeverALiveFix(t *testing.T) {
	f := newScopeFixture(t)
	migrateDetection(t, f)
	// The device declares that it reports its position, and is installed somewhere fixed.
	seedDevice(t, f.operator(), "tracker-1", "tracker", &model.LocationDeclaration{})
	publishProfile(t, f.operator(), "tracker")
	f.install(t, "DEVICE", "tracker-1", 40.7128, -74.0060)

	installedOnly := f.caller(auth.DeviceRead, auth.LocationInstalledRead)
	got := serve(t, installedOnly, devicePositionQuery, map[string]any{"t": []any{"tracker-1"}})
	if len(got.errs) != 0 {
		t.Fatalf("installedPosition errored for the installed-only caller: %v", got.errs)
	}
	dev := got.data["devicesByToken"].([]any)[0].(map[string]any)
	pos, _ := dev["installedPosition"].(map[string]any)
	if pos == nil || pos["latitude"] != 40.7128 || pos["longitude"] != -74.006 {
		t.Fatalf("the installed-only caller was not handed the installed position: %v", dev)
	}
	if dev["positionWithheld"] != false {
		t.Fatalf("positionWithheld = %v for a caller holding the authority", dev["positionWithheld"])
	}

	// The schema serves no field that could hand this caller a live fix.
	live := serve(t, installedOnly, `query($t: [String!]!) { devicesByToken(tokens: $t) { effectivePosition { latitude } } }`,
		map[string]any{"t": []any{"tracker-1"}})
	if len(live.errs) == 0 {
		t.Fatal("effectivePosition is served; it belongs to the slice that wires a reported-fix reader")
	}

	// And location:read implies the installed authority: it reads installedPosition too.
	full := f.caller(auth.DeviceRead, auth.LocationRead)
	both := serve(t, full, devicePositionQuery, map[string]any{"t": []any{"tracker-1"}})
	if p := both.data["devicesByToken"].([]any)[0].(map[string]any)["installedPosition"]; p == nil {
		t.Fatal("location:read did not imply location:installed:read")
	}
}

func errMessages(s served) []string {
	out := []string{}
	for _, e := range s.errs {
		out = append(out, e.Message)
	}
	return out
}

// 🔴 A caller with neither authority sees positionWithheld:true on EVERY entity, with and without a
// stored position, and the two payloads differ in nothing but the token: the field cannot be used
// to find out which devices have a position.
//
// Negative control (run): emit positionWithheld only when a position exists and this goes red.
func TestACallerWithNeitherAuthorityCannotTellWhichEntitiesHavePositions(t *testing.T) {
	f := newScopeFixture(t)
	f.install(t, "DEVICE", "acme-pump", 40.7, -74.0)
	f.install(t, "ASSET", "asset-acme", 40.7, -74.0)
	nobody := f.caller(auth.DeviceRead)

	strip := func(v any) any {
		b, _ := json.Marshal(v)
		return strings.NewReplacer(`"acme-pump"`, `"X"`, `"globex-pump"`, `"X"`, `"asset-acme"`, `"X"`, `"asset-globex"`, `"X"`).Replace(string(b))
	}
	for _, c := range []struct {
		name, query, field, withPos, without string
	}{
		{"device", `{ devicesByToken(tokens: ["%s"]) { token positionWithheld installedPosition { latitude } } }`, "devicesByToken", "acme-pump", "globex-pump"},
		{"asset", `{ assetsByToken(tokens: ["%s"]) { token positionWithheld installedPosition { latitude } } }`, "assetsByToken", "asset-acme", "asset-globex"},
	} {
		ask := func(token string) served {
			return serve(t, nobody, strings.Replace(c.query, "%s", token, 1), nil)
		}
		a, b := ask(c.withPos), ask(c.without)
		if len(a.errs) != 0 || len(b.errs) != 0 {
			t.Fatalf("%s: errors %v %v", c.name, a.errs, b.errs)
		}
		for _, s := range []served{a, b} {
			row := s.data[c.field].([]any)[0].(map[string]any)
			if row["positionWithheld"] != true || row["installedPosition"] != nil {
				t.Fatalf("%s: a caller with no position authority read %v", c.name, row)
			}
		}
		if !reflect.DeepEqual(strip(a.data), strip(b.data)) {
			t.Fatalf("%s: the payload differs between an entity with a position and one without: %v vs %v", c.name, a.data, b.data)
		}
	}

	// The control: a holder sees the position and withheld false, and an entity without one is null
	// but still not withheld.
	holder := f.caller(auth.DeviceRead, auth.LocationInstalledRead)
	have := serve(t, holder, `{ devicesByToken(tokens: ["acme-pump", "globex-pump"]) { token positionWithheld installedPosition { latitude } } }`, nil)
	rows := have.data["devicesByToken"].([]any)
	for _, r := range rows {
		row := r.(map[string]any)
		if row["positionWithheld"] != false {
			t.Fatalf("a holder read withheld=true: %v", row)
		}
		if (row["token"] == "acme-pump") != (row["installedPosition"] != nil) {
			t.Fatalf("a holder's installedPosition does not follow the stored position: %v", row)
		}
	}
}

// A scoped customer member cannot read or set positions outside its scope: the entity is NOT_FOUND
// for the write and absent for the read. Within scope it reads, and a write is refused by name.
func TestAScopedMemberCannotReadOrSetPositionsOutOfScope(t *testing.T) {
	f := newScopeFixture(t)
	f.install(t, "DEVICE", "globex-pump", 1, 2)
	f.install(t, "DEVICE", "acme-pump", 3, 4)
	member := f.scopedCaller("customer:acme", auth.DeviceRead, auth.DeviceWrite, auth.LocationInstalledRead)

	read := serve(t, member, `{ devicesByToken(tokens: ["acme-pump", "globex-pump"]) { token installedPosition { latitude } } }`, nil)
	if len(read.errs) != 0 {
		t.Fatalf("scoped read: %v", read.errs)
	}
	rows := read.data["devicesByToken"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["token"] != "acme-pump" || rows[0].(map[string]any)["installedPosition"] == nil {
		t.Fatalf("the member must see only its own device, with its position: %v", rows)
	}

	const set = `mutation($f: PositionFamily!, $t: String!) { setInstalledPosition(family: $f, token: $t,
		position: {latitude: 10, longitude: 20}) { latitude } }`
	if got := serve(t, member, set, map[string]any{"f": "DEVICE", "t": "globex-pump"}); codeOf(got) != "NOT_FOUND" {
		t.Fatalf("set out of scope: %v, want NOT_FOUND", got.errs)
	}
	const clear = `mutation($f: PositionFamily!, $t: String!) { clearInstalledPosition(family: $f, token: $t) }`
	if got := serve(t, member, clear, map[string]any{"f": "DEVICE", "t": "globex-pump"}); codeOf(got) != "NOT_FOUND" {
		t.Fatalf("clear out of scope: %v, want NOT_FOUND", got.errs)
	}
	// Neither touched globex's pump.
	untouched, _ := f.api.DevicesByToken(f.operator(), []string{"globex-pump"})
	if *untouched[0].InstalledLat != 1 {
		t.Fatalf("an out-of-scope write landed: %v", untouched[0].InstalledPosition)
	}
	// Within scope the member still may not WRITE: scoped members read, and the refusal is named.
	if got := serve(t, member, set, map[string]any{"f": "DEVICE", "t": "acme-pump"}); codeOf(got) != "FORBIDDEN" {
		t.Fatalf("set in scope by a scoped member: %v, want FORBIDDEN", got.errs)
	}
	if kept, _ := f.api.DevicesByToken(f.operator(), []string{"acme-pump"}); *kept[0].InstalledLat != 3 {
		t.Fatalf("a scoped member's write landed: %v", kept[0].InstalledPosition)
	}
	// And an asset outside the scope is NOT_FOUND too.
	if got := serve(t, member, set, map[string]any{"f": "ASSET", "t": "asset-globex"}); codeOf(got) != "NOT_FOUND" {
		t.Fatalf("set asset out of scope: %v, want NOT_FOUND", got.errs)
	}
	// A reader (no device:write) cannot set at all.
	reader := f.caller(auth.DeviceRead, auth.LocationInstalledRead)
	if got := serve(t, reader, set, map[string]any{"f": "DEVICE", "t": "acme-pump"}); codeOf(got) != "FORBIDDEN" {
		t.Fatalf("set without device:write: %v, want FORBIDDEN", got.errs)
	}
}

func TestLatitude91IsRefusedBeforeAnythingIsStored(t *testing.T) {
	f := newScopeFixture(t)
	op := f.operator()
	for _, c := range []struct{ name, pos string }{
		{"latitude 91", `{latitude: 91, longitude: 0}`},
		{"latitude -91", `{latitude: -91, longitude: 0}`},
		{"longitude 181", `{latitude: 0, longitude: 181}`},
		{"negative accuracy", `{latitude: 0, longitude: 0, accuracy: -1}`},
	} {
		got := serve(t, op, `mutation { setInstalledPosition(family: DEVICE, token: "acme-pump", position: `+c.pos+`) { latitude } }`, nil)
		if codeOf(got) != "INVALID_ARGUMENT" && len(got.errs) == 0 {
			t.Fatalf("%s was accepted", c.name)
		}
		if len(got.errs) == 0 {
			t.Fatalf("%s: no error", c.name)
		}
	}
	d, _ := f.api.DevicesByToken(op, []string{"acme-pump"})
	if d[0].InstalledLat != nil {
		t.Fatalf("a refused position was stored: %v", d[0].InstalledPosition)
	}
	// An unknown source is refused too, at the schema (enum) and by the model.
	if got := serve(t, op, `mutation { setInstalledPosition(family: DEVICE, token: "acme-pump", position: {latitude: 1, longitude: 1, source: GUESS}) { latitude } }`, nil); len(got.errs) == 0 {
		t.Fatal("an unknown source was accepted")
	}
}

func TestSetClearAndTheRoundTripOnADeviceAndAnAsset(t *testing.T) {
	f := newScopeFixture(t)
	op := f.operator()
	for _, c := range []struct{ family, token, field string }{
		{"DEVICE", "acme-pump", "devicesByToken"}, {"ASSET", "asset-acme", "assetsByToken"},
	} {
		set := serve(t, op, `mutation($f: PositionFamily!, $t: String!) { setInstalledPosition(family: $f, token: $t,
			position: {latitude: 40.12345678, longitude: -74.5, elevation: 12.5, accuracy: 3}) { latitude longitude elevation accuracy source updatedAt } }`,
			map[string]any{"f": c.family, "t": c.token})
		if len(set.errs) != 0 {
			t.Fatalf("%s set: %v", c.family, set.errs)
		}
		p := set.data["setInstalledPosition"].(map[string]any)
		if p["latitude"] != 40.12345678 || p["elevation"] != 12.5 || p["source"] != "MANUAL" || p["updatedAt"] == nil {
			t.Fatalf("%s set returned %v", c.family, p)
		}
		read := serve(t, op, `{ `+c.field+`(tokens: ["`+c.token+`"]) { installedPosition { latitude longitude elevation accuracy source } } }`, nil)
		if !reflect.DeepEqual(read.data[c.field].([]any)[0].(map[string]any)["installedPosition"], map[string]any{
			"latitude": 40.12345678, "longitude": -74.5, "elevation": 12.5, "accuracy": 3.0, "source": "MANUAL"}) {
			t.Fatalf("%s read back %v", c.family, read.data)
		}
		clr := serve(t, op, `mutation($f: PositionFamily!, $t: String!) { clearInstalledPosition(family: $f, token: $t) }`,
			map[string]any{"f": c.family, "t": c.token})
		if len(clr.errs) != 0 || clr.data["clearInstalledPosition"] != true {
			t.Fatalf("%s clear: %v %v", c.family, clr.data, clr.errs)
		}
		read = serve(t, op, `{ `+c.field+`(tokens: ["`+c.token+`"]) { installedPosition { latitude } } }`, nil)
		if read.data[c.field].([]any)[0].(map[string]any)["installedPosition"] != nil {
			t.Fatalf("%s: the position survived a clear", c.family)
		}
	}
}

// 🔴 A device or asset edit must not write the installed position back. UpdateDevice loads the row and
// saves it whole; without the write guard on the embedded columns, an edit that loaded the device
// BEFORE a setInstalledPosition would put the old (empty) position back.
//
// Negative control (run): drop `<-:false` from the InstalledPosition columns and this goes red.
func TestAnEditThatLoadedTheRowBeforeASetDoesNotRevertThePosition(t *testing.T) {
	f := newScopeFixture(t)
	op := f.operator()
	stale, err := f.api.DevicesByToken(op, []string{"acme-pump"})
	if err != nil || len(stale) != 1 {
		t.Fatalf("load: %v", err)
	}
	f.install(t, "DEVICE", "acme-pump", 7, 8)
	if err := f.api.RDB.DB(op).Save(stale[0]).Error; err != nil {
		t.Fatalf("stale save: %v", err)
	}
	fresh, _ := f.api.DevicesByToken(op, []string{"acme-pump"})
	if fresh[0].InstalledLat == nil || *fresh[0].InstalledLat != 7 {
		t.Fatalf("a stale whole-row save reverted the installed position: %v", fresh[0].InstalledPosition)
	}
	if got := serve(t, op, `mutation { updateDevice(token: "acme-pump", request: {name: "renamed"}) { token } }`, nil); len(got.errs) != 0 {
		t.Fatalf("update: %v", got.errs)
	}
	fresh, _ = f.api.DevicesByToken(op, []string{"acme-pump"})
	if *fresh[0].InstalledLat != 7 {
		t.Fatalf("UpdateDevice reverted the installed position: %v", fresh[0].InstalledPosition)
	}
}

func TestAnAreaBoundaryNamesAFenceThatExists(t *testing.T) {
	f := newScopeFixture(t)
	if err := f.db.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&model.GeoFence{}, &model.GeoFenceSetVersion{}, &model.GeoFenceGeometryBlob{}); err != nil {
		t.Fatalf("migrate fences: %v", err)
	}
	op := f.operator()
	if got := serve(t, op, `mutation { updateArea(token: "floor-acme", request: {boundaryFenceToken: "no-such-fence"}) { token } }`, nil); codeOf(got) != "NOT_FOUND" {
		t.Fatalf("an unknown boundary fence: %v, want NOT_FOUND", got.errs)
	}
	if _, err := f.api.CreateGeoFence(op, &model.GeoFenceCreateRequest{
		Token: "campus", Geometry: `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[0,1],[1,1],[1,0],[0,0]]]}}`}); err != nil {
		t.Fatalf("fence: %v", err)
	}
	const read = `{ areasByToken(tokens: ["floor-acme"]) { boundaryFence { token kind } } }`
	if got := serve(t, op, read, nil); got.data["areasByToken"].([]any)[0].(map[string]any)["boundaryFence"] != nil {
		t.Fatalf("an area with no boundary read %v", got.data)
	}
	if got := serve(t, op, `mutation { updateArea(token: "floor-acme", request: {boundaryFenceToken: "campus"}) { token } }`, nil); len(got.errs) != 0 {
		t.Fatalf("link: %v", got.errs)
	}
	got := serve(t, op, read, nil)
	if fence := got.data["areasByToken"].([]any)[0].(map[string]any)["boundaryFence"].(map[string]any); fence["token"] != "campus" {
		t.Fatalf("boundaryFence = %v", fence)
	}
	// A scoped caller gets null, not the geometry.
	scoped := f.scopedCaller("customer:acme", auth.DeviceRead)
	if s := serve(t, scoped, `{ areasByToken(tokens: ["floor-acme"]) { token boundaryFence { token } } }`, nil); len(s.errs) != 0 {
		t.Fatalf("scoped read: %v", s.errs)
	}
	// Unlinking: null clears it.
	if got := serve(t, op, `mutation { updateArea(token: "floor-acme", request: {boundaryFenceToken: null}) { token } }`, nil); len(got.errs) != 0 {
		t.Fatalf("unlink: %v", got.errs)
	}
	if got := serve(t, op, read, nil); got.data["areasByToken"].([]any)[0].(map[string]any)["boundaryFence"] != nil {
		t.Fatalf("the link survived a null: %v", got.data)
	}
	// An unrelated edit leaves the link alone (omitted = leave).
	serve(t, op, `mutation { updateArea(token: "floor-acme", request: {boundaryFenceToken: "campus"}) { token } }`, nil)
	serve(t, op, `mutation { updateArea(token: "floor-acme", request: {name: "renamed"}) { token } }`, nil)
	if got := serve(t, op, read, nil); got.data["areasByToken"].([]any)[0].(map[string]any)["boundaryFence"] == nil {
		t.Fatal("an edit that omitted the boundary cleared it")
	}
}

// 🔴 A fence an area names cannot be deleted: the delete is REFUSED (REFERENCE_VIOLATION) while the
// link stands, so a link never dangles, and a fence recreated under the same token can never be
// picked up by an area that once named a fence of that token. Only after the link is removed may
// the fence go, and its successor binds nothing.
func TestAFenceAnAreaNamesCannotBeDeletedOrSilentlyRebound(t *testing.T) {
	f := newScopeFixture(t)
	if err := f.db.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&model.GeoFence{}, &model.GeoFenceSetVersion{}, &model.GeoFenceGeometryBlob{}); err != nil {
		t.Fatalf("migrate fences: %v", err)
	}
	op := f.operator()
	box := func(x float64) string {
		return fmt.Sprintf(`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[%[1]v,0],[%[1]v,1],[%[2]v,1],[%[2]v,0],[%[1]v,0]]]}}`, x, x+1)
	}
	if _, err := f.api.CreateGeoFence(op, &model.GeoFenceCreateRequest{Token: "campus", Geometry: box(0)}); err != nil {
		t.Fatalf("fence: %v", err)
	}
	if got := serve(t, op, `mutation { updateArea(token: "floor-acme", request: {boundaryFenceToken: "campus"}) { token } }`, nil); len(got.errs) != 0 {
		t.Fatalf("link: %v", got.errs)
	}

	// Deleting the named fence is refused, and nothing is deleted.
	del := serve(t, op, `mutation { deleteGeoFence(token: "campus") }`, nil)
	if codeOf(del) != "REFERENCE_VIOLATION" {
		t.Fatalf("deleting a fence an area names: %v / %v, want REFERENCE_VIOLATION", del.data, del.errs)
	}
	if fences, _ := f.api.GeoFencesByToken(op, []string{"campus"}); len(fences) != 1 {
		t.Fatal("the refused delete removed the fence")
	}
	const read = `{ areasByToken(tokens: ["floor-acme"]) { boundaryFence { token } } }`
	if got := serve(t, op, read, nil); len(got.errs) != 0 {
		t.Fatalf("the link was left dangling: %v", got.errs)
	}

	// Remove the link, and the fence may go; recreating its token binds no area.
	serve(t, op, `mutation { updateArea(token: "floor-acme", request: {boundaryFenceToken: null}) { token } }`, nil)
	if ok, err := f.api.DeleteGeoFence(op, "campus"); err != nil || !ok {
		t.Fatalf("delete of an unreferenced fence: %v %v", ok, err)
	}
	if _, err := f.api.CreateGeoFence(op, &model.GeoFenceCreateRequest{Token: "campus", Geometry: box(50)}); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if got := serve(t, op, read, nil); len(got.errs) != 0 ||
		got.data["areasByToken"].([]any)[0].(map[string]any)["boundaryFence"] != nil {
		t.Fatalf("a recreated fence was bound to an area that no longer names it: %v %v", got.data, got.errs)
	}
}

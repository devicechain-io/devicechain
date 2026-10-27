// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Device aliases and resolveIntegrationDevice served end to end: the parsed schema, the
// resolvers, the API and the store.

const (
	wireCreateDevice = `mutation ($r: DeviceCreateRequest!) { createDevice(request: $r) {
  token aliases { scheme value } } }`
	wireUpdateDevice = `mutation ($t: String!, $r: DeviceUpdateRequest!) { updateDevice(token: $t, request: $r) {
  token name aliases { scheme value } } }`
	wireDeviceAliases = `query ($t: [String!]!) { devicesByToken(tokens: $t) { token aliases { scheme value } } }`
	wireFindByAlias   = `query ($c: DeviceSearchCriteria!) { devices(criteria: $c) { results { token } } }`
	wireResolve       = `mutation ($i: String!, $s: String!, $sc: String!, $v: String!) {
  resolveIntegrationDevice(integration: $i, secret: $s, scheme: $sc, value: $v) {
    outcome deviceToken deviceTypeToken overflow } }`
	wireUnregistered = `query { unregisteredIntegrationDevices(criteria: {pageNumber: 1, pageSize: 10, integration: "chirp-1"}) {
  results { integration scheme value count } } }`
)

func aliasVars(token string, aliases ...map[string]any) map[string]any {
	req := map[string]any{"token": token, "deviceTypeToken": "pump"}
	if len(aliases) > 0 {
		list := make([]any, 0, len(aliases))
		for _, a := range aliases {
			list = append(list, a)
		}
		req["aliases"] = list
	}
	return map[string]any{"r": req}
}

func alias(scheme, value string) map[string]any {
	return map[string]any{"scheme": scheme, "value": value}
}

func aliasesOf(t *testing.T, res served, field string) []string {
	t.Helper()
	if len(res.errs) > 0 {
		t.Fatalf("%s: %v", field, res.errs[0].Message)
	}
	var out []string
	for _, a := range res.data[field].(map[string]any)["aliases"].([]any) {
		m := a.(map[string]any)
		out = append(out, m["scheme"].(string)+"="+m["value"].(string))
	}
	return out
}

func TestDeviceAliasesOverTheWire(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()

	created := serve(t, admin, wireCreateDevice, aliasVars("d-1", alias("lorawan.deveui", "70b3d57ed0000001")))
	if got := aliasesOf(t, created, "createDevice"); len(got) != 1 || got[0] != "lorawan.deveui=70B3D57ED0000001" {
		t.Fatalf("a lower-case DevEUI must be served upper case, got %v", got)
	}

	// A different-case duplicate is a CONFLICT.
	dup := serve(t, admin, wireCreateDevice, aliasVars("d-2", alias("lorawan.deveui", "70B3D57ED0000001")))
	if len(dup.errs) == 0 || dup.errs[0].Extensions["code"] != "CONFLICT" {
		t.Fatalf("a held alias: want CONFLICT, got %v", dup.errs)
	}
	// A malformed one is INVALID_VALUE, and a misnamed field is a request error.
	bad := serve(t, admin, wireCreateDevice, aliasVars("d-3", alias("lorawan.deveui", "nothex")))
	if len(bad.errs) == 0 || bad.errs[0].Extensions["code"] != "INVALID_VALUE" {
		t.Fatalf("a malformed DevEUI: want INVALID_VALUE, got %v", bad.errs)
	}
	misnamed := serve(t, admin, wireCreateDevice, aliasVars("d-4", map[string]any{"scheme": "lorawan.deveui", "val": "70B3D57ED0000002"}))
	if len(misnamed.errs) == 0 {
		t.Fatal("a misnamed alias field was accepted")
	}
	if rows, err := f.api.DevicesByToken(admin, []string{"d-2", "d-3", "d-4"}); err != nil || len(rows) != 0 {
		t.Fatalf("a refused create left devices: %v %v", rows, err)
	}

	// The update's states: omitted keeps, a list replaces, null and [] both clear.
	step := func(name string, req map[string]any, want ...string) {
		t.Helper()
		res := serve(t, admin, wireUpdateDevice, map[string]any{"t": "d-1", "r": req})
		got := aliasesOf(t, res, "updateDevice")
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: aliases = %v, want %v", name, got, want)
		}
	}
	step("omitted", map[string]any{"name": "renamed"}, "lorawan.deveui=70B3D57ED0000001")
	step("replaced", map[string]any{"aliases": []any{alias("custom.acme", "Serial-1")}}, "custom.acme=Serial-1")
	step("null", map[string]any{"aliases": nil})
	step("set again", map[string]any{"aliases": []any{alias("tts.device-id", "Sensor-9")}}, "tts.device-id=sensor-9")
	step("empty list", map[string]any{"aliases": []any{}})

	// Searchable by alias, in any spelling.
	serve(t, admin, wireUpdateDevice, map[string]any{"t": "d-1", "r": map[string]any{"aliases": []any{alias("lorawan.deveui", "70B3D57ED0000001")}}})
	found := serve(t, admin, wireFindByAlias, map[string]any{"c": map[string]any{
		"pageNumber": 1, "pageSize": 10, "alias": alias("lorawan.deveui", "70b3d57ed0000001")}})
	if got := tokensAt(t, found.data, "devices", "results"); len(got) != 1 || got[0] != "d-1" {
		t.Fatalf("devices by alias: %v %v", got, found.errs)
	}
}

// The alias table declares no scope rule, so a caller limited to part of the tenant gets null at
// the field (never an empty list, which would read as "none") and is refused at the filter.
func TestAScopedCallerCannotReadOrSearchByAlias(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	serve(t, admin, wireCreateDevice, aliasVars("d-1", alias("lorawan.deveui", "70B3D57ED0000001")))

	member := f.member("customer:acme")
	if got := serve(t, member, `query { devicesByToken(tokens: ["acme-pump"]) { token } }`, nil); len(got.errs) > 0 {
		t.Fatalf("the control: a scoped member reads its own device: %v", got.errs[0].Message)
	}
	got := serve(t, member, wireDeviceAliases, map[string]any{"t": []any{"acme-pump"}})
	if len(got.errs) > 0 {
		t.Fatalf("Device.aliases for a scoped member is null, not an error: %v", got.errs)
	}
	// The device comes back with aliases null, never [] (which would read as "none").
	devices, _ := got.data["devicesByToken"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["token"] != "acme-pump" || devices[0].(map[string]any)["aliases"] != nil {
		t.Fatalf("the device must survive the refused field, with null aliases: %v", got.data)
	}
	search := serve(t, member, wireFindByAlias, map[string]any{"c": map[string]any{
		"pageNumber": 1, "pageSize": 10, "alias": alias("lorawan.deveui", "70B3D57ED0000001")}})
	if len(search.errs) == 0 {
		t.Fatal("a scoped member searched by alias")
	}
}

func resolveVars(integration, secret, scheme, value string) map[string]any {
	return map[string]any{"i": integration, "s": secret, "sc": scheme, "v": value}
}

func serviceWith(f scopeFixture, authorities ...auth.Authority) context.Context {
	names := make([]string, 0, len(authorities))
	for _, a := range authorities {
		names = append(names, string(a))
	}
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant, Authorities: names})
}

func TestResolveIntegrationDeviceOverTheWire(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	issued := serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)
	serve(t, admin, wireCreateDevice, aliasVars("d-1", alias("lorawan.deveui", "70B3D57ED0000001")))
	svc := service(f)

	outcome := func(res served) string {
		t.Helper()
		if len(res.errs) > 0 {
			t.Fatalf("resolve: %v", res.errs[0].Message)
		}
		return res.data["resolveIntegrationDevice"].(map[string]any)["outcome"].(string)
	}

	resolved := serve(t, svc, wireResolve, resolveVars("chirp-1", issued, "lorawan.deveui", "70b3d57ed0000001"))
	if outcome(resolved) != "RESOLVED" {
		t.Fatalf("resolve: %v", resolved.data)
	}
	got := resolved.data["resolveIntegrationDevice"].(map[string]any)
	if got["deviceToken"] != "d-1" || got["deviceTypeToken"] != "pump" || got["overflow"] != false {
		t.Fatalf("resolution = %v", got)
	}

	unknown := serve(t, svc, wireResolve, resolveVars("chirp-1", issued, "lorawan.deveui", "70B3D57ED00000FF"))
	if outcome(unknown) != "UNREGISTERED" || unknown.data["resolveIntegrationDevice"].(map[string]any)["deviceToken"] != nil {
		t.Fatalf("an unknown DevEUI: %v", unknown.data)
	}
	seen := serve(t, admin, wireUnregistered, nil)
	rows := seen.data["unregisteredIntegrationDevices"].(map[string]any)["results"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["value"] != "70B3D57ED00000FF" || rows[0].(map[string]any)["integration"] != "chirp-1" {
		t.Fatalf("the sighting: %v %v", rows, seen.errs)
	}

	if o := outcome(serve(t, svc, wireResolve, resolveVars("chirp-1", "wrong", "lorawan.deveui", "70B3D57ED0000001"))); o != "UNAUTHENTICATED" {
		t.Fatalf("a wrong token: %s", o)
	}
	serve(t, admin, `mutation { updateIntegration(token: "chirp-1", request: {enabled: false}) { token } }`, nil)
	if o := outcome(serve(t, svc, wireResolve, resolveVars("chirp-1", issued, "lorawan.deveui", "70B3D57ED0000001"))); o != "DISABLED" {
		t.Fatalf("a disabled integration: %s, want DISABLED (not UNAUTHENTICATED)", o)
	}

	// A scheme the integration does not resolve by, or a malformed value, is a request error.
	serve(t, admin, `mutation { updateIntegration(token: "chirp-1", request: {enabled: true}) { token } }`, nil)
	for _, c := range [][2]string{{"tts.device-id", "sensor-1"}, {"lorawan.deveui", "nothex"}} {
		res := serve(t, svc, wireResolve, resolveVars("chirp-1", issued, c[0], c[1]))
		if len(res.errs) == 0 || res.errs[0].Extensions["code"] != "INVALID_VALUE" {
			t.Fatalf("%v: want INVALID_VALUE, got %v", c, res.errs)
		}
	}
}

// resolveIntegrationDevice is the service plane: a user's access token is refused whatever it
// holds, a service token needs device:read, and only the combination is admitted.
func TestResolveIntegrationDeviceIsServiceOnly(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	issued := serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)
	vars := resolveVars("chirp-1", issued, "lorawan.deveui", "70B3D57ED0000001")

	if got := serve(t, service(f), wireResolve, vars); len(got.errs) > 0 || got.data["resolveIntegrationDevice"] == nil {
		t.Fatalf("the paired positive: a service token with device:read: %v %v", got.data, got.errs)
	}
	refused := map[string]context.Context{
		"an access token holding device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess,
			Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}}),
		"a tenant administrator":              admin,
		"a service token without device:read": serviceWith(f, auth.EventRead),
		"a scoped service token": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant,
			Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme"}}),
	}
	for who, ctx := range refused {
		got := serve(t, ctx, wireResolve, vars)
		if len(got.errs) == 0 || got.data["resolveIntegrationDevice"] != nil {
			t.Errorf("resolveIntegrationDevice was admitted for %s: %v %v", who, got.data, got.errs)
		}
	}
	// And nothing a refused caller attempted was recorded.
	if seen := serve(t, admin, wireUnregistered, nil); len(seen.errs) > 0 ||
		len(seen.data["unregisteredIntegrationDevices"].(map[string]any)["results"].([]any)) != 1 {
		t.Fatalf("only the admitted call recorded its sighting: %v %v", seen.data, seen.errs)
	}
}

func TestUnregisteredDevicesNeedUnscopedDeviceRead(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil))
	if got := serve(t, admin, wireUnregistered, nil); len(got.errs) > 0 {
		t.Fatalf("the control: %v", got.errs[0].Message)
	}
	scoped := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme"}})
	nothing := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant})
	for who, ctx := range map[string]context.Context{"a scoped reader": scoped, "no authority": nothing} {
		if got := serve(t, ctx, wireUnregistered, nil); len(got.errs) == 0 {
			t.Errorf("unregisteredIntegrationDevices was admitted for %s", who)
		}
	}
}

func TestClearUnregisteredIntegrationDevicesOverTheWire(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	issued := serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)
	for _, v := range []string{"70B3D57ED0000001", "70B3D57ED0000002"} {
		serve(t, service(f), wireResolve, resolveVars("chirp-1", issued, "lorawan.deveui", v))
	}
	const clear = `mutation { clearUnregisteredIntegrationDevices(integration: "chirp-1") }`

	viewer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}})
	scoped := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme"}})
	for who, ctx := range map[string]context.Context{"a viewer": viewer, "a scoped writer": scoped} {
		if got := serve(t, ctx, clear, nil); len(got.errs) == 0 {
			t.Errorf("clear was admitted for %s", who)
		}
	}
	if seen := serve(t, admin, wireUnregistered, nil); len(seen.data["unregisteredIntegrationDevices"].(map[string]any)["results"].([]any)) != 2 {
		t.Fatalf("refused clears removed rows: %v", seen.data)
	}
	got := serve(t, admin, clear, nil)
	if len(got.errs) > 0 || got.data["clearUnregisteredIntegrationDevices"] != float64(2) {
		t.Fatalf("the administrator's clear: %v %v", got.data, got.errs)
	}
	if seen := serve(t, admin, wireUnregistered, nil); len(seen.data["unregisteredIntegrationDevices"].(map[string]any)["results"].([]any)) != 0 {
		t.Fatalf("rows survived the clear: %v", seen.data)
	}
}

// The marker mutations are the same service plane as resolveIntegrationDevice.
func TestIntegrationMarkerMutationsAreServiceOnly(t *testing.T) {
	const undecoded = `mutation ($i: String!, $s: String!) { recordIntegrationUndecoded(integration: $i, secret: $s, count: 2, sample: "aabb") }`
	const authFailure = `mutation ($i: String!) { recordIntegrationAuthFailure(integration: $i) }`
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	issued := serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)
	uv := map[string]any{"i": "chirp-1", "s": issued}
	av := map[string]any{"i": "chirp-1"}

	if got := serve(t, service(f), undecoded, uv); len(got.errs) > 0 || got.data["recordIntegrationUndecoded"] != true {
		t.Fatalf("the paired positive (undecoded): %v %v", got.data, got.errs)
	}
	if got := serve(t, service(f), authFailure, av); len(got.errs) > 0 || got.data["recordIntegrationAuthFailure"] != true {
		t.Fatalf("the paired positive (auth failure): %v %v", got.data, got.errs)
	}
	refused := map[string]context.Context{
		"an access token holding device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess,
			Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}}),
		"a tenant administrator":              admin,
		"a service token without device:read": serviceWith(f, auth.EventRead),
	}
	for who, ctx := range refused {
		if got := serve(t, ctx, undecoded, uv); len(got.errs) == 0 || got.data["recordIntegrationUndecoded"] == true {
			t.Errorf("recordIntegrationUndecoded was admitted for %s", who)
		}
		if got := serve(t, ctx, authFailure, av); len(got.errs) == 0 || got.data["recordIntegrationAuthFailure"] == true {
			t.Errorf("recordIntegrationAuthFailure was admitted for %s", who)
		}
	}
	// A wrong token is refused and the tally is untouched.
	bad := serve(t, service(f), undecoded, map[string]any{"i": "chirp-1", "s": differentToken(issued)})
	if len(bad.errs) == 0 {
		t.Errorf("a wrong token wrote a marker: %v", bad.data)
	}
}

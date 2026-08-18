// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
)

// entity is one creatable thing: how to make it, and how to read it back.
//
// 🔑 THE TABLE IS THE COVERAGE CLAIM. Everything this tool asserts is scoped to
// what is listed here, so it is written as DATA and printed by `apiprobe
// coverage` — an entity nobody added is then a row somebody can count, rather
// than absent code nobody can see. A tool that silently covers 5 of 26 entities
// and reports PASS is the vacuous check this whole rig exists to argue against.
//
// ⚠️ IT IS AN ORDERED SEQUENCE, NOT A SET. A device type needs a profile, a
// device needs a type, an area needs an area type. Later entries read earlier
// tokens out of the state, so reordering the slice breaks the run — which is why
// dependencies are named in each entry's comment rather than inferred.
type entity struct {
	// Name is the stable identifier on the receipt. Never reuse or rename one:
	// a receipt written before an upgrade is read after it, so a renamed entity
	// reads as one entity missing and one unexpected.
	Name string

	// Area is the functional area whose GraphQL endpoint serves it, i.e. the
	// `<area>` in /api/<area>/graphql.
	Area string

	// Create is the mutation document. It takes exactly one variable, $req, so
	// that the input shape lives in Vars and the document stays a constant.
	Create string

	// Vars builds the create input. It receives the run state so an entry can
	// reference a token an earlier entry produced.
	Vars func(*state) map[string]any

	// Read is the read-back query. It takes exactly one variable, $token.
	//
	// 🔴 IT MUST SELECT THE SAME FIELDS THE CREATE RETURNS, because the
	// comparison is between the two responses. Selecting fewer fields on read
	// silently narrows what this tool can detect, and nothing fails when it does.
	Read string

	// CreateKey and ReadKey are the top-level response keys. They differ in KIND,
	// not just in name: every create returns one object, and every read-back is a
	// *sByToken(tokens: [String!]!) returning a NON-NULL LIST.
	//
	// 🔑 That asymmetry is load-bearing rather than an annoyance. It gives
	// "missing" an exact definition — the list came back EMPTY — which is a
	// different observation from a field whose value changed, and different again
	// from a query the server rejected. Those are the three exit codes, and this
	// is where the first of them is actually decided.
	CreateKey string
	ReadKey   string

	// Record, when set, stores tokens from the created object into the state so
	// later entities can reference them. Most entries need none.
	Record func(*state, map[string]any)
}

// state carries what earlier entities produced into the ones that depend on them.
type state struct {
	tenant string
	tokens map[string]string
}

func newState(tenant string) *state {
	return &state{tenant: tenant, tokens: map[string]string{}}
}

// tok returns a deterministic token for an entity. Deterministic rather than
// random deliberately: a receipt is read by a DIFFERENT process after an
// upgrade, and a value that has to survive that is easier to debug when it can
// be predicted from the entity name alone.
func (s *state) tok(name string) string { return "apiprobe-" + name }

// The coverage table.
//
// 📍 STATUS: 5 of the platform's 26 tenant-plane create mutations. The remaining
// 21 are mechanical — each is a schema lookup for the input shape and one row —
// and they are NOT yet here, so a green run today means five entities survived,
// not that the API did. `apiprobe coverage` prints this count so a rig operator
// reads the claim rather than assuming it.
var entities = []entity{
	{
		// The spine's root: everything a device does is declared on its profile.
		Name:      "device-profile",
		Area:      "device-management",
		Create:    `mutation($req:DeviceProfileCreateRequest){createDeviceProfile(request:$req){token name description category metadata}}`,
		Read:      `query($token:String!){deviceProfilesByToken(tokens:[$token]){token name description category metadata}}`,
		CreateKey: "createDeviceProfile",
		ReadKey:   "deviceProfilesByToken",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("device-profile"),
				"name":        "apiprobe profile",
				"description": "Written by apiprobe; read back to prove the upgrade preserved it.",
				"category":    "probe",
				"metadata":    `{"probe":"device-profile"}`,
			}}
		},
		Record: func(s *state, o map[string]any) { s.tokens["profile"] = str(o["token"]) },
	},
	{
		// Depends on: device-profile (adopts it, ADR-045 un-fused).
		Name:      "device-type",
		Area:      "device-management",
		Create:    `mutation($req:DeviceTypeCreateRequest){createDeviceType(request:$req){token name description manufacturer model metadata}}`,
		Read:      `query($token:String!){deviceTypesByToken(tokens:[$token]){token name description manufacturer model metadata}}`,
		CreateKey: "createDeviceType",
		ReadKey:   "deviceTypesByToken",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":        s.tok("device-type"),
				"name":         "apiprobe type",
				"description":  "Adopts the apiprobe profile.",
				"profileToken": s.tokens["profile"],
				"manufacturer": "apiprobe",
				"model":        "P-1",
				"metadata":     `{"probe":"device-type"}`,
			}}
		},
		Record: func(s *state, o map[string]any) { s.tokens["deviceType"] = str(o["token"]) },
	},
	{
		// Depends on: device-type.
		Name:      "device",
		Area:      "device-management",
		Create:    `mutation($req:DeviceCreateRequest){createDevice(request:$req){token externalId name description metadata}}`,
		Read:      `query($token:String!){devicesByToken(tokens:[$token]){token externalId name description metadata}}`,
		CreateKey: "createDevice",
		ReadKey:   "devicesByToken",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":           s.tok("device"),
				"externalId":      "apiprobe-ext-1",
				"name":            "apiprobe device",
				"description":     "One device, to prove the spine survived.",
				"deviceTypeToken": s.tokens["deviceType"],
				"metadata":        `{"probe":"device"}`,
			}}
		},
		Record: func(s *state, o map[string]any) { s.tokens["device"] = str(o["token"]) },
	},
	{
		Name:      "area-type",
		Area:      "device-management",
		Create:    `mutation($req:AreaTypeCreateRequest){createAreaType(request:$req){token name description metadata}}`,
		Read:      `query($token:String!){areaTypesByToken(tokens:[$token]){token name description metadata}}`,
		CreateKey: "createAreaType",
		ReadKey:   "areaTypesByToken",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("area-type"),
				"name":        "apiprobe area type",
				"description": "Parent of the probe area.",
				"metadata":    `{"probe":"area-type"}`,
			}}
		},
		Record: func(s *state, o map[string]any) { s.tokens["areaType"] = str(o["token"]) },
	},
	{
		// Depends on: area-type.
		Name:      "area",
		Area:      "device-management",
		Create:    `mutation($req:AreaCreateRequest){createArea(request:$req){token name description metadata}}`,
		Read:      `query($token:String!){areasByToken(tokens:[$token]){token name description metadata}}`,
		CreateKey: "createArea",
		ReadKey:   "areasByToken",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":         s.tok("area"),
				"name":          "apiprobe area",
				"description":   "Child of the probe area type.",
				"areaTypeToken": s.tokens["areaType"],
				"metadata":      `{"probe":"area"}`,
			}}
		},
	},
}

// tenantCreateMutations is the platform's total, measured from the served
// schemas. It is written down so printCoverage can state the DENOMINATOR: "5
// entities covered" invites the reader to assume that is all there is, and
// "5 of 26" does not.
//
// ⚠️ Kept honest by TestCoverageDenominatorMatchesTheSchemas, which counts the
// create mutations in the served schemas rather than trusting this number.
const tenantCreateMutations = 26

func printCoverage() {
	byArea := map[string][]string{}
	for _, e := range entities {
		byArea[e.Area] = append(byArea[e.Area], e.Name)
	}
	areas := make([]string, 0, len(byArea))
	for a := range byArea {
		areas = append(areas, a)
	}
	sort.Strings(areas)

	fmt.Printf("apiprobe covers %d of the platform's %d tenant-plane create mutations.\n\n",
		len(entities), tenantCreateMutations)
	for _, a := range areas {
		fmt.Printf("  %s\n", a)
		for _, n := range byArea[a] {
			fmt.Printf("      %s\n", n)
		}
	}
	if len(entities) < tenantCreateMutations {
		fmt.Printf("\n⚠️  %d create mutations are NOT covered. A green verify says the entities\n"+
			"    listed above survived — it says nothing about the rest of the API.\n",
			tenantCreateMutations-len(entities))
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

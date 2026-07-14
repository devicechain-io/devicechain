// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"strings"
	"testing"
)

// pgParams are the standard bindings a Postgres resolution uses; NumericCast is the
// production "::numeric" form so these goldens pin the real emitted SQL.
func pgParams() LowerParams {
	return LowerParams{
		TenantId: "acme", MemberType: "device", MemberTable: "devices",
		FacetScope: "SHARED", NumericCast: func(c string) string { return c + "::numeric" },
	}
}

// The lowered fragment and args are pinned for each leaf shape, so a change to the emitted
// SQL is a visible, reviewed diff rather than a silent behavior shift.
func TestLower_Goldens(t *testing.T) {
	const ea = "EXISTS (SELECT 1 FROM entity_attributes ea WHERE ea.tenant_id = ? AND ea.entity_type = ? AND ea.entity_id = devices.id AND ea.scope = ? AND ea.deleted_at IS NULL AND ea.attr_key = ?"

	cases := []struct {
		src      string
		wantFrag string
		wantArgs []any
	}{
		{
			`"climate" in attr`,
			ea + ")",
			[]any{"acme", "device", "SHARED", "climate"},
		},
		{
			`attr["climate"] == "arid"`,
			ea + " AND ea.value_type = 'STRING' AND ea.value = ?)",
			[]any{"acme", "device", "SHARED", "climate", "arid"},
		},
		{
			`attr["climate"] != "arid"`,
			ea + " AND ea.value_type = 'STRING' AND ea.value <> ?)",
			[]any{"acme", "device", "SHARED", "climate", "arid"},
		},
		{
			`attr["coastal"] == true`,
			ea + " AND ea.value_type = 'BOOLEAN' AND ea.value = ?)",
			[]any{"acme", "device", "SHARED", "coastal", "true"},
		},
		{
			`attr["population"] > 1000`,
			ea + " AND CASE WHEN ea.value_type IN ('LONG','DOUBLE') THEN ea.value::numeric > ? ELSE FALSE END)",
			[]any{"acme", "device", "SHARED", "population", int64(1000)},
		},
		{
			// Commuted: `1000 < attr["population"]` ≡ population > 1000.
			`1000 < attr["population"]`,
			ea + " AND CASE WHEN ea.value_type IN ('LONG','DOUBLE') THEN ea.value::numeric > ? ELSE FALSE END)",
			[]any{"acme", "device", "SHARED", "population", int64(1000)},
		},
		{
			`!(attr["climate"] == "arid")`,
			"(NOT " + ea + " AND ea.value_type = 'STRING' AND ea.value = ?))",
			[]any{"acme", "device", "SHARED", "climate", "arid"},
		},
	}

	for _, c := range cases {
		sel, err := Compile(c.src, "device", 1000)
		if err != nil {
			t.Fatalf("compile %q: %v", c.src, err)
		}
		frag, args, err := sel.Lower(pgParams())
		if err != nil {
			t.Fatalf("lower %q: %v", c.src, err)
		}
		if frag != c.wantFrag {
			t.Errorf("selector %q fragment:\n got: %s\nwant: %s", c.src, frag, c.wantFrag)
		}
		if !argsEqual(args, c.wantArgs) {
			t.Errorf("selector %q args:\n got: %#v\nwant: %#v", c.src, args, c.wantArgs)
		}
	}
}

// A compound selector composes leaf fragments with real SQL AND/OR and shares no state
// between leaves — the args accumulate in fragment (placeholder) order.
func TestLower_CompoundOrder(t *testing.T) {
	sel, err := Compile(`attr["climate"] == "arid" && attr["population"] > 100000`, "device", 1000)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	frag, args, err := sel.Lower(pgParams())
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if !strings.HasPrefix(frag, "(") || !strings.Contains(frag, " AND ") {
		t.Errorf("compound fragment not AND-composed: %s", frag)
	}
	// climate leaf args come first (it is the left operand), then population's.
	want := []any{"acme", "device", "SHARED", "climate", "arid", "acme", "device", "SHARED", "population", int64(100000)}
	if !argsEqual(args, want) {
		t.Errorf("compound args:\n got: %#v\nwant: %#v", args, want)
	}
}

func argsEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

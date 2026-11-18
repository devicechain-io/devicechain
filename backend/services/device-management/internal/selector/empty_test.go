// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import "testing"

// The existing lowering/oracle corpus, extended with negated presence and nested
// boolean expressions. Every case has both an explicit expectation and a SQL oracle.
func TestMatchesEmpty_SQLOracle(t *testing.T) {
	db, ctx := newOracleDB(t)
	id := seedMember(t, db, ctx, "empty", oracleTenant, nil)
	cases := []struct {
		src  string
		want bool
	}{
		{`"climate" in attr`, false},
		{`!("retired" in attr)`, true},
		{`attr["climate"] == "arid"`, false},
		{`attr["climate"] != "arid"`, false},
		{`!(attr["climate"] == "arid")`, true},
		{`!(attr["climate"] != "arid")`, true},
		{`"arid" == attr["climate"]`, false},
		{`"arid" != attr["climate"]`, false},
		{`attr["coastal"] == true`, false},
		{`attr["coastal"] != true`, false},
		{`attr["population"] == 1000000`, false},
		{`attr["population"] != 1u`, false},
		{`attr["population"] == 1.5`, false},
		{`attr["population"] > 1000`, false},
		{`attr["population"] >= 1000000`, false},
		{`attr["population"] < 1000`, false},
		{`attr["population"] <= 1000`, false},
		{`1000 < attr["population"]`, false},
		{`1000 <= attr["population"]`, false},
		{`1000 > attr["population"]`, false},
		{`1000 >= attr["population"]`, false},
		{`!(attr["population"] > 1000)`, true},
		{`attr["climate"] == "arid" && attr["population"] > 100000`, false},
		{`attr["climate"] == "arid" || attr["climate"] == "tropical"`, false},
		{`attr["climate"] == "arid" && "coastal" in attr`, false},
		{`!(!("retired" in attr))`, false},
		{`!(!(!("retired" in attr)))`, true},
		{`!("retired" in attr) && !(attr["climate"] == "arid")`, true},
		{`!("retired" in attr) && "climate" in attr`, false},
		{`"climate" in attr || !("retired" in attr)`, true},
		{`!("retired" in attr) || "climate" in attr`, true},
		{`!(("retired" in attr) || (attr["climate"] == "arid" && attr["population"] > 1000))`, true},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sel, err := Compile(tc.src, "device")
			if err != nil {
				t.Fatal(err)
			}
			got, err := sel.MatchesEmpty()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("MatchesEmpty = %v, want %v", got, tc.want)
			}
			frag, args, err := sel.Lower(LowerParams{
				TenantId: oracleTenant, MemberType: "device", MemberTable: "devices",
				FacetScope: "SHARED", NumericCast: sqliteCast,
			})
			if err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.WithContext(ctx).Model(&oracleMember{}).Where("devices.id = ?", id).
				Where(frag, args...).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if sqlMatch := count > 0; got != sqlMatch {
				t.Errorf("MatchesEmpty = %v, SQL = %v; fragment: %s", got, sqlMatch, frag)
			}
		})
	}
}

func TestMatchesEmpty_RejectsLikeLower(t *testing.T) {
	// Bypass Compile's lowering gate to exercise defensive validation, including
	// invalid branches that a boolean evaluator might otherwise short-circuit.
	for _, src := range []string{
		`true`, `!false`, `attr["a"] == attr["b"]`, `attr["a"] > "x"`,
		`memberType == "device"`, `size(attr) > 0`,
		`!("a" in attr) || true`, `"a" in attr && true`,
	} {
		t.Run(src, func(t *testing.T) {
			env, err := Env()
			if err != nil {
				t.Fatal(err)
			}
			ast, issues := env.Compile(src)
			if issues != nil && issues.Err() != nil {
				t.Fatal(issues.Err())
			}
			sel := &Selector{source: src, ast: ast}
			_, emptyErr := sel.MatchesEmpty()
			_, _, lowerErr := sel.Lower(pgParams())
			if emptyErr == nil || lowerErr == nil || emptyErr.Error() != lowerErr.Error() {
				t.Fatalf("MatchesEmpty error = %v, Lower error = %v", emptyErr, lowerErr)
			}
		})
	}
}

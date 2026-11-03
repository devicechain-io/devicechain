// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import "testing"

func TestColumnUpsertFragmentsMustBeLiteralsOrConstants(t *testing.T) {
	clean := map[string]string{
		"literals": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{
	Set:   []rdb.Assignment{rdb.SetTo("rev", "excluded.rev"), rdb.SetTo("at", "greatest(t.at, excluded.at)")},
	Where: "t.rev < excluded.rev",
}, cols...)`,
		"constants and concatenation": `package model
const guard = "t.rev < excluded.rev"
const more = guard + " OR t.flag"
var tbl = rdb.NewColumnUpsert[Row](tenant, key, rdb.Upsert{
	Set:   []rdb.Assignment{rdb.SetTo("rev", "excluded." + "rev")},
	Where: more,
})`,
		"assignment literals, and a pointer to the terms": `package model
var tbl = NewColumnUpsert(tenant, key, &Upsert{
	Set: []Assignment{{Column: "rev", Expr: "excluded.rev"}, {"n", "excluded.n"}},
})`,
		"no guard": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", "excluded.a")}})`,
	}
	for name, src := range clean {
		t.Run(name, func(t *testing.T) {
			if res := scanSQL(t, src, nil); len(res.Findings) != 0 {
				t.Fatalf("want clean, got %v", res.Findings)
			}
		})
	}

	flagged := map[string]string{
		"a variable in WHERE": `package model
var tbl = func(g string) any { return rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", "excluded.a")}, Where: rdb.Fragment(g)}) }`,
		"a variable in SET": `package model
var tbl = func(g string) any { return rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", rdb.Fragment(g))}}) }`,
		"a package var, which can be reassigned": `package model
var g = "x"
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", g)}})`,
		"a local that shadows a constant": `package model
const g = "excluded.a"
var tbl = func() any { g := other(); return rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", g)}}) }`,
		"a call": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", build())}})`,
		"a formatted fragment": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", fmt.Sprintf("x%d", 1))}})`,
		"terms held in a variable": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, terms)`,
		"a SET list held in a variable": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: sets})`,
		"a SET element built elsewhere": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{mk()}})`,
		"a fragment in the second assignment only": `package model
var tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", "excluded.a"), rdb.SetTo("b", other)}})`,
	}
	for name, src := range flagged {
		t.Run(name, func(t *testing.T) {
			if res := scanSQL(t, src, nil); len(res.Findings) != 1 {
				t.Fatalf("want exactly 1 finding, got %d: %v", len(res.Findings), res.Findings)
			}
		})
	}
}

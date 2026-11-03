// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import "testing"

const goodTable = `rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo("a", "excluded.a")}})`

// NewColumnUpsert is only ever the function of a call in a package-level var initializer.
// Any other use hides the fragments from the literal/constant rule.
func TestColumnUpsertMustBeCalledInAPackageLevelVar(t *testing.T) {
	flagged := map[string]string{
		"a function value": `package model
var mk = rdb.NewColumnUpsert[Row]
var tbl = mk(tenant, key, terms)`,
		"a function value, bare identifier": `package model
var mk = NewColumnUpsert[Row]`,
		"a function value in a function body": `package model
func f() { mk := rdb.NewColumnUpsert[Row]; _ = mk }`,
		"a call in a function body": `package model
func f() any { return ` + goodTable + ` }`,
		"a call in a method": `package model
func (x X) f() any { return ` + goodTable + ` }`,
		"a call in a local var": `package model
func f() { var t = ` + goodTable + `; _ = t }`,
		"a function value passed along": `package model
var tbl = wrap(rdb.NewColumnUpsert[Row])`,
	}
	for name, src := range flagged {
		t.Run(name, func(t *testing.T) {
			if res := scanSQL(t, src, nil); len(res.Findings) == 0 {
				t.Fatal("want a finding, got none")
			}
		})
	}
	// The counterweights: the permitted form, plain and instantiated, and a mention in a
	// comment or string.
	for name, src := range map[string]string{
		"package var":      "package model\nvar tbl = " + goodTable,
		"instantiated":     "package model\nvar tbl = rdb.NewColumnUpsert[Row](tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo(\"a\", \"excluded.a\")}})",
		"in a var block":   "package model\nvar (\n\ttbl = " + goodTable + "\n)",
		"a comment/string": "package model\n// NewColumnUpsert is documented here\nvar s = \"NewColumnUpsert\"",
	} {
		t.Run("ok "+name, func(t *testing.T) {
			if res := scanSQL(t, src, nil); len(res.Findings) != 0 {
				t.Fatalf("want clean, got %v", res.Findings)
			}
		})
	}
}

// A file named migration_* or baseline* is exempt from the raw-SQL rule, not from this one.
func TestColumnUpsertRuleReachesMigrationAndBaselineFiles(t *testing.T) {
	bad := "package model\nvar tbl = rdb.NewColumnUpsert(tenant, key, rdb.Upsert{Set: []rdb.Assignment{rdb.SetTo(\"a\", g)}})"
	for _, name := range []string{"migration_x.go", "baseline.go", "baseline_snapshot.go"} {
		root := fixture(t, map[string]string{"models.go": models, name: bad})
		res, err := rawSQLScan(nil, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Findings) != 1 {
			t.Errorf("%s: want 1 finding, got %v", name, res.Findings)
		}
	}
}

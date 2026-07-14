// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"gorm.io/gorm"
)

// This is the correctness oracle for the SQL lowering (ADR-061 G3 §3.5). For a matrix of
// selectors × seeded entities/attributes it asserts the lowered, DB-executed query returns
// EXACTLY the set an independent in-memory reference evaluator (refEval) computes from the
// same checked AST — two implementations of the declared absent-safe facet semantics on
// different substrates (SQL string-gen vs. Go value eval). Agreement across the matrix is
// the evidence that lets us trust SQL as the evaluator while CEL stays the authored contract.
// It runs on sqlite (via the dialect numeric-cast seam) so it is always-green in CI.

// oracleMember is a minimal stand-in for a member family table ("devices"): the lowering only
// correlates on its id, and tenant scoping needs the TenantId field.
type oracleMember struct {
	ID        uint `gorm:"primaryKey"`
	Token     string
	TenantId  string
	DeletedAt gorm.DeletedAt
}

func (oracleMember) TableName() string { return "devices" }

// oracleAttr mirrors entity_attributes' columns the lowering references.
type oracleAttr struct {
	ID         uint `gorm:"primaryKey"`
	TenantId   string
	EntityType string
	EntityId   uint
	Scope      string
	AttrKey    string
	ValueType  string
	Value      sql.NullString
	DeletedAt  gorm.DeletedAt
}

func (oracleAttr) TableName() string { return "entity_attributes" }

// storedVal is a seeded facet value (type tag + text), used by both the seed and refEval.
type storedVal struct {
	valueType string
	value     string
}

// facetSet is one entity's SHARED facets, keyed by attr_key.
type facetSet map[string]storedVal

const oracleTenant = "acme"

func sqliteCast(col string) string { return "CAST(" + col + " AS REAL)" }

func newOracleDB(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&oracleMember{}, &oracleAttr{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, core.WithTenant(context.Background(), oracleTenant)
}

// seedMember inserts a member row plus its SHARED facets, all for the given tenant. It
// returns the member id. attrs write with scope SHARED; the caller adds noise separately.
func seedMember(t *testing.T, db *gorm.DB, ctx context.Context, token, tenant string, attrs facetSet) uint {
	t.Helper()
	m := &oracleMember{Token: token, TenantId: tenant}
	if err := db.WithContext(ctx).Create(m).Error; err != nil {
		t.Fatalf("seed member %s: %v", token, err)
	}
	for k, v := range attrs {
		a := &oracleAttr{
			TenantId: tenant, EntityType: "device", EntityId: m.ID,
			Scope: "SHARED", AttrKey: k, ValueType: v.valueType,
			Value: sql.NullString{String: v.value, Valid: true},
		}
		if err := db.WithContext(ctx).Create(a).Error; err != nil {
			t.Fatalf("seed attr %s.%s: %v", token, k, err)
		}
	}
	return m.ID
}

// entity is a seeded row: its id + its facets, so refEval can be run against it.
type entity struct {
	id     uint
	facets facetSet
}

func TestLowering_MatchesReferenceEvaluator(t *testing.T) {
	db, ctx := newOracleDB(t)

	// A matrix of areas with varied facets. Some lack a facet entirely (absence), some
	// carry numerics, one carries a bool, one a JSON value (never satisfies a scalar leaf).
	seeds := []struct {
		token  string
		facets facetSet
	}{
		{"a-arid-large", facetSet{"climate": {"STRING", "arid"}, "population": {"LONG", "1000000"}, "coastal": {"BOOLEAN", "false"}}},
		{"a-arid-small", facetSet{"climate": {"STRING", "arid"}, "population": {"LONG", "500"}}},
		{"a-humid-coastal", facetSet{"climate": {"STRING", "humid"}, "population": {"DOUBLE", "250000.5"}, "coastal": {"BOOLEAN", "true"}}},
		{"a-noclimate", facetSet{"population": {"LONG", "42"}}},
		{"a-empty", facetSet{}},
		{"a-json", facetSet{"climate": {"JSON", `{"x":1}`}, "population": {"STRING", "notanumber"}}},
		{"a-tropical", facetSet{"climate": {"STRING", "tropical"}, "population": {"LONG", "1000000"}, "coastal": {"BOOLEAN", "true"}}},
	}
	entities := make([]entity, 0, len(seeds))
	for _, s := range seeds {
		id := seedMember(t, db, ctx, s.token, oracleTenant, s.facets)
		entities = append(entities, entity{id: id, facets: s.facets})
	}
	// Cross-tenant noise: another tenant's identical arid area must never match.
	otherCtx := core.WithTenant(context.Background(), "other")
	seedMember(t, db, otherCtx, "other-arid", "other", facetSet{"climate": {"STRING", "arid"}, "population": {"LONG", "1000000"}})
	// Wrong-scope noise: a CLIENT-scope climate must not define membership (facets are SHARED).
	noise := &oracleAttr{TenantId: oracleTenant, EntityType: "device", EntityId: entities[4].id,
		Scope: "CLIENT", AttrKey: "climate", ValueType: "STRING", Value: sql.NullString{String: "arid", Valid: true}}
	if err := db.WithContext(ctx).Create(noise).Error; err != nil {
		t.Fatalf("seed noise: %v", err)
	}
	// Cross-tenant collision: an 'other'-tenant climate=arid attribute whose entity_id equals
	// acme's a-noclimate (index 3), which itself has NO climate facet. ONLY the explicit
	// ea.tenant_id guard in the semi-join keeps this from making a-noclimate spuriously match
	// a climate leaf — so this seed is what makes the oracle fail if that guard is dropped
	// (the outer tenant-scope callback alone would not catch it).
	collide := &oracleAttr{TenantId: "other", EntityType: "device", EntityId: entities[3].id,
		Scope: "SHARED", AttrKey: "climate", ValueType: "STRING", Value: sql.NullString{String: "arid", Valid: true}}
	if err := db.WithContext(otherCtx).Create(collide).Error; err != nil {
		t.Fatalf("seed collision: %v", err)
	}

	selectors := []string{
		`"climate" in attr`,
		`attr["climate"] == "arid"`,
		`attr["climate"] != "arid"`,    // present AND different (absent excluded)
		`!(attr["climate"] == "arid")`, // absent INCLUDED (NOT of a present-and-equal)
		`attr["population"] > 1000`,
		`attr["population"] >= 1000000`,
		`attr["population"] < 1000`,
		`1000 < attr["population"]`, // commuted → population > 1000
		`attr["climate"] == "arid" && attr["population"] > 100000`,
		`attr["climate"] == "arid" || attr["climate"] == "tropical"`,
		`attr["coastal"] == true`,
		`attr["coastal"] != true`, // present-and-false only
		`attr["climate"] == "arid" && "coastal" in attr`,
		`attr["population"] == 1000000`, // numeric equality across LONG/DOUBLE
	}

	for _, src := range selectors {
		sel, err := Compile(src, "device", 1000)
		if err != nil {
			t.Fatalf("compile %q: %v", src, err)
		}
		frag, args, err := sel.Lower(LowerParams{
			TenantId: oracleTenant, MemberType: "device", MemberTable: "devices",
			FacetScope: "SHARED", NumericCast: sqliteCast,
		})
		if err != nil {
			t.Fatalf("lower %q: %v", src, err)
		}

		// SQL side: the lowered, tenant-scoped, paginated-style query.
		var sqlIds []uint
		if err := db.WithContext(ctx).Model(&oracleMember{}).
			Where(frag, args...).Order("id").Pluck("id", &sqlIds).Error; err != nil {
			t.Fatalf("run lowered %q: %v\nfragment: %s\nargs: %v", src, err, frag, args)
		}

		// Reference side: independent in-memory eval of the same AST.
		var refIds []uint
		root := sel.ast.NativeRep().Expr()
		for _, e := range entities {
			if refEval(t, root, e.facets) {
				refIds = append(refIds, e.id)
			}
		}
		sort.Slice(sqlIds, func(i, j int) bool { return sqlIds[i] < sqlIds[j] })
		sort.Slice(refIds, func(i, j int) bool { return refIds[i] < refIds[j] })

		if !equalIds(sqlIds, refIds) {
			t.Errorf("selector %q: SQL set %v != reference set %v\nfragment: %s\nargs: %v",
				src, sqlIds, refIds, frag, args)
		}
	}
}

func equalIds(a, b []uint) bool {
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

// refEval is the independent reference: it evaluates a checked selector AST against one
// entity's facet set using the declared absent-safe leaf semantics (a leaf comparison
// requires the facet present and of the matching value_type; boolean ops are real logic).
// It shares only the shape-recognition helpers (attrIndexKey/stringLiteral/attrAndScalar)
// with the lowering; the value semantics below are implemented independently of the SQL.
func refEval(t *testing.T, e celast.Expr, facets facetSet) bool {
	t.Helper()
	if e.Kind() != celast.CallKind {
		t.Fatalf("refEval: non-call node %v", e.Kind())
	}
	call := e.AsCall()
	args := call.Args()
	switch call.FunctionName() {
	case operators.LogicalAnd:
		return refEval(t, args[0], facets) && refEval(t, args[1], facets)
	case operators.LogicalOr:
		return refEval(t, args[0], facets) || refEval(t, args[1], facets)
	case operators.LogicalNot:
		return !refEval(t, args[0], facets)
	case operators.In, operators.OldIn:
		key, _ := stringLiteral(args[0])
		_, present := facets[key]
		return present
	case operators.Equals, operators.NotEquals:
		key, lit, _, _ := attrAndScalar(args)
		return refLeafEq(facets[key], present(facets, key), lit, call.FunctionName() == operators.NotEquals)
	case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals:
		key, lit, flipped, _ := attrAndScalar(args)
		return refLeafNum(facets[key], present(facets, key), call.FunctionName(), flipped, lit)
	default:
		t.Fatalf("refEval: unsupported op %q", call.FunctionName())
		return false
	}
}

func present(f facetSet, key string) bool { _, ok := f[key]; return ok }

// refLeafEq is the independent equality/inequality leaf: present AND matching value_type AND
// (equal, or different for !=).
func refLeafEq(sv storedVal, present bool, lit celast.Expr, negate bool) bool {
	if !present {
		return false
	}
	switch v := lit.AsLiteral().Value().(type) {
	case string:
		if sv.valueType != "STRING" {
			return false
		}
		return (sv.value == v) != negate
	case bool:
		if sv.valueType != "BOOLEAN" {
			return false
		}
		return (sv.value == strconv.FormatBool(v)) != negate
	case int64:
		return refNumEq(sv, float64(v), negate)
	case uint64:
		return refNumEq(sv, float64(v), negate)
	case float64:
		return refNumEq(sv, v, negate)
	default:
		return false
	}
}

func refNumEq(sv storedVal, n float64, negate bool) bool {
	if sv.valueType != "LONG" && sv.valueType != "DOUBLE" {
		return false
	}
	f, err := strconv.ParseFloat(sv.value, 64)
	if err != nil {
		return false
	}
	return (f == n) != negate
}

// refLeafNum is the independent ordered-comparison leaf: present AND numeric AND ordered.
func refLeafNum(sv storedVal, present bool, fn string, flipped bool, lit celast.Expr) bool {
	if !present || (sv.valueType != "LONG" && sv.valueType != "DOUBLE") {
		return false
	}
	f, err := strconv.ParseFloat(sv.value, 64)
	if err != nil {
		return false
	}
	var n float64
	switch v := lit.AsLiteral().Value().(type) {
	case int64:
		n = float64(v)
	case uint64:
		n = float64(v)
	case float64:
		n = v
	default:
		return false
	}
	// Compute the effective comparison independently of the lowering's numericSQLOp, so a
	// flip bug there cannot hide in a shared helper. `less` is the direction; a commuted
	// literal (attr on the right) inverts the direction but not the or-equal part.
	less := fn == operators.Less || fn == operators.LessEquals
	orEq := fn == operators.LessEquals || fn == operators.GreaterEquals
	if flipped {
		less = !less
	}
	switch {
	case less && orEq:
		return f <= n
	case less:
		return f < n
	case orEq:
		return f >= n
	default:
		return f > n
	}
}

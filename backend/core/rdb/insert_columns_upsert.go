// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Fragment is a piece of SQL a ColumnTable upsert splices into its statement: an
// assignment's right-hand side, or the caller's WHERE guard.
//
// 🔴 IT IS A NAMED STRING TYPE SO THAT ONLY A CONSTANT CAN BE ONE. An untyped string
// constant converts to it implicitly; a string VARIABLE does not, so `Fragment(x)` is the
// only way to hand it a runtime value and the raw-SQL guard (backend/tools/rdbguard) flags
// every upsert construction site whose fragments are not literals or constants. A fragment
// is text a reviewer reads once, at the place the table is built, and never anything that
// was computed.
type Fragment string

// Assignment is one `column = expression` of an upsert's DO UPDATE SET.
type Assignment struct {
	Column string
	Expr   Fragment
}

// SetTo assigns expr to column when a row conflicts. expr may name the existing row as
// `t.<column>` and the incoming row as `excluded.<column>`. The guarantee is an ALLOWLIST,
// not a filter: a fragment is accepted only if it is built from those column references,
// numeric and plain string literals, AND / OR / NOT / IS [NOT] [DISTINCT FROM] / NULL /
// TRUE / FALSE / CASE, COALESCE and NULLIF, arithmetic and comparison operators and
// parentheses (see insert_columns_fragment.go). It can therefore reach no other table, call
// no other function and carry no second assignment. GREATEST and LEAST are not allowed (they
// do not exist on SQLite); use CASE WHEN.
func SetTo(column string, expr Fragment) Assignment {
	return Assignment{Column: column, Expr: expr}
}

// Upsert is the update arm of a column-array upsert.
type Upsert struct {
	// Set is the DO UPDATE SET list. It may not assign the tenant column, `id` or
	// `created_at`, nor a column outside the table's columns; it is not empty.
	Set []Assignment
	// Where is the caller's guard on the update arm (a monotonic clock, say); "" for none.
	// It is rendered `(<Where>) AND t.tenant_id = $1`, so it cannot widen the tenant term.
	Where Fragment
}

// NewColumnUpsert describes the column-array UPSERT of the model R: the statement
//
//	INSERT INTO t AS t (tenant_id, c1, …) SELECT $1::text, unnest($2::T1[]), …
//	ON CONFLICT (…) DO UPDATE SET <Set> WHERE (<Where>) AND t.tenant_id = $1
//
// Everything NewColumnTable's insert checks and does is checked and done here (the tenant
// bound from the context, the erasure fence read once per transaction, the refused models),
// with these terms on top:
//
//   - the conflict target must name the tenant column, and the update arm is confined to the
//     statement's own tenant by a term the caller's guard cannot reach: a conflicting row of
//     another tenant is neither updated nor counted;
//   - SET may not assign the tenant column, `id` or `created_at`;
//   - a gorm.Model-shaped table is accepted on explicit terms: `id` is omitted (the sequence
//     applies), `created_at` and `updated_at` are columns the CALLER stamps (a zero is
//     refused with ErrColumnValue, because this write reads no clock), and `deleted_at` is
//     never written, so it is NULL, and never in SET. A field whose default is a database
//     expression is refused;
//   - a zero value in a column whose field has a LITERAL default is replaced by that default,
//     exactly as a gorm Create does.
//
// It panics when a fragment is not acceptable (validateFragment): the fragments are
// constants, so a bad one is a programming error and fails the first time the package that
// declares the table is loaded, not on the first batch.
func NewColumnUpsert[R any](tenant func(*R) *string, conflict []string, up Upsert, columns ...Column[R]) *ColumnTable[R] {
	up.Set = slices.Clone(up.Set)
	var refs []string
	for _, a := range up.Set {
		r, err := validateFragment("SET "+a.Column, string(a.Expr), true)
		if err != nil {
			panic(err)
		}
		refs = append(refs, r...)
	}
	r, err := validateFragment("WHERE", string(up.Where), false)
	if err != nil {
		panic(err)
	}
	refs = append(refs, r...)
	return &ColumnTable[R]{tenant: tenant, conflict: conflict, columns: columns, upsert: &up, fragmentColumns: refs}
}

// omittedByUpsert is the generated primary key: an upsert leaves `id` to the sequence.
func omittedByUpsert(f *schema.Field) bool {
	return f.DBName == "id" && f.PrimaryKey && f.AutoIncrement
}

// isSoftDelete is gorm.DeletedAt's column, which an upsert never writes (NULL) or sets.
func isSoftDelete(f *schema.Field) bool {
	return f.FieldType == reflect.TypeFor[gorm.DeletedAt]()
}

// upsertAssignments renders the SET list, refusing an assignment the terms forbid.
func upsertAssignments(up *Upsert, s *schema.Schema, tenantField *schema.Field, index map[string]int,
	q func(string) string) ([]string, error) {
	if len(up.Set) == 0 {
		return nil, fmt.Errorf("the upsert has an empty SET; an insert that does nothing on a conflict is Insert")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(up.Set))
	for _, a := range up.Set {
		f := s.LookUpField(a.Column)
		switch {
		case f == nil || f.DBName != a.Column:
			return nil, fmt.Errorf("SET names %q, which is not a column of the model", a.Column)
		case f == tenantField:
			return nil, fmt.Errorf("SET assigns the tenant column %q, which would move a row between tenants", a.Column)
		case omittedByUpsert(f):
			return nil, fmt.Errorf("SET assigns %q, the generated id", a.Column)
		case isSoftDelete(f):
			return nil, fmt.Errorf("SET assigns %q, which an upsert never writes", a.Column)
		case a.Column == "created_at" || f.AutoCreateTime != 0:
			return nil, fmt.Errorf("SET assigns %q, which records when the row was first written", a.Column)
		}
		if _, ok := index[a.Column]; !ok {
			return nil, fmt.Errorf("SET names %q, which is not one of the table's columns", a.Column)
		}
		if seen[a.Column] {
			return nil, fmt.Errorf("SET assigns %q twice", a.Column)
		}
		seen[a.Column] = true
		out = append(out, q(a.Column)+" = "+string(a.Expr))
	}
	return out, nil
}

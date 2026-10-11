// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ColumnTable is the column-array insert for one tenant-scoped model: a batch of rows
// written as ONE statement that binds one array per column, instead of the multi-row
// VALUES list a gorm Create builds, which binds one parameter per value. On Postgres the
// statement is
//
//	INSERT INTO t (tenant_id, c1, …, cn)
//	SELECT $1::text, unnest($2::T1[]), …, unnest($n+1::Tn[])
//	ON CONFLICT (…) DO NOTHING
//
// so it binds n+1 parameters for any number of rows, and its SQL text — and therefore its
// prepared statement — is the same for every batch size. A Create of 64 rows of an
// eleven-column model binds 704 parameters, each paying gorm reflection, a database/sql
// argument and a driver encode-plan lookup.
//
// 🔴 IT BYPASSES GORM'S CREATE CALLBACK CHAIN, SO IT DOES ITSELF EVERYTHING THAT CHAIN DOES
// FOR THE MODELS IT ACCEPTS — and refuses, when the table is built, every model for which
// that would not be true. What the chain does on a create, and where each piece lives here:
//
//   - dc:tenant_create (tenant_scope.go): a tenant is REQUIRED in the context
//     (core.ErrNoTenant otherwise, and a system context is refused the same way — this write
//     takes its tenant from the context and a system context names none); a row naming a
//     different tenant is refused with ErrTenantMismatch; every row is then stamped with the
//     context's tenant. Here the stamp is also STRUCTURAL: the tenant column is not one of
//     the arrays at all but the single scalar $1, bound from the context, so no row value can
//     reach that column. The row's own field is still checked and stamped, so a caller that
//     filed a row under the wrong tenant hears about it, and the rows it gets back read as
//     a Create's would.
//   - dc:tenant_upsert_check: only acts on DO UPDATE, which this does not offer. The
//     conflict target must still name the tenant column (refused at build otherwise), the
//     rule tenant_upsert.go applies to an upsert's target.
//   - dc:tenant_fence_create (tenant_fence.go): the same readFence, with the same
//     per-transaction memo, read exactly when the handle has that callback registered — that
//     is, exactly when a Create on the same handle would read it.
//   - dc:token_grammar_create: acts only on a model with a Token field. Such a model is
//     REFUSED here, so a future entity cannot adopt this path and skip token validation.
//   - dc:audit_create: acts only on a model that is not AuditExempt. Such a model is
//     REFUSED here, so a control-plane entity cannot adopt this path and drop out of the
//     journal.
//   - gorm's own create machinery: hooks (BeforeCreate, AfterCreate, BeforeSave, AfterSave),
//     associations, auto create/update times, column defaults and expression-valued fields
//     all change what a Create writes. A model with any of them is REFUSED, as is one whose
//     creatable columns the table does not cover exactly, so the rows written are the rows
//     a Create would have written.
//
// The one raw statement it issues is reviewed in the raw-SQL guard's allow-list
// (backend/tools/rdbguard), like every other raw statement over a tenant table; its text
// is built from the model's own schema and never from a caller.
//
// Only Postgres and SQLite are rendered. SQLite has no unnest, so there the same checks
// guard a multi-row VALUES statement: that rendering exists so the unit tests that drive
// the write path on an in-memory database exercise this code rather than a different one.
// Any other dialect is refused.
type ColumnTable[R any] struct {
	tenant   func(*R) *string
	conflict []string
	columns  []Column[R]

	// plans caches the validated statement per dialect and resolved table name: the name
	// depends on the handle's naming strategy (its schema prefix), not on the model alone.
	plans sync.Map // dialect + "/" + table -> *columnPlan
}

// Column is one column of a ColumnTable: its name, the Postgres element type its array
// is bound as, and how its values are read from a row. Build one with the typed
// constructors (TextColumn, BytesColumn, …), which fix the Go type of the array.
type Column[R any] struct {
	name   string
	pgType string
	// array builds the column's values for every row, as the slice the driver binds, or
	// refuses a value the column cannot hold (ErrColumnValue).
	array func(rows []*R) (any, error)
	// value reads one row's value, for the VALUES rendering.
	value func(row *R) any
}

func typedColumn[R, V any](name, pgType string, get func(*R) V) Column[R] {
	return Column[R]{
		name:   name,
		pgType: pgType,
		array: func(rows []*R) (any, error) {
			out := make([]V, len(rows))
			for i, r := range rows {
				out[i] = get(r)
			}
			return out, nil
		},
		value: func(r *R) any { return get(r) },
	}
}

// TextColumn binds a string column (text or varchar) as text[].
func TextColumn[R any](name string, get func(*R) string) Column[R] {
	return typedColumn(name, "text", get)
}

// NullTextColumn binds a nullable string column as text[]; a nil pointer is NULL.
func NullTextColumn[R any](name string, get func(*R) *string) Column[R] {
	return typedColumn(name, "text", get)
}

// BytesColumn binds a bytea column as bytea[].
func BytesColumn[R any](name string, get func(*R) []byte) Column[R] {
	return typedColumn(name, "bytea", get)
}

// Int64Column binds an integer column as int8[].
func Int64Column[R any](name string, get func(*R) int64) Column[R] {
	return typedColumn(name, "int8", get)
}

// NullInt64Column binds a nullable integer column as int8[]; a nil pointer is NULL.
func NullInt64Column[R any](name string, get func(*R) *int64) Column[R] {
	return typedColumn(name, "int8", get)
}

// NullUintColumn binds a nullable unsigned integer stored in a bigint column as int8[]; a
// nil pointer is NULL. A value above the bigint range is REFUSED (ErrColumnValue), never
// converted: a plain int64 conversion would wrap it into a negative number and store that,
// where binding the uint as a single parameter fails in the driver.
func NullUintColumn[R any](name string, get func(*R) *uint) Column[R] {
	conv := func(r *R) (*int64, error) {
		v := get(r)
		if v == nil {
			return nil, nil
		}
		if uint64(*v) > math.MaxInt64 {
			return nil, fmt.Errorf("%w: column %q: %d is above the bigint range", ErrColumnValue, name, *v)
		}
		c := int64(*v)
		return &c, nil
	}
	return Column[R]{
		name:   name,
		pgType: "int8",
		array: func(rows []*R) (any, error) {
			out := make([]*int64, len(rows))
			for i, r := range rows {
				c, err := conv(r)
				if err != nil {
					return nil, err
				}
				out[i] = c
			}
			return out, nil
		},
		// Only reached after array has accepted every row (see Insert).
		value: func(r *R) any { c, _ := conv(r); return c },
	}
}

// TimeColumn binds a timestamptz column as timestamptz[].
func TimeColumn[R any](name string, get func(*R) time.Time) Column[R] {
	return typedColumn(name, "timestamptz", get)
}

// NumericColumn binds a nullable numeric/decimal column.
//
// 🔴 ON POSTGRES THE VALUES TRAVEL AS TEXT, AND THE TEXT IS EXACTLY THE DRIVER'S. A float64
// bound as a single numeric parameter is encoded by the driver from
// strconv.FormatFloat(v, 'f', -1, 64) — the shortest decimal that round-trips — and the
// server then rounds that decimal to the column's scale. Binding the array as text[] of
// the same string, cast to numeric[] by the server, hands the server the same decimal, so
// it rounds identically. A float8[] would not: the server's float8-to-numeric cast prints
// only 15 significant digits. NaN and ±Inf are spelled the way numeric reads them, which
// is what the driver's binary encoding of them means.
func NumericColumn[R any](name string, get func(*R) sql.NullFloat64) Column[R] {
	return Column[R]{
		name:   name,
		pgType: "numeric",
		array: func(rows []*R) (any, error) {
			out := make([]*string, len(rows))
			for i, r := range rows {
				if v := get(r); v.Valid {
					s := numericText(v.Float64)
					out[i] = &s
				}
			}
			return out, nil
		},
		value: func(r *R) any { return get(r) },
	}
}

// numericText renders v as the decimal the driver would have encoded for a numeric
// parameter. See NumericColumn.
func numericText(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ErrColumnarModel is the refusal for a model this write cannot stand in a Create for.
// It is raised the first time a ColumnTable is used on a handle — every call refuses the
// same way — and names what disqualified the model.
var ErrColumnarModel = errors.New("the column-array insert cannot write this model the way a gorm Create would")

// ErrColumnValue is the refusal for a batch the insert cannot bind as its columns: a
// value the column's type cannot hold (an unsigned integer above the bigint range), or a
// column array that does not carry exactly one element per row. Nothing is written.
var ErrColumnValue = errors.New("the column-array insert cannot bind this batch")

// NewColumnTable describes the column-array insert of the model R (a struct type, e.g.
// Event): tenant returns a row's own tenant field — it must be the model's tenant field,
// which is checked when the table is built — and that field is checked and stamped;
// conflict is the ON CONFLICT … DO NOTHING target, by column name; columns are every other
// creatable column of the model, each once. Nothing is checked here: the model is
// validated against the handle's schema the first time Insert runs on it, and refused
// there (ErrColumnarModel) if a Create would write anything different.
func NewColumnTable[R any](tenant func(*R) *string, conflict []string, columns ...Column[R]) *ColumnTable[R] {
	return &ColumnTable[R]{tenant: tenant, conflict: conflict, columns: columns}
}

// columnPlan is a ColumnTable validated against one resolved table.
type columnPlan struct {
	// unnest is the whole Postgres statement.
	unnest string
	// head is "INSERT INTO t (cols) VALUES " and tail the ON CONFLICT clause, for the
	// VALUES rendering, which repeats a placeholder group per row between them.
	head, tail string
	// group is one row's placeholder group, "(?,…,?)".
	group string
}

// sqliteMaxVariables is SQLite's default bound on the parameters one statement binds
// (SQLITE_MAX_VARIABLE_NUMBER since 3.32). The VALUES rendering splits by it.
const sqliteMaxVariables = 32766

// Insert writes rows ON CONFLICT … DO NOTHING under the tenant in db's context and
// returns the number of rows inserted (those the conflict target did not absorb). db is
// the handle to write on — a transaction, normally — with the context of the tenant the
// rows belong to (db.WithContext(ctx)). Outside a transaction it opens one, as a Create
// does, so the fence read and the write commit together.
//
// Before anything is written, every row's tenant field is checked against the context's
// and then set to it; on a refusal nothing is stamped and nothing is written. An empty
// rows writes nothing and reads nothing.
func (t *ColumnTable[R]) Insert(db *gorm.DB, rows []*R) (int64, error) {
	if db.Error != nil {
		return 0, db.Error
	}
	ctx := db.Statement.Context
	if core.IsSystemContext(ctx) {
		return 0, fmt.Errorf("%w: the column-array insert takes its tenant from the context, and a "+
			"system context names none", core.ErrNoTenant)
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return 0, core.ErrNoTenant
	}
	plan, err := t.plan(db)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, r := range rows {
		if named := *t.tenant(r); named != "" && named != tenant {
			return 0, fmt.Errorf("%w: a row names tenant %q while the context names %q",
				ErrTenantMismatch, named, tenant)
		}
	}
	arrays, err := t.arrays(rows)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		*t.tenant(r) = tenant
	}
	fenced := db.Callback().Create().Get(fenceCreateCallback) != nil
	write := func(tx *gorm.DB) (int64, error) {
		if fenced {
			if err := readFence(tx, []string{tenant}); err != nil {
				return 0, err
			}
		}
		return t.exec(tx, plan, tenant, rows, arrays)
	}
	session := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	if _, inTx := session.Statement.ConnPool.(gorm.TxCommitter); inTx {
		return write(session)
	}
	var affected int64
	err = session.Transaction(func(tx *gorm.DB) error {
		n, err := write(tx)
		affected = n
		return err
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// arrays builds every column's array for rows and checks it carries exactly one element
// per row. unnest pads a shorter array with NULLs and the statement would succeed, so a
// short array is refused here rather than stored as missing values. It runs for every
// dialect, so a value a column refuses is refused on sqlite too.
func (t *ColumnTable[R]) arrays(rows []*R) ([]any, error) {
	out := make([]any, len(t.columns))
	for i, c := range t.columns {
		a, err := c.array(rows)
		if err != nil {
			return nil, err
		}
		if n := reflect.ValueOf(a).Len(); n != len(rows) {
			return nil, fmt.Errorf("%w: column %q has %d values for %d rows", ErrColumnValue, c.name, n, len(rows))
		}
		out[i] = a
	}
	return out, nil
}

// exec issues the statement(s) for rows on tx; arrays are the checked column arrays.
func (t *ColumnTable[R]) exec(tx *gorm.DB, plan *columnPlan, tenant string, rows []*R, arrays []any) (int64, error) {
	if plan.unnest != "" {
		args := make([]any, 0, len(t.columns)+1)
		args = append(args, tenant)
		args = append(args, arrays...)
		res := tx.Exec(plan.unnest, args...)
		return res.RowsAffected, res.Error
	}
	per := sqliteMaxVariables / (len(t.columns) + 1)
	var affected int64
	for start := 0; start < len(rows); start += per {
		chunk := rows[start:min(start+per, len(rows))]
		var b strings.Builder
		b.WriteString(plan.head)
		args := make([]any, 0, len(chunk)*(len(t.columns)+1))
		for i, r := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(plan.group)
			args = append(args, tenant)
			for _, c := range t.columns {
				args = append(args, c.value(r))
			}
		}
		b.WriteString(plan.tail)
		res := tx.Exec(b.String(), args...)
		if res.Error != nil {
			return 0, res.Error
		}
		affected += res.RowsAffected
	}
	return affected, nil
}

// plan validates the table against db's schema for its model, once per resolved table,
// and returns the statement to issue.
func (t *ColumnTable[R]) plan(db *gorm.DB) (*columnPlan, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(new(R)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrColumnarModel, err)
	}
	// The statement is the dialect's as much as the table's: its quoting and its shape.
	key := db.Dialector.Name() + "/" + stmt.Schema.Table
	if p, ok := t.plans.Load(key); ok {
		return p.(*columnPlan), nil
	}
	p, err := t.build(db, stmt)
	if err != nil {
		return nil, err
	}
	t.plans.Store(key, p)
	return p, nil
}

func (t *ColumnTable[R]) build(db *gorm.DB, stmt *gorm.Statement) (*columnPlan, error) {
	s := stmt.Schema
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w (%s): %s", ErrColumnarModel, s.Name, fmt.Sprintf(format, args...))
	}
	tenantField := schemaTenantField(s)
	if tenantField == nil {
		return nil, refuse("it carries no tenant field, and this write exists only for tenant-scoped tables")
	}
	if _, ok := s.FieldsByName[tokenFieldName]; ok {
		return nil, refuse("it has a %s field, whose grammar a Create validates and this write would not",
			tokenFieldName)
	}
	if s.ModelType != reflect.TypeFor[R]() {
		return nil, refuse("the row type is not the model's struct type")
	}
	// The accessor must hand back the model's own tenant field: one pointing at any other
	// field would check, stamp and bind the tenant through that column instead.
	row := new(R)
	want := reflect.ValueOf(row).Elem().FieldByIndex(tenantField.StructField.Index).Addr().Pointer()
	if got := t.tenant(row); got == nil || reflect.ValueOf(got).Pointer() != want {
		return nil, refuse("the tenant accessor does not return the model's %s field", tenantField.Name)
	}
	probe := any(row)
	if exempt, ok := probe.(AuditExempt); !ok || !exempt.AuditExempt() {
		return nil, refuse("it is audited, and this write would leave no audit-journal row")
	}
	if exempt, ok := probe.(FenceExempt); ok && exempt.FenceExempt() {
		return nil, refuse("it is exempt from the erasure fence, which this write always reads")
	}
	if s.BeforeCreate || s.AfterCreate || s.BeforeSave || s.AfterSave {
		return nil, refuse("it has create or save hooks, which a Create runs and this write would not")
	}
	if len(s.Relationships.Relations) > 0 {
		return nil, refuse("it has associations, which a Create also writes")
	}

	named := map[string]bool{}
	for _, c := range t.columns {
		if named[c.name] {
			return nil, refuse("column %q is listed twice", c.name)
		}
		named[c.name] = true
		f := s.LookUpField(c.name)
		if f == nil || f.DBName != c.name {
			return nil, refuse("column %q is not a column of the model", c.name)
		}
		if f == tenantField {
			return nil, refuse("column %q is the tenant column, which the write binds from the context", c.name)
		}
		if !f.Creatable {
			return nil, refuse("column %q is not written by a Create", c.name)
		}
	}
	for _, name := range s.DBNames {
		f := s.FieldsByDBName[name]
		if f == nil || !f.Creatable {
			continue
		}
		if f.AutoCreateTime != 0 || f.AutoUpdateTime != 0 {
			return nil, refuse("column %q is stamped by gorm on create", name)
		}
		if f.HasDefaultValue {
			return nil, refuse("column %q has a default, which a Create applies to a zero value", name)
		}
		if bindsExpression(f.FieldType) {
			return nil, refuse("column %q binds an expression", name)
		}
		if f != tenantField && !named[name] {
			return nil, refuse("column %q is not covered, so the write would leave it to the database "+
				"where a Create writes the field's value", name)
		}
	}
	if len(t.conflict) == 0 {
		return nil, refuse("no conflict target")
	}
	hasTenant := false
	for _, name := range t.conflict {
		f := s.LookUpField(name)
		if f == nil || f.DBName != name {
			return nil, refuse("conflict column %q is not a column of the model", name)
		}
		hasTenant = hasTenant || f == tenantField
	}
	if !hasTenant {
		return nil, refuse("the conflict target does not name the tenant column, so one tenant's row " +
			"could absorb another's")
	}

	q := func(name string) string { return stmt.Quote(name) }
	cols := make([]string, 0, len(t.columns)+1)
	cols = append(cols, q(tenantField.DBName))
	for _, c := range t.columns {
		cols = append(cols, q(c.name))
	}
	target := make([]string, len(t.conflict))
	for i, name := range t.conflict {
		target[i] = q(name)
	}
	// Schema.Table, not Statement.Table: the latter is the bare name, with the schema prefix
	// split off into TableExpr, and the statement must name the table exactly as a Create does.
	insert := "INSERT INTO " + q(stmt.Schema.Table) + " (" + strings.Join(cols, ",") + ")"
	tail := " ON CONFLICT (" + strings.Join(target, ",") + ") DO NOTHING"

	p := &columnPlan{}
	switch name := db.Dialector.Name(); name {
	case "postgres":
		// One unnest per column in the SELECT LIST, not unnest(a, b, …) in FROM. The FROM
		// form is ROWS FROM (unnest(a), unnest(b), …), a function scan that materialises
		// every column's elements into a tuplestore of its own before it emits a row; in the
		// select list the unnests run in lockstep, one element of each per row, with nothing
		// materialised. On the event store's tables that is the difference between costing
		// the server a little more per batch than the VALUES list it replaces and a little
		// less. Every array has one element per row, so lockstep pairs them exactly.
		unnests := make([]string, len(t.columns))
		for i, c := range t.columns {
			if c.pgType == "numeric" {
				// Text on the wire, numeric to the server: see NumericColumn.
				unnests[i] = "unnest($" + strconv.Itoa(i+2) + "::text[]::numeric[])"
			} else {
				unnests[i] = "unnest($" + strconv.Itoa(i+2) + "::" + c.pgType + "[])"
			}
		}
		p.unnest = insert + " SELECT $1::text, " + strings.Join(unnests, ", ") + tail
	case "sqlite":
		p.head = insert + " VALUES "
		p.tail = tail
		p.group = "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	default:
		return nil, refuse("dialect %q is not rendered; only postgres and sqlite are", name)
	}
	// gorm's Exec rewrites '?' (and '@name') in the SQL it is handed. The Postgres statement
	// must contain neither, or its arrays would be bound somewhere other than where its $n
	// placeholders say; the VALUES statement must contain no '?' but its own.
	check := p.unnest
	if check == "" {
		check = p.head + p.tail
	}
	if strings.ContainsAny(check, "?@") {
		return nil, refuse("a table or column name contains '?' or '@', which the statement cannot carry")
	}
	return p, nil
}

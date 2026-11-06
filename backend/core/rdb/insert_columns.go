// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
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
//   - dc:tenant_upsert_check: only acts on DO UPDATE, which Insert does not offer. The
//     conflict target must still name the tenant column (refused at build otherwise), the
//     rule tenant_upsert.go applies to an upsert's target. Upsert (NewColumnUpsert, in
//     insert_columns_upsert.go) does offer it, and does itself what that callback does:
//     the update arm is confined to the statement's own tenant, SET may not assign the
//     tenant column, and the caller's guard is parenthesised.
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
// is built from the model's own schema and, for an upsert, from SQL fragments that are
// constants given once when the table is built (see Fragment), never from a caller's data.
//
// Only Postgres and SQLite are rendered. SQLite has no unnest, so there the same checks
// guard a multi-row VALUES statement: that rendering exists so the unit tests that drive
// the write path on an in-memory database exercise this code rather than a different one.
// Any other dialect is refused.
type ColumnTable[R any] struct {
	tenant   func(*R) *string
	conflict []string
	columns  []Column[R]

	// upsert is nil for the insert (ON CONFLICT DO NOTHING) and set for the upsert mode
	// built by NewColumnUpsert.
	upsert *Upsert
	// fragmentColumns are the columns the upsert's fragments name, checked at plan time.
	fragmentColumns []string

	// plans caches the validated statement per dialect and resolved table name: the name
	// depends on the handle's naming strategy (its schema prefix), not on the model alone.
	plans sync.Map // dialect + "/" + table -> *columnPlan[R]
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
	// zero reports whether one row's value is the Go zero value; set only by the
	// constructors an upsert needs it for (TimeColumn: a caller-stamped time must be set).
	zero func(row *R) bool
	// withDefault returns this column with a zero value replaced by def, the value gorm
	// parsed from the field's literal `default:` tag; nil for a column type that cannot
	// substitute. Upsert mode only.
	withDefault func(def any) (Column[R], error)
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

// defaulting makes the withDefault of a typed column: the column rebuilt so a zero value
// reads as def, which is what a gorm Create does for a field with a literal default.
func defaulting[R, V any](name, pgType string, get func(*R) V, isZero func(V) bool,
	conv func(any) (V, bool)) func(any) (Column[R], error) {
	return func(def any) (Column[R], error) {
		v, ok := conv(def)
		if !ok {
			return Column[R]{}, fmt.Errorf("column %q: its default %v (%T) is not a value this column type holds",
				name, def, def)
		}
		return typedColumn(name, pgType, func(r *R) V {
			if x := get(r); !isZero(x) {
				return x
			}
			return v
		}), nil
	}
}

// TextColumn binds a string column (text or varchar) as text[].
func TextColumn[R any](name string, get func(*R) string) Column[R] {
	c := typedColumn(name, "text", get)
	c.withDefault = defaulting(name, "text", get, func(v string) bool { return v == "" },
		func(d any) (string, bool) { s, ok := d.(string); return s, ok })
	return c
}

// BoolColumn binds a boolean column as bool[].
func BoolColumn[R any](name string, get func(*R) bool) Column[R] {
	c := typedColumn(name, "bool", get)
	c.withDefault = defaulting(name, "bool", get, func(v bool) bool { return !v },
		func(d any) (bool, bool) { b, ok := d.(bool); return b, ok })
	return c
}

// IntColumn binds an int column as int8[] (the server narrows it to the column's width
// on assignment, as it does a single bound parameter).
func IntColumn[R any](name string, get func(*R) int) Column[R] {
	wide := func(r *R) int64 { return int64(get(r)) }
	c := typedColumn(name, "int8", wide)
	c.withDefault = defaulting(name, "int8", wide, func(v int64) bool { return v == 0 }, int64Default)
	return c
}

// int64Default reads a gorm-parsed integer default.
func int64Default(d any) (int64, bool) {
	switch v := d.(type) {
	case int64:
		return v, true
	case uint64:
		if v <= math.MaxInt64 {
			return int64(v), true
		}
	}
	return 0, false
}

// Uint64Column binds an unsigned 64-bit integer stored in a bigint column as int8[]. A
// value above the bigint range is REFUSED (ErrColumnValue), never converted: a plain int64
// conversion would wrap it into a negative number and store that, where binding the uint64
// as a single parameter fails in the driver (see IsEncodeRefusal).
func Uint64Column[R any](name string, get func(*R) uint64) Column[R] {
	conv := func(v uint64) (int64, error) {
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("%w: column %q: %d is above the bigint range", ErrColumnValue, name, v)
		}
		return int64(v), nil
	}
	return Column[R]{
		name:   name,
		pgType: "int8",
		array: func(rows []*R) (any, error) {
			out := make([]int64, len(rows))
			for i, r := range rows {
				c, err := conv(get(r))
				if err != nil {
					return nil, err
				}
				out[i] = c
			}
			return out, nil
		},
		// Only reached after array has accepted every row (see Insert).
		value: func(r *R) any { c, _ := conv(get(r)); return c },
		withDefault: func(def any) (Column[R], error) {
			d, ok := int64Default(def)
			if !ok {
				return Column[R]{}, fmt.Errorf("column %q: its default %v (%T) is not a bigint", name, def, def)
			}
			return Uint64Column(name, func(r *R) uint64 {
				if v := get(r); v != 0 {
					return v
				}
				return uint64(d)
			}), nil
		},
	}
}

// anchorsPgType marks the Anchors column: the array is text[] of each row's array LITERAL
// and the statement casts every element, so the column is text[] in the table.
const anchorsPgType = "text[]"

// AnchorsColumn binds a text[] anchors column (declared as Anchors).
//
// 🔴 EACH ROW TRAVELS AS THE STRING Anchors.Value() RENDERS: a PostgreSQL array literal
// with every element quoted, the exact text a gorm Create binds. The statement then reads
// `unnest($n::text[])::text[]`, so the server parses each literal the way it parses the
// single bound parameter. That is what keeps a nil list NULL (a nil element in the bound
// text[]), an empty list '{}', and an element holding a quote, backslash, comma or brace
// intact: none of them is ever spliced into SQL, and none is split by the array syntax of
// the outer bind (the driver encodes each string as one element of its own).
func AnchorsColumn[R any](name string, get func(*R) Anchors) Column[R] {
	return Column[R]{
		name:   name,
		pgType: anchorsPgType,
		array: func(rows []*R) (any, error) {
			out := make([]*string, len(rows))
			for i, r := range rows {
				v, err := get(r).Value()
				if err != nil {
					return nil, fmt.Errorf("%w: column %q: %v", ErrColumnValue, name, err)
				}
				if v != nil {
					s := v.(string)
					out[i] = &s
				}
			}
			return out, nil
		},
		value: func(r *R) any { return get(r) },
	}
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
	c := typedColumn(name, "int8", get)
	c.withDefault = defaulting(name, "int8", get, func(v int64) bool { return v == 0 }, int64Default)
	return c
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
	c := typedColumn(name, "timestamptz", get)
	c.zero = func(r *R) bool { return get(r).IsZero() }
	return c
}

// NullTimeColumn binds a nullable timestamptz column (sql.NullTime) as timestamptz[]; an
// invalid value is NULL. A Valid zero time is written as the zero time, as a Create writes it.
func NullTimeColumn[R any](name string, get func(*R) sql.NullTime) Column[R] {
	return Column[R]{
		name:   name,
		pgType: "timestamptz",
		array: func(rows []*R) (any, error) {
			out := make([]*time.Time, len(rows))
			for i, r := range rows {
				if v := get(r); v.Valid {
					t := v.Time
					out[i] = &t
				}
			}
			return out, nil
		},
		value: func(r *R) any { return get(r) },
	}
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
type columnPlan[R any] struct {
	// unnest is the whole Postgres statement.
	unnest string
	// unnestPlain is the same statement without the ON CONFLICT clause, issued when the
	// caller opted in with WithPlainInsert. Empty for an upsert, and for sqlite.
	unnestPlain string
	// head is "INSERT INTO t (cols) VALUES " and tail the ON CONFLICT clause, for the
	// VALUES rendering, which repeats a placeholder group per row between them.
	head, tail string
	// group is one row's placeholder group, "(?,…,?)".
	group string
	// cols are the columns as written: the table's, with a zero value replaced by its
	// field's literal default where the upsert's model has one.
	cols []Column[R]
	// mustSet are the columns whose zero value is refused (caller-stamped times).
	mustSet []Column[R]
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
	if t.upsert != nil {
		return 0, fmt.Errorf("%w: this table was built by NewColumnUpsert; call Upsert", ErrColumnarModel)
	}
	return t.write(db, rows)
}

// Upsert writes rows ON CONFLICT … DO UPDATE under the tenant in db's context, the way
// Insert writes them, and returns the rows written: those inserted plus those updated. A
// conflicting row the update arm's WHERE excludes (the caller's guard, or another
// tenant's row) is neither updated nor counted. Everything Insert checks is checked here,
// and the terms of the upsert (NewColumnUpsert) on top. Two rows of one batch that name the
// same conflict key fail on Postgres (a row cannot be affected twice by one statement), as
// they do in a gorm upsert (SQLite applies them in order, the last winning), so the caller
// deduplicates and a Postgres test must cover it. The update arm also touches a soft-deleted
// row, as a gorm upsert does. A PARTIAL unique index cannot be the arbiter (the conflict
// target carries no index predicate): such an upsert fails loudly on Postgres.
//
// The rows' generated ids are not read back (there is no RETURNING), and a substituted
// default is not written back to the row; only the tenant is stamped on it.
func (t *ColumnTable[R]) Upsert(db *gorm.DB, rows []*R) (int64, error) {
	if t.upsert == nil {
		return 0, fmt.Errorf("%w: this table was built by NewColumnTable; call Insert", ErrColumnarModel)
	}
	return t.write(db, rows)
}

func (t *ColumnTable[R]) write(db *gorm.DB, rows []*R) (int64, error) {
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
	// The scope create callback (dc:scope_create) is bypassed here like every other, and
	// what it would do cannot be done row by row from a column array, so a scoped caller
	// is refused outright. Nothing a scoped member may call writes through this path:
	// it is the event store's ingest write, and it runs under a service's own context.
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		return 0, fmt.Errorf("%w (the column-array insert)", auth.ErrScopeUnsupported)
	}
	// The placement guard (shard.go) is a create callback this path bypasses, so it is asked
	// here, for the context's tenant, when the handle carries it.
	if db.Callback().Create().Get(shardGuardCreate) != nil {
		if err := checkPlacement(db, []string{tenant}); err != nil {
			return 0, err
		}
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
	arrays, err := t.arrays(plan, rows)
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
func (t *ColumnTable[R]) arrays(plan *columnPlan[R], rows []*R) ([]any, error) {
	for _, z := range plan.mustSet {
		for _, r := range rows {
			if z.zero(r) {
				return nil, fmt.Errorf("%w: column %q is stamped by the caller in an upsert and a row leaves it "+
					"zero", ErrColumnValue, z.name)
			}
		}
	}
	out := make([]any, len(plan.cols))
	for i, c := range plan.cols {
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
func (t *ColumnTable[R]) exec(tx *gorm.DB, plan *columnPlan[R], tenant string, rows []*R, arrays []any) (int64, error) {
	if plan.unnest != "" {
		args := make([]any, 0, len(plan.cols)+1)
		args = append(args, tenant)
		args = append(args, arrays...)
		stmt := plan.unnest
		if plan.unnestPlain != "" && plainInsert(tx.Statement.Context) {
			stmt = plan.unnestPlain
		}
		res := tx.Exec(stmt, args...)
		return res.RowsAffected, res.Error
	}
	per := sqliteMaxVariables / (len(plan.cols) + 1)
	var affected int64
	for start := 0; start < len(rows); start += per {
		chunk := rows[start:min(start+per, len(rows))]
		var b strings.Builder
		b.WriteString(plan.head)
		args := make([]any, 0, len(chunk)*(len(plan.cols)+1))
		for i, r := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(plan.group)
			args = append(args, tenant)
			for _, c := range plan.cols {
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
func (t *ColumnTable[R]) plan(db *gorm.DB) (*columnPlan[R], error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(new(R)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrColumnarModel, err)
	}
	// The statement is the dialect's as much as the table's: its quoting and its shape.
	key := db.Dialector.Name() + "/" + stmt.Schema.Table
	if p, ok := t.plans.Load(key); ok {
		return p.(*columnPlan[R]), nil
	}
	p, err := t.build(db, stmt)
	if err != nil {
		return nil, err
	}
	t.plans.Store(key, p)
	return p, nil
}

func (t *ColumnTable[R]) build(db *gorm.DB, stmt *gorm.Statement) (*columnPlan[R], error) {
	s := stmt.Schema
	up := t.upsert
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

	for _, ref := range t.fragmentColumns {
		if f := s.LookUpField(ref); f == nil || f.DBName != ref {
			return nil, refuse("a fragment names %q, which is not a column of the model", ref)
		}
	}
	cols := slices.Clone(t.columns)
	index := map[string]int{}
	for i, c := range cols {
		if _, dup := index[c.name]; dup {
			return nil, refuse("column %q is listed twice", c.name)
		}
		index[c.name] = i
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
		if up != nil && (omittedByUpsert(f) || isSoftDelete(f)) {
			return nil, refuse("column %q is never written by an upsert: the database generates the id, "+
				"and deleted_at is always NULL", c.name)
		}
	}
	var mustSet []Column[R]
	for _, name := range s.DBNames {
		f := s.FieldsByDBName[name]
		if f == nil || !f.Creatable {
			continue
		}
		if up != nil && (omittedByUpsert(f) || isSoftDelete(f)) {
			continue // not a column of the statement: the sequence / NULL applies
		}
		i, covered := index[name]
		if f.AutoCreateTime != 0 || f.AutoUpdateTime != 0 {
			if up == nil {
				return nil, refuse("column %q is stamped by gorm on create", name)
			}
			// gorm would stamp a zero time with now(); this write does not read a clock, so
			// the caller stamps it and a zero is refused (arrays).
			if !covered || cols[i].zero == nil {
				return nil, refuse("column %q is stamped by gorm on create; an upsert needs it as a "+
					"TimeColumn the caller stamps", name)
			}
			mustSet = append(mustSet, cols[i])
			continue
		}
		if f.HasDefaultValue {
			if up == nil {
				return nil, refuse("column %q has a default, which a Create applies to a zero value", name)
			}
			if f.DefaultValueInterface == nil {
				return nil, refuse("column %q has a default that is a database expression (%q), which an "+
					"upsert cannot apply the way a Create does", name, f.DefaultValue)
			}
			if !covered {
				return nil, refuse("column %q is not covered, so the write would leave it to the database "+
					"where a Create writes the field's value", name)
			}
			if cols[i].withDefault == nil {
				return nil, refuse("column %q has a default, and its column type cannot apply it to a zero "+
					"value as a Create does", name)
			}
			sub, err := cols[i].withDefault(f.DefaultValueInterface)
			if err != nil {
				return nil, refuse("%v", err)
			}
			cols[i] = sub
		}
		if bindsExpression(f.FieldType) {
			return nil, refuse("column %q binds an expression", name)
		}
		if f != tenantField && !covered {
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
		if up != nil && (omittedByUpsert(f) || isSoftDelete(f)) {
			return nil, refuse("conflict column %q is never written by an upsert (the database generates "+
				"the id, deleted_at is always NULL), so nothing could ever conflict on it", name)
		}
		hasTenant = hasTenant || f == tenantField
	}
	if !hasTenant {
		return nil, refuse("the conflict target does not name the tenant column, so one tenant's row " +
			"could absorb another's")
	}

	q := func(name string) string { return stmt.Quote(name) }
	names := make([]string, 0, len(cols)+1)
	names = append(names, q(tenantField.DBName))
	for _, c := range cols {
		names = append(names, q(c.name))
	}
	target := make([]string, len(t.conflict))
	for i, name := range t.conflict {
		target[i] = q(name)
	}
	// Schema.Table, not Statement.Table: the latter is the bare name, with the schema prefix
	// split off into TableExpr, and the statement must name the table exactly as a Create does.
	insert := "INSERT INTO " + q(stmt.Schema.Table)
	if up != nil {
		insert += " AS t"
	}
	insert += " (" + strings.Join(names, ",") + ")"
	tail := " ON CONFLICT (" + strings.Join(target, ",") + ") DO NOTHING"
	dialect := db.Dialector.Name()
	if up != nil {
		sets, err := upsertAssignments(up, s, tenantField, index, q)
		if err != nil {
			return nil, refuse("%v", err)
		}
		// The caller's guard stands in parentheses, so an OR in it cannot take the tenant
		// term as one of its operands; the tenant term is the statement's own.
		tenantTerm := "t." + q(tenantField.DBName) + " = $1::text"
		if dialect == "sqlite" {
			tenantTerm = "t." + q(tenantField.DBName) + " = excluded." + q(tenantField.DBName)
		}
		guard := tenantTerm
		if up.Where != "" {
			guard = "(" + string(up.Where) + ") AND " + tenantTerm
		}
		tail = " ON CONFLICT (" + strings.Join(target, ",") + ") DO UPDATE SET " + strings.Join(sets, ", ") +
			" WHERE " + guard
	}

	p := &columnPlan[R]{cols: cols, mustSet: mustSet}
	switch dialect {
	case "postgres":
		// One unnest per column in the SELECT LIST, not unnest(a, b, …) in FROM. The FROM
		// form is ROWS FROM (unnest(a), unnest(b), …), a function scan that materialises
		// every column's elements into a tuplestore of its own before it emits a row; in the
		// select list the unnests run in lockstep, one element of each per row, with nothing
		// materialised. On the event store's tables that is the difference between costing
		// the server a little more per batch than the VALUES list it replaces and a little
		// less. Every array has one element per row, so lockstep pairs them exactly.
		unnests := make([]string, len(cols))
		for i, c := range cols {
			param := "$" + strconv.Itoa(i+2)
			switch c.pgType {
			case "numeric":
				// Text on the wire, numeric to the server: see NumericColumn.
				unnests[i] = "unnest(" + param + "::text[]::numeric[])"
			case anchorsPgType:
				// Array literals on the wire, text[] to the server: see AnchorsColumn.
				unnests[i] = "unnest(" + param + "::text[])::text[]"
			default:
				unnests[i] = "unnest(" + param + "::" + c.pgType + "[])"
			}
		}
		p.unnest = insert + " SELECT $1::text, " + strings.Join(unnests, ", ") + tail
		if up == nil {
			p.unnestPlain = insert + " SELECT $1::text, " + strings.Join(unnests, ", ")
		}
	case "sqlite":
		p.head = insert + " VALUES "
		p.tail = tail
		p.group = "(" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
	default:
		return nil, refuse("dialect %q is not rendered; only postgres and sqlite are", dialect)
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

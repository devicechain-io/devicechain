// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// tenantFieldName is the Go struct field name contributed by the embedded
// TenantScoped type, and the spelling almost every model in the tree uses.
const tenantFieldName = "TenantId"

// plainTenantFieldName is event-processing's spelling. Its tables are not irregular by
// accident: they are read-models keyed by their own natural keys, embedding no core
// mixins, with the tenant as a plain column called `tenant` that is part of the composite
// primary key.
//
// Renaming it is possible — an appended migration could — but it would be a schema change
// to six tables and a primary-key column, bought for nothing. The spelling is a choice
// this area is entitled to make; the only question that matters is whether the mechanisms
// enforcing isolation know about it, and for a long time one of the three did not.
const plainTenantFieldName = "Tenant"

// tenantFieldNames are the two spellings, in lookup order.
//
// 🔴 THIS IS THE ONE PLACE THAT DECIDES WHAT "TENANT-SCOPED" MEANS, and it is one place
// because it used to be three that disagreed. The erasure fence and the tenant purge
// both learned the second spelling when event-processing's tables arrived; this
// callback — the only one of the three that FAILS CLOSED — did not. The result was not
// a refusal but a silence: a model carrying real tenant data was classified exactly
// like a migration bookkeeping table, so it got the fence and the sweep and no
// predicate, and six tables' isolation rested on hand-written WHERE clauses that
// nothing checked were there.
//
// A mechanism that answers a question differently from its neighbours is not a
// difference in policy, it is a bug waiting for the table that lands between them.
var tenantFieldNames = []string{tenantFieldName, plainTenantFieldName}

// TenantColumnNames are the database column names the two spellings produce, for the
// callers that work from the database catalog rather than from a parsed gorm schema —
// tenantpurge's sweep, above all.
//
// 🔴 THEY ARE COMPUTED FROM tenantFieldNames, not written out beside them. A second
// literal reading {"tenant_id", "tenant"} would be a third list to keep in step with the
// other two, which is the exact failure this file exists to end — and review caught this
// file committing it while claiming otherwise. Adding a field name now adds its column
// name, so the sweep cannot come to classify a different set from the one the callback
// scopes.
//
// gorm's NamingStrategy is the authority on the mapping because it is what actually named
// the columns; TablePrefix does not affect ColumnName, so the zero value is the right one
// to ask.
var TenantColumnNames = tenantColumnNames()

func tenantColumnNames() []string {
	var ns schema.NamingStrategy
	out := make([]string, 0, len(tenantFieldNames))
	for _, name := range tenantFieldNames {
		out = append(out, ns.ColumnName("", name))
	}
	return out
}

// ErrUnscopedStatement is the refusal for a statement this callback cannot classify:
// one that names a table but whose destination gorm could not parse into a schema, so
// there is no tenant field to look for and no way to tell whether a predicate is
// owed. It names both ways out, because a developer meeting it needs to know which of
// the two situations they are in rather than which line to delete.
var ErrUnscopedStatement = errors.New("tenant isolation could not be applied: gorm built no schema for this " +
	"statement, so it cannot be told whether the table is tenant-scoped. Give the statement a typed " +
	"destination (Model(&T{}), Find(&[]T{}), Create(&T{})) so the tenant predicate can be injected, or run " +
	"it under core.WithSystemContext if it is genuinely instance-scoped — schema migration is, including " +
	"gorm's own AutoMigrate, which reads an existing table's columns through a statement of exactly this shape")

// ErrTenantMismatch is the refusal for a create whose row names one tenant while the
// context names another. See conflictingRowTenant for why this is an error rather than
// the silent overwrite it used to be.
var ErrTenantMismatch = errors.New("tenant isolation refused the write: a row being created names a " +
	"different tenant from the one in context. Create it under that tenant's context, or leave the " +
	"tenant unset on the row and let the callback stamp it")

// RegisterTenantScoping installs global GORM callbacks that enforce per-tenant
// row-level isolation for any model carrying a tenant field — TenantId from the
// embedded TenantScoped type, or the plain Tenant that event-processing's projections
// use. See tenantFieldNames. It is applied once here, not at each call site.
//
//   - Query / Row / Update / Delete: a "WHERE <tenant column> = ?" predicate is
//     injected from the tenant in db.Statement.Context, naming whichever column the
//     model's own schema carries. A missing tenant aborts the statement with
//     core.ErrNoTenant (fail-closed) so that an unscoped read can never leak another
//     tenant's rows.
//   - Create: the tenant from context is stamped onto every row (struct, slice,
//     or array). A missing tenant aborts with core.ErrNoTenant, and a row naming a
//     DIFFERENT tenant aborts with ErrTenantMismatch rather than being rewritten.
//
// Models with no tenant field in either spelling (migration bookkeeping tables, and
// event-processing's partition-keyed DetectSnapshot) pass through untouched.
//
// 🔴 WHAT IT DOES AND DOES NOT COVER, said here rather than left to be discovered — the
// same sentence tenant_fence.go writes about the erasure fence, because it is the same
// gap. APPLICABILITY IS A PROPERTY OF THE DESTINATION TYPE, NOT OF THE CALL, and the
// invariant is a biconditional, not a promise: THE PREDICATE IS APPLIED IF AND ONLY IF
// THE DESTINATION'S SCHEMA CARRIES ONE OF THE TENANT FIELDS. gorm decides what a
// statement is by parsing Statement.Model or Statement.Dest, and these callbacks can
// only ask the resulting schema for those names. Three ways a statement over a
// tenant-scoped table lands outside it, and only the second is refused:
//
//   - Raw SQL (Exec, and Raw with the SQL already written). Exec runs on gorm's Raw
//     processor, which has no callbacks registered on it at all, and a statement whose
//     SQL is already built has no clause list left to inject a predicate into. Isolation
//     is therefore a property of the ORM path, not of the database. tenantpurge's sweep
//     is raw Exec, which is why it still works. NOT refused, and cannot usefully be:
//     refusing here would move the same limit to a different error message while
//     breaking every legitimate raw read.
//   - A destination gorm cannot parse at all — the classic being a bare Table(...) with
//     a map, which is the natural way to spell a bulk update. This used to proceed with
//     no predicate and no error, which is the failure mode this doc once denied. It is
//     now REFUSED with ErrUnscopedStatement (see scopedTenant).
//   - A destination that DOES parse but whose schema is not the table's: a projection
//     struct (Table("widgets").Find(&[]struct{ Name string }{})) or a model that does not
//     match the table it is pointed at. NOT refused, and deliberately so — the schema is
//     well-formed and carries no tenant field, which is indistinguishable from an honest read
//     of a genuinely unscoped table. Nothing here can tell the two apart, so the predicate
//     is simply not owed. Naming the entity type in the destination is what earns it.
//
// Timing note (GORM v1.31.x): for the *Before* hooks the statement schema is
// already parsed by the time our callback runs, because processor.Execute()
// calls stmt.Parse(stmt.Model) before invoking the registered callback funcs.
// As a defensive measure we still re-parse from Model/Dest if Schema is nil so
// scoping is never silently skipped on a tenant-scoped model. The isolation
// test (tenant_scope_test.go) is the standing guarantee this still holds.
func RegisterTenantScoping(db *gorm.DB) error {
	// Query / row / update / delete inject the tenant predicate; create stamps it.
	for _, register := range []func() error{
		func() error {
			return db.Callback().Query().Before("gorm:query").Register("dc:tenant_query", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Row().Before("gorm:row").Register("dc:tenant_row", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Update().Before("gorm:update").Register("dc:tenant_update", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Delete().Before("gorm:delete").Register("dc:tenant_delete", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Create().Before("gorm:create").Register("dc:tenant_create", tenantScopeCreate)
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// ensureSchema makes sure db.Statement.Schema is populated. Returns true when a
// usable schema is available after the attempt. Find/First set Dest; Model(...)
// sets Model. The trailing check is authoritative.
//
// It is deliberately free of side effects: the token-grammar and erasure-fence
// callbacks use it too, and for them a statement with no schema is simply out of
// scope. Turning "no schema" into a refusal is this file's judgement, made in
// scopedTenant where the tenant predicate is owed, not here.
func ensureSchema(db *gorm.DB) bool {
	if db.Statement.Schema != nil {
		return true
	}
	if src := db.Statement.Model; src != nil {
		_ = db.Statement.Parse(src)
	} else if src := db.Statement.Dest; src != nil {
		_ = db.Statement.Parse(src)
	}
	return db.Statement.Schema != nil
}

// tenantField returns the schema field carrying this statement's tenant, or nil when
// the model has none and is therefore not tenant-scoped.
//
// The FIELD is returned rather than a bool because both callbacks need more than the
// yes/no: the predicate needs the column name to write WHERE against, and the create
// stamp needs the Go field name to set. Returning the field is what keeps those two
// from re-deriving the spelling independently and drifting apart — which is the
// smaller, in-file version of the divergence tenantFieldNames documents.
func tenantField(db *gorm.DB) *schema.Field {
	if !ensureSchema(db) {
		return nil
	}
	for _, name := range tenantFieldNames {
		if field, ok := db.Statement.Schema.FieldsByName[name]; ok {
			return field
		}
	}
	return nil
}

// namesATableWithoutASchema reports the one case this callback must refuse rather than
// wave through: gorm could not build a schema, but the statement DOES name a table, so
// it is about to write or read real rows with no predicate and nothing to say so.
//
// The two exclusions are what keep the refusal to that case:
//
//   - SQL already built (Raw). There is no clause list left to add a predicate to, so a
//     refusal here would not be isolation, it would be an outage on every raw read. Raw
//     is documented as out of reach on RegisterTenantScoping instead.
//   - No table at all. gorm itself already errors on that one — processor.Execute turns
//     an unparseable destination with no table into "Table not set, please set it like:
//     db.Model(&user) or db.Table(\"users\")" — so re-reporting it would replace a
//     precise message with a vaguer one.
func namesATableWithoutASchema(db *gorm.DB) bool {
	stmt := db.Statement
	if stmt.Schema != nil || stmt.SQL.Len() > 0 {
		return false
	}
	return stmt.Table != "" || stmt.TableExpr != nil
}

// statementTable renders what the refused statement named, for the error message. It
// falls back to the table EXPRESSION because Table is empty for a statement that only
// set one — reporting `table ""` there would name nothing in the very message whose job
// is to say which statement was refused.
func statementTable(stmt *gorm.Statement) string {
	if stmt.Table != "" {
		return stmt.Table
	}
	if stmt.TableExpr != nil {
		return stmt.TableExpr.SQL
	}
	return ""
}

// scopedTenant returns the tenant field and the context tenant when db is a live,
// tenant-scoped statement. ok=false means the callback should return without acting; any
// fail-closed error (no tenant in context for a tenant-scoped model, or a statement
// that cannot be classified at all) has already been recorded on db.
func scopedTenant(db *gorm.DB) (*schema.Field, string, bool) {
	if db.Error != nil {
		return nil, "", false
	}
	// A deliberate system context (core.WithSystemContext) runs unscoped: no
	// predicate is injected and the fail-closed check is skipped. This is the
	// sanctioned bypass for bootstrap operations that must run before a tenant
	// is known (e.g. the login lookup). See core.WithSystemContext for the
	// security contract governing its use.
	//
	// 🔑 IT IS TESTED FIRST, BEFORE CLASSIFICATION, and the order is load-bearing.
	// The unclassifiable-statement refusal below names WithSystemContext as one of its
	// two ways out, so a system context has to reach it. It is also what the migration
	// path relies on: gorm's own migrator reads a table's columns with
	// Table(name).Limit(1).Rows(), and gormigrate reads and writes its bookkeeping
	// table the same way — statements with no schema, all of them issued from inside a
	// library rather than from a call site anyone here could retype. rdb.go runs the
	// whole migration under a system context, which is what makes them legal.
	if core.IsSystemContext(db.Statement.Context) {
		return nil, "", false
	}
	field := tenantField(db)
	if field == nil {
		// Not tenant-scoped, or not classifiable. Only the second is a problem, and
		// only when a table is named — see namesATableWithoutASchema.
		if namesATableWithoutASchema(db) {
			_ = db.AddError(fmt.Errorf("%w (table %q)", ErrUnscopedStatement, statementTable(db.Statement)))
		}
		return nil, "", false
	}
	tenant, ok := core.TenantFromContext(db.Statement.Context)
	if !ok {
		_ = db.AddError(core.ErrNoTenant)
		return nil, "", false
	}
	return field, tenant, true
}

// tenantScopeQuery injects the tenant predicate for read/update/delete-style
// statements and fails closed when no tenant is present.
func tenantScopeQuery(db *gorm.DB) {
	field, tenant, ok := scopedTenant(db)
	if !ok {
		return
	}
	db.Statement.AddClause(clause.Where{
		Exprs: []clause.Expression{
			clause.Eq{
				// The column comes from the schema, not from a literal. A literal
				// "tenant_id" here silently produced NO predicate at all for a model
				// spelling it "tenant" — the statement would have referenced a column
				// that does not exist on the table.
				Column: clause.Column{Table: db.Statement.Table, Name: field.DBName},
				Value:  tenant,
			},
		},
	})
}

// tenantScopeCreate stamps the tenant id onto every row being created (SetColumn
// handles struct, slice and array destinations, i.e. batch inserts) and fails
// closed when no tenant is present.
func tenantScopeCreate(db *gorm.DB) {
	field, tenant, ok := scopedTenant(db)
	if !ok {
		return
	}
	if named, conflicts := conflictingRowTenant(db, field.Name, tenant); conflicts {
		_ = db.AddError(fmt.Errorf("%w: a row names tenant %q while the context names %q",
			ErrTenantMismatch, named, tenant))
		return
	}
	db.Statement.SetColumn(field.Name, tenant, true)
}

// conflictingRowTenant reports a tenant named on one of the rows being created that is
// not the context's, which is always a caller bug and must not be papered over.
//
// The stamp below OVERWRITES, and for most of this tree that is both intended and
// harmless: a model embedding TenantScoped leaves TenantId zero and lets the callback
// fill it, so there is nothing to disagree with. It stops being harmless for a model
// that carries its tenant as part of the PRIMARY KEY and sets it by hand — a silent
// rewrite there does not just relabel the row, it moves it to a different key, where
// the ON CONFLICT target the caller wrote will not find it. The write then succeeds and
// the projection is quietly wrong for two tenants at once.
//
// Refusing is not a security measure; the stamp was already safe in that sense, since
// a row can only ever be rewritten TOWARDS the context's own tenant and never away from
// it. It is a correctness measure, and it converts an invisible data defect into an
// error at the call site that caused it.
func conflictingRowTenant(db *gorm.DB, field, tenant string) (string, bool) {
	for _, named := range destTenants(db.Statement.Dest, field) {
		if named != "" && named != tenant {
			return named, true
		}
	}
	return "", false
}

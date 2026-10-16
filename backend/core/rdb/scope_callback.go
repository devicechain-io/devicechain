// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// The scope callbacks are the storage half of scoped memberships (ADR-081); the
// resolver half is auth.Authorize, which refuses every scoped caller until a resolver
// opts in with auth.AuthorizeScoped. Once one has, this is what stops it from reading
// past the scope.
//
// They act only when the statement's context carries a SCOPED caller
// (auth.ScopeFromContext), and then only on a TENANT-SCOPED model — the same
// classification the tenant callback makes (tenantField), so the two cannot disagree
// about which tables they cover. For such a statement:
//
//   - a model that declares no rule (ScopedModel) is refused with
//     auth.ErrScopeUnsupported, on every operation. Unopted is FORBIDDEN, never visible;
//   - a read (query, row, count, a preload, a subquery built through gorm) gets the
//     rule's predicate ADDED beside the tenant predicate the tenant callback has already
//     added — never in its place;
//   - an update gets the predicate too, and is refused unless the rule permits scoped
//     updates and the update leaves the anchor column alone;
//   - a create is refused unless the rule permits scoped creates, and then every row
//     must carry an anchor in the caller's scope and the statement must not be an upsert;
//   - a delete is always refused.
//
// An unscoped caller — every caller before ADR-081, every service token, every identity
// token — is untouched: no predicate, no refusal, no cost beyond reading the claims.
//
// 🔴 WHAT IT DOES NOT COVER is what the tenant callback does not cover, for the same
// reason (see RegisterTenantScoping): applicability is a property of the destination
// type. Exec runs on gorm's Raw processor, which has no callbacks at all; a projection
// struct whose schema carries no tenant field is indistinguishable from an honest read
// of an unscoped table; a JOINed table is not the statement's model; a context that
// carries a tenant but no claims (a processor's, a consumer's) is unscoped; and a
// system context (core.WithSystemContext) runs with neither predicate. A resolver that
// opts a scoped caller in and then reaches rows through any of those applies the scope
// itself, and carries a cross-customer test that proves it.
//
// One departure from the tenant callback, in the fail-closed direction: a READ whose SQL
// is already written (Raw(...).Scan/Find/Rows) DOES run these callbacks, and is refused
// for a scoped caller rather than waved through — see scopedStatement for why the
// tenant callback's tolerance of that shape would here be a silent no-op.

// registerScopeCallbacks installs the scope callbacks, each immediately after the tenant
// callback for the same operation. It is called by RegisterTenantScoping and nowhere
// else, so a handle with tenant isolation always has scope isolation as well — there is
// no way to register one without the other.
func registerScopeCallbacks(db *gorm.DB) error {
	for _, register := range []func() error{
		func() error {
			return db.Callback().Query().Before("gorm:query").After("dc:tenant_query").
				Register("dc:scope_query", scopeRead)
		},
		func() error {
			return db.Callback().Row().Before("gorm:row").After("dc:tenant_row").
				Register("dc:scope_row", scopeRead)
		},
		func() error {
			return db.Callback().Update().Before("gorm:update").After("dc:tenant_update").
				Register("dc:scope_update", scopeUpdate)
		},
		func() error {
			return db.Callback().Delete().Before("gorm:delete").After("dc:tenant_delete").
				Register("dc:scope_delete", scopeDelete)
		},
		func() error {
			return db.Callback().Create().Before("gorm:create").After("dc:tenant_create").
				Register("dc:scope_create", scopeCreate)
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// scopedStatement is the shared classification. ok=false means the callback returns
// without acting; a refusal has then already been recorded on db.
func scopedStatement(db *gorm.DB) (rule *scopeRule, tenant string, scope auth.Scope, ok bool) {
	if db.Error != nil {
		return nil, "", auth.Scope{}, false
	}
	ctx := db.Statement.Context
	// The sanctioned bypass, as for the tenant callback, and tested first for the same
	// reason: the migration path issues statements no call site can retype.
	if core.IsSystemContext(ctx) {
		return nil, "", auth.Scope{}, false
	}
	scope, scoped := auth.ScopeFromContext(ctx)
	if !scoped {
		return nil, "", auth.Scope{}, false
	}
	// 🔴 SQL ALREADY WRITTEN (Raw) IS REFUSED, AND THIS IS WHERE THE SCOPE CALLBACK
	// DEPARTS FROM THE TENANT ONE. Only Exec runs on gorm's callback-less Raw processor;
	// Raw(...).Scan/Find/Rows run the query and row callbacks with Statement.SQL already
	// built, and a predicate added to such a statement is DISCARDED — BuildQuerySQL
	// builds only when the SQL is empty. Without this check the callback would find the
	// model, find its rule, add the predicate and record no error, and the resolver
	// would have every reason to believe the read was scoped. The tenant callback
	// tolerates that shape because unscoped raw reads are legitimate (and it says so);
	// a scoped caller has no legitimate raw read, so the fail-closed answer costs
	// nothing. It is tested BEFORE classification because a Raw().Rows() has no
	// destination, hence no schema and no tenant field to classify by.
	if db.Statement.SQL.Len() > 0 {
		_ = db.AddError(fmt.Errorf("%w (a statement whose SQL was already written, so no scope predicate can be added)",
			auth.ErrScopeUnsupported))
		return nil, "", auth.Scope{}, false
	}
	if tenantField(db) == nil {
		// Not tenant-scoped (an instance-global table, migration bookkeeping): the
		// scope, like the tenant, does not apply. An UNCLASSIFIABLE statement was
		// already refused by the tenant callback, which runs first.
		return nil, "", auth.Scope{}, false
	}
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		// The tenant callback has refused this already; never proceed without one.
		_ = db.AddError(core.ErrNoTenant)
		return nil, "", auth.Scope{}, false
	}
	rule = ruleOf(db.Statement.Schema)
	if rule == nil {
		_ = db.AddError(fmt.Errorf("%w (table %q declares no scope rule)",
			auth.ErrScopeUnsupported, db.Statement.Table))
		return nil, "", auth.Scope{}, false
	}
	return rule, tenant, scope, true
}

// ruleOf returns the model's scope rule, or nil when it declares none. It asks a zero
// value of the model type through a pointer, so value- and pointer-receiver methods both
// count.
func ruleOf(s *schema.Schema) *scopeRule {
	if s == nil || s.ModelType == nil {
		return nil
	}
	m, ok := reflect.New(s.ModelType).Interface().(ScopedModel)
	if !ok {
		return nil
	}
	r := m.ScopedBy()
	if r == nil {
		return nil
	}
	return r.rule()
}

// refuseScoped records a scope refusal naming what was refused.
func refuseScoped(db *gorm.DB, what string) {
	_ = db.AddError(fmt.Errorf("%w (%s on table %q)", auth.ErrScopeUnsupported, what, db.Statement.Table))
}

// scopeRead adds the rule's predicate to a read.
func scopeRead(db *gorm.DB) {
	rule, tenant, scope, ok := scopedStatement(db)
	if !ok {
		return
	}
	addScopePredicate(db, rule, tenant, scope)
}

// scopeUpdate refuses an update the rule does not permit, refuses one that writes the
// anchor column, and otherwise limits it to rows in scope.
func scopeUpdate(db *gorm.DB) {
	rule, tenant, scope, ok := scopedStatement(db)
	if !ok {
		return
	}
	if !rule.allows(AllowScopedUpdate) {
		refuseScoped(db, "a scoped update")
		return
	}
	if rule.kind == ruleAnchorColumn {
		field := db.Statement.Schema.LookUpField(rule.column)
		if field == nil {
			refuseScoped(db, fmt.Sprintf("a scope rule naming no column %q", rule.column))
			return
		}
		if writesColumn(db, field) {
			refuseScoped(db, "an update writing the anchor column")
			return
		}
	}
	addScopePredicate(db, rule, tenant, scope)
}

// scopeDelete refuses every scoped delete.
func scopeDelete(db *gorm.DB) {
	if _, _, _, ok := scopedStatement(db); !ok {
		return
	}
	refuseScoped(db, "a scoped delete")
}

// scopeCreate refuses a create the rule does not permit, an upsert, and any row that
// does not carry an anchor in the caller's scope.
func scopeCreate(db *gorm.DB) {
	// The audit journal's own row, written by the audit callback on the scoped caller's
	// statement to record what that caller did. It is the platform's write, not the
	// caller's, and refusing it would refuse every permitted scoped write with it. Only
	// the journal's CREATE is exempt: a scoped READ of the journal is refused as for any
	// model that declares no rule.
	if db.Statement.Schema != nil && db.Statement.Schema.Name == auditSchemaName {
		return
	}
	rule, _, scope, ok := scopedStatement(db)
	if !ok {
		return
	}
	if !rule.allows(AllowScopedCreate) || rule.kind != ruleAnchorColumn {
		refuseScoped(db, "a scoped create")
		return
	}
	if _, upsert := db.Statement.Clauses["ON CONFLICT"]; upsert {
		refuseScoped(db, "a scoped upsert")
		return
	}
	field := db.Statement.Schema.LookUpField(rule.column)
	if field == nil {
		refuseScoped(db, fmt.Sprintf("a scope rule naming no column %q", rule.column))
		return
	}
	if !everyRowInScope(db, field, scope) {
		refuseScoped(db, "a scoped create of a row outside the caller's scope")
	}
}

// addScopePredicate adds rule's predicate for scope. A scoped caller with NO anchors
// sees nothing; it never sees everything.
func addScopePredicate(db *gorm.DB, rule *scopeRule, tenant string, scope auth.Scope) {
	anchors := scope.Anchors()
	switch rule.kind {
	case ruleCatalogue:
		return
	case ruleAnchorColumn:
		field := db.Statement.Schema.LookUpField(rule.column)
		if field == nil {
			refuseScoped(db, fmt.Sprintf("a scope rule naming no column %q", rule.column))
			return
		}
		if len(anchors) == 0 {
			addNothingPredicate(db)
			return
		}
		vars := make([]any, 0, len(anchors)+1)
		vars = append(vars, clause.Column{Table: db.Statement.Table, Name: field.DBName})
		for _, a := range anchors {
			vars = append(vars, a)
		}
		sql := "? && ARRAY[?" + strings.Repeat(", ?", len(anchors)-1) + "]::text[]"
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: sql, Vars: vars}}})
	case ruleAnchorTable:
		expr, err := anchorTableExpr(db, rule.set, tenant, anchors)
		if err != nil {
			refuseScoped(db, err.Error())
			return
		}
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{expr}})
	case ruleHeldAnchors:
		expr, err := heldAnchorsExpr(db, rule.holder, tenant, anchors)
		if err != nil {
			refuseScoped(db, err.Error())
			return
		}
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{expr}})
	case ruleDeviceEdge:
		expr, err := deviceEdgeExpr(db, rule.edges, tenant, anchors)
		if err != nil {
			refuseScoped(db, err.Error())
			return
		}
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{expr}})
	case ruleOwnAnchor:
		expr, err := ownAnchorExpr(db, rule, anchors)
		if err != nil {
			refuseScoped(db, err.Error())
			return
		}
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{expr}})
	default:
		refuseScoped(db, "an unknown scope rule")
	}
}

// Aliases of the edge and type tables inside a DeviceEdge predicate.
const (
	scopeEdgeAlias = "dc_scope_edge"
	scopeTypeAlias = "dc_scope_type"
)

// parseRuleModel parses a model a rule names and returns its schema, refusing one that is
// not tenant-scoped: every table a scope predicate reads carries its own tenant clause.
func parseRuleModel(db *gorm.DB, model any, what string) (*schema.Schema, *schema.Field, error) {
	if model == nil {
		return nil, nil, fmt.Errorf("a %s rule with no model", what)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil || stmt.Schema == nil {
		return nil, nil, fmt.Errorf("a %s rule whose model does not parse", what)
	}
	tenant := schemaTenantField(stmt.Schema)
	if tenant == nil {
		return nil, nil, fmt.Errorf("a %s rule over a table that is not tenant-scoped (%q)", what, stmt.Schema.Table)
	}
	return stmt.Schema, tenant, nil
}

// aliasedColumn resolves a field or column name of s to a column under alias.
func aliasedColumn(s *schema.Schema, alias, name string) (clause.Column, error) {
	if name == "" {
		return clause.Column{}, fmt.Errorf("an ownership rule naming an empty column on %q", s.Table)
	}
	f := s.LookUpField(name)
	if f == nil {
		return clause.Column{}, fmt.Errorf("an ownership rule naming no column %q on %q", name, s.Table)
	}
	return clause.Column{Table: alias, Name: f.DBName}, nil
}

// softDeleteColumn returns the soft-delete column of s, if it has one: a deleted edge or
// type row never confers visibility, whether or not its writers delete hard today.
func softDeleteColumn(s *schema.Schema) *schema.Field {
	for _, f := range s.Fields {
		if f.DBName != "" && f.FieldType == reflect.TypeOf(gorm.DeletedAt{}) {
			return f
		}
	}
	return nil
}

// deviceEdgeExpr builds a DeviceEdge predicate (see DeviceEdge). Every name comes from a
// parsed schema and is quoted by gorm; the tenant, the source kind, the tracked flag and
// every anchor are bound parameters.
func deviceEdgeExpr(db *gorm.DB, e OwnershipEdges, tenant string, anchors []string) (clause.Expression, error) {
	edge, edgeTenant, err := parseRuleModel(db, e.Edge, "device-edge")
	if err != nil {
		return nil, err
	}
	typ, typeTenant, err := parseRuleModel(db, e.Type, "device-edge type")
	if err != nil {
		return nil, err
	}
	col := func(s *schema.Schema, alias, name string) clause.Column {
		c, cerr := aliasedColumn(s, alias, name)
		if cerr != nil && err == nil {
			err = cerr
		}
		return c
	}
	typeID := col(typ, scopeTypeAlias, e.TypeID)
	edgeType := col(edge, scopeEdgeAlias, e.EdgeType)
	tracked := col(typ, scopeTypeAlias, e.Tracked)
	sourceType := col(edge, scopeEdgeAlias, e.SourceType)
	sourceID := col(edge, scopeEdgeAlias, e.SourceID)
	targetType := col(edge, scopeEdgeAlias, e.TargetType)
	targetToken := col(edge, scopeEdgeAlias, e.TargetToken)
	if err != nil {
		return nil, err
	}
	if e.SourceKind == "" {
		return nil, fmt.Errorf("a device-edge rule with no source kind")
	}
	row := db.Statement.Schema.LookUpField(e.Row)
	if row == nil {
		return nil, fmt.Errorf("a device-edge rule naming no row column %q", e.Row)
	}

	var sql strings.Builder
	vars := []any{
		clause.Table{Name: edge.Table}, clause.Table{Name: scopeEdgeAlias},
		clause.Table{Name: typ.Table}, clause.Table{Name: scopeTypeAlias},
		typeID, edgeType,
		clause.Column{Table: scopeEdgeAlias, Name: edgeTenant.DBName}, tenant,
		clause.Column{Table: scopeTypeAlias, Name: typeTenant.DBName}, tenant,
	}
	sql.WriteString("EXISTS (SELECT 1 FROM ? AS ? JOIN ? AS ? ON ? = ? WHERE ? = ? AND ? = ?")
	if f := softDeleteColumn(edge); f != nil {
		sql.WriteString(" AND ? IS NULL")
		vars = append(vars, clause.Column{Table: scopeEdgeAlias, Name: f.DBName})
	}
	if f := softDeleteColumn(typ); f != nil {
		sql.WriteString(" AND ? IS NULL")
		vars = append(vars, clause.Column{Table: scopeTypeAlias, Name: f.DBName})
	}
	sql.WriteString(" AND ? = ? AND ? = ? AND ? = ?")
	vars = append(vars, tracked, true, sourceType, e.SourceKind,
		sourceID, clause.Column{Table: db.Statement.Table, Name: row.DBName})
	if len(anchors) == 0 {
		sql.WriteString(" AND 1 = 0)")
		return clause.Expr{SQL: sql.String(), Vars: vars}, nil
	}
	sql.WriteString(" AND (")
	for i, a := range anchors {
		kind, token, perr := auth.ParseAnchor(a)
		if perr != nil {
			return nil, perr
		}
		if i > 0 {
			sql.WriteString(" OR ")
		}
		sql.WriteString("(? = ? AND ? = ?)")
		vars = append(vars, targetType, kind, targetToken, token)
	}
	sql.WriteString("))")
	return clause.Expr{SQL: sql.String(), Vars: vars}, nil
}

// ownAnchorExpr builds an OwnAnchor or NamedAnchor predicate: the row's own (kind, token)
// is one of the scope's anchors.
func ownAnchorExpr(db *gorm.DB, rule *scopeRule, anchors []string) (clause.Expression, error) {
	table := db.Statement.Table
	tokenField := db.Statement.Schema.LookUpField(rule.column)
	if tokenField == nil {
		return nil, fmt.Errorf("an own-anchor rule naming no column %q", rule.column)
	}
	tokenCol := clause.Column{Table: table, Name: tokenField.DBName}
	var kindCol clause.Column
	if rule.kindColumn != "" {
		kf := db.Statement.Schema.LookUpField(rule.kindColumn)
		if kf == nil {
			return nil, fmt.Errorf("an own-anchor rule naming no column %q", rule.kindColumn)
		}
		kindCol = clause.Column{Table: table, Name: kf.DBName}
	} else if rule.kindConst == "" {
		return nil, fmt.Errorf("an own-anchor rule with neither a kind column nor a kind")
	}
	var sql strings.Builder
	var vars []any
	for _, a := range anchors {
		kind, token, err := auth.ParseAnchor(a)
		if err != nil {
			return nil, err
		}
		if rule.kindColumn == "" && kind != rule.kindConst {
			continue
		}
		if sql.Len() > 0 {
			sql.WriteString(" OR ")
		}
		if rule.kindColumn != "" {
			sql.WriteString("(? = ? AND ? = ?)")
			vars = append(vars, kindCol, kind, tokenCol, token)
		} else {
			sql.WriteString("? = ?")
			vars = append(vars, tokenCol, token)
		}
	}
	if sql.Len() == 0 {
		// No anchor of a kind this model's rows can be: nothing, never everything.
		return clause.Expr{SQL: "1 = 0"}, nil
	}
	return clause.Expr{SQL: "(" + sql.String() + ")", Vars: vars}, nil
}

// addNothingPredicate limits a statement to no rows.
func addNothingPredicate(db *gorm.DB) {
	db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
}

// scopeSetAlias is the set table's alias inside an AnchorTable predicate. AnchorSet.Where
// is written against it.
const scopeSetAlias = "dc_scope_set"

// anchorTableExpr builds an AnchorTable predicate. Every name in it comes from a parsed
// schema — the set model's own, and the statement's — and is quoted by gorm; the anchors
// and the tenant are bound parameters. The one exception is AnchorSet.Where, a constant
// the model declares.
func anchorTableExpr(db *gorm.DB, set AnchorSet, tenant string, anchors []string) (clause.Expression, error) {
	if set.Model == nil || len(set.Keys) == 0 || set.KindColumn == "" || set.TokenColumn == "" {
		return nil, fmt.Errorf("an incomplete anchor-set rule")
	}
	setStmt := &gorm.Statement{DB: db}
	if err := setStmt.Parse(set.Model); err != nil || setStmt.Schema == nil {
		return nil, fmt.Errorf("an anchor-set rule whose model does not parse")
	}
	setSchema := setStmt.Schema
	setTenant := schemaTenantField(setSchema)
	if setTenant == nil {
		return nil, fmt.Errorf("an anchor-set rule over a table that is not tenant-scoped (%q)", setSchema.Table)
	}
	setColumn := func(name string) (clause.Column, error) {
		f := setSchema.LookUpField(name)
		if f == nil {
			return clause.Column{}, fmt.Errorf("an anchor-set rule naming no column %q on %q", name, setSchema.Table)
		}
		return clause.Column{Table: scopeSetAlias, Name: f.DBName}, nil
	}

	var sql strings.Builder
	vars := []any{clause.Table{Name: setSchema.Table}, clause.Table{Name: scopeSetAlias}}
	sql.WriteString("EXISTS (SELECT 1 FROM ? AS ? WHERE ? = ?")
	vars = append(vars, clause.Column{Table: scopeSetAlias, Name: setTenant.DBName}, tenant)
	for _, k := range set.Keys {
		sc, err := setColumn(k.Set)
		if err != nil {
			return nil, err
		}
		rf := db.Statement.Schema.LookUpField(k.Row)
		if rf == nil {
			return nil, fmt.Errorf("an anchor-set rule naming no row column %q", k.Row)
		}
		sql.WriteString(" AND ? = ?")
		vars = append(vars, sc, clause.Column{Table: db.Statement.Table, Name: rf.DBName})
	}
	if w := strings.TrimSpace(set.Where); w != "" {
		sql.WriteString(" AND (" + w + ")")
	}
	if len(anchors) == 0 {
		sql.WriteString(" AND 1 = 0)")
		return clause.Expr{SQL: sql.String(), Vars: vars}, nil
	}
	kindCol, err := setColumn(set.KindColumn)
	if err != nil {
		return nil, err
	}
	tokenCol, err := setColumn(set.TokenColumn)
	if err != nil {
		return nil, err
	}
	sql.WriteString(" AND (")
	for i, a := range anchors {
		kind, token, err := auth.ParseAnchor(a)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			sql.WriteString(" OR ")
		}
		sql.WriteString("(? = ? AND ? = ?)")
		vars = append(vars, kindCol, kind, tokenCol, token)
	}
	sql.WriteString("))")
	return clause.Expr{SQL: sql.String(), Vars: vars}, nil
}

// scopeHolderAlias is the holder table's alias inside a HeldAnchors predicate.
const scopeHolderAlias = "dc_scope_holder"

// heldAnchorsExpr builds a HeldAnchors predicate (see HeldAnchors). Every name comes from a
// parsed schema — the holder's and the statement's — and is quoted by gorm; the tenant and
// every anchor are bound parameters.
func heldAnchorsExpr(db *gorm.DB, h AnchorHolder, tenant string, anchors []string) (clause.Expression, error) {
	if len(h.Keys) == 0 || h.Column == "" {
		return nil, fmt.Errorf("an incomplete held-anchors rule")
	}
	holder, holderTenant, err := parseRuleModel(db, h.Model, "held-anchors")
	if err != nil {
		return nil, err
	}
	column := holder.LookUpField(h.Column)
	if column == nil {
		return nil, fmt.Errorf("a held-anchors rule naming no column %q on %q", h.Column, holder.Table)
	}

	var sql strings.Builder
	vars := []any{clause.Table{Name: holder.Table}, clause.Table{Name: scopeHolderAlias},
		clause.Column{Table: scopeHolderAlias, Name: holderTenant.DBName}, tenant}
	sql.WriteString("EXISTS (SELECT 1 FROM ? AS ? WHERE ? = ?")
	for _, k := range h.Keys {
		hc, err := aliasedColumn(holder, scopeHolderAlias, k.Set)
		if err != nil {
			return nil, err
		}
		rf := db.Statement.Schema.LookUpField(k.Row)
		if rf == nil {
			return nil, fmt.Errorf("a held-anchors rule naming no row column %q", k.Row)
		}
		sql.WriteString(" AND ? = ?")
		vars = append(vars, hc, clause.Column{Table: db.Statement.Table, Name: rf.DBName})
	}
	if f := softDeleteColumn(holder); f != nil {
		sql.WriteString(" AND ? IS NULL")
		vars = append(vars, clause.Column{Table: scopeHolderAlias, Name: f.DBName})
	}
	if len(anchors) == 0 {
		sql.WriteString(" AND 1 = 0)")
		return clause.Expr{SQL: sql.String(), Vars: vars}, nil
	}
	sql.WriteString(" AND ? && ARRAY[?" + strings.Repeat(", ?", len(anchors)-1) + "]::text[])")
	vars = append(vars, clause.Column{Table: scopeHolderAlias, Name: column.DBName})
	for _, a := range anchors {
		vars = append(vars, a)
	}
	return clause.Expr{SQL: sql.String(), Vars: vars}, nil
}

// writesColumn reports whether an update statement writes field's column, by every route
// gorm offers: a map key, a SET clause, a Select (Save selects "*"), or — with no Select
// restricting it — a non-zero struct field, which Updates writes. It errs towards "yes":
// a scoped update that cannot be shown to leave the column alone is refused.
func writesColumn(db *gorm.DB, field *schema.Field) bool {
	stmt := db.Statement
	if c, ok := stmt.Clauses["SET"]; ok {
		if set, ok := c.Expression.(clause.Set); ok {
			for _, a := range set {
				if namesColumn(db, field, a.Column.Name) {
					return true
				}
			}
		}
	}
	if m, ok := stmt.Dest.(map[string]any); ok {
		for k := range m {
			if namesColumn(db, field, k) {
				return true
			}
		}
		return false
	}
	selected, restricted := stmt.SelectAndOmitColumns(false, true)
	for name, on := range selected {
		if namesColumn(db, field, name) {
			return on
		}
	}
	if restricted {
		return false
	}
	v := reflect.ValueOf(stmt.Dest)
	for v.IsValid() && (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		// Not a shape this can read; refuse rather than guess.
		return true
	}
	if v.Type() != stmt.Schema.ModelType {
		return true
	}
	_, zero := field.ValueOf(stmt.Context, v)
	return !zero
}

// everyRowInScope reports whether every row a create writes carries at least one anchor
// in scope. A destination it cannot read row by row (a map, a value of the wrong type,
// an anchor field that is not a list of strings) answers false: refused, not guessed.
func everyRowInScope(db *gorm.DB, field *schema.Field, scope auth.Scope) bool {
	in := make(map[string]struct{})
	for _, a := range scope.Anchors() {
		in[a] = struct{}{}
	}
	stmt := db.Statement
	rows := 0
	ok := true
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
			if v.IsNil() {
				ok = false
				return
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len() && ok; i++ {
				walk(v.Index(i))
			}
		case reflect.Struct:
			if v.Type() != stmt.Schema.ModelType {
				ok = false
				return
			}
			rows++
			val, _ := field.ValueOf(stmt.Context, v)
			rv := reflect.ValueOf(val)
			if !rv.IsValid() || rv.Kind() != reflect.Slice || rv.Type().Elem().Kind() != reflect.String {
				ok = false
				return
			}
			for i := 0; i < rv.Len(); i++ {
				if _, hit := in[rv.Index(i).String()]; hit {
					return
				}
			}
			ok = false
		default:
			ok = false
		}
	}
	walk(reflect.ValueOf(stmt.Dest))
	return ok && rows > 0
}

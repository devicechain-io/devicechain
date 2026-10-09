// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import (
	"go/ast"
	"go/token"
	"path"
	"reflect"
	"sort"
	"strings"

	"gorm.io/gorm/schema"
)

// tenantFieldNames are the two Go field spellings that make a gorm model tenant-scoped.
//
// 🔴 THIS RESTATES core/rdb's tenantFieldNames, and cannot import them: core/rdb is a
// runtime library and this tool deliberately has no workspace dependencies. The copy is
// pinned rather than trusted — TestTenantFieldNamesMatchCore reads core/rdb/tenant_scope.go
// and fails when the two disagree, so a third spelling arriving in core turns that test
// red instead of silently shrinking the set of tables these guards know to protect.
var tenantFieldNames = map[string]bool{"TenantId": true, "Tenant": true}

// tenantColumnNames are the database column names those spellings produce
// (core/rdb.TenantColumnNames), used when reading a join's ON clause.
var tenantColumnNames = []string{"tenant_id", "tenant"}

// pkgFacts is everything the guards need to know about one directory (one Go package, as
// far as source layout goes), collected in a first pass over the tree.
type pkgFacts struct {
	// structs maps a type name to its declaration.
	structs map[string]*ast.StructType
	// tableNames maps a type name to the literal its TableName method returns.
	tableNames map[string]string
	// strs maps a package-level const or var name to the expression initialising it, so a
	// query held in a named constant can be read at its use site.
	strs map[string]ast.Expr
}

// facts is the cross-package knowledge gathered by the first pass.
type facts struct {
	pkgs map[string]*pkgFacts
	// tenantTypes are the simple names of struct types that carry a tenant field, directly
	// or through an embedded type that does. Keyed by simple name rather than package
	// because an embed is written as `rdb.TenantScoped`; a same-named type in another
	// package can only make the set LARGER, which is the safe direction for a guard.
	tenantTypes map[string]bool
	// tables is the derived tenant-scoped table set: table name -> a type that produced it.
	tables map[string]string
}

func newFacts() *facts {
	return &facts{pkgs: map[string]*pkgFacts{}, tenantTypes: map[string]bool{}, tables: map[string]string{}}
}

func (f *facts) pkg(dir string) *pkgFacts {
	p := f.pkgs[dir]
	if p == nil {
		p = &pkgFacts{structs: map[string]*ast.StructType{}, tableNames: map[string]string{}, strs: map[string]ast.Expr{}}
		f.pkgs[dir] = p
	}
	return p
}

// collect is the first-pass fileScanner: it records declarations and reports nothing.
func (f *facts) collect(_ *token.FileSet, file *ast.File, rel string, _ *Result) []Finding {
	p := f.pkg(path.Dir(rel))
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						p.structs[s.Name.Name] = st
					}
				case *ast.ValueSpec:
					// Only CONSTS are followed: a package var can be reassigned (init(), a
					// test hook) so its initialiser is not its value. A multi-value spec has no
					// single expression per name.
					if d.Tok == token.CONST && len(s.Names) == len(s.Values) {
						for i, n := range s.Names {
							p.strs[n.Name] = s.Values[i]
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv == nil || d.Name.Name != "TableName" || d.Body == nil || len(d.Body.List) != 1 {
				continue
			}
			ret, ok := d.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, ok := unquote(lit.Value); ok {
					p.tableNames[recvTypeName(d.Recv)] = v
				}
			}
		}
	}
	return nil
}

// derive computes tenantTypes and tables once every file has been collected.
func (f *facts) derive() {
	// Fixed point over embedding: a struct is tenant-bearing if it has a tenant field, or
	// embeds a tenant-bearing type.
	for changed := true; changed; {
		changed = false
		for _, p := range f.pkgs {
			for name, st := range p.structs {
				if f.tenantTypes[name] || !f.bearsTenant(st) {
					continue
				}
				f.tenantTypes[name] = true
				changed = true
			}
		}
	}
	var ns schema.NamingStrategy
	dirs := make([]string, 0, len(f.pkgs))
	for d := range f.pkgs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		p := f.pkgs[d]
		for name, st := range p.structs {
			// TenantScoped is the mixin itself, never a table.
			if name == "TenantScoped" || !f.tenantTypes[name] || !isGormModel(st, f.tenantTypes) {
				continue
			}
			table := p.tableNames[name]
			if table == "" {
				table = ns.TableName(name)
			}
			if _, seen := f.tables[table]; !seen {
				f.tables[table] = d + "." + name
			}
			// A many2many join table has no struct of its own, but it hangs off this tenant-
			// bearing owner: the purge classes it transitive, and a raw statement over it
			// touches tenant rows. Its name is in the owner's tag.
			for _, j := range many2manyTables(st) {
				if _, seen := f.tables[j]; !seen {
					f.tables[j] = d + "." + name + " (many2many)"
				}
			}
		}
	}
}

// many2manyTables returns the join-table names in a struct's `gorm:"many2many:<name>"` tags.
func many2manyTables(st *ast.StructType) []string {
	var out []string
	for _, fld := range st.Fields.List {
		if fld.Tag == nil {
			continue
		}
		tag, ok := unquote(fld.Tag.Value)
		if !ok {
			continue
		}
		for _, part := range strings.Split(reflect.StructTag(tag).Get("gorm"), ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), ":"); ok && strings.EqualFold(k, "many2many") && v != "" {
				out = append(out, strings.ToLower(v))
			}
		}
	}
	return out
}

func (f *facts) bearsTenant(st *ast.StructType) bool {
	for _, fld := range st.Fields.List {
		if len(fld.Names) == 0 {
			if f.tenantTypes[embeddedName(fld.Type)] {
				return true
			}
			continue
		}
		for _, n := range fld.Names {
			if tenantFieldNames[n.Name] {
				return true
			}
		}
	}
	return false
}

// isGormModel separates a gorm model from an unrelated struct that merely has a field
// called Tenant (a JSON claim, a CLI row). A model either embeds a tenant-bearing mixin or
// carries a `gorm:"…"` tag somewhere — every real model in this tree does, because the
// tenant column is the primary key of the event-processing projections or is indexed.
func isGormModel(st *ast.StructType, tenantTypes map[string]bool) bool {
	for _, fld := range st.Fields.List {
		if len(fld.Names) == 0 && tenantTypes[embeddedName(fld.Type)] {
			return true
		}
		if fld.Tag != nil {
			if tag, ok := unquote(fld.Tag.Value); ok {
				if _, has := reflect.StructTag(tag).Lookup("gorm"); has {
					return true
				}
			}
		}
	}
	return false
}

func embeddedName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.StarExpr:
		return embeddedName(x.X)
	}
	return ""
}

func recvTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	t := recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// TenantTables returns the tenant-scoped table names derived from the Go models under
// roots, table name -> the type that produced it.
//
// # How a table is classified, and why from source
//
// The runtime authority is the callback in core/rdb: a statement is tenant-scoped when
// its destination schema carries a TenantId or Tenant field (tenantFieldNames), and the
// erasure sweep classifies the same set from the catalog by the matching column names
// (rdb.TenantColumnNames). Both are answers about a RUNNING database. A source guard has
// no database, so it asks the same question of the same artefact the callback does — the
// model struct — and applies gorm's own NamingStrategy to get the table name. It is
// derived on every run rather than kept as a list, so there is no second list to drift:
// a model that gains a tenant field is protected the moment it compiles.
//
// A many2many join table has no struct, so it is added from the tag on its tenant-bearing
// owner (the tenant purge classes it transitive). What this still cannot see is a table
// with a tenant column and no Go struct and no such owner: it has no model for the callback
// to scope either, so the guards cannot be more precise than the thing they stand in for.
// TestDerivedSetCoversGoldenSchemas checks the derived set against the frozen schemas.
func TenantTables(roots ...string) (map[string]string, error) {
	f, err := collectTenantFacts(roots)
	if err != nil {
		return nil, err
	}
	return f.tables, nil
}

func collectTenantFacts(roots []string) (*facts, error) {
	f := newFacts()
	if _, err := scan(f.collect, roots...); err != nil {
		return nil, err
	}
	f.derive()
	return f, nil
}

// refersToTenantTable returns the tenant tables named in a SQL text, sorted. Identifiers
// are matched as whole words after quotes are stripped, so `"device-management".devices`
// and `devices d` both name `devices`, and `device_types` is not mistaken for `devices`.
func refersToTenantTable(sql string, tables map[string]string) []string {
	seen := map[string]bool{}
	for _, tok := range identTokens(sql) {
		if _, ok := tables[tok]; ok {
			seen[tok] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// identTokens splits SQL into lower-cased identifier-ish words. Hyphens are NOT word
// characters here, so a schema like "device-management" splits into two harmless words.
func identTokens(sql string) []string {
	sql = strings.ToLower(sql)
	var out []string
	start := -1
	for i := 0; i <= len(sql); i++ {
		word := i < len(sql) && (sql[i] == '_' || sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= '0' && sql[i] <= '9')
		switch {
		case word && start < 0:
			start = i
		case !word && start >= 0:
			out = append(out, sql[start:i])
			start = -1
		}
	}
	return out
}

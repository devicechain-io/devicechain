// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import (
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// siteEntry exempts the flagged sites inside ONE function of ONE file.
//
// 🔴 THE ANCHOR IS A FUNCTION NAME AND A COUNT, NOT A LINE. A line number goes stale on
// every edit above it, which teaches a maintainer to renumber entries without reading
// them; a function name survives edits and says what the code is FOR. The Count is what
// stops the exemption growing: an entry absorbs exactly Count flagged sites, so a second
// Exec added to an exempted function does not inherit the first one's reason — the run
// fails (exit 2) until somebody reads the new statement and either fixes it or raises the
// count with a reason that covers it.
//
// 🔴 AN ENTRY MUST BE EXERCISED. An entry whose function no longer exists, or whose site
// no longer matches the rule (the SQL stopped naming a tenant table, the table stopped
// being tenant-scoped), absorbs nothing and FAILS THE RUN. An exemption that outlives its
// reason is the quietest way for a guard to stop guarding.
type siteEntry struct {
	// Path is repo-relative and matched EXACTLY — see matchesAllowPath.
	Path string
	// Func is "Name" for a function and "Recv.Name" for a method, with the receiver's
	// pointer stripped; "<package-level>" for a site in a var or const initialiser.
	Func string
	// Count is how many flagged sites in Func the entry covers.
	Count int
	Why   string
}

func (e siteEntry) key() string { return e.Path + ":" + e.Func }

func siteAllowKeys(entries []siteEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.key())
	}
	return out
}

// siteStale reports every entry whose absorbed-site count is not exactly its Count.
func siteStale(entries []siteEntry, res Result) []string {
	var out []string
	for _, e := range entries {
		if got := res.Allowed[e.key()]; got != e.Count {
			out = append(out, fmt.Sprintf(
				"allow-list entry %s covers %d flagged site(s), but the scan found %d — the function "+
					"moved or changed (stale entry), or a site was added to it (the new statement has not "+
					"been read)", e.key(), e.Count, got))
		}
	}
	return out
}

// siteKind selects which question a siteScanner asks of a call.
type siteKind int

const (
	kindRawSQL siteKind = iota
	kindRawJoin
)

type siteScanner struct {
	kind  siteKind
	facts *facts
	allow []siteEntry
	// locals are the names declared inside the declaration being scanned; see localNames.
	locals map[string]bool
}

// RawSQLScan reports every non-test, non-migration `.Raw(` / `.Exec(` whose SQL names a
// tenant-scoped table, or whose SQL cannot be read statically, unless the enclosing
// function carries an allow-list entry.
//
// # What the rule protects
//
// The tenant-scope callback injects its predicate into a statement it BUILDS. `Raw`
// hands gorm a finished SQL string, and `Exec` runs on a processor with no callbacks at
// all, so neither can have the predicate added: whether a raw statement is tenant-safe is
// a property of its text, which only a reader can check. This guard is that reader's
// checklist — every raw statement over a tenant-scoped table is either gone or sits in an
// allow-list beside the reason it is safe. It is defence in depth beside the callback,
// not a replacement for review.
//
// # Scope, and what it cannot see
//
//   - FAIL CLOSED ON WHAT IT CANNOT READ. SQL built from a variable, a call or another
//     package's constant is flagged: the table it touches is unknown, and "unknown" is
//     not "safe". Literals, `+` concatenation, fmt.Sprintf and same-package constants
//     are followed.
//   - Files named migration_*.go and baseline*.go are skipped. They run once, under the
//     migration system context, from frozen snapshots, and carry most of the DDL in the
//     tree. The cost is that a runtime query in a file with that name would not be seen.
//   - Calls whose first argument is a context (`conn.Exec(ctx, sql)`) are pgx and
//     graphql executors, not gorm, whose Exec/Raw take the SQL first; they cannot compile
//     against gorm.
//   - The tenant-table set is DERIVED from the models under the same roots (see
//     TenantTables), so a model outside the roots is invisible, and a table with a tenant
//     column but no Go struct is not in the set.
//   - OUT OF SCOPE: database/sql's *Context calls (ExecContext, QueryContext, QueryRowContext)
//     on a raw *sql.DB / *sql.Tx, and SQL fragments inside an ORM statement — a subquery in
//     Where, gorm.Expr, Select or Clauses. Only gorm's Raw and Exec are read.
//   - Statements routed through a local closure that forwards a parameter to Exec/Raw are
//     counted at each CALL of the closure, with the argument resolved there. A helper
//     function or method that does the same is not followed; its own body is flagged as
//     dynamic SQL instead.
//   - Only the SQL TEXT is read. A statement that names no tenant table but reaches one
//     through a view, function or foreign table is out of reach.
func RawSQLScan(roots ...string) (Result, error) {
	return rawSQLScan(rawSQLAllowList, roots...)
}

// RawJoinScan reports every non-test `Joins(` that joins a tenant-scoped table without a
// tenant-column equality in its ON clause, and every association join (`Joins("Device")`)
// whose joined table cannot be read from the call, unless allow-listed.
//
// # What the rule protects
//
// The callback scopes the OUTER table of a statement. A joined table is added by hand-
// written SQL, so nothing filters it: a join reaches a row of another tenant whenever the
// foreign key it follows names one. Today every such key is written under a tenant-scoped
// write, which makes the join safe by PROVENANCE of the key. An ON clause that also
// equates the tenant columns makes it safe by QUERY — the property can be read in one
// place, and does not depend on every present and future writer of the key.
//
// The equality accepted is `<joined table or alias>.tenant_id = <anything>.tenant_id`
// (either order, either column spelling) or `… = ?`. It does not check the other side is
// the outer table; that is a reviewer's call, and over-accepting there is the cheaper way
// to be wrong than demanding one spelling.
func RawJoinScan(roots ...string) (Result, error) {
	return rawJoinScan(rawJoinAllowList, roots...)
}

func rawSQLScan(allow []siteEntry, roots ...string) (Result, error) {
	return siteScan(kindRawSQL, allow, roots)
}

func rawJoinScan(allow []siteEntry, roots ...string) (Result, error) {
	return siteScan(kindRawJoin, allow, roots)
}

func siteScan(kind siteKind, allow []siteEntry, roots []string) (Result, error) {
	f, err := collectTenantFacts(roots)
	if err != nil {
		return Result{}, err
	}
	s := &siteScanner{kind: kind, facts: f, allow: allow}
	res, err := scan(s.scanFile, roots...)
	if err != nil {
		return res, err
	}
	res.Stale = siteStale(allow, res)
	return res, nil
}

func (s *siteScanner) scanFile(fset *token.FileSet, file *ast.File, rel string, res *Result) []Finding {
	base := path.Base(rel)
	if s.kind == kindRawSQL && (strings.HasPrefix(base, "migration_") || strings.HasPrefix(base, "baseline")) {
		return nil
	}
	p := s.facts.pkg(path.Dir(rel))
	var out []Finding

	for _, decl := range file.Decls {
		fn := declName(decl)
		s.locals = localNames(decl)
		closures, inner := s.sqlClosures(decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 || inner[call] {
				return true
			}
			// A call to a local closure that forwards a parameter to Exec/Raw IS the site:
			// the statement's text is the argument at the call, not the closure's parameter.
			if id, ok := call.Fun.(*ast.Ident); ok && s.kind == kindRawSQL {
				if idx, ok := closures[id.Name]; ok && idx < len(call.Args) {
					if reason := s.rawSQLReason(call.Args[idx], p); reason != "" {
						if s.absorb(rel, fn, res) {
							return true
						}
						out = append(out, Finding{
							Pos:     fset.Position(id.Pos()),
							Message: reason + " (via closure " + id.Name + ", in " + fn + ")",
							Source:  id.Name + "(…)",
						})
					}
				}
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			var reason string
			switch s.kind {
			case kindRawSQL:
				if (sel.Sel.Name != "Raw" && sel.Sel.Name != "Exec") || contextFirst(call.Args[0]) {
					return true
				}
				reason = s.rawSQLReason(call.Args[0], p)
			case kindRawJoin:
				if sel.Sel.Name != "Joins" {
					return true
				}
				reason = s.rawJoinReason(call.Args[0], p)
			}
			if reason == "" {
				return true
			}
			if s.absorb(rel, fn, res) {
				return true
			}
			out = append(out, Finding{
				Pos:     fset.Position(sel.Sel.Pos()),
				Message: reason + " (in " + fn + ")",
				Source:  exprText(sel) + "(…)",
			})
			return true
		})
	}
	return out
}

// absorb counts a flagged site against the allow-list entry for (rel, fn), if any.
func (s *siteScanner) absorb(rel, fn string, res *Result) bool {
	for _, e := range s.allow {
		if matchesAllowPath(rel, e.Path) && e.Func == fn {
			res.Allowed[e.key()]++
			return true
		}
	}
	return false
}

// localNames is every name declared inside a declaration: parameters, results, receivers,
// := targets, var/const specs and range variables. An identifier in this set is NOT the
// package-level constant of the same name.
func localNames(d ast.Decl) map[string]bool {
	out := map[string]bool{}
	fields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				out[n.Name] = true
			}
		}
	}
	ast.Inspect(d, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			fields(x.Recv)
			fields(x.Type.Params)
			fields(x.Type.Results)
		case *ast.FuncLit:
			fields(x.Type.Params)
			fields(x.Type.Results)
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE {
				for _, e := range []ast.Expr{x.Key, x.Value} {
					if id, ok := e.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			// Package-level specs never reach here: only a function declaration is walked.
			for _, id := range x.Names {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

// sqlClosures finds local closures (`exec := func(step, stmt string, ...) { db.Exec(stmt, ...) }`)
// that forward one of their own parameters to Exec/Raw. It returns the closure's name ->
// the index of the forwarded parameter, and the set of inner Exec/Raw calls, which are not
// sites themselves: each CALL of the closure is.
//
// 🔴 WITHOUT THIS an allow-listed function that routes its statements through a closure
// hides every new statement behind the one counted site inside the closure.
func (s *siteScanner) sqlClosures(d ast.Decl) (map[string]int, map[*ast.CallExpr]bool) {
	closures, inner := map[string]int{}, map[*ast.CallExpr]bool{}
	if s.kind != kindRawSQL {
		return closures, inner
	}
	ast.Inspect(d, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		name, ok := as.Lhs[0].(*ast.Ident)
		lit, isLit := as.Rhs[0].(*ast.FuncLit)
		if !ok || !isLit {
			return true
		}
		var params []string
		for _, f := range lit.Type.Params.List {
			for _, pn := range f.Names {
				params = append(params, pn.Name)
			}
		}
		ast.Inspect(lit.Body, func(m ast.Node) bool {
			c, ok := m.(*ast.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Exec" && sel.Sel.Name != "Raw") {
				return true
			}
			if id, ok := c.Args[0].(*ast.Ident); ok {
				for i, pn := range params {
					if pn == id.Name {
						closures[name.Name] = i
						inner[c] = true
					}
				}
			}
			return true
		})
		return true
	})
	return closures, inner
}

func declName(d ast.Decl) string {
	fd, ok := d.(*ast.FuncDecl)
	if !ok {
		return "<package-level>"
	}
	if fd.Recv != nil {
		if r := recvTypeName(fd.Recv); r != "" {
			return r + "." + fd.Name.Name
		}
	}
	return fd.Name.Name
}

// contextFirst reports whether a call's first argument is a context, which marks a pgx or
// graphql executor rather than gorm (gorm's Exec/Raw take the SQL string first).
func contextFirst(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "ctx"
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "context" {
				return true
			}
			return sel.Sel.Name == "Context"
		}
	}
	return false
}

// sqlText is the statically recoverable text of a SQL expression.
type sqlText struct {
	b       strings.Builder
	dynamic bool
}

func (t *sqlText) String() string { return t.b.String() }

// resolve folds e into t. Anything it cannot read sets dynamic rather than being dropped.
func (s *siteScanner) resolve(e ast.Expr, p *pkgFacts, depth int, t *sqlText) {
	switch x := e.(type) {
	case *ast.BasicLit:
		switch x.Kind {
		case token.STRING:
			if v, ok := unquote(x.Value); ok {
				t.b.WriteString(v)
				t.b.WriteByte(' ')
				return
			}
		case token.INT, token.FLOAT:
			t.b.WriteString(x.Value + " ")
			return
		}
		t.dynamic = true
	case *ast.ParenExpr:
		s.resolve(x.X, p, depth, t)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			t.dynamic = true
			return
		}
		s.resolve(x.X, p, depth, t)
		s.resolve(x.Y, p, depth, t)
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sprintf" || len(x.Args) == 0 {
			t.dynamic = true
			return
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "fmt" {
			t.dynamic = true
			return
		}
		for _, a := range x.Args {
			s.resolve(a, p, depth, t)
		}
	case *ast.Ident:
		if init, ok := p.strs[x.Name]; ok && depth < 8 && !s.locals[x.Name] {
			s.resolve(init, p, depth+1, t)
			return
		}
		t.dynamic = true
	default:
		t.dynamic = true
	}
}

func (s *siteScanner) rawSQLReason(arg ast.Expr, p *pkgFacts) string {
	var t sqlText
	s.resolve(arg, p, 0, &t)
	if tabs := refersToTenantTable(t.String(), s.facts.tables); len(tabs) > 0 {
		return "raw SQL names tenant-scoped table " + strings.Join(tabs, ", ") +
			", which the tenant-scope callback cannot filter in prebuilt SQL"
	}
	if t.dynamic {
		return "raw SQL is built dynamically, so the table it touches cannot be read statically"
	}
	return ""
}

var (
	joinKeyword = regexp.MustCompile(`(?i)\bjoin\b`)
	// joinClause reads `[schema.]table [[AS] alias] ON <condition>` from the text after the
	// JOIN keyword.
	joinClause = regexp.MustCompile(`(?is)^\s*(?:"?[\w-]+"?\.)?"?(\w+)"?(?:\s+(?:as\s+)?"?(\w+)"?)?\s+on\s+(.*)$`)
)

func (s *siteScanner) rawJoinReason(arg ast.Expr, p *pkgFacts) string {
	var t sqlText
	s.resolve(arg, p, 0, &t)
	text := t.String()
	if t.dynamic {
		return "join is built dynamically, so the table it joins cannot be read statically"
	}
	if !joinKeyword.MatchString(text) {
		return "association join " + strconv.Quote(strings.TrimSpace(text)) +
			" joins a table that is not named at the call, and the callback does not filter it"
	}
	idx := joinKeyword.FindAllStringIndex(text, -1)
	var bad []string
	for i, loc := range idx {
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		seg := text[loc[1]:end]
		m := joinClause.FindStringSubmatch(seg)
		if m == nil {
			// A subquery, USING, or a form this reader does not know: fail closed only when
			// it could be a tenant table.
			if len(refersToTenantTable(seg, s.facts.tables)) > 0 || strings.Contains(seg, "(") {
				bad = append(bad, "an unreadable join clause")
			}
			continue
		}
		table, alias, on := strings.ToLower(m[1]), strings.ToLower(m[2]), m[3]
		if _, tenant := s.facts.tables[table]; !tenant {
			continue
		}
		if !hasTenantEquality(on, table, alias) {
			bad = append(bad, "tenant-scoped table "+table)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return "joins " + strings.Join(bad, ", ") + " without a tenant-column equality in its ON clause"
}

// hasTenantEquality reports whether the ON text equates the joined table's tenant column
// with something: `<ref>.tenant_id = x.tenant_id` / `= ?`, in either order.
func hasTenantEquality(on, table, alias string) bool {
	refs := []string{regexp.QuoteMeta(table)}
	if alias != "" && alias != "on" {
		refs = append(refs, regexp.QuoteMeta(alias))
	}
	ref := `"?(?:` + strings.Join(refs, "|") + `)"?`
	col := `"?(?:` + strings.Join(tenantColumnNames, "|") + `)"?`
	other := `(?:"?\w+"?\.` + col + `|\?)`
	fwd := regexp.MustCompile(`(?i)\b` + ref + `\.` + col + `\s*=\s*` + other)
	rev := regexp.MustCompile(`(?i)` + other + `\s*=\s*\b` + ref + `\.` + col)
	return fwd.MatchString(on) || rev.MatchString(on)
}

// unquote reads a Go string literal.
func unquote(lit string) (string, bool) {
	v, err := strconv.Unquote(lit)
	return v, err == nil
}

// RawSQLAllowKeys / RawJoinAllowKeys are every (file, function) pair the allow-lists
// declare, in the form Result.Allowed is keyed by.
func RawSQLAllowKeys() []string  { return siteAllowKeys(rawSQLAllowList) }
func RawJoinAllowKeys() []string { return siteAllowKeys(rawJoinAllowList) }

// SetAllowListForSelfTest REPLACES the named check's allow-list with the given specs, each
// "path|func|count". It exists so the binary's own exit-code behaviour on stale and grown
// entries can be exercised against a fixture tree with the liveness check ON; the real
// lists are in-tree and a fixture can name none of their files. The scripts never pass it
// on a run over the repository.
func SetAllowListForSelfTest(check, specs string) error {
	var entries []siteEntry
	for _, spec := range strings.Split(specs, ";") {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		parts := strings.Split(spec, "|")
		if len(parts) != 3 {
			return fmt.Errorf("bad allow-list spec %q, want path|func|count", spec)
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 {
			return fmt.Errorf("bad count in %q", spec)
		}
		entries = append(entries, siteEntry{Path: parts[0], Func: parts[1], Count: n, Why: "self-test"})
	}
	switch check {
	case "raw-sql":
		rawSQLAllowList = entries
	case "raw-join":
		rawJoinAllowList = entries
	default:
		return fmt.Errorf("check %q has no site allow-list", check)
	}
	return nil
}

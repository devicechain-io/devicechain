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
	kind   siteKind
	facts  *facts
	allow  []siteEntry
	srcDir string
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
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
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
			for _, e := range s.allow {
				if matchesAllowPath(rel, e.Path) && e.Func == fn {
					res.Allowed[e.key()]++
					return true
				}
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
		if init, ok := p.strs[x.Name]; ok && depth < 8 {
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
